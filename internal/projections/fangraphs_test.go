package projections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFanGraphsSource_ParsesJSON(t *testing.T) {
	fixture := []map[string]interface{}{
		{"PlayerName": "Aaron Judge", "Team": "NYY", "G": 141.0, "PA": 633.0, "H": 143.0,
			"1B": 77.0, "2B": 23.0, "3B": 1.0, "HR": 42.0,
			"R": 109.0, "RBI": 102.0, "BB": 112.0, "SB": 9.0, "CS": 2.0, "HBP": 6.0, "SO": 156.0, "GDP": nil},
		{"PlayerName": "Freddie Freeman", "Team": "LAD", "G": 138.0, "PA": 590.0, "H": 160.0,
			"1B": 100.0, "2B": 35.0, "3B": 1.0, "HR": 24.0,
			"R": 95.0, "RBI": 90.0, "BB": 70.0, "SB": 10.0, "CS": 3.0, "HBP": 6.0, "SO": 100.0, "GDP": 10.0},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	orig := fangraphsBattingURL
	fangraphsBattingURL = srv.URL
	defer func() { fangraphsBattingURL = orig }()

	src, err := NewFanGraphsSource()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := src.GetProjection("Aaron Judge", "NYY")
	if !ok {
		t.Fatal("expected projection for Aaron Judge")
	}
	if p.HR != 42 {
		t.Errorf("expected HR=42, got %v", p.HR)
	}
	if p.G != 141 {
		t.Errorf("expected G=141, got %v", p.G)
	}
}

func TestFanGraphsSource_CaseInsensitiveTeam(t *testing.T) {
	src := &FanGraphsSource{projections: map[string]*Projection{
		"freddie freeman|LAD": {G: 138, HR: 24},
	}}

	p, ok := src.GetProjection("Freddie Freeman", "lad")
	if !ok {
		t.Fatal("expected projection with lowercase team")
	}
	if p.HR != 24 {
		t.Errorf("expected HR=24, got %v", p.HR)
	}
}

func TestFanGraphsSource_NameFallback(t *testing.T) {
	src := &FanGraphsSource{projections: map[string]*Projection{
		"manny machado|SD": {G: 140, HR: 26},
	}}

	// Different team (traded) - should still find by name.
	p, ok := src.GetProjection("Manny Machado", "LAD")
	if !ok {
		t.Fatal("expected name-only fallback to work")
	}
	if p.HR != 26 {
		t.Errorf("expected HR=26, got %v", p.HR)
	}
}

func TestFanGraphsSource_HitterBats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"PlayerName": "Aaron Judge", "Team": "NYY", "G": 150.0, "PA": 600.0, "H": 160.0,
				"1B": 80.0, "2B": 30.0, "3B": 2.0, "HR": 48.0, "RBI": 120.0, "R": 100.0,
				"BB": 90.0, "SB": 5.0, "CS": 2.0, "HBP": 10.0, "SO": 170.0, "GDP": nil,
				"Bats": "R"},
			{"PlayerName": "Juan Soto", "Team": "NYM", "G": 155.0, "PA": 650.0, "H": 165.0,
				"1B": 90.0, "2B": 32.0, "3B": 1.0, "HR": 35.0, "RBI": 100.0, "R": 110.0,
				"BB": 120.0, "SB": 3.0, "CS": 1.0, "HBP": 8.0, "SO": 130.0, "GDP": 10.0,
				"Bats": "L"},
			{"PlayerName": "Ozzie Albies", "Team": "ATL", "G": 140.0, "PA": 580.0, "H": 150.0,
				"1B": 85.0, "2B": 30.0, "3B": 5.0, "HR": 25.0, "RBI": 80.0, "R": 85.0,
				"BB": 35.0, "SB": 15.0, "CS": 5.0, "HBP": 3.0, "SO": 100.0, "GDP": 12.0,
				"Bats": "B"},
		})
	}))
	defer srv.Close()

	old := fangraphsBattingURL
	fangraphsBattingURL = srv.URL
	defer func() { fangraphsBattingURL = old }()

	src, err := NewFanGraphsSource()
	if err != nil {
		t.Fatal(err)
	}

	bats := src.HitterBats()
	if bats["aaron judge"] != "R" {
		t.Errorf("Judge: got %q, want R", bats["aaron judge"])
	}
	if bats["juan soto"] != "L" {
		t.Errorf("Soto: got %q, want L", bats["juan soto"])
	}
	if bats["ozzie albies"] != "S" {
		t.Errorf("Albies: got %q, want S (normalized from B)", bats["ozzie albies"])
	}
}

func TestChainedSource_FallsThrough(t *testing.T) {
	primary := &FanGraphsSource{projections: map[string]*Projection{
		"aaron judge|NYY": {G: 141, HR: 40},
	}}

	rolling := NewRollingSource()
	rolling.AddPlayer("mystery player", 14, 2.0, 0.5, 0.1, 0.3, 1.5, 1.2, 1.0, 0.3, 0.0, 0.1, 1.5, 0.2)

	chained := NewChainedSource(primary, rolling)

	_, ok := chained.GetProjection("Aaron Judge", "NYY")
	if !ok {
		t.Error("expected primary source hit")
	}

	_, ok2 := chained.GetProjection("mystery player", "COL")
	if !ok2 {
		t.Error("expected rolling fallback hit")
	}

	_, ok3 := chained.GetProjection("nobody", "XYZ")
	if ok3 {
		t.Error("expected miss for unknown player")
	}
}
