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
