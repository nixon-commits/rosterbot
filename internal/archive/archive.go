// Package archive captures faithful daily snapshots of ephemeral upstream data
// (data that only exists "as of now" and is unrecoverable once the day passes).
// It is a leaf: it imports only the standard library. Concrete Sources live in
// their home packages and are wired together by cmd/archive.go.
package archive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Artifact is one file to archive: the upstream response bytes verbatim plus the
// filename it should land under within the source's dated partition.
type Artifact struct {
	Filename string
	Bytes    []byte
}

// Source fetches the ephemeral artifacts for one upstream (e.g. all HKB
// rankings, or all Savant CSVs) as of the given capture date.
type Source interface {
	Name() string
	Fetch(ctx context.Context, date time.Time) ([]Artifact, error)
}

// FuncSource adapts a plain fetch function into a Source, so cmd/archive.go can
// wire each package's ArchiveArtifacts without a bespoke type per source.
type FuncSource struct {
	N string
	F func(ctx context.Context, date time.Time) ([]Artifact, error)
}

func (s FuncSource) Name() string { return s.N }
func (s FuncSource) Fetch(ctx context.Context, date time.Time) ([]Artifact, error) {
	return s.F(ctx, date)
}

// Writer lays artifacts down under Root/<source>/dt=YYYY-MM-DD/<filename>.
type Writer struct{ Root string }

// Write is atomic per (source, date): it stages artifacts in a sibling temp dir,
// then swaps it into place, so a partial fetch never lands as a complete
// partition. Re-writing a date fully replaces that day's blobs (last-write-wins).
func (w Writer) Write(date time.Time, source string, arts []Artifact) error {
	dir := filepath.Join(w.Root, source, "dt="+date.UTC().Format("2006-01-02"))
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	for _, a := range arts {
		if err := os.WriteFile(filepath.Join(tmp, a.Filename), a.Bytes, 0o644); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// archiveHTTPClient is the shared client for raw archival fetches. Timeout is
// generous (Savant CSVs are the slowest); ctx carries any tighter deadline.
var archiveHTTPClient = &http.Client{Timeout: 30 * time.Second}

// getRetryBackoff controls Get's retry cadence; one entry per RETRY (so
// len+1 total attempts). Overridable in tests to eliminate sleep delays.
var getRetryBackoff = []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}

// Get performs a raw HTTP GET and returns the response body verbatim. It is the
// single fetch primitive every Source uses, so the GET+status+read block is not
// duplicated across the hkb/projections/waivers/prospects archive files.
//
// Transient failures (transport errors and 5xx responses) are retried according
// to getRetryBackoff. 4xx responses are permanent and returned immediately.
func Get(ctx context.Context, url string) ([]byte, error) {
	maxAttempts := 1 + len(getRetryBackoff)
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err // bad URL — not retryable
		}
		resp, err := archiveHTTPClient.Do(req)
		if err != nil {
			// Transport error: retryable.
			lastErr = err
		} else if resp.StatusCode == http.StatusOK {
			b, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			return b, readErr
		} else {
			// Non-200: drain body so the connection can be reused, then decide.
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
			if resp.StatusCode < 500 {
				// 4xx: permanent failure — do not retry.
				return nil, lastErr
			}
			// 5xx: retryable (fall through to sleep + next attempt).
		}

		// Retryable failure: sleep before the next attempt if one remains.
		if attempt+1 < maxAttempts {
			d := getRetryBackoff[attempt]
			if d > 0 {
				timer := time.NewTimer(d)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	return nil, fmt.Errorf("GET %s: failed after %d attempt(s): %w", url, maxAttempts, lastErr)
}
