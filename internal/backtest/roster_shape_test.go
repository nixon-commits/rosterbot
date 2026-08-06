package backtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPitcherIsDeployable_ExcludesRestDayStarters is the guard that keeps this
// metric from degenerating into a measurement of rotation cadence.
//
// SnapshotPlayer.ProjPtsPerGame for a pitcher is ScoredPitcher.ExpectedPts,
// which is UNDISCOUNTED — NonStarterSPDiscount (0.05x) is applied downstream,
// only to the local `generic` slice inside OptimizePitcherLineup, and never
// reaches the snapshot. HasGame means only that the pitcher's MLB team plays.
// So a HasGame-based denominator credits an ace his full ~16pt projection on
// every rest day, which against a five-man rotation is four days in five.
func TestPitcherIsDeployable_ExcludesRestDayStarters(t *testing.T) {
	restDayAce := SnapshotPlayer{
		Status: "Active", Role: "SP", IsPitcher: true,
		ProjPtsPerGame: 16.0, HasGame: true, IsStarter: false, GSSuppressed: false,
	}
	if pitcherIsDeployable(restDayAce) {
		t.Error("an SP whose team plays but who is not a probable starter must not count as deployable value")
	}
}

// TestPitcherIsDeployable_IncludesGateSuppressedStart pins that the gate's own
// suppressions land in the pitcher denominator. applyGSGate flips IsStarter to
// false on its way out, so IsStarter||GSSuppressed is what reconstructs the
// pre-gate probable-starter set. This is what makes the roster-shape block
// cohere with the GS-gate block printed directly above it.
func TestPitcherIsDeployable_IncludesGateSuppressedStart(t *testing.T) {
	suppressed := SnapshotPlayer{
		Status: "Active", Role: "SP", IsPitcher: true,
		ProjPtsPerGame: 11.0, HasGame: true, IsStarter: false, GSSuppressed: true,
	}
	if !pitcherIsDeployable(suppressed) {
		t.Error("a start the GS gate declined is owned value the roster could not field")
	}
}

