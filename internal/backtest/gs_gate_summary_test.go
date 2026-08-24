package backtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sameDayGeneratedAt returns a GeneratedAt timestamp that unambiguously falls
// on date's Eastern-time calendar day: date itself (as used throughout this
// file) is midnight UTC, which is the previous ET evening, so tests that want
// a "fresh, same-day" snapshot must not use date verbatim.
func sameDayGeneratedAt(date time.Time) time.Time {
	return date.Add(18 * time.Hour)
}

func writeTestSnapshot(t *testing.T, dir string, date time.Time, pitchers []SnapshotPlayer) {
	t.Helper()
	writeTestSnapshotGenerated(t, dir, date, sameDayGeneratedAt(date), pitchers)
}

func writeTestSnapshotGenerated(t *testing.T, dir string, date, generatedAt time.Time, pitchers []SnapshotPlayer) {
	t.Helper()
	snap := Snapshot{
		Date:        date.Format("2006-01-02"),
		GeneratedAt: generatedAt,
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

func TestSummarizeGSGate_SumsSuppressedStartsAcrossDays(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshot(t, dir, day("2026-04-10"), []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5, GSSuppressed: true},
		{PlayerID: "p2", IsPitcher: true, ProjPtsPerGame: 9.0},
	})
	writeTestSnapshot(t, dir, day("2026-04-11"), []SnapshotPlayer{
		{PlayerID: "p3", IsPitcher: true, ProjPtsPerGame: 7.5, GSSuppressed: true},
		{PlayerID: "p4", IsPitcher: true, ProjPtsPerGame: 4.0, GSSuppressed: true},
	})

	got := SummarizeGSGate(NewFileSnapshotStore(dir), "", []time.Time{day("2026-04-10"), day("2026-04-11")})

	if got.SuppressedStarts != 3 {
		t.Errorf("SuppressedStarts = %d, want 3", got.SuppressedStarts)
	}
	if got.SuppressedPts != 24.0 {
		t.Errorf("SuppressedPts = %v, want 24.0", got.SuppressedPts)
	}
	if got.Days != 2 || got.DaysWithSnapshot != 2 {
		t.Errorf("Days = %d, DaysWithSnapshot = %d, want 2 and 2", got.Days, got.DaysWithSnapshot)
	}
	if len(got.ByDate) != 2 {
		t.Fatalf("ByDate = %d entries, want 2", len(got.ByDate))
	}
	if got.ByDate[0].Starts != 1 || got.ByDate[0].Pts != 12.5 {
		t.Errorf("ByDate[0] = %+v, want 1 start / 12.5 pts", got.ByDate[0])
	}
}

// A missing day is not a zero-suppression day. DaysWithSnapshot is what makes a
// window thinned by failed runs visible instead of reading as a quiet week.
func TestSummarizeGSGate_MissingDayIsNotAZero(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshot(t, dir, day("2026-04-10"), []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5, GSSuppressed: true},
	})

	got := SummarizeGSGate(NewFileSnapshotStore(dir), "", []time.Time{day("2026-04-10"), day("2026-04-11")})

	if got.Days != 2 {
		t.Errorf("Days = %d, want 2 (the window asked for)", got.Days)
	}
	if got.DaysWithSnapshot != 1 {
		t.Errorf("DaysWithSnapshot = %d, want 1", got.DaysWithSnapshot)
	}
	if len(got.ByDate) != 1 {
		t.Errorf("ByDate = %d entries, want only the day that had a snapshot", len(got.ByDate))
	}
}

