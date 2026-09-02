package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// tenantStores builds one lineupapi.TenantView per tenant, on demand, and keeps
// it.
//
// Caching is not an optimisation to revisit later. Each view is eight S3-backed
// stores and every constructor resolves AWS credentials, so building them per
// request would put a credential lookup on the path of every dashboard poll —
// and the dashboard polls. Tenant count is a pilot cohort, so an unbounded map
// is the right size of solution; it would need bounding at a scale this project
// does not have.
type tenantStores struct {
	bucket string
	// defaultTenant answers for the bearer-token caller, which has no UserID by
	// construction. See For.
	defaultTenant lineupapi.UserID

	mu   sync.Mutex
	sets map[lineupapi.UserID]lineupapi.TenantView
}

func newTenantStores(bucket string, defaultTenant lineupapi.UserID) *tenantStores {
	return &tenantStores{
		bucket:        bucket,
		defaultTenant: defaultTenant,
		sets:          map[lineupapi.UserID]lineupapi.TenantView{},
	}
}

// For returns the caller's own stores.
//
// AN EMPTY uid IS THE BEARER-TOKEN CALLER, not an anonymous one — authorization
// has already happened by the time this runs, and that path deliberately never
// touches the user store, so it has no UserID to offer. It resolves to the
// deployment's own tenant, which keeps break-glass CLI access working and is
// safe because the token is the operator's. Treating it as an empty prefix
// instead would point it at the pre-migration layout, where nothing is written
// any more.
//
// An error here must NOT be softened into "use the default": that is the bug
// this whole type exists to remove, and it would reappear on the one path
// nobody exercises. lineupapi.tenantView turns the error into a 503.
func (t *tenantStores) For(ctx context.Context, uid lineupapi.UserID) (lineupapi.TenantView, error) {
	if uid == "" {
		uid = t.defaultTenant
	}
	if uid == "" {
		return lineupapi.TenantView{}, fmt.Errorf("no tenant for this caller and no default configured")
	}

	t.mu.Lock()
	if v, ok := t.sets[uid]; ok {
		t.mu.Unlock()
		return v, nil
	}
	t.mu.Unlock()

	v, err := t.build(ctx, uid)
	if err != nil {
		return lineupapi.TenantView{}, err
	}

	t.mu.Lock()
	t.sets[uid] = v
	t.mu.Unlock()
	return v, nil
}

// build constructs the nine per-tenant stores. Prefixes come from
// layout.Artifact.PrefixFor rather than being composed here, so the reader and
// the producers cannot disagree about where a tenant's data lives — which is
// exactly how the read half came to be pointed at one tenant while the write
// half fanned out correctly.
func (t *tenantStores) build(ctx context.Context, uid lineupapi.UserID) (lineupapi.TenantView, error) {
	tenant := string(uid)
	var v lineupapi.TenantView
	var err error

	store := func(prefix string) (*s3lineup.Store, error) {
		return s3lineup.New(ctx, t.bucket, prefix)
	}
	if v.Lineups, err = store(layout.Lineup.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: lineup store: %w", uid, err)
	}
	if v.Trades, err = store(layout.Trades.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: trades store: %w", uid, err)
	}
	if v.TradeValues, err = store(layout.TradeValues.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: trade values store: %w", uid, err)
	}
	if v.AvailablePool, err = store(layout.AvailablePool.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: available pool store: %w", uid, err)
	}
	if v.RosterValues, err = store(layout.RosterValues.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: roster values store: %w", uid, err)
	}
	if v.Reports, err = store(layout.Reports.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: reports store: %w", uid, err)
	}
	if v.Runs, err = s3lineup.NewRuns(ctx, t.bucket, layout.RunLedger.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: runs store: %w", uid, err)
	}
	if v.Notifications, err = s3lineup.NewNotifications(ctx, t.bucket, layout.Notification.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: notifications store: %w", uid, err)
	}
	if v.Output, err = s3lineup.NewOutput(ctx, t.bucket, layout.RunOutput.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: output store: %w", uid, err)
	}
	if v.Progress, err = s3lineup.NewProgress(ctx, t.bucket, layout.Progress.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: progress store: %w", uid, err)
	}
	return v, nil
}
