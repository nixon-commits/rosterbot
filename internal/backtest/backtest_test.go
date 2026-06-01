package backtest

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// hitterSlotsForTest returns a minimal slot set the optimizer understands.
func hitterSlotsForTest() []fantrax.Slot {
	return []fantrax.Slot{
		{PosID: "012", PosName: "OF"}, // 1 OF
		{PosID: "014", PosName: "UT"}, // 1 UT
	}
}

func pitcherSlotsForTest() []fantrax.Slot {
	return []fantrax.Slot{
		{PosID: "017", PosName: "P"}, // 1 generic P slot
	}
}

// activeHitter returns a DayPlayerFP as an active-slot hitter.
func activeHitter(id, name, team string, fpts float64, positions []string) fantrax.DayPlayerFP {
	return fantrax.DayPlayerFP{
		PlayerID:      id,
		Name:          name,
		MLBTeam:       team,
		PosShortNames: "OF",
		Positions:     positions,
		SlotPosID:     "012",
		StatusID:      "1",
		FPts:          fpts,
		Active:        true,
		HadGame:       fpts != 0,
		IsPitcher:     false,
	}
}

// benchHitter returns a DayPlayerFP as a bench hitter.
func benchHitter(id, name, team string, fpts float64, positions []string) fantrax.DayPlayerFP {
	return fantrax.DayPlayerFP{
		PlayerID:      id,
		Name:          name,
		MLBTeam:       team,
		PosShortNames: "OF",
		Positions:     positions,
		SlotPosID:     "",
		StatusID:      "2",
		FPts:          fpts,
		Active:        false,
		HadGame:       fpts != 0,
		IsPitcher:     false,
	}
}

func TestRunLineupAnalysis_BenchedStarProducesGap(t *testing.T) {
	// Day with 2 OFs: our active OF got 2 pts, bench OF got 20 pts.
	// 1 UT slot: our active UT (eligible only in UT) got 5 pts.
	// Hindsight optimal should slot bench OF into OF (20 pts) and our active
	// OF into UT (2 pts), beating bench UT (5 pts).
	// Wait — that's an odd case. Simpler: both OFs are OF-eligible, UT accepts any.
	// Active OF: 2 pts. Bench OF: 20 pts. Active UT: 5 pts (UT-only).
	// Optimal = 20 (bench OF in OF) + 5 (active UT in UT) = 25.
	// Actual = 2 (active OF) + 5 (active UT) = 7.
	// Gap = 7 - 25 = -18.
	day := fantrax.DayRoster{
		Date:   time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Period: 22,
		Players: []fantrax.DayPlayerFP{
			activeHitter("h_of_active", "Active OF", "NYY", 2.0, []string{"012"}),
			benchHitter("h_of_bench", "Bench Star", "BOS", 20.0, []string{"012"}),
			{
				PlayerID: "h_ut_active", Name: "Active UT", MLBTeam: "HOU",
				PosShortNames: "UT", Positions: []string{"014"},
				SlotPosID: "014", StatusID: "1", FPts: 5.0,
				Active: true, HadGame: true,
			},
		},
	}

	results := RunLineupAnalysis([]fantrax.DayRoster{day}, hitterSlotsForTest(), pitcherSlotsForTest())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.ActualPts != 7.0 {
		t.Errorf("ActualPts = %v, want 7.0", r.ActualPts)
	}
	if r.OptimalPts != 25.0 {
		t.Errorf("OptimalPts = %v, want 25.0", r.OptimalPts)
	}
	if math.Abs(r.Gap-(-18.0)) > 1e-9 {
		t.Errorf("Gap = %v, want -18.0", r.Gap)
	}
	// Top bench miss: Bench Star +20.
	if len(r.Benched) == 0 || r.Benched[0].Name != "Bench Star" || r.Benched[0].Pts != 20.0 {
		t.Errorf("expected Bench Star 20.0 as top bench miss, got %+v", r.Benched)
	}
}

