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
			d.Sinks = append(d.Sinks, &notify.PushoverSink{UserKey: u, APIToken: tkn})
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