// Hitters never carry the flag, but guard against a future writer setting it.
func TestSummarizeGSGate_IgnoresHitters(t *testing.T) {
	dir := t.TempDir()
	snap := Snapshot{
		Date:        "2026-04-10",
		GeneratedAt: sameDayGeneratedAt(day("2026-04-10")),
		Hitters:     []SnapshotPlayer{{PlayerID: "h1", ProjPtsPerGame: 5, GSSuppressed: true}},
		Pitchers:    []SnapshotPlayer{{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 8, GSSuppressed: true}},
	}
	b, _ := json.Marshal(snap)
	if err := os.WriteFile(filepath.Join(dir, "2026-04-10.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	got := SummarizeGSGate(NewFileSnapshotStore(dir), "", []time.Time{day("2026-04-10")})

	if got.SuppressedStarts != 1 || got.SuppressedPts != 8 {
		t.Errorf("got %d starts / %v pts, want 1 / 8 (hitter ignored)", got.SuppressedStarts, got.SuppressedPts)
	}
}

func TestSummarizeGSGate_EmptyWindow(t *testing.T) {
	got := SummarizeGSGate(NewFileSnapshotStore(t.TempDir()), "", nil)
	if got.SuppressedStarts != 0 || got.Days != 0 || len(got.ByDate) != 0 {
		t.Errorf("empty window = %+v, want zero value", got)
	}
}

// A --matchup pre-write for 2026-04-11 that was generated on 2026-04-08 (an
// earlier hourly run forecasting the rest of the matchup week) and never
// overwritten by 2026-04-11's own run is a stale snapshot: it carries
// GSSuppressed=false for every pitcher because the gate only ever ran for
// 2026-04-08. It must be counted as stale, not as a fully-measured,
// zero-suppression day.
func TestSummarizeGSGate_StaleSnapshotIsNotCountedAsMeasured(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshotGenerated(t, dir, day("2026-04-11"), sameDayGeneratedAt(day("2026-04-08")),
		[]SnapshotPlayer{
			{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5}, // GSSuppressed never set — the gate ran for 04-08, not 04-11
		})

	got := SummarizeGSGate(NewFileSnapshotStore(dir), "", []time.Time{day("2026-04-11")})

	if got.DaysStale != 1 {
		t.Errorf("DaysStale = %d, want 1", got.DaysStale)
	}
	if got.DaysWithSnapshot != 0 {
		t.Errorf("DaysWithSnapshot = %d, want 0 (stale snapshot must not count as measured)", got.DaysWithSnapshot)
	}
	if got.SuppressedStarts != 0 || got.SuppressedPts != 0 {
		t.Errorf("SuppressedStarts/Pts = %d/%v, want 0/0 (stale contributes nothing)", got.SuppressedStarts, got.SuppressedPts)
	}
	if len(got.ByDate) != 0 {
		t.Errorf("ByDate = %d entries, want 0", len(got.ByDate))
	}
}

// A day whose snapshot is present, fresh, and has zero suppressions (the gate
// ran and simply had nothing to decline) must count as measured but must not
// appear in ByDate — ByDate is "days that had a suppression", not "days that
// had a snapshot".
func TestSummarizeGSGate_ZeroSuppressionDayExcludedFromByDate(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshot(t, dir, day("2026-04-10"), []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5}, // GSSuppressed false: gate ran, declined nothing
	})

	got := SummarizeGSGate(NewFileSnapshotStore(dir), "", []time.Time{day("2026-04-10")})

	if got.DaysWithSnapshot != 1 {
		t.Errorf("DaysWithSnapshot = %d, want 1", got.DaysWithSnapshot)
	}
	if got.SuppressedStarts != 0 {
		t.Errorf("SuppressedStarts = %d, want 0", got.SuppressedStarts)
	}
	if len(got.ByDate) != 0 {
		t.Errorf("ByDate = %d entries, want 0 (measured but no suppression)", len(got.ByDate))
	}
}

