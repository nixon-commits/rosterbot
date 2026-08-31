package playername

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestResolveMLBAMIDs_BridgesNicknames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/people/search", "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"people": [
					{"id": 815888, "fullName": "Leo De Vries", "firstName": "Leodalis", "lastName": "De Vries", "useName": "Leo"}
				]
			}`))
		default:
			// An explicitly EMPTY season index, rather than a 404 that fails the
			// build by accident: these tests exist to exercise the SEARCH
			// fallback and must say which path they are on.
			if strings.HasPrefix(r.URL.Path, "/api/v1/sports/") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"people":[]}`))
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := mlbBaseURL
	mlbBaseURL = srv.URL
	resetSeasonIndexMemo()
	defer func() { mlbBaseURL = old; resetSeasonIndexMemo() }()

	rp, err := ResolveMLBAMIDsNoCache([]string{"Leo De Vries", "Leodalis De Vries"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leoID, ok := rp.ByName[Normalize("Leo De Vries")]
	if !ok || leoID != 815888 {
		t.Errorf("expected Leo De Vries → 815888, got %d (ok=%v)", leoID, ok)
	}

	leodalisID, ok := rp.ByName[Normalize("Leodalis De Vries")]
	if !ok || leodalisID != 815888 {
		t.Errorf("expected Leodalis De Vries → 815888, got %d (ok=%v)", leodalisID, ok)
	}

	if rp.ByID[815888] != "Leo De Vries" {
		t.Errorf("expected ByID[815888] = Leo De Vries, got %q", rp.ByID[815888])
	}
}

func TestResolveMLBAMIDs_DeduplicatesNames(t *testing.T) {
	searchCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/people/search":
			searchCalls++
			fallthrough
		case "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"people": [
					{"id": 100, "fullName": "Mike Trout", "firstName": "Michael", "lastName": "Trout", "useName": "Mike"}
				]
			}`))
		default:
			// An explicitly EMPTY season index, rather than a 404 that fails the
			// build by accident: these tests exist to exercise the SEARCH
			// fallback and must say which path they are on.
			if strings.HasPrefix(r.URL.Path, "/api/v1/sports/") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"people":[]}`))
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := mlbBaseURL
	mlbBaseURL = srv.URL
	resetSeasonIndexMemo()
	defer func() { mlbBaseURL = old; resetSeasonIndexMemo() }()

	// Same name twice — should only search once after dedup.
	_, err := ResolveMLBAMIDsNoCache([]string{"Mike Trout", "mike trout"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searchCalls != 1 {
		t.Errorf("expected 1 search call, got %d", searchCalls)
	}
}

// twoNamesakeServer returns two players sharing a normalized name — one active,
// one retired — in the caller's chosen order, so the collision can be driven
// from both directions.
func twoNamesakeServer(t *testing.T, activeFirst bool) *httptest.Server {
	t.Helper()
	active := `{"id":514888,"fullName":"Jose Altuve","firstName":"Jose","lastName":"Altuve","active":true}`
	retired := `{"id":501447,"fullName":"Jose Altuve","firstName":"Jose","lastName":"Altuve","active":false}`
	order := retired + "," + active
	if activeFirst {
		order = active + "," + retired
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/people/search", "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"people":[` + order + `]}`))
		default:
			// An explicitly EMPTY season index, rather than a 404 that fails the
			// build by accident: these tests exist to exercise the SEARCH
			// fallback and must say which path they are on.
			if strings.HasPrefix(r.URL.Path, "/api/v1/sports/") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"people":[]}`))
				return
			}
			http.NotFound(w, r)
		}
	}))
}

// MLB's people-search spans historical players, so common names collide with
// retired namesakes. Writing the name key unconditionally meant last-write-wins,
// and since the search batches run in parallel the winner was nondeterministic:
// "Jose Altuve" resolved to a retired catcher rather than the active 2B
// (rosterbot-bms). The active player must win from either arrival order.
func TestResolveMLBAMIDs_PrefersActivePlayerOverRetiredNamesake(t *testing.T) {
	for _, activeFirst := range []bool{true, false} {
		srv := twoNamesakeServer(t, activeFirst)
		old := mlbBaseURL
		mlbBaseURL = srv.URL
		resetSeasonIndexMemo()

		rp, err := ResolveMLBAMIDsNoCache([]string{"Jose Altuve"})
		mlbBaseURL = old
		resetSeasonIndexMemo()
		srv.Close()
		if err != nil {
			t.Fatalf("activeFirst=%v: %v", activeFirst, err)
		}
		if got := rp.ByName[Normalize("Jose Altuve")]; got != 514888 {
			t.Errorf("activeFirst=%v: Jose Altuve → %d, want 514888 (the active player)", activeFirst, got)
		}
	}
}

