package hkb

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testFixtureHTML = `<!DOCTYPE html><html><head></head><body>
<script id="__NEXT_DATA__" type="application/json">
{
  "props": {
    "pageProps": {
      "lastUpdated": "2026-03-30T16:24:40.430Z",
      "players": [
        {
          "id": "XQAIQ8Yr",
          "originalIndex": 17,
          "rank": 18,
          "name": "Konnor Griffin",
          "age": 19.9,
          "positions": ["SS"],
          "positionRanks": {"SS": 4},
          "team": "PIT",
          "level": "AAA",
          "hitterStats": {
            "level": null,
            "gamesPlayed": 122,
            "runs": 117,
            "homeRuns": 21,
            "strikeOuts": 122,
            "baseOnBalls": 50,
            "avg": 0.3326,
            "atBats": 484,
            "obp": 0.4148,
            "slg": 0.5266,
            "ops": 0.9414,
            "caughtStealing": 13,
            "stolenBases": 65,
            "plateAppearances": 563,
            "rbi": 94,
            "totalMetric": 563
          },
          "pitcherStats": null,
          "statsYear": 2025,
          "activeLevels": "AA/A+/A",
          "value": 5520,
          "valueChange30Days": -51,
          "rankChange30Days": 0,
          "valueChange7Days": 68,
          "rankChange7Days": 0,
          "assetType": "PLAYER",
          "valueHistory30Days": [5571, 5550],
          "rankHistory30Days": [18, 18],
          "active": true,
          "prospect": true,
          "fypd": false
        },
        {
          "id": "ABC123",
          "originalIndex": 50,
          "rank": 51,
          "name": "Test Pitcher",
          "age": 22.1,
          "positions": ["SP"],
          "positionRanks": {"SP": 10},
          "team": "NYY",
          "level": "AA",
          "hitterStats": null,
          "pitcherStats": {
            "level": null,
            "gamesPlayed": 25,
            "inningsPitched": 130.2,
            "strikeOuts": 145,
            "baseOnBalls": 40,
            "era": 2.85,
            "whip": 1.05,
            "wins": 9,
            "losses": 4,
            "saves": 0,
            "homeRuns": 8,
            "hitsAllowed": 95,
            "gamesStarted": 25,
            "totalMetric": 25
          },
          "statsYear": 2025,
          "activeLevels": "AA",
          "value": 3200,
          "valueChange30Days": 100,
          "rankChange30Days": -2,
          "valueChange7Days": 30,
          "rankChange7Days": -1,
          "assetType": "PLAYER",
          "valueHistory30Days": [3100, 3150],
          "rankHistory30Days": [53, 52],
          "active": true,
          "prospect": true,
          "fypd": false
        },
        {
          "id": "FYPD001",
          "originalIndex": 200,
          "rank": 201,
          "name": "Draft Pick",
          "age": 18.0,
          "positions": ["OF"],
          "positionRanks": {"OF": 30},
          "team": "LAD",
          "level": "",
          "hitterStats": null,
          "pitcherStats": null,
          "statsYear": 0,
          "activeLevels": "",
          "value": 1500,
          "valueChange30Days": 0,
          "rankChange30Days": 0,
          "valueChange7Days": 0,
          "rankChange7Days": 0,
          "assetType": "PLAYER",
          "valueHistory30Days": [],
          "rankHistory30Days": [],
          "active": false,
          "prospect": false,
          "fypd": true
        }
      ]
    }
  }
}
</script></body></html>`

func TestGetPlayers_ParsesNextData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testFixtureHTML))
	}))
	defer srv.Close()

	origURL := fetchURL
	fetchURL = srv.URL
	defer func() { fetchURL = origURL }()

	tmpDir := t.TempDir()
	players, err := GetPlayers(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(players) != 3 {
		t.Fatalf("expected 3 players, got %d", len(players))
	}

	p := players[0]
	if p.Name != "Konnor Griffin" {
		t.Errorf("expected name 'Konnor Griffin', got %q", p.Name)
	}
	if p.Rank != 18 {
		t.Errorf("expected rank 18, got %d", p.Rank)
	}
	if p.Team != "PIT" {
		t.Errorf("expected team PIT, got %q", p.Team)
	}
	if p.HitterStats == nil {
		t.Fatal("expected hitterStats to be non-nil")
	}
	if p.HitterStats.HomeRuns != 21 {
		t.Errorf("expected 21 HR, got %d", p.HitterStats.HomeRuns)
	}
	if !p.Prospect {
		t.Error("expected prospect=true")
	}

	pit := players[1]
	if pit.PitcherStats == nil {
		t.Fatal("expected pitcherStats to be non-nil for pitcher")
	}
	if pit.PitcherStats.StrikeOuts != 145 {
		t.Errorf("expected 145 K, got %d", pit.PitcherStats.StrikeOuts)
	}

	if players[2].Name != "Draft Pick" {
		t.Errorf("expected 'Draft Pick', got %q", players[2].Name)
	}
}

func TestGetPlayers_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origURL := fetchURL
	fetchURL = srv.URL
	defer func() { fetchURL = origURL }()

	tmpDir := t.TempDir()
	_, err := GetPlayers(tmpDir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetPlayers_NoNextData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>no data here</body></html>"))
	}))
	defer srv.Close()

	origURL := fetchURL
	fetchURL = srv.URL
	defer func() { fetchURL = origURL }()

	tmpDir := t.TempDir()
	_, err := GetPlayers(tmpDir)
	if err == nil {
		t.Fatal("expected error for missing __NEXT_DATA__, got nil")
	}
}

func TestGetPlayers_UsesCache(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte(testFixtureHTML))
	}))
	defer srv.Close()

	origURL := fetchURL
	fetchURL = srv.URL
	defer func() { fetchURL = origURL }()

	tmpDir := t.TempDir()

	_, err := GetPlayers(tmpDir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", callCount)
	}

	_, err = GetPlayers(tmpDir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected cache hit (still 1 HTTP call), got %d", callCount)
	}
}