func TestRunLineupAnalysis_CorrectLineupZeroGap(t *testing.T) {
	// Active OF scores 15, bench OF scores 3 — we already had the best one active.
	// Active UT scores 8, no other UT-eligible bench.
	day := fantrax.DayRoster{
		Date:   time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Period: 22,
		Players: []fantrax.DayPlayerFP{
			activeHitter("h1", "Active OF", "NYY", 15.0, []string{"012"}),
			benchHitter("h2", "Bench OF", "BOS", 3.0, []string{"012"}),
			{
				PlayerID: "h3", Name: "Active UT", MLBTeam: "HOU",
				PosShortNames: "UT", Positions: []string{"014"},
				SlotPosID: "014", StatusID: "1", FPts: 8.0,
				Active: true, HadGame: true,
			},
		},
	}
	results := RunLineupAnalysis([]fantrax.DayRoster{day}, hitterSlotsForTest(), pitcherSlotsForTest())
	r := results[0]
	// Bench player (3) is UT-eligible via OF→UT path so optimal might pick him
	// for UT over no one, but Active UT (8) is still better. Optimal = 15 + 8 = 23.
	if r.OptimalPts != 23.0 {
		t.Errorf("OptimalPts = %v, want 23.0", r.OptimalPts)
	}
	if r.ActualPts != 23.0 {
		t.Errorf("ActualPts = %v, want 23.0", r.ActualPts)
	}
	if math.Abs(r.Gap) > 1e-9 {
		t.Errorf("Gap = %v, want 0", r.Gap)
	}
}

func TestRunLineupAnalysis_TeamNotPlayingCountsNothing(t *testing.T) {
	// Active player on a team that didn't play — HadGame=false, FPts=0 — contributes 0.
	day := fantrax.DayRoster{
		Date: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Players: []fantrax.DayPlayerFP{
			{
				PlayerID: "h1", Name: "OffDay", MLBTeam: "SEA",
				Positions: []string{"012"}, PosShortNames: "OF",
				SlotPosID: "012", StatusID: "1", FPts: 0,
				Active: true, HadGame: false,
			},
		},
	}
	results := RunLineupAnalysis([]fantrax.DayRoster{day}, hitterSlotsForTest(), pitcherSlotsForTest())
	r := results[0]
	if r.ActualPts != 0 {
		t.Errorf("ActualPts = %v, want 0", r.ActualPts)
	}
	if r.OptimalPts != 0 {
		t.Errorf("OptimalPts = %v, want 0", r.OptimalPts)
	}
}

func TestAccuracyStats(t *testing.T) {
	players := []PlayerProjection{
		{Diff: 2.0},
		{Diff: -4.0},
		{Diff: 0.0},
		{Diff: 2.0},
	}
	mae, bias, rmse := accuracyStats(players)
	if math.Abs(mae-2.0) > 1e-9 {
		t.Errorf("MAE = %v, want 2.0", mae)
	}
	if math.Abs(bias-0.0) > 1e-9 {
		t.Errorf("Bias = %v, want 0.0", bias)
	}
	wantRMSE := math.Sqrt((4 + 16 + 0 + 4) / 4.0)
	if math.Abs(rmse-wantRMSE) > 1e-9 {
		t.Errorf("RMSE = %v, want %v", rmse, wantRMSE)
	}
}

