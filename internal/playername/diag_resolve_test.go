//go:build diag

// Temporary diagnostic harness for rosterbot-i52. Build-tagged so it never
// runs in CI. Run with:
//
//	go test -tags diag ./internal/playername/ -run TestDiagResolve -v -timeout 20m
package playername

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	mlb "github.com/pmurley/go-mlb"
)

// diagNames is the realistic flagged-name set, supplied via env as a
// newline-separated list so the harness has no Fantrax dependency.
func diagNames(t *testing.T) []string {
	raw := os.Getenv("DIAG_NAMES")
	if raw == "" {
		t.Skip("DIAG_NAMES unset")
	}
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func newDiagClient() *mlb.Client {
	return mlb.NewClient(
		mlb.WithBaseURL(mlbBaseURL),
		mlb.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
		mlb.WithCache(nil),
	)
}

// TestDiagResolve answers one question: when ResolveMLBAMIDs leaves a name
// unresolved, is that because the batch search mechanism dropped it, or
// because MLB genuinely has no match for that string?
func TestDiagResolve(t *testing.T) {
	names := diagNames(t)
	t.Logf("input names: %d", len(names))

	// --- Pass 1: the production path, exactly as backfillDailyFPts calls it.
	start := time.Now()
	rp, err := ResolveMLBAMIDsNoCache(names)
	if err != nil {
		t.Fatalf("ResolveMLBAMIDsNoCache: %v", err)
	}
	t.Logf("batch pass: %d players indexed, %d name variants, took %s",
		len(rp.ByID), len(rp.ByName), time.Since(start).Round(time.Millisecond))

	var missing []string
	for _, n := range names {
		if id, ok := rp.ByName[Normalize(n)]; !ok || id == 0 {
			missing = append(missing, n)
		}
	}
	t.Logf("UNRESOLVED after batch pass: %d/%d", len(missing), len(names))
	for _, m := range missing {
		t.Logf("   miss: %q (normalized %q)", m, Normalize(m))
	}
	if len(missing) == 0 {
		t.Log("no misses to re-test individually")
		return
	}

	// --- Pass 2: re-search each miss ALONE. If it resolves now, the batch
	// mechanism dropped it; if it still misses, MLB has no match for the string.
	client := newDiagClient()
	ctx := context.Background()
	var recovered, genuine []string
	for _, m := range missing {
		people, err := client.People.Search(ctx,
			mlb.WithNames(m),
			mlb.WithQueryParam("sportIds", "11,12,13,14,16,1"),
		)
		if err != nil {
			t.Logf("   solo-search ERROR for %q: %v", m, err)
			continue
		}
		var hit bool
		for _, p := range people {
			if Normalize(p.FullName) == Normalize(m) {
				hit = true
				t.Logf("   RECOVERED solo: %q -> %d (%s)", m, p.ID, p.FullName)
				break
			}
		}
		if hit {
			recovered = append(recovered, m)
		} else {
			genuine = append(genuine, m)
			var sample []string
			for i, p := range people {
				if i >= 3 {
					break
				}
				sample = append(sample, p.FullName)
			}
			t.Logf("   GENUINE miss: %q — search returned %d, e.g. %v", m, len(people), sample)
		}
		time.Sleep(150 * time.Millisecond)
	}

	t.Logf("=== VERDICT: %d dropped by batch mechanism, %d genuine name misses ===",
		len(recovered), len(genuine))
}
