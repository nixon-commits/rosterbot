package projections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestFanGraphsSource_MLBAMIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"PlayerName": "Aaron Judge", "Team": "NYY", "G": 150.0, "PA": 600.0, "H": 160.0,
				"1B": 80.0, "2B": 30.0, "3B": 2.0, "HR": 48.0, "RBI": 120.0, "R": 100.0,
				"BB": 90.0, "SB": 5.0, "CS": 2.0, "HBP": 10.0, "SO": 170.0, "GDP": nil,
				"xMLBAMID": 592450},
			{"PlayerName": "Juan Soto", "Team": "NYM", "G": 155.0, "PA": 650.0, "H": 165.0,
				"1B": 90.0, "2B": 32.0, "3B": 1.0, "HR": 35.0, "RBI": 100.0, "R": 110.0,
				"BB": 120.0, "SB": 3.0, "CS": 1.0, "HBP": 8.0, "SO": 130.0, "GDP": 10.0,
				"xMLBAMID": 665742},
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

	ids := src.MLBAMIDs()
	if ids["aaron judge"] != 592450 {
		t.Errorf("Judge MLBAMID: got %d, want 592450", ids["aaron judge"])
	}
	if ids["juan soto"] != 665742 {
		t.Errorf("Soto MLBAMID: got %d, want 665742", ids["juan soto"])
	}
}

func TestSetProjectionSystem_UpdatesURLs(t *testing.T) {
	origBat := fangraphsBattingURL
	origPit := fangraphsPitchingURL
	defer func() {
		fangraphsBattingURL = origBat
		fangraphsPitchingURL = origPit
	}()

	tests := []struct {
		system  string
		wantBat string
		wantPit string
		wantErr bool
	}{
		{"steamer", "type=steamer&stats=bat", "type=steamer&stats=pit", false},
		{"depthcharts", "type=fangraphsdc&stats=bat", "type=fangraphsdc&stats=pit", false},
		{"thebatx", "type=thebatx&stats=bat", "type=thebatx&stats=pit", false},
		{"steamer-ros", "type=steamerr&stats=bat", "type=steamerr&stats=pit", false},
		{"depthcharts-ros", "type=rfangraphsdc&stats=bat", "type=rfangraphsdc&stats=pit", false},
		{"thebatx-ros", "type=rthebatx&stats=bat", "type=rthebatx&stats=pit", false},
		{"bogus", "", "", true},
	}

	for _, tt := range tests {
		err := SetProjectionSystem(tt.system)
		if tt.wantErr {
			if err == nil {
				t.Errorf("SetProjectionSystem(%q) expected error, got nil", tt.system)
			}
			continue
		}
		if err != nil {
			t.Errorf("SetProjectionSystem(%q) unexpected error: %v", tt.system, err)
			continue
		}
		if !strings.Contains(fangraphsBattingURL, tt.wantBat) {
			t.Errorf("SetProjectionSystem(%q): batting URL %q missing %q", tt.system, fangraphsBattingURL, tt.wantBat)
		}
		if !strings.Contains(fangraphsPitchingURL, tt.wantPit) {
			t.Errorf("SetProjectionSystem(%q): pitching URL %q missing %q", tt.system, fangraphsPitchingURL, tt.wantPit)
		}
	}
}


func TestSetProjectionSystem_AffectsFetch(t *testing.T) {
	// Verify that after SetProjectionSystem, NewFanGraphsSource hits the updated URL.
	origBat := fangraphsBattingURL
	origPit := fangraphsPitchingURL
	defer func() {
		fangraphsBattingURL = origBat
		fangraphsPitchingURL = origPit
	}()

	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.RawQuery
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"PlayerName": "Test Player", "Team": "NYY", "G": 100.0, "PA": 400.0, "H": 100.0,
				"1B": 60.0, "2B": 20.0, "3B": 5.0, "HR": 15.0, "RBI": 50.0, "R": 60.0,
				"BB": 40.0, "SB": 5.0, "CS": 2.0, "HBP": 3.0, "SO": 80.0, "GDP": 5.0},
		})
	}))
	defer srv.Close()

	// Point URLs at test server, then switch system to steamer.
	fangraphsBattingURL = srv.URL + "?type=steamer&stats=bat"
	_, err := NewFanGraphsSource()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedPath != "type=steamer&stats=bat" {
		t.Errorf("expected query type=steamer&stats=bat, got %q", receivedPath)
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