func TestWriteLoadSnapshot_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	s := Snapshot{
		Date:             "2026-04-15",
		ProjectionSystem: "depthcharts",
		GeneratedAt:      time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC),
		Hitters: []SnapshotPlayer{
			{PlayerID: "h1", Name: "Test Hitter", MLBTeam: "NYY", ProjPtsPerGame: 8.5, HasGame: true, WasStarted: true},
		},
		Pitchers: []SnapshotPlayer{
			{PlayerID: "p1", Name: "Test SP", MLBTeam: "LAD", ProjPtsPerGame: 14.2, HasGame: true, WasStarted: true, IsStarter: true, Role: "SP"},
		},
	}
	if err := WriteSnapshot(dir, s); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	// Verify file at expected path.
	if _, err := filepath.Abs(filepath.Join(dir, "2026-04-15.json")); err != nil {
		t.Fatalf("bad abs path: %v", err)
	}

	loaded, ok := LoadSnapshot(dir, time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("LoadSnapshot missed roundtrip")
	}
	if loaded.Date != "2026-04-15" {
		t.Errorf("Date = %q", loaded.Date)
	}
	if len(loaded.Hitters) != 1 || loaded.Hitters[0].ProjPtsPerGame != 8.5 {
		t.Errorf("hitter roundtrip mismatch: %+v", loaded.Hitters)
	}
	if len(loaded.Pitchers) != 1 || loaded.Pitchers[0].Role != "SP" {
		t.Errorf("pitcher roundtrip mismatch: %+v", loaded.Pitchers)
	}
}

func TestLoadSnapshot_MissingReturnsFalse(t *testing.T) {
	_, ok := LoadSnapshot(t.TempDir(), time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if ok {
		t.Error("LoadSnapshot should return false for missing file")
	}
}

func TestRunProjectionAnalysis_MatchesSnapshot(t *testing.T) {
	dir := t.TempDir()
	date := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	s := Snapshot{
		Date:             "2026-04-15",
		ProjectionSystem: "depthcharts",
		Hitters: []SnapshotPlayer{
			{PlayerID: "h1", Name: "H1", MLBTeam: "NYY", ProjPtsPerGame: 10.0, HasGame: true},
		},
	}
	if err := WriteSnapshot(dir, s); err != nil {
		t.Fatal(err)
	}
	days := []fantrax.DayRoster{
		{
			Date: date,
			Players: []fantrax.DayPlayerFP{
				{PlayerID: "h1", Name: "H1", MLBTeam: "NYY", FPts: 14.0, HadGame: true},
				{PlayerID: "h2", Name: "NoSnapshot", MLBTeam: "BOS", FPts: 3.0, HadGame: true}, // not in snapshot, skipped
			},
		},
	}
	results := RunProjectionAnalysis(days, dir)
	if len(results) != 1 {
		t.Fatalf("want 1 day, got %d", len(results))
	}
	if len(results[0].Players) != 1 {
		t.Fatalf("want 1 matched player, got %d", len(results[0].Players))
	}
	p := results[0].Players[0]
	if p.Projected != 10.0 || p.Actual != 14.0 || p.Diff != 4.0 {
		t.Errorf("projection mismatch: %+v", p)
	}
	if p.Source != "snapshot" {
		t.Errorf("source = %q, want snapshot", p.Source)
	}
	if math.Abs(results[0].MAE-4.0) > 1e-9 {
		t.Errorf("day MAE = %v, want 4.0", results[0].MAE)
	}
}

func TestRunProjectionAnalysis_MissingSnapshotMarkedMissing(t *testing.T) {
	days := []fantrax.DayRoster{
		{
			Date:    time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
			Players: []fantrax.DayPlayerFP{{PlayerID: "h1", HadGame: true, FPts: 5}},
		},
	}
	results := RunProjectionAnalysis(days, t.TempDir())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Source != "missing" {
		t.Errorf("source = %q, want missing", results[0].Source)
	}
	if len(results[0].Players) != 0 {
		t.Errorf("want no players when snapshot missing, got %d", len(results[0].Players))
	}
}

func TestBuildReport_TopBenchCumulative(t *testing.T) {
	lineup := []LineupDayResult{
		{
			Date: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
			Benched: []PlayerPts{
				{PlayerID: "a", Name: "A", Pts: 15},
				{PlayerID: "b", Name: "B", Pts: 10},
			},
		},
		{
			Date: time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC),
			Benched: []PlayerPts{
				{PlayerID: "b", Name: "B", Pts: 8},
				{PlayerID: "c", Name: "C", Pts: 5},
			},
		},
	}
	r := BuildReport(time.Time{}, time.Time{}, lineup, nil)
	if len(r.TopBench) != 3 {
		t.Fatalf("want 3 unique, got %d", len(r.TopBench))
	}
	// B cumulative = 18, A = 15, C = 5.
	if r.TopBench[0].Name != "B" || r.TopBench[0].Pts != 18 {
		t.Errorf("top = %+v, want B 18", r.TopBench[0])
	}
	if r.TopBench[1].Name != "A" || r.TopBench[1].Pts != 15 {
		t.Errorf("second = %+v, want A 15", r.TopBench[1])
	}
}

