package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/apns"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

// installNotifyDispatcher builds the process-wide dispatcher for the nine
// fantasy-event call sites (notify.Send). The four operator sends keep
// calling notify.SendPushover directly and never pass through here.
//
// The feed sink is required and runs first — its record's id is what the
// APNs payload carries. APNs and Pushover are added only when their config
// is present: a deployment with no APNs key still records every event in the
// activity feed, which is the behaviour every local run and test relies on.
func installNotifyDispatcher() {
	w, err := statestore.FromEnv().Notifications()
	if err != nil {
		return // no durable feed configured; notify.Send stays a no-op
	}
	uid := statestore.Tenant()

	d := &notify.Dispatcher{
		Feed: &notify.FeedWriterSink{
			Writer: w,
			UserID: uid,
			RunID:  os.Getenv("RUN_ID"), // set by entrypoint.sh; links feed -> run
		},
	}

	if sink := apnsSink(lineupapi.UserID(uid)); sink != nil {
		d.Sinks = append(d.Sinks, sink)
	}
	// Cutover window only (see the spec's Cutover section): while this flag is
	// set, every fantasy event also reaches Pushover, so APNs delivery can be
	// confirmed against a channel known to work. Unsetting it completes the
	// migration with no code change — and deliberately without touching
	// PUSHOVER_USER_KEY, which the operator channel reads permanently.
	if os.Getenv("PUSHOVER_FANTASY_DUAL_SEND") != "" {
		if u, tkn := os.Getenv("PUSHOVER_USER_KEY"), os.Getenv("PUSHOVER_API_TOKEN"); u != "" && tkn != "" {
			d.Sinks = append(d.Sinks, &notify.PushoverSink{
				UserKey:     u,
				APIToken:    tkn,
				TenantLabel: resolveTenantLabel(context.Background(), uid),
			})
		}
	}

	notify.Default = d
}

// apnsSink returns nil when APNs is not configured, which is the normal state
// locally and in every test. All four inputs are required: the key pair to
// sign with, the team to sign as, the table holding device registrations,
// and a tenant to fan out to — an event with no tenant has no devices.
func apnsSink(uid lineupapi.UserID) notify.Sink {
	keyPEM, keyID := os.Getenv("APNS_AUTH_KEY"), os.Getenv("APNS_KEY_ID")
	teamID, table := os.Getenv("APNS_TEAM_ID"), os.Getenv("IDENTITY_TABLE")
	if keyPEM == "" || keyID == "" || teamID == "" || table == "" || uid == "" {
		return nil
	}
	key, err := apns.ParseAuthKey([]byte(keyPEM))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: APNS_AUTH_KEY unusable, push disabled: %v\n", err)
		return nil
	}
	devices, err := ddbPushDeviceStore(table)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: device store unavailable, push disabled: %v\n", err)
		return nil
	}
	client := apns.New(apns.NewTokenSource(key, keyID, teamID), &http.Client{Timeout: 30 * time.Second})
	return notify.NewAPNsSink(client, devices, uid)
}

func ddbPushDeviceStore(table string) (lineupapi.PushDeviceStore, error) {
	st, err := ddbuser.New(context.Background(), table)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// userLookup is the narrowest slice of the identity store resolveTenantLabel
// needs. It is deliberately smaller than tenantDirectory (tenantcreds.go),
// which also needs ConnectionStore to run a job as a tenant — this read only
// decorates a notification title and never gates writing anywhere, so it has
// no business requiring the connection half.
type userLookup interface {
	GetUser(ctx context.Context, id lineupapi.UserID) (*lineupapi.User, bool, error)
}

// resolveTenantLabel names the tenant a dual-send Pushover push should be
// tagged with, for PushoverSink.TenantLabel (rosterbot-b1oh) — the minimum
// per-tenant routing the bead's own text accepts in lieu of per-user Pushover
// keys: every tenant's dual-send traffic still lands on the deployment's one
// PUSHOVER_USER_KEY (the operator's phone), so a tester's push is tagged with
// their name instead of reading as the operator's own team.
//
// It returns "" — untagged, today's byte-identical behaviour — whenever a tag
// would be either meaningless or actively wrong:
//   - uid is empty: a single-tenant/local-dev run has no tenant to tag.
//   - uid IS the operator's own tenant (OPERATOR_USER_ID, see
//     viewsReportBelongsHere in projection_scope.go for the same comparison
//     used for a different report). The fix exists to stop OTHER tenants'
//     pushes reading as the operator's; a tag on the operator's own pushes
//     would just be noise on every message they already know is theirs.
//   - IDENTITY_TABLE is unset: there is no directory to look the tenant up
//     in, and this must not construct a store against an empty table name.
//   - the lookup errors, the record is missing, or DisplayName is empty:
//     nothing usable to tag with — refusing to guess matches the direction
//     tenantRunConfig takes on its own read failure (act on what is known).
func resolveTenantLabel(ctx context.Context, uid string) string {
	if uid == "" || uid == os.Getenv("OPERATOR_USER_ID") {
		return ""
	}
	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		return ""
	}
	store, err := ddbuser.New(ctx, table)
	if err != nil {
		return ""
	}
	return tenantLabelFrom(ctx, store, lineupapi.UserID(uid))
}

// tenantLabelFrom does the actual directory read, split out from
// resolveTenantLabel so the decision is testable against a stub rather than
// DynamoDB.
func tenantLabelFrom(ctx context.Context, dir userLookup, uid lineupapi.UserID) string {
	u, ok, err := dir.GetUser(ctx, uid)
	if err != nil || !ok {
		return ""
	}
	return u.DisplayName
}
