package prospects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
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

// ---------------------------------------------------------------------------
// Unresolved-ID coverage tests (rosterbot-2onc)
// ---------------------------------------------------------------------------

// statsapiStub serves the two MLB statsapi endpoints FetchPerformanceAlerts
// hits. A name absent from people is the unresolvable case: the real API
// answers a search it cannot match with an empty list, not an error.
// gameLogsFail makes the stub's game-log endpoint 500 while people-search keeps
// working. The two statsapi endpoints fail independently in production, and
// that asymmetry is the whole reason PerformanceCoverage counts the two drops
// separately.
func gameLogsFail(c *stubConfig) { c.gameLogsFail = true }

type stubConfig struct{ gameLogsFail bool }

type stubOpt func(*stubConfig)

func statsapiStub(t *testing.T, people map[string]int, opts ...stubOpt) {
	t.Helper()
	var sc stubConfig
	for _, o := range opts {
		o(&sc)
	}

	hotHitterLog := func() any {
		var splits []map[string]any
		for i := 0; i < 15; i++ {
			splits = append(splits, map[string]any{
				"date":  "2026-05-01",
				"sport": map[string]any{"abbreviation": "AAA"},
				"stat":  map[string]any{"atBats": 4, "hits": 1},
			})
		}
		for i := 0; i < 5; i++ {
			splits = append(splits, map[string]any{
				"date":  "2026-06-01",
				"sport": map[string]any{"abbreviation": "AAA"},
				"stat": map[string]any{
					"atBats": 5, "hits": 3, "doubles": 1, "homeRuns": 1, "baseOnBalls": 1,
				},
			})
		}
		return map[string]any{"stats": []map[string]any{{"splits": splits}}}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/log" {
			if sc.gameLogsFail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(hotHitterLog())
			return
		}
		name := r.URL.Query().Get("names")
		id, ok := people[name]
		if !ok {
			json.NewEncoder(w).Encode(map[string]any{"people": []map[string]any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"people": []map[string]any{{
			"id":          id,
			"fullName":    name,
			"currentTeam": map[string]any{"abbreviation": "ATH"},
		}}})
	}))

	origSearch, origLog, origDir := mlbPlayerSearchURL, mlbGameLogURL, performanceCacheDir
	mlbPlayerSearchURL = srv.URL + "/search?names=%s"
	mlbGameLogURL = srv.URL + "/log?id=%d&group=%s&season=%d"
	performanceCacheDir = t.TempDir()
	t.Cleanup(func() {
		srv.Close()
		mlbPlayerSearchURL, mlbGameLogURL, performanceCacheDir = origSearch, origLog, origDir
	})
}

func minorsProspect(name string) fantrax.Player {
	return fantrax.Player{Name: name, MLBTeam: "ATH", PosShortNames: "OF"}
}

func TestFetchPerformanceAlerts_ReportsAnUnresolvedProspectAndStillSkipsHim(t *testing.T) {
	// The live case from rosterbot-2onc: "Jamie Arnold" (ATH) resolves to
	// nothing, is dropped before any game log is read, and used to leave no
	// trace but a log line nothing counted.
	statsapiStub(t, map[string]int{"Jackson Holliday": 808080})

	prospects := []fantrax.Player{
		minorsProspect("Jackson Holliday"),
		minorsProspect("Jamie Arnold"),
	}
	alerts, cov, err := FetchPerformanceAlerts(prospects, nil, 2026, 14, 5)
	if err != nil {
		t.Fatalf("unresolved names must not fail the scan: %v", err)
	}

	if cov.Scanned != 2 || cov.Resolved() != 1 {
		t.Errorf("expected 2 scanned / 1 resolved, got %d / %d", cov.Scanned, cov.Resolved())
	}
	if len(cov.Unresolved) != 1 || cov.Unresolved[0] != "Jamie Arnold (ATH)" {
		t.Errorf("expected the unresolved prospect named, got %v", cov.Unresolved)
	}
	if !strings.Contains(cov.String(), "Jamie Arnold (ATH)") {
		t.Errorf("coverage line must name him: %q", cov.String())
	}

	// Skipped from alerts, and the resolvable prospect beside him still scanned.
	for _, a := range alerts {
		if a.PlayerName == "Jamie Arnold" {
			t.Errorf("unresolved prospect must not produce an alert: %+v", a)
		}
	}
	var sawResolved bool
	for _, a := range alerts {
		if a.PlayerName == "Jackson Holliday" {
			sawResolved = true
		}
	}
	if !sawResolved {
		t.Errorf("one unresolved name must not suppress the rest of the roster; got %d alerts", len(alerts))
	}
}

