package prospects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/playername"
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
// Unresolved-ID coverage tests (rosterbot-2onc)
// ---------------------------------------------------------------------------

// statsapiStub serves the MLB statsapi endpoints FetchPerformanceAlerts now
// reaches through playername.ResolveMLBAMIDs (the season-index dumps and the
// people-search fallback, at their real paths) plus this package's own
// game-log endpoint. A name absent from `people` is the unresolvable case:
// the real search API answers a query it cannot match with an empty list,
// not an error. The season-index sport dumps always answer empty — well
// below seasonIndexMinPlayers — so every name is refused by the index and
// falls through to the search, which is the path these tests exist to
// exercise. gameLogsFail makes the game-log endpoint 500 while people-search
// keeps working: the two statsapi endpoints fail independently in
// production, and that asymmetry is the whole reason PerformanceCoverage
// counts the two drops separately.
func gameLogsFail(c *stubConfig) { c.gameLogsFail = true }

type stubConfig struct{ gameLogsFail bool }

type stubOpt func(*stubConfig)

func statsapiStub(t *testing.T, people map[string]int, opts ...stubOpt) {
	t.Helper()
	var sc stubConfig
	for _, o := range opts {
		o(&sc)
	}

	idToName := make(map[int]string, len(people))
	for name, id := range people {
		idToName[id] = name
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
		switch {
		case r.URL.Path == "/log":
			if sc.gameLogsFail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(hotHitterLog())
			return
		case r.URL.Path == "/api/v1/people/search":
			var out []map[string]any
			for _, name := range strings.Split(r.URL.Query().Get("names"), ",") {
				if id, ok := people[name]; ok {
					out = append(out, map[string]any{"id": id, "fullName": name, "firstName": name, "lastName": name, "active": true})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"people": out})
			return
		case r.URL.Path == "/api/v1/people":
			var out []map[string]any
			for _, idStr := range strings.Split(r.URL.Query().Get("personIds"), ",") {
				id, _ := strconv.Atoi(idStr)
				if name, ok := idToName[id]; ok {
					out = append(out, map[string]any{"id": id, "fullName": name, "firstName": name, "lastName": name, "active": true})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"people": out})
			return
		case strings.HasPrefix(r.URL.Path, "/api/v1/sports/"):
			// Deliberately empty: below seasonIndexMinPlayers, so the season
			// index refuses and every name falls through to the search above.
			_ = json.NewEncoder(w).Encode(map[string]any{"people": []map[string]any{}})
			return
		default:
			http.NotFound(w, r)
		}
	}))

	restoreBaseURL := playername.SetBaseURLForTest(srv.URL)
	origLog, origDir := mlbGameLogURL, performanceCacheDir
	mlbGameLogURL = srv.URL + "/log?id=%d&group=%s&season=%d"
	performanceCacheDir = t.TempDir()
	t.Cleanup(func() {
		srv.Close()
		restoreBaseURL()
		mlbGameLogURL, performanceCacheDir = origLog, origDir
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

// ---------------------------------------------------------------------------
// Season-index integration (rosterbot-hdiu)
// ---------------------------------------------------------------------------

// TestFetchPerformanceAlerts_PrefersActiveNamesakeOverRetired pins that
// FetchPerformanceAlerts resolves through playername.ResolveMLBAMIDs — the
// same season-index-then-search path buildRankLookups uses — rather than its
// own former one-search-per-player path (resolveMLBPlayerID/
// fetchMLBPlayerID, deleted). MLB's people-search spans retired players, so a
// common name can return both an active player and a retired namesake; the
// season index resolves this class structurally by excluding the retired
// player from its season dump, but here the dump is deliberately too small
// (seasonIndexMinPlayers refuses it) so the request falls through to the
// search, where playername.claimName's active-first tie-break must still
// pick the active player. The old per-player search had no such tie-break —
// it took whichever result matched by name+team first, which the mutation
// proof exercises directly.
func TestFetchPerformanceAlerts_PrefersActiveNamesakeOverRetired(t *testing.T) {
	var mu sync.Mutex
	var gameLogIDs []int

	// The retired catcher (501447) is listed first — MLB's search response
	// order is not to be relied on, and this is the ordering that broke the
	// old per-player search (rosterbot-bms).
	peopleFixture := `{"people":[` +
		`{"id":501447,"fullName":"Jose Altuve","firstName":"Jose","lastName":"Altuve","active":false},` +
		`{"id":514888,"fullName":"Jose Altuve","firstName":"Jose","lastName":"Altuve","active":true}` +
		`]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/people/search", r.URL.Path == "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(peopleFixture))
		case strings.HasPrefix(r.URL.Path, "/api/v1/sports/"):
			// Deliberately empty — below seasonIndexMinPlayers — so the
			// season index refuses and the request reaches the search above.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"people":[]}`))
		case r.URL.Path == "/gamelog":
			id, _ := strconv.Atoi(r.URL.Query().Get("id"))
			mu.Lock()
			gameLogIDs = append(gameLogIDs, id)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"stats":[{"splits":[{"date":"2026-05-01","sport":{"abbreviation":"AAA"},"stat":{"atBats":4,"hits":1}}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	restoreBaseURL := playername.SetBaseURLForTest(srv.URL)
	defer restoreBaseURL()
	origLog, origDir := mlbGameLogURL, performanceCacheDir
	mlbGameLogURL = srv.URL + "/gamelog?id=%d&group=%s&season=%d"
	performanceCacheDir = t.TempDir()
	defer func() { mlbGameLogURL, performanceCacheDir = origLog, origDir }()

	_, cov, err := FetchPerformanceAlerts([]fantrax.Player{minorsProspect("Jose Altuve")}, nil, 2026, 14, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cov.Unresolved) != 0 {
		t.Fatalf("expected Jose Altuve to resolve, got unresolved=%v", cov.Unresolved)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gameLogIDs) != 1 || gameLogIDs[0] != 514888 {
		t.Errorf("expected the game log fetched for the ACTIVE player (514888), got %v", gameLogIDs)
	}
}

// TestFetchPerformanceAlerts_UnresolvedNameNeverFetchesAGameLog is the
// absence case beside the namesake test above: a name the resolver cannot
// place at all must be skipped without error and without ever reaching the
// game-log endpoint — the same silent-drop contract
// TestFetchPerformanceAlerts_ReportsAnUnresolvedProspectAndStillSkipsHim
// pins for the alerts list, asserted here at the HTTP boundary instead.
func TestFetchPerformanceAlerts_UnresolvedNameNeverFetchesAGameLog(t *testing.T) {
	var mu sync.Mutex
	var gameLogHits int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/people/search", r.URL.Path == "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"people":[]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/sports/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"people":[]}`))
		case r.URL.Path == "/gamelog":
			mu.Lock()
			gameLogHits++
			mu.Unlock()
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	restoreBaseURL := playername.SetBaseURLForTest(srv.URL)
	defer restoreBaseURL()
	origLog, origDir := mlbGameLogURL, performanceCacheDir
	mlbGameLogURL = srv.URL + "/gamelog?id=%d&group=%s&season=%d"
	performanceCacheDir = t.TempDir()
	defer func() { mlbGameLogURL, performanceCacheDir = origLog, origDir }()

	alerts, cov, err := FetchPerformanceAlerts([]fantrax.Player{minorsProspect("Nobody Findable")}, nil, 2026, 14, 5)
	if err != nil {
		t.Fatalf("an unresolvable name must not fail the scan: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected no alerts, got %d", len(alerts))
	}
	if len(cov.Unresolved) != 1 || cov.Unresolved[0] != "Nobody Findable (ATH)" {
		t.Errorf("expected him named as unresolved, got %v", cov.Unresolved)
	}
	if gameLogHits != 0 {
		t.Errorf("expected zero game-log requests for an unresolved name, got %d", gameLogHits)
	}
}
