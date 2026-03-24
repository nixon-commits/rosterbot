package projections

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFanGraphsPitcherSource_PitcherInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"PlayerName": "Gerrit Cole", "Team": "NYY", "G": 30.0, "GS": 30.0, "IP": 190.0,
				"SO": 220.0, "BB": 45.0, "H": 150.0, "ER": 60.0, "HR": 20.0,
				"W": 14.0, "L": 7.0, "QS": 18.0, "SV": 0.0, "HLD": 0.0, "BS": 0.0,
				"HBP": 5.0, "WP": 3.0, "BK": 0.0, "CG": 1.0, "SHO": 0.0, "PKO": 1.0,
				"Throws": "R", "FIP": 2.80},
			{"PlayerName": "Yusei Kikuchi", "Team": "LAA", "G": 28.0, "GS": 28.0, "IP": 160.0,
				"SO": 180.0, "BB": 55.0, "H": 140.0, "ER": 70.0, "HR": 22.0,
				"W": 10.0, "L": 9.0, "QS": 14.0, "SV": 0.0, "HLD": 0.0, "BS": 0.0,
				"HBP": 4.0, "WP": 5.0, "BK": 1.0, "CG": 0.0, "SHO": 0.0, "PKO": 0.0,
				"Throws": "L", "FIP": 4.20},
		})
	}))
	defer srv.Close()

	old := fangraphsPitchingURL
	fangraphsPitchingURL = srv.URL
	defer func() { fangraphsPitchingURL = old }()

	src, err := NewFanGraphsPitcherSource()
	if err != nil {
		t.Fatal(err)
	}

	handedness, fip, avgFIP := src.PitcherInfo()

	if handedness["gerrit cole"] != "R" {
		t.Errorf("Cole handedness: got %q, want R", handedness["gerrit cole"])
	}
	if handedness["yusei kikuchi"] != "L" {
		t.Errorf("Kikuchi handedness: got %q, want L", handedness["yusei kikuchi"])
	}
	if math.Abs(fip["gerrit cole"]-2.80) > 0.01 {
		t.Errorf("Cole FIP: got %.2f, want 2.80", fip["gerrit cole"])
	}
	// IP-weighted avg: (190*2.80 + 160*4.20) / (190+160) = (532 + 672) / 350 = 3.44
	wantAvg := (190.0*2.80 + 160.0*4.20) / (190.0 + 160.0)
	if math.Abs(avgFIP-wantAvg) > 0.01 {
		t.Errorf("avgFIP: got %.2f, want %.2f", avgFIP, wantAvg)
	}
}
