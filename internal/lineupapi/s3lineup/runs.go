package s3lineup

import (
	"context"
	"encoding/json"
	"fmt"
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
	// The window is the interface's documented contract (lineupapi.RunLookback),
	// pinned to the file adapter by runstoretest.
	recs, err := s.recent(ctx, lineupapi.RunLookback)
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

// readNewest reads the newest `limit` JSON records directly under blob's
// prefix (via flatKeys) and decodes each into T. Records whose object cannot
// be read or decoded are skipped rather than failing the listing — one
// malformed record must not blank the dashboard's run list or activity feed.
//
// BUT A TOTAL READ FAILURE IS NOT AN EMPTY LEDGER, and the difference is the
// whole reason this returns an error at all. Skipping every record turns
// "listed N keys and could not read one of them" into (nil, nil), which every
// caller is then entitled to read as "this tenant has never run": the admin
// tenants page renders a green "Never run" badge and attributes the row to
// nobody, and GET /v1/runs serves an empty list with a 200. That is the
// degrade-to-silence shape this repo keeps being bitten by — the same reason
// PrefixListing.Truncated exists rather than letting a short listing pass as a
// complete one.
//
// So: zero keys listed is an empty ledger and stays (nil, nil); keys listed
// with SOME readable is the partial case the skip rule is for; keys listed
// with NONE readable is a failure and says so. Cancellation is reported the
// moment it is seen rather than spending the rest of the loop skipping keys
// whose reads cannot possibly succeed — the caller's deadline expiring is
// exactly when a summary is most likely to be believed.
func readNewest[T any](ctx context.Context, blob *s3blob.Blob, limit int) ([]T, error) {
	keys, err := flatKeys(ctx, blob, "", limit)
	if err != nil {
		return nil, err
	}

	var out []T
	var lastErr error
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("run ledger: read cut short after %d of %d records: %w",
				len(out), len(keys), err)
		}
		data, found, err := blob.Get(ctx, k)
		if err != nil {
			lastErr = err
			continue
		}
		if !found {
			continue
		}
		var v T
		if json.Unmarshal(data, &v) == nil {
			out = append(out, v)
		}
	}
	if len(out) == 0 && len(keys) > 0 && lastErr != nil {
		return nil, fmt.Errorf("run ledger: none of the %d listed records could be read: %w",
			len(keys), lastErr)
	}
	return out, nil
}

// recent lists the newest `limit` ledger records and reads each.
func (s *RunsStore) recent(ctx context.Context, limit int) ([]lineupapi.RunDetail, error) {
	return readNewest[lineupapi.RunDetail](ctx, s.blob, limit)
}

var _ lineupapi.RunStore = (*RunsStore)(nil)
