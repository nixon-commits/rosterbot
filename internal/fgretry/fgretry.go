// Package fgretry holds the one retry predicate for rosterbot's FanGraphs
// fetches. It is a stdlib-only leaf so both call paths can share it without
// either importing the other: internal/archive for the daily capture, and
// internal/projections for the hourly lineup fetch.
//
// It exists because those two paths disagreed. archive.Get classed every 4xx
// as permanent, while projections.fetchJSON treated 403 as a blip. Only the
// second was right about this upstream, and the cost of the first is measured:
// FanGraphs answered 403 to every fetch from 2026-11-24, and archive's daily
// run gave up after 1-2 seconds without a single retry on each of four
// consecutive days. dt=2026-11-24 through dt=2026-11-27 of archive/projections
// and archive/prospects are therefore gone for good -- FanGraphs publishes no
// history, so nothing can backfill them.
//
// The rule below is NARROW ON PURPOSE and must not be adopted elsewhere by
// default. internal/fantrax/retry.go and internal/fantrax/mlb_backfill.go each
// state at length why their own upstream needs the stricter rule.
package fgretry

import "net/http"

// Retryable reports whether an HTTP status from FanGraphs is worth another
// attempt.
//
// 403 is the entry that matters, and it is not the usual rule -- normally a
// 403 is a durable authorization answer and retrying it is waste. FanGraphs'
// /api/projections and /api/prospects are unauthenticated, so their 403 is
// only ever Cloudflare's status for an unsolved interactive challenge, and
// that IS intermittent. There is no real 403 on these routes for the looser
// rule to mask. On an authenticated route the same rule would be wrong, which
// is what the two internal/fantrax predicates exist to say.
//
// 5xx covers Cloudflare's own 52x family (520 unknown, 522 connect timeout,
// 524 origin timeout) as well as ordinary 502/503/504. All of them describe an
// edge or an origin that failed to answer, none of them a durable verdict
// about the request.
func Retryable(code int) bool {
	return code == http.StatusForbidden ||
		code == http.StatusTooManyRequests ||
		code >= http.StatusInternalServerError
}
