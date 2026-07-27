package s3lineup

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// RunsStore is the S3-backed run ledger. Records live at <prefix><key>.json
// where key is lineupapi.RunKey (inverted-timestamp prefix), so a plain
// ascending listing returns newest first.
type RunsStore struct{ blob *s3blob.Blob }

// NewRuns builds a RunsStore. prefix should end in "/", e.g. "runs/".
func NewRuns(ctx context.Context, bucket, prefix string) (*RunsStore, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &RunsStore{blob: b}, nil
}

func ledgerKey(key string) string { return key + ".json" }

// PutRun writes (or overwrites) a run ledger record. Called by the run-ledger
// CLI command from inside the task.
func (s *RunsStore) PutRun(ctx context.Context, rec lineupapi.RunDetail) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.blob.PutJSON(ctx, ledgerKey(lineupapi.RunKey(rec.StartedAt, rec.ID)), data)
}

func (s *RunsStore) List(ctx context.Context, limit int) ([]lineupapi.Run, error) {
	recs, err := s.recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	runs := make([]lineupapi.Run, 0, len(recs))
	for _, r := range recs {
		runs = append(runs, r.Run)
	}
	return runs, nil
}

func (s *RunsStore) Get(ctx context.Context, id string) (*lineupapi.RunDetail, bool, error) {
	// Run detail lookups are always for a recent run (just triggered or visible
	// in the list), so scanning the newest window is sufficient and bounded.
	recs, err := s.recent(ctx, 200)
	if err != nil {
		return nil, false, err
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], true, nil
		}
	}
	return nil, false, nil
}

// flatKeys lists every ledger key immediately under prefix — i.e. with no
// further "/" in the remainder, which excludes per-run sub-objects like
// <prefix><id>/output.json. Keys come back relative to blob's own prefix, so a
// store-scoped Blob yields bare record names and a bucket-root Blob (the ledger
// migration) yields whole keys.
//
// If limit > 0, walking stops as soon as limit ledger keys have been collected;
// limit <= 0 collects every match. Because the walk follows continuation tokens,
// stopping early is safe: sub-objects sharing the prefix can fill whole pages
// ahead of the ledger block without hiding it (rosterbot-432).
func flatKeys(ctx context.Context, blob *s3blob.Blob, prefix string, limit int) ([]string, error) {
	var keys []string
	err := blob.Walk(ctx, prefix, func(o s3blob.Object) bool {
		if strings.Contains(strings.TrimPrefix(o.Key, prefix), "/") {
			return true
		}
		keys = append(keys, o.Key)
		return limit <= 0 || len(keys) < limit
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys) // defensive: ensure newest-first ordering
	return keys, nil
}

// recent lists the newest `limit` ledger records and reads each. Records whose
// object cannot be read or decoded are skipped rather than failing the listing —
// one malformed record must not blank the dashboard's run list.
func (s *RunsStore) recent(ctx context.Context, limit int) ([]lineupapi.RunDetail, error) {
	keys, err := flatKeys(ctx, s.blob, "", limit)
	if err != nil {
		return nil, err
	}

	var recs []lineupapi.RunDetail
	for _, k := range keys {
		data, found, err := s.blob.Get(ctx, k)
		if err != nil || !found {
			continue
		}
		var rec lineupapi.RunDetail
		if json.Unmarshal(data, &rec) == nil {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}

var _ lineupapi.RunStore = (*RunsStore)(nil)
