package prospects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Hitter breakout tests
// ---------------------------------------------------------------------------

func TestComputeHitterBreakout_HotAAA(t *testing.T) {
	// Season: 20 games of mediocre hitting (roughly .250/.300/.400 = .700 OPS)
	// Recent 5 games: much hotter (roughly .400/.450/.800 = 1.250 OPS)
	// Delta ~0.550, threshold for AAA is 0.150 → should be hot
	season := make([]gameLogEntry, 20)
	for i := range season {
		season[i] = gameLogEntry{
			Date: "2026-05-01", Level: "AAA",
			AB: 4, H: 1, Doubles: 0, Triples: 0, HR: 0, BB: 0, HBP: 0, SF: 0,
		}
	}
	// Override last 5 with hot games
	for i := 15; i < 20; i++ {
		season[i] = gameLogEntry{
			Date: "2026-06-01", Level: "AAA",
			AB: 5, H: 3, Doubles: 1, Triples: 0, HR: 1, BB: 1, HBP: 0, SF: 0,
		}
	}

	hot, cold, recentLine, seasonLine := computeHitterBreakout(season, 5, "AAA")
	if !hot {
		t.Errorf("expected hot=true, got false (recent=%s, season=%s)", recentLine, seasonLine)
	}
	if cold {
		t.Error("expected cold=false, got true")
	}
	if recentLine == "" || seasonLine == "" {
		t.Error("expected non-empty stat lines")
	}
}

func TestComputeHitterBreakout_MinGameFilter(t *testing.T) {
	logs := []gameLogEntry{
		{Date: "2026-05-01", Level: "AAA", AB: 4, H: 3, BB: 1},
		{Date: "2026-05-02", Level: "AAA", AB: 4, H: 3, BB: 1},
	}
	// minGames = 5, only 2 games → should return no breakout
	hot, cold, _, _ := computeHitterBreakout(logs, 5, "AAA")
	if hot || cold {
		t.Errorf("expected no breakout with insufficient games, got hot=%v cold=%v", hot, cold)
	}
}

func TestComputeHitterBreakout_LevelThresholds(t *testing.T) {
	// Build logs where delta is ~0.220 OPS
	// AA threshold is 0.200 → should be hot
	// A threshold is 0.250 → should NOT be hot
	buildLogs := func(level string) []gameLogEntry {
		logs := make([]gameLogEntry, 20)
		// Season baseline: .250 AVG, no walks, no power → OPS ~.500
		for i := range logs {
			logs[i] = gameLogEntry{
				Date: "2026-05-01", Level: level,
				AB: 4, H: 1, Doubles: 0, Triples: 0, HR: 0, BB: 0, HBP: 0, SF: 0,
			}
		}
		// Recent 5: slightly higher → OPS ~.750 (delta ~.250)
		for i := 15; i < 20; i++ {
			logs[i] = gameLogEntry{
				Date: "2026-06-01", Level: level,
				AB: 4, H: 2, Doubles: 1, Triples: 0, HR: 0, BB: 1, HBP: 0, SF: 0,
			}
		}
		return logs
	}

	// AA: threshold 0.200
	hotAA, _, _, _ := computeHitterBreakout(buildLogs("AA"), 5, "AA")
	if !hotAA {
		t.Error("expected hot=true for AA level with delta ~0.250")
	}

	// A: threshold 0.250 — use a modest improvement that stays below threshold
	// Season baseline: 30 games of 1-for-4, recent 5: 1-for-3 with a walk
	// This gives a small OPS bump (~0.15) which is above AA threshold but below A threshold
	logsA := make([]gameLogEntry, 30)
	for i := range logsA {
		logsA[i] = gameLogEntry{
			Date: "2026-05-01", Level: "A",
			AB: 4, H: 1, Doubles: 0, Triples: 0, HR: 0, BB: 0, HBP: 0, SF: 0,
		}
	}
	// Recent 5: slightly better but not enough for A threshold
	for i := 25; i < 30; i++ {
		logsA[i] = gameLogEntry{
			Date: "2026-06-01", Level: "A",
			AB: 4, H: 1, Doubles: 0, Triples: 0, HR: 0, BB: 1, HBP: 0, SF: 0,
		}
	}
	hotA, coldA, _, _ := computeHitterBreakout(logsA, 5, "A")
	_ = coldA
	if hotA {
		t.Error("expected hot=false for A level with delta below 0.250")
	}
}

// ---------------------------------------------------------------------------
// Pitcher breakout tests
// ---------------------------------------------------------------------------