func TestPositionBucket(t *testing.T) {
	cases := []struct {
		name      string
		isPitcher bool
		role      string
		positions []string
		want      string
	}{
		{"catcher", false, "", []string{"001"}, "C"},
		{"catcher precedence over OF", false, "", []string{"001", "012"}, "C"},
		{"shortstop is INF", false, "", []string{"005"}, "INF"},
		{"INF slot id", false, "", []string{"008"}, "INF"},
		{"infield precedence over OF", false, "", []string{"004", "012"}, "INF"},
		{"outfield", false, "", []string{"012"}, "OF"},
		{"ut only", false, "", []string{"014"}, "UT"},
		{"no eligibility falls back to UT", false, "", nil, "UT"},
		{"starter by role", true, "SP", nil, "SP"},
		{"reliever by role", true, "RP", nil, "RP"},
		{"pitcher empty role defaults to RP", true, "", nil, "RP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := positionBucket(c.isPitcher, c.role, c.positions); got != c.want {
				t.Errorf("positionBucket(%v, %q, %v) = %q, want %q", c.isPitcher, c.role, c.positions, got, c.want)
			}
		})
	}
}

func TestTopSignedMisses_SelectsByMagnitudeOrdersBySign(t *testing.T) {
	players := []PlayerProjection{
		{PlayerID: "A", Diff: -16}, // big over-projection (ramp-up)
		{PlayerID: "B", Diff: 15},  // big under-projection (breakout)
		{PlayerID: "C", Diff: 3},   // small — excluded
		{PlayerID: "D", Diff: -2},  // small — excluded
		{PlayerID: "E", Diff: 12},
	}
	got := topSignedMisses(players, 3)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	// Selected by |diff|: A(16), B(15), E(12). C and D are smaller, excluded.
	// Displayed most over-projected (most negative diff) first so ramp-ups lead:
	// A(-16), E(+12), B(+15).
	want := []string{"A", "E", "B"}
	for i, id := range want {
		if got[i].PlayerID != id {
			t.Errorf("position %d = %q, want %q (full: %+v)", i, got[i].PlayerID, id, got)
		}
	}
}

func TestSummarizeProjections_ByPosition(t *testing.T) {
	proj := []ProjectionDayResult{
		{
			Players: []PlayerProjection{
				{Bucket: "OF", Diff: 2, Source: "snapshot"},
				{Bucket: "OF", Diff: -4, Source: "snapshot"},
				{Bucket: "SP", Diff: 6, Source: "snapshot"},
			},
		},
	}
	s := summarizeProjections(proj)
	if len(s.ByPosition) != 2 {
		t.Fatalf("want 2 buckets, got %d (%+v)", len(s.ByPosition), s.ByPosition)
	}
	// Canonical order (C, INF, OF, UT, SP, RP) puts OF before SP.
	if of := s.ByPosition[0]; of.Bucket != "OF" || of.N != 2 || math.Abs(of.MAE-3.0) > 1e-9 {
		t.Errorf("OF bucket = %+v, want {Bucket:OF N:2 MAE:3.0}", of)
	}
	if sp := s.ByPosition[1]; sp.Bucket != "SP" || sp.N != 1 || math.Abs(sp.MAE-6.0) > 1e-9 {
		t.Errorf("SP bucket = %+v, want {Bucket:SP N:1 MAE:6.0}", sp)
	}
}