// flakySearchServer fails the people-search endpoint its first failN calls,
// then serves a normal result. /people (the detail fetch) always succeeds.
func flakySearchServer(t *testing.T, failN int) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/people/search":
			calls++
			if calls <= failN {
				http.Error(w, "upstream busy", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"people":[{"id":100,"fullName":"Mike Trout","firstName":"Mike","lastName":"Trout","active":true}]}`))
		case "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"people":[{"id":100,"fullName":"Mike Trout","firstName":"Mike","lastName":"Trout","active":true}]}`))
		default:
			// An explicitly EMPTY season index, rather than a 404 that fails the
			// build by accident: these tests exist to exercise the SEARCH
			// fallback and must say which path they are on.
			if strings.HasPrefix(r.URL.Path, "/api/v1/sports/") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"people":[]}`))
				return
			}
			http.NotFound(w, r)
		}
	}))
	return srv, &calls
}

// A search batch that fails once must be retried rather than silently dropping
// all 25 of its names (rosterbot-i52).
func TestResolveMLBAMIDs_RetriesTransientSearchFailure(t *testing.T) {
	srv, calls := flakySearchServer(t, 1)
	defer srv.Close()
	old, oldBackoff := mlbBaseURL, searchRetryBackoff
	mlbBaseURL, searchRetryBackoff = srv.URL, time.Millisecond
	resetSeasonIndexMemo()
	defer func() { mlbBaseURL, searchRetryBackoff = old, oldBackoff; resetSeasonIndexMemo() }()

	rp, err := ResolveMLBAMIDsNoCache([]string{"Mike Trout"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rp.ByName[Normalize("Mike Trout")]; got != 100 {
		t.Errorf("Mike Trout → %d, want 100 (recovered on retry)", got)
	}
	if *calls < 2 {
		t.Errorf("search called %d times, want >= 2 (the retry)", *calls)
	}
}

// A resolution that could not complete must NOT be written to the cache: doing
// so turns a transient upstream blip into a deterministic wrong answer served
// for the full 7-day TTL. Three such all-zero entries were found in production
// (rosterbot-i52).
func TestResolveMLBAMIDs_DoesNotCacheDegradedResult(t *testing.T) {
	srv, _ := flakySearchServer(t, 1000) // never recovers
	defer srv.Close()
	old, oldBackoff := mlbBaseURL, searchRetryBackoff
	mlbBaseURL, searchRetryBackoff = srv.URL, time.Millisecond
	resetSeasonIndexMemo()
	defer func() { mlbBaseURL, searchRetryBackoff = old, oldBackoff; resetSeasonIndexMemo() }()

	dir := t.TempDir()
	if _, err := ResolveMLBAMIDs([]string{"Mike Trout"}, dir); err != nil {
		t.Fatalf("degraded resolution should still return usable (empty) data: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mlb-player-ids") {
			t.Errorf("degraded resolution was cached as %q — it must not be", e.Name())
		}
	}
}

// The complement: a clean resolution must still be cached, or every caller pays
// a full upstream round trip every time.
func TestResolveMLBAMIDs_CachesCleanResult(t *testing.T) {
	srv, _ := flakySearchServer(t, 0)
	defer srv.Close()
	old := mlbBaseURL
	mlbBaseURL = srv.URL
	resetSeasonIndexMemo()
	defer func() { mlbBaseURL = old; resetSeasonIndexMemo() }()

	dir := t.TempDir()
	if _, err := ResolveMLBAMIDs([]string{"Mike Trout"}, dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	var found bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mlb-player-ids") {
			found = true
		}
	}
	if !found {
		t.Error("clean resolution was not cached")
	}
}

// MLB's people-search matches the literal string, so a name carrying periods or
// a generational suffix returns nothing ("A.J. Blubaugh" → 0 results) while its
// normalized form finds the player. The inverse holds for hyphens
// ("Ha-Seong Kim" works raw, fails normalized), so neither form is a superset
// and the resolver must try the normalized form for names the raw pass missed
// (rosterbot-i52).
func TestResolveMLBAMIDs_RetriesMissesWithNormalizedName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/people/search":
			// Mimic MLB: the punctuated form matches nothing; the normalized
			// form matches. "Ha-Seong Kim" behaves the opposite way. Names
			// arrive comma-joined, exactly as go-mlb's WithNames sends them.
			var out []string
			for _, n := range strings.Split(r.URL.Query().Get("names"), ",") {
				switch n {
				case "aj blubaugh":
					out = append(out, `{"id":700,"fullName":"AJ Blubaugh","firstName":"AJ","lastName":"Blubaugh","active":true}`)
				case "Ha-Seong Kim":
					out = append(out, `{"id":800,"fullName":"Ha-Seong Kim","firstName":"Ha-Seong","lastName":"Kim","active":true}`)
				}
			}
			_, _ = w.Write([]byte(`{"people":[` + strings.Join(out, ",") + `]}`))
		case "/api/v1/people":
			ids := r.URL.Query().Get("personIds")
			var out []string
			if strings.Contains(ids, "700") {
				out = append(out, `{"id":700,"fullName":"AJ Blubaugh","firstName":"AJ","lastName":"Blubaugh","active":true}`)
			}
			if strings.Contains(ids, "800") {
				out = append(out, `{"id":800,"fullName":"Ha-Seong Kim","firstName":"Ha-Seong","lastName":"Kim","active":true}`)
			}
			_, _ = w.Write([]byte(`{"people":[` + strings.Join(out, ",") + `]}`))
		default:
			// An explicitly EMPTY season index, rather than a 404 that fails the
			// build by accident: these tests exist to exercise the SEARCH
			// fallback and must say which path they are on.
			if strings.HasPrefix(r.URL.Path, "/api/v1/sports/") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"people":[]}`))
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := mlbBaseURL
	mlbBaseURL = srv.URL
	resetSeasonIndexMemo()
	defer func() { mlbBaseURL = old; resetSeasonIndexMemo() }()

	rp, err := ResolveMLBAMIDsNoCache([]string{"A.J. Blubaugh", "Ha-Seong Kim"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rp.ByName[Normalize("A.J. Blubaugh")]; got != 700 {
		t.Errorf("A.J. Blubaugh → %d, want 700 (recovered via normalized search)", got)
	}
	if got := rp.ByName[Normalize("Ha-Seong Kim")]; got != 800 {
		t.Errorf("Ha-Seong Kim → %d, want 800 (raw search must still win)", got)
	}
}