func TestFetchPerformanceAlerts_ReportsCoverageWhenEveryProspectResolves(t *testing.T) {
	// The zero case is the whole point: a clean run must still say so, or a
	// clean roster and a dead resolver are indistinguishable.
	statsapiStub(t, map[string]int{"Jackson Holliday": 808080})

	_, cov, err := FetchPerformanceAlerts([]fantrax.Player{minorsProspect("Jackson Holliday")}, nil, 2026, 14, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cov.Unresolved) != 0 {
		t.Fatalf("expected nothing unresolved, got %v", cov.Unresolved)
	}
	if cov.Read() != 1 {
		t.Errorf("expected 1 game log read, got %d", cov.Read())
	}
	want := "prospect scan coverage: 1 scanned, 1 game logs read (0 unresolved, 0 no game log)"
	if cov.String() != want {
		t.Errorf("zero case must still report coverage:\n got %q\nwant %q", cov.String(), want)
	}
}

func TestFetchPerformanceAlerts_SortsUnresolvedNames(t *testing.T) {
	// The errgroup completes in upstream-response order, so without the sort
	// the same roster prints a different line every run.
	statsapiStub(t, nil)

	prospects := []fantrax.Player{
		minorsProspect("Zed Zeller"),
		minorsProspect("Jamie Arnold"),
		minorsProspect("Mick Middleton"),
	}
	_, cov, err := FetchPerformanceAlerts(prospects, nil, 2026, 14, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(cov.Unresolved, ", ")
	want := "Jamie Arnold (ATH), Mick Middleton (ATH), Zed Zeller (ATH)"
	if got != want {
		t.Errorf("unresolved names must be sorted:\n got %q\nwant %q", got, want)
	}
}

// The second silent drop, and the one that makes an ID-only coverage line
// dangerous: the prospect resolves perfectly and is then dropped anyway
// because his game log could not be fetched. Counting only unresolved names
// would report a fully-covered scan that in fact read nothing — the same
// blindness this coverage line exists to end, one step later.
func TestFetchPerformanceAlerts_ReportsAProspectWhoseGameLogFailed(t *testing.T) {
	statsapiStub(t, map[string]int{"Jackson Holliday": 808080}, gameLogsFail)

	alerts, cov, err := FetchPerformanceAlerts(
		[]fantrax.Player{minorsProspect("Jackson Holliday")}, nil, 2026, 14, 5)
	if err != nil {
		t.Fatalf("a failed game log must not fail the scan: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("a prospect with no game log cannot produce an alert, got %d", len(alerts))
	}

	// The distinction that matters: he DID resolve. A coverage line counting
	// only Unresolved would call this a clean 1-of-1 scan.
	if len(cov.Unresolved) != 0 {
		t.Errorf("he resolved; Unresolved must stay empty, got %v", cov.Unresolved)
	}
	if cov.Resolved() != 1 {
		t.Errorf("Resolved() should still count him, got %d", cov.Resolved())
	}
	if cov.Read() != 0 {
		t.Errorf("no game log was read, so Read() must be 0, got %d", cov.Read())
	}
	if len(cov.NoGameLog) != 1 || cov.NoGameLog[0] != "Jackson Holliday (ATH)" {
		t.Errorf("expected him named under NoGameLog, got %v", cov.NoGameLog)
	}
	line := cov.String()
	if !strings.Contains(line, "0 game logs read") || !strings.Contains(line, "Jackson Holliday (ATH)") {
		t.Errorf("coverage line must show the scan read nothing and name him: %q", line)
	}
}

// Both drops at once, so the line cannot conflate them.
func TestFetchPerformanceAlerts_CountsTheTwoDropsSeparately(t *testing.T) {
	statsapiStub(t, map[string]int{"Jackson Holliday": 808080}, gameLogsFail)

	_, cov, err := FetchPerformanceAlerts([]fantrax.Player{
		minorsProspect("Jackson Holliday"), // resolves, game log 500s
		minorsProspect("Jamie Arnold"),     // never resolves
	}, nil, 2026, 14, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cov.Scanned != 2 || cov.Resolved() != 1 || cov.Read() != 0 {
		t.Errorf("scanned/resolved/read = %d/%d/%d, want 2/1/0", cov.Scanned, cov.Resolved(), cov.Read())
	}
	if len(cov.Unresolved) != 1 || len(cov.NoGameLog) != 1 {
		t.Errorf("the two drops must not be merged: unresolved=%v noGameLog=%v", cov.Unresolved, cov.NoGameLog)
	}
}
