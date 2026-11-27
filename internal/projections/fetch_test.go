package projections

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fgretry"
)

// noBackoff removes the sleeps so a retry test costs nothing, and restores the
// real schedule afterwards.
func noBackoff(t *testing.T, attempts int) {
	t.Helper()
	prev := fgBackoff
	t.Cleanup(func() { fgBackoff = prev })
	fgBackoff = make([]time.Duration, attempts-1)
}

func TestFetchJSON_RetriesThroughAnIntermittentCloudflareChallenge(t *testing.T) {
	noBackoff(t, 3)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusForbidden) // cf-mitigated: challenge
			return
		}
		w.Write([]byte(`[{"PlayerName":"Test Player"}]`))
	}))
	defer srv.Close()

	var rows []fgRow
	if err := fetchJSON(t.Context(), srv.URL, "test", &rows); err != nil {
		t.Fatalf("a challenge that clears on retry must not fail the fetch: %v", err)
	}
	if calls != 3 {
		t.Fatalf("made %d attempts, want 3", calls)
	}
	if len(rows) != 1 || rows[0].PlayerName != "Test Player" {
		t.Fatalf("decoded %+v, want the row from the successful attempt", rows)
	}
}

func TestFetchJSON_DoesNotRetryAStatusThatWillNotChange(t *testing.T) {
	noBackoff(t, 3)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var rows []fgRow
	if err := fetchJSON(t.Context(), srv.URL, "test", &rows); err == nil {
		t.Fatal("a 404 is an error, not a blip")
	}
	if calls != 1 {
		t.Fatalf("made %d attempts; a permanent status must not be retried", calls)
	}
}

func TestFetchJSON_ReportsTheLastStatusAfterExhaustingRetries(t *testing.T) {
	noBackoff(t, 3)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	var rows []fgRow
	err := fetchJSON(t.Context(), srv.URL, "test", &rows)
	if err == nil {
		t.Fatal("want an error once retries are exhausted")
	}
	if calls != 3 {
		t.Fatalf("made %d attempts, want 3", calls)
	}
	// The stale-cache alert quotes this text; "403" is what makes it diagnosable.
	if got := err.Error(); got != "test: status 403" {
		t.Fatalf("err = %q, want %q", got, "test: status 403")
	}
}

// The sibling of archive's TestGetRetryPolicyFollowsSharedPredicate. Both
// FanGraphs fetch paths now answer to one predicate; this is the half that
// pins the live lineup path to it. The two together are what stops the paths
// from drifting back apart -- which they had, with archive calling every 4xx
// permanent while this one correctly called 403 a blip.
func TestFetchJSON_RetryPolicyFollowsSharedPredicate(t *testing.T) {
	noBackoff(t, 3)
	for _, code := range []int{400, 401, 403, 404, 410, 429, 500, 502, 503, 524} {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(code)
		}))

		var rows []fgRow
		if err := fetchJSON(t.Context(), srv.URL, "test", &rows); err == nil {
			t.Errorf("status %d: want an error", code)
		}
		srv.Close()

		want := 1
		if fgretry.Retryable(code) {
			want = len(fgBackoff) + 1
		}
		if calls != want {
			t.Errorf("status %d: made %d attempts, want %d (fgretry.Retryable = %v)",
				code, calls, want, fgretry.Retryable(code))
		}
	}
}
