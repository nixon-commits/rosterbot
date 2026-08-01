package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// ledgerWindow is how many of the newest ledger records to read when computing
// a streak. At ~26 runs/day across all jobs that is roughly a week of history —
// far past any outage whose exact length still matters, and bounded so the
// Lambda's work does not grow with the ledger.
const ledgerWindow = 200

// ledgerReader reads run ledger records straight out of S3.
//
// It goes through internal/s3blob rather than internal/lineupapi/s3lineup —
// which already has this code — because s3lineup imports internal/lineupapi,
// and that transitively pulls internal/fantrax and chromedp into the Lambda.
// s3blob depends only on the AWS S3 SDK.
type ledgerReader struct{ blob *s3blob.Blob }

func newLedgerReader(ctx context.Context, bucket string) (*ledgerReader, error) {
	b, err := s3blob.New(ctx, bucket, "runledger/")
	if err != nil {
		return nil, err
	}
	return &ledgerReader{blob: b}, nil
}

// recent returns the newest `limit` records, newest first. Ledger keys carry an
// inverted-timestamp prefix, so an ascending listing is reverse-chronological.
//
// Sub-objects that share the prefix are skipped, and the walk follows
// continuation tokens before stopping early — the combination that makes a
// "newest N" listing safe (rosterbot-432).
func (l *ledgerReader) recent(ctx context.Context, limit int) ([]opsalert.Record, error) {
	var keys []string
	err := l.blob.Walk(ctx, "", func(o s3blob.Object) bool {
		if strings.Contains(o.Key, "/") {
			return true
		}
		keys = append(keys, o.Key)
		return len(keys) < limit
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys) // defensive: ensure newest-first ordering

	var recs []opsalert.Record
	for _, k := range keys {
		data, found, err := l.blob.Get(ctx, k)
		if err != nil || !found {
			continue
		}
		var r opsalert.Record
		if json.Unmarshal(data, &r) == nil {
			recs = append(recs, r)
		}
	}
	return recs, nil
}
