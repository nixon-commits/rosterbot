package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// ledgerWindow is how many of the newest ledger records to read when computing
// a streak. At ~26 runs/day across all jobs that is roughly a week of history —
// far past any outage whose exact length still matters, and bounded so the
// Lambda's work does not grow with the ledger.
const ledgerWindow = 200

// heartbeatCap bounds the heartbeat's time-bounded read. The horizon (twice the
// longest declared cadence, ~16 days) is the real stopping condition; this is
// only a backstop against a ledger that has grown far denser than the ~26
// runs/day it was sized for, so one slow invocation cannot run out the 60s
// Lambda ceiling.
const heartbeatCap = 1500

// fetchConcurrency is how many ledger objects are read at once. Sequential reads
// of the 200-record streak window already cost 2-4s of the 60s budget, and the
// heartbeat's horizon is several times that; a timeout here is a *silently
// missed* alert, so the read is widened rather than left to grow into one.
const fetchConcurrency = 32

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

// keys returns the newest ledger keys, newest first, at most limit of them.
//
// Ledger keys carry an inverted-timestamp prefix, so an ascending listing is
// reverse-chronological. Sub-objects that share the prefix are skipped, and the
// walk follows continuation tokens before stopping early — the combination that
// makes a "newest N" listing safe (rosterbot-432).
func (l *ledgerReader) keys(ctx context.Context, limit int) ([]string, error) {
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
	return keys, nil
}

// fetch reads one chunk of keys concurrently, preserving the input order.
// Objects that fail to read or decode are dropped: one unreadable record must
// not cost the whole history, and every caller's judgement degrades gracefully
// with a shorter one.
func (l *ledgerReader) fetch(ctx context.Context, keys []string) []opsalert.Record {
	out := make([]*opsalert.Record, len(keys))
	var wg sync.WaitGroup
	sem := make(chan struct{}, fetchConcurrency)
	for i, k := range keys {
		wg.Add(1)
		go func(i int, k string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, found, err := l.blob.Get(ctx, k)
			if err != nil || !found {
				return
			}
			var r opsalert.Record
			if json.Unmarshal(data, &r) == nil {
				out[i] = &r
			}
		}(i, k)
	}
	wg.Wait()

	recs := make([]opsalert.Record, 0, len(keys))
	for _, r := range out {
		if r != nil {
			recs = append(recs, *r)
		}
	}
	return recs
}

// recent returns the newest `limit` records, newest first.
func (l *ledgerReader) recent(ctx context.Context, limit int) ([]opsalert.Record, error) {
	keys, err := l.keys(ctx, limit)
	if err != nil {
		return nil, err
	}
	return l.fetch(ctx, keys), nil
}

// since returns every record started at or after cutoff, newest first.
//
// This is the heartbeat's read, and it is bounded by *time* rather than by
// count on purpose. A fixed count is unsound for the question the heartbeat
// asks: the hourly Lineup job writes ~14 records a day, so a weekly job's
// perfectly healthy run sits ~180 records deep and a 200-record window would
// drop it on any busy week — reporting a live job as never having run. Reading
// back to a cutoff derived from the declared cadences makes the absence of a
// record proof of absence.
//
// Keys are time-ordered, so the walk stops at the first chunk that reaches past
// the cutoff rather than reading the whole ledger.
func (l *ledgerReader) since(ctx context.Context, cutoff time.Time) ([]opsalert.Record, error) {
	keys, err := l.keys(ctx, heartbeatCap)
	if err != nil {
		return nil, err
	}

	var recs []opsalert.Record
	for start := 0; start < len(keys); start += fetchConcurrency {
		end := min(start+fetchConcurrency, len(keys))
		chunk := l.fetch(ctx, keys[start:end])
		reached := false
		for _, r := range chunk {
			if t := r.Started(); !t.IsZero() && t.Before(cutoff) {
				reached = true
				continue
			}
			recs = append(recs, r)
		}
		if reached {
			break
		}
	}
	return recs, nil
}
