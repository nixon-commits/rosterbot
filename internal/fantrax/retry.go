package fantrax

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// Fantrax sits behind Cloudflare (`server: cloudflare`), so a stalled origin
// surfaces here as a 524 — Cloudflare's own "origin took longer than ~100s"
// status, not something Fantrax ever sends itself. On 2026-08-23T23:00Z one
// such 524 on getAllMatchups killed a whole hourly `optimize --matchup` run
// (ledger d9b89a97, 221s against a 16-26s norm) and paged the operator.
//
// The call had no defence at either layer: the fork makes ONE request and
// returns on any non-200, and `allMatchups` is deliberately memoized
// in-memory only (the in-progress week's scores mutate intra-day), so unlike
// every disk-cached read beside it there is no stale copy to fall back on.
// That is why this specific call broke and `GetSeasonDateRange` — same
// function, one line earlier, `tierStable` on disk — did not.
//
// This mirrors projections.fetchJSON, which got the same treatment for the
// same reason: it "covers the single-attempt blip that used to burn a whole
// run's fetch". It deliberately does NOT live in the fork's transport, where
// it would also wrap edit_roster/set_period_matchups — retrying a MUTATING
// POST after a 524 is unsafe, because a timed-out origin may have applied the
// change before it stopped answering.
var errRetryExhausted = errors.New("retries exhausted")

// statusRe pulls the code out of the fork's error text. The fork returns a
// bare fmt.Errorf("API returned non-200 status code: %d") with no typed error
// and no response handle, so matching the string is the only seam available
// without a fork release (which would also need the `replace` pin bumped in
// both root and lambda/go.mod, then `make check-pins`).
var statusRe = regexp.MustCompile(`non-200 status code: (\d{3})`)

// statusOf reports the HTTP status carried by a fork error, and whether one
// was found at all. A transport error (no response) yields (0, false) — the
// caller treats that as retryable on its own terms, since a connection that
// never completed cannot have been applied server-side.
func statusOf(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	m := statusRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	code, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return 0, false
	}
	return code, true
}

// fantraxBackoff is the pause before each RETRY, so len(fantraxBackoff)+1 is
// the attempt count.
//
// TUNABLE. The observed failure mode is a SINGLE blip (1 failure in 146
// optimize runs over 10 days), and against that a short first pause is what
// matters — attempt 2 costs 2s and rescues the run. The pauses are longer
// than projections.fgBackoff's {1s, 4s} because that schedule was tuned for a
// fast-failing 403, whereas a 524 means the origin already burned ~100s and
// an immediate retry likely lands on the same stall.
//
// The cost ceiling is the thing to weigh in review: against a PERSISTENTLY
// stalled origin this makes the run hang ~3x100s + 10s before failing,
// against the 221s it took today. That stays far inside the hourly cadence,
// so no two runs can overlap, but it does delay the failure signal.
var fantraxBackoff = []time.Duration{2 * time.Second, 8 * time.Second}

// retryableFantraxStatus reports whether a Fantrax status is worth another
// attempt.
//
// This is deliberately NOT projections.retryableStatus. That one retries 403,
// which is correct there and wrong here: FanGraphs' /api/projections is
// unauthenticated, so its 403 is only ever Cloudflare's unsolved-challenge
// status and there is no real 403 for the looser rule to mask. Every call on
// this path IS authenticated, so a 401/403 is a genuine session answer that
// re-logging-in fixes and retrying cannot — the session ladder owns that
// recovery, and burning three attempts first only delays it.
//
// 5xx covers Cloudflare's own 52x family (520 unknown, 522 connect timeout,
// 524 origin timeout) alongside ordinary 502/503/504; all of them describe an
// edge or origin that failed to answer, none of them a durable verdict about
// the request.
func retryableFantraxStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code >= http.StatusInternalServerError
}

// withRetry runs fetch until it succeeds, until the policy declines the
// failure, or until the backoff schedule is spent. It is for IDEMPOTENT READS
// ONLY — never wire an apply/write path through it.
//
// label names the call in the returned error so an exhausted retry is
// diagnosable from the ledger's log_tail alone.
func withRetry[T any](label string, backoff []time.Duration, fetch func() (T, error)) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt <= len(backoff); attempt++ {
		if attempt > 0 {
			time.Sleep(backoff[attempt-1])
		}

		v, err := fetch()
		if err == nil {
			return v, nil
		}
		lastErr = err

		code, ok := statusOf(err)
		if ok && !retryableFantraxStatus(code) {
			return zero, err
		}
	}
	return zero, fmt.Errorf("%s: %w after %d attempts: %w",
		label, errRetryExhausted, len(backoff)+1, lastErr)
}
