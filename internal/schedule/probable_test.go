package schedule

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbableStarters_ParsesResponse(t *testing.T) {
	fixture := map[string]interface{}{
		"dates": []map[string]interface{}{
			{
				"games": []map[string]interface{}{
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team":            map[string]interface{}{"abbreviation": "NYY"},
								"probablePitcher": map[string]interface{}{"fullName": "Gerrit Cole"},
							},
							"home": map[string]interface{}{
								"team":            map[string]interface{}{"abbreviation": "BOS"},
								"probablePitcher": map[string]interface{}{"fullName": "Brayan Bello"},
							},
						},
					},
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "LAD"},
								// No probable pitcher (TBD)
							},
							"home": map[string]interface{}{
								"team":            map[string]interface{}{"abbreviation": "SF"},
								"probablePitcher": map[string]interface{}{"fullName": "Logan Webb"},
							},
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbProbablePitcherURL
	mlbProbablePitcherURL = srv.URL + "?date=%s"
	defer func() { mlbProbablePitcherURL = origURL }()

	c := NewClient()
	starters, err := c.ProbableStarters(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name string
		team string
	}{
		{"gerrit cole", "NYY"},
		{"brayan bello", "BOS"},
		{"logan webb", "SF"},
	}
	for _, tc := range tests {
		if got := starters[tc.name]; got != tc.team {
			t.Errorf("starters[%q] = %q, want %q", tc.name, got, tc.team)
		}
	}

	// LAD has no probable pitcher — should not appear.
	for name, team := range starters {
		if team == "LAD" {
			t.Errorf("LAD should have no probable pitcher, found %q", name)
		}
	}

	if len(starters) != 3 {
		t.Errorf("expected 3 starters, got %d: %v", len(starters), starters)
	}
}

func TestProbableStarters_EmptySchedule(t *testing.T) {
	fixture := map[string]interface{}{"dates": []interface{}{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbProbablePitcherURL
	mlbProbablePitcherURL = srv.URL + "?date=%s"
	defer func() { mlbProbablePitcherURL = origURL }()

	c := NewClient()
	starters, err := c.ProbableStarters(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(starters) != 0 {
		t.Errorf("expected empty map, got %v", starters)
	}
}

func TestProbableStarters_CacheStickiness_PreservesDroppedTeam(t *testing.T) {
	// Simulate two successive calls: the first returns all three probables,
	// the second omits CIN (transient MLB API gap). With a cache dir set,
	// the second call should still include Burns/CIN via sticky merge.
	apiProbables := map[string]interface{}{
		"chase burns": "CIN",
		"jack leiter": "TEX",
		"shane baz":   "BAL",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dates := []map[string]interface{}{{"games": buildGamesFromMap(apiProbables)}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"dates": dates})
	}))
	defer srv.Close()

	origURL := mlbProbablePitcherURL
	mlbProbablePitcherURL = srv.URL + "?date=%s"
	defer func() { mlbProbablePitcherURL = origURL }()

	c := NewClient()
	c.CacheDir = t.TempDir()
	date := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)

	first, err := c.ProbableStarters(date)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first call expected 3 probables, got %d: %v", len(first), first)
	}

	// Second call: API drops Burns/CIN.
	delete(apiProbables, "chase burns")
	second, err := c.ProbableStarters(date)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if team, ok := second["chase burns"]; !ok || team != "CIN" {
		t.Errorf("expected Burns/CIN to persist via cache, got team=%q ok=%v", team, ok)
	}
	if len(second) != 3 {
		t.Errorf("expected 3 merged probables, got %d: %v", len(second), second)
	}
}

func TestProbableStarters_CacheStickiness_ScratchOverridesCache(t *testing.T) {
	// When the API returns a different probable for a team already in cache,
	// the API value wins (treated as a scratch/replacement).
	apiProbables := map[string]interface{}{"chase burns": "CIN"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dates := []map[string]interface{}{{"games": buildGamesFromMap(apiProbables)}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"dates": dates})
	}))
	defer srv.Close()

	origURL := mlbProbablePitcherURL
	mlbProbablePitcherURL = srv.URL + "?date=%s"
	defer func() { mlbProbablePitcherURL = origURL }()

	c := NewClient()
	c.CacheDir = t.TempDir()
	date := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)

	if _, err := c.ProbableStarters(date); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Simulate scratch: Burns replaced by Greene on CIN.
	delete(apiProbables, "chase burns")
	apiProbables["hunter greene"] = "CIN"

	second, err := c.ProbableStarters(date)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if _, burnsStillListed := second["chase burns"]; burnsStillListed {
		t.Error("Burns should have been replaced by the scratch — cache should not resurrect him")
	}
	if second["hunter greene"] != "CIN" {
		t.Errorf("expected Greene to be CIN's probable, got %v", second)
	}
}

func TestProbableStarters_CacheStickiness_FallbackOnAPIFailure(t *testing.T) {
	// First call succeeds and populates cache. Second call's API returns 500
	// — we should fall back to cached values rather than returning an error.
	callCount := 0
	apiProbables := map[string]interface{}{
		"chase burns": "CIN",
		"shane baz":   "BAL",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			dates := []map[string]interface{}{{"games": buildGamesFromMap(apiProbables)}}
			json.NewEncoder(w).Encode(map[string]interface{}{"dates": dates})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origURL := mlbProbablePitcherURL
	mlbProbablePitcherURL = srv.URL + "?date=%s"
	defer func() { mlbProbablePitcherURL = origURL }()

	c := NewClient()
	c.CacheDir = t.TempDir()
	date := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)

	if _, err := c.ProbableStarters(date); err != nil {
		t.Fatalf("first call: %v", err)
	}

	second, err := c.ProbableStarters(date)
	if err != nil {
		t.Fatalf("second call (API 500) should fall back to cache, got error: %v", err)
	}
	if len(second) != 2 {
		t.Errorf("expected cache fallback to return 2 probables, got %d: %v", len(second), second)
	}
}

// buildGamesFromMap emits the minimal MLB-schedule game shape needed for
// probable-pitcher parsing. Each pitcher goes into its own game as the home
// probablePitcher; the away side has no probable. Team is set from the map value.
func buildGamesFromMap(probables map[string]interface{}) []map[string]interface{} {
	games := make([]map[string]interface{}, 0, len(probables))
	for name, teamRaw := range probables {
		team := teamRaw.(string)
		games = append(games, map[string]interface{}{
			"teams": map[string]interface{}{
				"away": map[string]interface{}{
					"team": map[string]interface{}{"abbreviation": "ZZZ" + team},
				},
				"home": map[string]interface{}{
					"team":            map[string]interface{}{"abbreviation": team},
					"probablePitcher": map[string]interface{}{"fullName": titleCase(name)},
				},
			},
		})
	}
	return games
}

func titleCase(s string) string {
	// The MLB API returns titlecased names like "Chase Burns". Fixture inputs
	// are normalized lowercase keys so tests match downstream consumers — we
	// feed titled names to the server but assert on normalized output.
	out := []rune(s)
	upperNext := true
	for i, r := range out {
		if r == ' ' {
			upperNext = true
			continue
		}
		if upperNext && r >= 'a' && r <= 'z' {
			out[i] = r - ('a' - 'A')
		}
		upperNext = false
	}
	return string(out)
}

func TestNormalizePitcherName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Gerrit Cole", "gerrit cole"},
		{"José Berríos", "jose berrios"},
		{" Logan Webb ", "logan webb"},
	}
	for _, tc := range tests {
		if got := normalizePitcherName(tc.input); got != tc.want {
			t.Errorf("normalizePitcherName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