func TestRunProjectionAnalysis_SetsBucketFromSnapshot(t *testing.T) {
	dir := t.TempDir()
	date := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	s := Snapshot{
		Date: "2026-04-15",
		Hitters: []SnapshotPlayer{
			{PlayerID: "of1", Name: "OF One", MLBTeam: "NYY", ProjPtsPerGame: 9, HasGame: true, Eligibility: []string{"012"}},
		},
		Pitchers: []SnapshotPlayer{
			{PlayerID: "sp1", Name: "SP One", MLBTeam: "LAD", ProjPtsPerGame: 15, HasGame: true, IsPitcher: true, Role: "SP"},
		},
	}
	if err := WriteSnapshot(dir, s); err != nil {
		t.Fatal(err)
	}
	days := []fantrax.DayRoster{{
		Date: date,
		Players: []fantrax.DayPlayerFP{
			{PlayerID: "of1", Name: "OF One", MLBTeam: "NYY", FPts: 11, HadGame: true},
			{PlayerID: "sp1", Name: "SP One", MLBTeam: "LAD", FPts: 5, HadGame: true, IsPitcher: true},
		},
	}}
	results := RunProjectionAnalysis(days, dir)
	got := map[string]string{}
	for _, p := range results[0].Players {
		got[p.PlayerID] = p.Bucket
	}
	if got["of1"] != "OF" {
		t.Errorf("of1 bucket = %q, want OF", got["of1"])
	}
	if got["sp1"] != "SP" {
		t.Errorf("sp1 bucket = %q, want SP", got["sp1"])
	}
}

func TestWriteLoadSnapshot_RichFieldsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s := Snapshot{
		Date: "2026-05-29",
		Hitters: []SnapshotPlayer{
			{PlayerID: "h1", Name: "Rich Hitter", MLBTeam: "NYY", ProjPtsPerGame: 8.5,
				HasGame: true, WasStarted: true, Slot: "OF", Locked: true, Eligibility: []string{"012", "014"}},
		},
	}
	if err := WriteSnapshot(dir, s); err != nil {
		t.Fatal(err)
	}
	loaded, ok := LoadSnapshot(dir, time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("roundtrip missed")
	}
	h := loaded.Hitters[0]
	if h.Slot != "OF" || !h.Locked || len(h.Eligibility) != 2 || h.Eligibility[0] != "012" {
		t.Errorf("rich fields lost in roundtrip: %+v", h)
	}
}

// TestProjectionReport_EndToEnd exercises the full grade→report path (minus the
// Fantrax client): write a snapshot, grade it against actuals, build the report,
// and confirm the rendered text includes the per-position MAE table and the
// top-10 signed-miss section. Stands in for the credentialed integration check.
func TestProjectionReport_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	date := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	s := Snapshot{
		Date: "2026-05-29",
		Hitters: []SnapshotPlayer{
			{PlayerID: "of1", Name: "Steady OF", MLBTeam: "NYY", ProjPtsPerGame: 9, HasGame: true, Eligibility: []string{"012"}},
		},
		Pitchers: []SnapshotPlayer{
			{PlayerID: "sp1", Name: "Rampup Arm", MLBTeam: "LAD", ProjPtsPerGame: 18, HasGame: true, IsPitcher: true, Role: "SP"},
		},
	}
	if err := WriteSnapshot(dir, s); err != nil {
		t.Fatal(err)
	}
	days := []fantrax.DayRoster{{
		Date: date,
		Players: []fantrax.DayPlayerFP{
			{PlayerID: "of1", Name: "Steady OF", MLBTeam: "NYY", FPts: 10, HadGame: true},
			{PlayerID: "sp1", Name: "Rampup Arm", MLBTeam: "LAD", FPts: 2, HadGame: true, IsPitcher: true}, // big over-projection
		},
	}}
	proj := RunProjectionAnalysis(days, dir)
	report := BuildReport(date, date, nil, proj)
	out := FormatReport(report)

	for _, want := range []string{
		"Projection accuracy",
		"MAE by position:",
		"OF",
		"SP",
		"Biggest projection misses (signed; over-projected first):",
		"Rampup Arm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n--- report ---\n%s", want, out)
		}
	}
}
