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
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
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
	b, err := s3blob.New(ctx, bucket, layout.RunLedger.S3Prefix)
	if err != nil {
		return nil, err
	}
	return &ledgerReader{blob: b}, nil
}

// isLedgerRecord reports whether a key's basename is a run-ledger record.
//
// The shape comes from lineupapi.RunKey: fmt.Sprintf("%010d-%s", inv, id) plus
// ".json". The zero padding is what makes lexicographic order equal
// chronological order, and the id may itself contain hyphens
// ("local-20260617211937"), so only the FIRST segment is checked.
//
// Shape is the discriminator rather than key depth. The previous test — skip
// anything containing a slash — was written to stop sub-objects sharing the
// prefix from hiding the ledger block (rosterbot-432), and it did that job
// until the ledger itself moved under user=<uid>/, at which point it excluded
// every real record. Shape survives that move, and is strictly stronger: it
// also rejects a stray top-level object, which the depth test never did.
func isLedgerRecord(base string) bool {
	if !strings.HasSuffix(base, ".json") {
		return false
	}
	stamp, rest, ok := strings.Cut(strings.TrimSuffix(base, ".json"), "-")
	if !ok || stamp == "" || rest == "" {
		return false
	}
	for _, r := range stamp {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// basename returns the part of a key after the last slash.
func basename(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// keys returns the newest ledger keys, newest first, at most limit of them.
//
// ORDERING IS ON THE BASENAME, NOT THE WHOLE KEY, and that is the whole
// difficulty. Ledger keys carry an inverted-timestamp prefix, so an ascending
// listing is reverse-chronological — but only WITHIN one partition. Since the
// tenant cutover the ledger is spread across runledger/user=<uid>/ partitions,
// and "user=" sorts after every digit, so ordering on the whole key would put
// every pre-migration flat record ahead of every tenant record no matter when
// they ran. The basename is the inverted timestamp for flat and tenant records
// alike, so sorting on it interleaves partitions correctly.
//
// The consequence is that the LISTING can no longer stop early: a newer record
// may live in a partition that sorts last, so every key has to be seen before
// the newest N are known. Only the listing pays this — the fetch, which is the
// expensive part at one GetObject per record, still stops at limit. At the
// ~26 runs/day this is sized for that is a handful of ListObjectsV2 calls, and
// the walk follows continuation tokens so a partition cannot be hidden behind a
// page boundary (rosterbot-432, which partitions make easier to hit: one page
// can now be filled entirely by one tenant).
func (l *ledgerReader) keys(ctx context.Context, limit int) ([]string, error) {
	var keys []string
	err := l.blob.Walk(ctx, "", func(o s3blob.Object) bool {
		if isLedgerRecord(basename(o.Key)) {
			keys = append(keys, o.Key)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(keys, func(i, j int) bool {
		bi, bj := basename(keys[i]), basename(keys[j])
		if bi != bj {
			return bi < bj
		}
		return keys[i] < keys[j] // stable across partitions on a basename collision
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
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