func TestDeployability_TableCases(t *testing.T) {
	tests := []struct {
		name   string
		player SnapshotPlayer
		hitter bool // run through hitterIsDeployable rather than pitcherIsDeployable
		want   bool
	}{
		{
			name:   "hitter active with a game",
			player: SnapshotPlayer{Status: "Active", HasGame: true},
			hitter: true, want: true,
		},
		{
			name:   "hitter on the bench with a game is still owned value",
			player: SnapshotPlayer{Status: "Reserve", HasGame: true},
			hitter: true, want: true,
		},
		{
			name:   "hitter whose team is idle",
			player: SnapshotPlayer{Status: "Active", HasGame: false},
			hitter: true, want: false,
		},
		{
			name:   "injured hitter is not a roster shape",
			player: SnapshotPlayer{Status: "Injured Reserve", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "minor-league hitter cannot be fielded",
			player: SnapshotPlayer{Status: "Minors", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "pre-schema hitter has no status to judge",
			player: SnapshotPlayer{Status: "", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "probable starter",
			player: SnapshotPlayer{Status: "Active", Role: "SP", IsStarter: true, HasGame: true},
			want:   true,
		},
		{
			name:   "reliever whose team plays",
			player: SnapshotPlayer{Status: "Reserve", Role: "RP", HasGame: true},
			want:   true,
		},
		{
			name:   "reliever whose team is idle",
			player: SnapshotPlayer{Status: "Active", Role: "RP", HasGame: false},
			want:   false,
		},
		{
			name:   "injured probable starter",
			player: SnapshotPlayer{Status: "Injured Reserve", Role: "SP", IsStarter: true, HasGame: true},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			if tt.hitter {
				got = hitterIsDeployable(tt.player)
			} else {
				got = pitcherIsDeployable(tt.player)
			}
			if got != tt.want {
				t.Errorf("deployable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSideShape_StrandedAndRate(t *testing.T) {
	s := SideShape{OwnedPts: 200, FieldedPts: 150}
	if got := s.StrandedPts(); got != 50 {
		t.Errorf("StrandedPts = %v, want 50", got)
	}
	rate, ok := s.FieldedRate()
	if !ok {
		t.Fatal("FieldedRate should be defined when owned value is positive")
	}
	if rate != 0.75 {
		t.Errorf("FieldedRate = %v, want 0.75", rate)
	}
}

// TestSideShape_ZeroOwnedHasNoRate pins that an empty side reports "undefined"
// rather than 0%. A window in which nothing was deployable is not a window in
// which everything was stranded, and rendering it as 0% would read as a
// catastrophic roster failure.
func TestSideShape_ZeroOwnedHasNoRate(t *testing.T) {
	if _, ok := (SideShape{}).FieldedRate(); ok {
		t.Error("FieldedRate must be undefined when no deployable value exists")
	}
}

// writeShapeSnapshot writes a snapshot carrying both roles plus a GS cap.
// The existing writeTestSnapshot helper is pitcher-only, which the roster-shape
// tests cannot use.
func writeShapeSnapshot(t *testing.T, dir string, date time.Time, gsLimit int, hitters, pitchers []SnapshotPlayer) {
	t.Helper()
	writeShapeSnapshotGenerated(t, dir, date, sameDayGeneratedAt(date), gsLimit, hitters, pitchers)
}

func writeShapeSnapshotGenerated(t *testing.T, dir string, date, generatedAt time.Time, gsLimit int, hitters, pitchers []SnapshotPlayer) {
	t.Helper()
	snap := Snapshot{
		Date:        date.Format("2006-01-02"),
		GeneratedAt: generatedAt,
		GSLimit:     gsLimit,
		Hitters:     hitters,
		Pitchers:    pitchers,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(dir, date.Format("2006-01-02")+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func TestSummarizeRosterShape_AccumulatesBothSides(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12,
		[]SnapshotPlayer{
			{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10},
			{PlayerID: "h2", Status: "Reserve", HasGame: true, ProjPtsPerGame: 4},
			{PlayerID: "h3", Status: "Minors", HasGame: true, ProjPtsPerGame: 99},
		},
		[]SnapshotPlayer{
			{PlayerID: "p1", IsPitcher: true, Role: "SP", Status: "Active", IsStarter: true, HasGame: true, WasStarted: true, ProjPtsPerGame: 16},
			{PlayerID: "p2", IsPitcher: true, Role: "SP", Status: "Active", GSSuppressed: true, HasGame: true, ProjPtsPerGame: 11},
			{PlayerID: "p3", IsPitcher: true, Role: "SP", Status: "Active", HasGame: true, ProjPtsPerGame: 20},
		},
	)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06")}, 13, 6)

	if got.DaysWithSnapshot != 1 || got.Days != 1 {
		t.Errorf("Days = %d / DaysWithSnapshot = %d, want 1 / 1", got.Days, got.DaysWithSnapshot)
	}
	if got.HitterSlots != 13 || got.PitcherSlots != 6 {
		t.Errorf("slots = %d/%d, want 13/6", got.HitterSlots, got.PitcherSlots)
	}
	if got.GSCapMin != 12 || got.GSCapMax != 12 {
		t.Errorf("cap = %d–%d, want 12–12", got.GSCapMin, got.GSCapMax)
	}
	// Minors hitter excluded: owned is 10+4, fielded is 10.
	if got.Hitters.OwnedPts != 14 || got.Hitters.FieldedPts != 10 {
		t.Errorf("hitters = %+v, want owned 14 fielded 10", got.Hitters)
	}
	// p3 is a rest-day SP and excluded entirely; owned is 16+11, fielded is 16.
	if got.Pitchers.OwnedPts != 27 || got.Pitchers.FieldedPts != 16 {
		t.Errorf("pitchers = %+v, want owned 27 fielded 16", got.Pitchers)
	}
}

// TestSummarizeRosterShape_ExcludesPreSchemaDay pins the trap this field
// introduces. On a snapshot written before Status existed, every status is ""
// and the deployable filter yields zero owned value — which is indistinguishable
// from a day measured as fully fielded. It has to be counted and excluded
// explicitly, the same way DaysStale is one layer up.
func TestSummarizeRosterShape_ExcludesPreSchemaDay(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 0,
		[]SnapshotPlayer{{PlayerID: "h1", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}},
		[]SnapshotPlayer{{PlayerID: "p1", IsPitcher: true, Role: "SP", IsStarter: true, HasGame: true, ProjPtsPerGame: 16}},
	)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06")}, 13, 6)

	if got.DaysPreSchema != 1 {
		t.Errorf("DaysPreSchema = %d, want 1", got.DaysPreSchema)
	}
	if got.DaysWithSnapshot != 0 {
		t.Errorf("DaysWithSnapshot = %d, want 0 — a pre-schema day is not a measured day", got.DaysWithSnapshot)
	}
	if _, ok := got.Hitters.FieldedRate(); ok {
		t.Error("a pre-schema-only window must not produce a defined rate")
	}
}

// TestSummarizeRosterShape_ExcludesStaleDay reuses the sameETDate rule: a
// --matchup pre-write never overwritten by that day's own run carries a roster
// state from a different day and must not be measured.
func TestSummarizeRosterShape_ExcludesStaleDay(t *testing.T) {
	dir := t.TempDir()
	d := day("2026-08-06")
	writeShapeSnapshotGenerated(t, dir, d, sameDayGeneratedAt(d.AddDate(0, 0, -3)), 12,
		[]SnapshotPlayer{{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}},
		nil,
	)

	got := SummarizeRosterShape(dir, []time.Time{d}, 13, 6)

	if got.DaysStale != 1 || got.DaysWithSnapshot != 0 {
		t.Errorf("DaysStale = %d / DaysWithSnapshot = %d, want 1 / 0", got.DaysStale, got.DaysWithSnapshot)
	}
	if got.Hitters.OwnedPts != 0 {
		t.Errorf("stale day contributed %v owned pts, want 0", got.Hitters.OwnedPts)
	}
}

// TestSummarizeRosterShape_MissingDayIsNotMeasured pins that a date with no
// snapshot at all counts toward Days but contributes nothing, matching
// SummarizeGSGate.
func TestSummarizeRosterShape_MissingDayIsNotMeasured(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12,
		[]SnapshotPlayer{{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}},
		nil,
	)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06"), day("2026-08-07")}, 13, 6)

	if got.Days != 2 || got.DaysWithSnapshot != 1 {
		t.Errorf("Days = %d / DaysWithSnapshot = %d, want 2 / 1", got.Days, got.DaysWithSnapshot)
	}
}

// TestSummarizeRosterShape_IsValueWeightedNotMeanOfRates pins that rates come
// from summed totals rather than averaged daily rates. A quiet Monday with one
// player having a game must not weigh the same as a full Saturday.
//
// The two days are sized to separate the formulas: day 1 owns 100 and fields
// none (0%), day 2 owns 10 and fields all of it (100%). Mean-of-daily-rates
// gives 50%; value-weighted gives 10/110 ≈ 9.1%.
func TestSummarizeRosterShape_IsValueWeightedNotMeanOfRates(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12,
		[]SnapshotPlayer{{PlayerID: "h1", Status: "Reserve", HasGame: true, ProjPtsPerGame: 100}}, nil)
	writeShapeSnapshot(t, dir, day("2026-08-07"), 12,
		[]SnapshotPlayer{{PlayerID: "h2", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}}, nil)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06"), day("2026-08-07")}, 13, 6)

	rate, ok := got.Hitters.FieldedRate()
	if !ok {
		t.Fatal("rate should be defined")
	}
	if want := 10.0 / 110.0; rate != want {
		t.Errorf("FieldedRate = %v, want %v (value-weighted, not the 50%% mean of daily rates)", rate, want)
	}
}

// TestSummarizeRosterShape_CapRangeIgnoresUntrackedDays pins that a day with GS
// tracking off records 0 and is skipped rather than dragging the minimum to
// zero and rendering a bogus "GS cap 0–18/wk".
func TestSummarizeRosterShape_CapRangeIgnoresUntrackedDays(t *testing.T) {
	dir := t.TempDir()
	hitters := []SnapshotPlayer{{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 5}}
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12, hitters, nil)
	writeShapeSnapshot(t, dir, day("2026-08-07"), 0, hitters, nil)
	writeShapeSnapshot(t, dir, day("2026-08-08"), 18, hitters, nil)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06"), day("2026-08-07"), day("2026-08-08")}, 13, 6)

	if got.GSCapMin != 12 || got.GSCapMax != 18 {
		t.Errorf("cap = %d–%d, want 12–18", got.GSCapMin, got.GSCapMax)
	}
}
