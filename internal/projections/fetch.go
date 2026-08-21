package projections

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// fgBackoff is the pause before each RETRY, so len(fgBackoff)+1 is the attempt
// budget. Kept short and bounded: this runs inside the hourly Lineup job, and a
// fetch that hangs on retries is worse than one that fails into the stale
// fallback the caller already has.
//
// It is a var so tests can drop the sleeps.
var fgBackoff = []time.Duration{time.Second, 4 * time.Second}

// fetchJSON GETs url and decodes the JSON body into out, retrying the failures
// that are known to clear on their own.
//
// It is worth being precise about what this does and does not buy. FanGraphs
// put /api/projections behind a Cloudflare INTERACTIVE challenge on 2026-08-19
// (cf-mitigated: challenge), and that block moves on a scale of HOURS, not
// seconds — measured across two days it held from roughly 17:00 to 03:00 UTC
// and cleared outside that window. So these retries do not defeat it, and are
// not meant to: they cover the single-attempt blip (a dropped connection, a
// 5xx, a rate-limit) that used to burn a whole hourly run's fetch. The real
// protection against the challenge window is the stale fallback plus the fact
// that some run in the clean window lands the refresh.
func fetchJSON(url, label string, out any) error {
	client := &http.Client{Timeout: 15 * time.Second}

	var lastErr error
	for attempt := 0; attempt <= len(fgBackoff); attempt++ {
		if attempt > 0 {
			time.Sleep(fgBackoff[attempt-1])
		}

		resp, err := client.Get(url)
		if err != nil {
			lastErr = fmt.Errorf("%s fetch: %w", label, err)
			continue // a transport error is always worth one more try
		}

		if resp.StatusCode != http.StatusOK {
			// Drain before closing so the connection can be reused by the
			// retry rather than being torn down and redialed.
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: status %d", label, resp.StatusCode)
			if !retryableStatus(resp.StatusCode) {
				return lastErr
			}
			continue
		}

		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		if err != nil {
			// A truncated body is retryable; a genuinely malformed one will
			// simply fail again and surface on the last attempt.
			lastErr = fmt.Errorf("%s json: %w", label, err)
			continue
		}
		return nil
	}
	return lastErr
}

// retryableStatus reports whether a status is worth another attempt.
//
// 403 is included, which is not the usual rule — normally a 403 is a durable
// authorization answer and retrying it is waste. Here it is the status
// Cloudflare returns for an unsolved challenge, and that IS intermittent, so
// treating it as permanent would give up on the one failure this path most
// often sees. Nothing on this endpoint is authenticated, so there is no real
// 403 for the looser rule to mask.
func retryableStatus(code int) bool {
	return code == http.StatusForbidden ||
		code == http.StatusTooManyRequests ||
		code >= http.StatusInternalServerError
}