// writeFloorSnapshot writes a snapshot carrying a GS floor, for the floor-side
// gate summary (rosterbot-dpm).
func writeFloorSnapshot(t *testing.T, dir string, date time.Time, floor int, pitchers []SnapshotPlayer) {
	t.Helper()
	snap := Snapshot{
		Date:        date.Format("2006-01-02"),
		GeneratedAt: sameDayGeneratedAt(date),
		GSFloor:     floor,
		Pitchers:    pitchers,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, date.Format("2006-01-02")+".json"), b, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

// TestSummarizeGSGate_CountsProtectedStartsSeparately pins that floor
// protections are tallied and never folded into the suppression totals. They
// are opposite events — one declined a start, the other refused to — and a
// summary that added them would report the gate as busier the more it left
// alone.
func TestSummarizeGSGate_CountsProtectedStartsSeparately(t *testing.T) {
	dir := t.TempDir()
	writeFloorSnapshot(t, dir, day("2026-04-10"), 10, []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5, GSSuppressed: true},
		{PlayerID: "p2", IsPitcher: true, ProjPtsPerGame: 8.0, GSFloorProtected: true},
		{PlayerID: "p3", IsPitcher: true, ProjPtsPerGame: 9.0},
	})

	got := SummarizeGSGate(NewFileSnapshotStore(dir), "", []time.Time{day("2026-04-10")})

	if got.SuppressedStarts != 1 || got.SuppressedPts != 12.5 {
		t.Errorf("suppressed = %d/%.1f, want 1/12.5", got.SuppressedStarts, got.SuppressedPts)
	}
	if got.ProtectedStarts != 1 || got.ProtectedPts != 8.0 {
		t.Errorf("protected = %d/%.1f, want 1/8.0", got.ProtectedStarts, got.ProtectedPts)
	}
	if got.FloorDays != 1 || got.FloorMin != 10 || got.FloorMax != 10 {
		t.Errorf("floor = %d days %d–%d, want 1 day 10–10", got.FloorDays, got.FloorMin, got.FloorMax)
	}
}

// TestSummarizeGSGate_ProtectionOnlyDayGetsARow pins that a day on which the
// gate protected a start but suppressed nothing still appears in ByDate. The
// gate acted; it acted on the floor's behalf. Dropping the row would make the
// floor side invisible in exactly the weeks it did something.
func TestSummarizeGSGate_ProtectionOnlyDayGetsARow(t *testing.T) {
	dir := t.TempDir()
	writeFloorSnapshot(t, dir, day("2026-04-10"), 10, []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 8.0, GSFloorProtected: true},
	})

	got := SummarizeGSGate(NewFileSnapshotStore(dir), "", []time.Time{day("2026-04-10")})

	if len(got.ByDate) != 1 {
		t.Fatalf("ByDate has %d rows, want 1 — a protection-only day is still a day the gate acted", len(got.ByDate))
	}
	if got.ByDate[0].Protected != 1 || got.ByDate[0].Starts != 0 {
		t.Errorf("row = %+v, want 0 suppressed / 1 protected", got.ByDate[0])
	}
}

// TestFormatGateSummary_FloorLineDistinguishesAbsentFromMet pins the three
// floor states apart. "No minimum configured" and "a minimum that needed no
// help" are different facts, and the second is the one that matters on a week
// finishing at the floor — the suppression line above reads identically
// (0 suppressed) in both.
func TestFormatGateSummary_FloorLineDistinguishesAbsentFromMet(t *testing.T) {
	absent := FormatGateSummary(GateSummary{Days: 7, DaysWithSnapshot: 7})
	if !strings.Contains(absent, "no GS minimum configured") {
		t.Errorf("absent-floor output should say so plainly:\n%s", absent)
	}

	met := FormatGateSummary(GateSummary{Days: 7, DaysWithSnapshot: 7, FloorDays: 7, FloorMin: 10, FloorMax: 10})
	if !strings.Contains(met, "GS minimum 10/wk in force") || !strings.Contains(met, "no start needed protecting") {
		t.Errorf("in-force floor with no protections should be reported:\n%s", met)
	}

	fired := FormatGateSummary(GateSummary{
		Days: 7, DaysWithSnapshot: 7, FloorDays: 7, FloorMin: 10, FloorMax: 15,
		ProtectedStarts: 2, ProtectedPts: 21.5,
	})
	if !strings.Contains(fired, "10–15/wk") {
		t.Errorf("a floor that moved across the window should render as a range:\n%s", fired)
	}
	if !strings.Contains(fired, "2 start(s) kept off the chopping block") {
		t.Errorf("protections should be reported:\n%s", fired)
	}
}