func TestComputePitcherBreakout_HotERA(t *testing.T) {
	// Season: 4.50 ERA overall
	// Recent: 2.00 ERA → delta = -2.50, AAA threshold is -1.00 → hot
	logs := make([]gameLogEntry, 15)
	for i := range logs {
		logs[i] = gameLogEntry{
			Date: "2026-05-01", Level: "AAA",
			IP: 6.0, ER: 3, SO: 6, BBA: 2, HA: 6,
		}
	}
	// Recent 5: much lower ERA
	for i := 10; i < 15; i++ {
		logs[i] = gameLogEntry{
			Date: "2026-06-01", Level: "AAA",
			IP: 6.0, ER: 1, SO: 6, BBA: 2, HA: 4,
		}
	}

	hot, cold, recentLine, seasonLine := computePitcherBreakout(logs, 5, "AAA")
	if !hot {
		t.Errorf("expected hot=true for ERA improvement, recent=%s season=%s", recentLine, seasonLine)
	}
	if cold {
		t.Error("expected cold=false")
	}
}

func TestComputePitcherBreakout_HotK9(t *testing.T) {
	// Season: ~6.0 K/9
	// Recent: ~10.0 K/9 → delta = +4.0, AAA threshold is 2.0 → hot
	logs := make([]gameLogEntry, 15)
	for i := range logs {
		logs[i] = gameLogEntry{
			Date: "2026-05-01", Level: "AAA",
			IP: 6.0, ER: 3, SO: 4, BBA: 2, HA: 6,
		}
	}
	// Recent 5: high strikeout games
	for i := 10; i < 15; i++ {
		logs[i] = gameLogEntry{
			Date: "2026-06-01", Level: "AAA",
			IP: 6.0, ER: 3, SO: 10, BBA: 2, HA: 6,
		}
	}

	hot, coldK9, _, _ := computePitcherBreakout(logs, 5, "AAA")
	_ = coldK9
	if !hot {
		t.Error("expected hot=true for K/9 improvement")
	}
}

func TestComputeHitterBreakout_Cold(t *testing.T) {
	// Season: decent OPS ~.800
	// Recent 5: terrible OPS ~.400 → delta ~ -0.400, threshold is -0.200 → cold
	logs := make([]gameLogEntry, 20)
	for i := range logs {
		logs[i] = gameLogEntry{
			Date: "2026-05-01", Level: "AAA",
			AB: 4, H: 2, Doubles: 1, Triples: 0, HR: 0, BB: 1, HBP: 0, SF: 0,
		}
	}
	// Recent 5: terrible
	for i := 15; i < 20; i++ {
		logs[i] = gameLogEntry{
			Date: "2026-06-01", Level: "AAA",
			AB: 5, H: 0, Doubles: 0, Triples: 0, HR: 0, BB: 0, HBP: 0, SF: 0,
		}
	}

	hot, cold, _, _ := computeHitterBreakout(logs, 5, "AAA")
	if hot {
		t.Error("expected hot=false")
	}
	if !cold {
		t.Error("expected cold=true for large negative OPS delta")
	}
}

// ---------------------------------------------------------------------------
// resolveMLBPlayerID tests
// ---------------------------------------------------------------------------

func TestResolveMLBPlayerID_SearchAPI(t *testing.T) {
	// Hits the upstream once on cache miss, then cache hit on second call —
	// the second call should not depend on the test server (we close it
	// before the second call to prove that).
	fixture := map[string]any{
		"people": []map[string]any{
			{
				"id":       808080,
				"fullName": "Jackson Holliday",
				"currentTeam": map[string]any{
					"abbreviation": "BAL",
				},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))

	origURL := mlbPlayerSearchURL
	mlbPlayerSearchURL = srv.URL + "?names=%s"
	origDir := performanceCacheDir
	performanceCacheDir = t.TempDir()
	defer func() {
		mlbPlayerSearchURL = origURL
		performanceCacheDir = origDir
	}()

	id, found := resolveMLBPlayerID("Jackson Holliday", "BAL")
	if !found {
		t.Fatal("expected found=true from API")
	}
	if id != 808080 {
		t.Errorf("expected id=808080, got %d", id)
	}

	// Second call after upstream is gone — must come from the file cache.
	srv.Close()
	id2, found2 := resolveMLBPlayerID("Jackson Holliday", "BAL")
	if !found2 || id2 != 808080 {
		t.Errorf("expected cached id=808080, got id=%d found=%v", id2, found2)
	}
}
