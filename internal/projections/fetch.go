package projections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fgretry"
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
//
// That 17:00-03:00 map is no longer accurate, and nothing should be scheduled
// against it. From 2026-11-24 the block held at 14:15 UTC on four consecutive
// days, which is inside what used to be the clean morning window, so the
// window has either widened or become continuous. Treat its shape as unknown.
func fetchJSON(ctx context.Context, url, label string, out any) error {
	client := &http.Client{Timeout: 15 * time.Second}

	var lastErr error
	for attempt := 0; attempt <= len(fgBackoff); attempt++ {
		if attempt > 0 {
			time.Sleep(fgBackoff[attempt-1])
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("%s: build request: %w", label, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s fetch: %w", label, err)
			continue // a transport error is always worth one more try
		}

		if resp.StatusCode != http.StatusOK {
			// Drain before closing so the connection can be reused by the
			// retry rather than being torn down and redialed.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("%s: status %d", label, resp.StatusCode)
			if !fgretry.Retryable(resp.StatusCode) {
				return lastErr
			}
			continue
		}

		err = json.NewDecoder(resp.Body).Decode(out)
		_ = resp.Body.Close()
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
