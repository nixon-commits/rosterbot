package lineuprun

import (
	"reflect"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeSchedule stands in for internal/schedule.Client — the GS-budget phase's
// only network dependency. Keyed by YYYY-MM-DD so a test can describe a whole
// week without touching MLB statsapi.
type fakeSchedule struct {
	playing   map[string]map[string]bool
	probables map[string]map[string]string
	locked    map[string]map[string]bool
}

func (f *fakeSchedule) TeamsPlayingOn(d time.Time) (map[string]bool, error) {
	return f.playing[d.Format("2006-01-02")], nil
}
func (f *fakeSchedule) ProbableStarters(d time.Time) (map[string]string, error) {
	return f.probables[d.Format("2006-01-02")], nil
}
func (f *fakeSchedule) LockedTeams(d time.Time) (map[string]bool, error) {
	return f.locked[d.Format("2006-01-02")], nil
}

// --- countTodayStarts: fully pure, no I/O ---

func activeSP(id, name, team string) fantrax.Player {
	return fantrax.Player{ID: id, Name: name, MLBTeam: team, Status: "Active", PosShortNames: "SP"}
}

// Only a rostered SP who is BOTH on a locked team AND today's probable for that
// team consumes a game start. Each of those conditions rules out a real
// miscount that the inline version was written to avoid.
func TestCountTodayStarts_OnlyLockedProbableActiveSPs(t *testing.T) {
	roster := []fantrax.Player{
		activeSP("starting", "Ace Pitcher", "LAD"),  // counts
		activeSP("notprobable", "Bench Arm", "LAD"), // locked team, not probable
		activeSP("unlocked", "Later Guy", "NYY"),    // probable but team not locked yet
		activeSP("wrongteam", "Traded Guy", "SEA"),  // probable under a different team
		{ID: "reliever", Name: "Rel Iever", MLBTeam: "LAD", Status: "Active", PosShortNames: "RP"},
		{ID: "benched", Name: "Bench Ed", MLBTeam: "LAD", Status: "Reserve", PosShortNames: "SP"},
		{ID: "injured", Name: "Hurt Guy", MLBTeam: "LAD", Status: "Active", PosShortNames: "SP", IsInjured: true},
		{ID: "minors", Name: "Farm Hand", MLBTeam: "LAD", Status: "Active", PosShortNames: "SP", InMinors: true},
	}
	locked := map[string]bool{"LAD": true, "SEA": true}
	probs := map[string]string{
		"ace pitcher": "LAD",
		"later guy":   "NYY",
		"traded guy":  "TEX", // roster says SEA — must not count
	}

	if got := countTodayStarts(roster, locked, probs); got != 1 {
		t.Errorf("got %d, want 1 (only the locked+probable active SP)", got)
	}
}

func TestCountTodayStarts_EmptyInputsAreZero(t *testing.T) {
	if got := countTodayStarts(nil, nil, nil); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

// --- buildGSForecast: no network, only the schedule seam ---

func TestBuildGSForecast_ConfirmedProbablesUseProjectedPoints(t *testing.T) {
	today := day(2026, 7, 25)
	sched := &fakeSchedule{
		probables: map[string]map[string]string{
			"2026-07-26": {"ace pitcher": "LAD", "other guy": "NYY"},
		},
	}
	spNames := map[string]fantrax.Player{
		"ace pitcher": activeSP("a", "Ace Pitcher", "LAD"),
		"other guy":   activeSP("b", "Other Guy", "NYY"),
	}
	pts := func(p fantrax.Player) float64 {
		return map[string]float64{"a": 20, "b": 5}[p.ID]
	}

	got := buildGSForecast(sched, spNames, 5, today, day(2026, 7, 26), pts)

	if len(got) != 1 {
		t.Fatalf("expected one forecast day, got %d", len(got))
	}
	if got[0].Estimated != 0 {
		t.Errorf("confirmed probables must not also estimate, got %v", got[0].Estimated)
	}
	want := []float64{20, 5}
	gotPts := append([]float64(nil), got[0].ConfirmedStarters...)
	if !reflect.DeepEqual(sortedDesc(gotPts), want) {
		t.Errorf("confirmed starters = %v, want %v", gotPts, want)
	}
}

// More probables than active P slots: only the highest-value ones consume the
// budget, because only active-slot SPs can accrue a game start.
func TestBuildGSForecast_CapsConfirmedAtActivePitcherSlots(t *testing.T) {
	today := day(2026, 7, 25)
	sched := &fakeSchedule{
		probables: map[string]map[string]string{
			"2026-07-26": {"p1": "AAA", "p2": "BBB", "p3": "CCC"},
		},
	}
	spNames := map[string]fantrax.Player{
		"p1": activeSP("1", "p1", "AAA"),
		"p2": activeSP("2", "p2", "BBB"),
		"p3": activeSP("3", "p3", "CCC"),
	}
	pts := func(p fantrax.Player) float64 {
		return map[string]float64{"1": 3, "2": 30, "3": 20}[p.ID]
	}

	got := buildGSForecast(sched, spNames, 2, today, day(2026, 7, 26), pts)

	if len(got[0].ConfirmedStarters) != 2 {
		t.Fatalf("expected cap at 2 slots, got %d", len(got[0].ConfirmedStarters))
	}
	if !reflect.DeepEqual(sortedDesc(got[0].ConfirmedStarters), []float64{30, 20}) {
		t.Errorf("cap must keep the highest-value probables, got %v", got[0].ConfirmedStarters)
	}
}

// No probables published yet for a day → fall back to rotation math:
// rostered SPs whose team plays / 5, capped at active P slots.
func TestBuildGSForecast_NoProbablesEstimatesByRotation(t *testing.T) {
	today := day(2026, 7, 25)
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{
			"2026-07-26": {"AAA": true, "BBB": true, "CCC": true},
		},
	}
	spNames := map[string]fantrax.Player{
		"p1": activeSP("1", "p1", "AAA"),
		"p2": activeSP("2", "p2", "BBB"),
		"p3": activeSP("3", "p3", "CCC"),
		"p4": activeSP("4", "p4", "OFF"), // team not playing
	}

	got := buildGSForecast(sched, spNames, 5, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 0 })

	if len(got[0].ConfirmedStarters) != 0 {
		t.Errorf("no probables means no confirmed starters, got %v", got[0].ConfirmedStarters)
	}
	if got[0].Estimated != 3.0/5.0 {
		t.Errorf("estimated = %v, want 0.6 (3 SPs playing / 5-man rotation)", got[0].Estimated)
	}
}

// The slot cap bounds STARTS, so it applies after the rotation divisor. Here 20
// SPs playing is 4 expected starts, which the 2-slot cap binds down to 2.
func TestBuildGSForecast_EstimateCapsAtActivePitcherSlots(t *testing.T) {
	today := day(2026, 7, 25)
	playing := map[string]bool{}
	spNames := map[string]fantrax.Player{}
	for i := 0; i < 20; i++ {
		team := string(rune('A' + i))
		playing[team] = true
		spNames[team] = activeSP(team, team, team)
	}
	sched := &fakeSchedule{playing: map[string]map[string]bool{"2026-07-26": playing}}

	got := buildGSForecast(sched, spNames, 2, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 0 })

	if got[0].Estimated != 2.0 {
		t.Errorf("estimated = %v, want 2 (20/5 = 4 starts, capped at 2 P slots)", got[0].Estimated)
	}
}

// Regression for rosterbot-abd: with more rostered SPs playing than there are
// pitcher slots, the old code capped the pitcher count before dividing and
// reported numPSlots/rotationSize — an understatement on nearly every day of a
// real roster (10 rostered SPs, 6 active P slots). The slot cap must not bind
// here: 7 SPs playing is 1.4 expected starts, well under 6 slots.
func TestBuildGSForecast_MoreSPsPlayingThanSlotsDoesNotUnderstate(t *testing.T) {
	today := day(2026, 7, 25)
	playing := map[string]bool{}
	spNames := map[string]fantrax.Player{}
	for i := 0; i < 7; i++ {
		team := string(rune('A' + i))
		playing[team] = true
		spNames[team] = activeSP(team, team, team)
	}
	sched := &fakeSchedule{playing: map[string]map[string]bool{"2026-07-26": playing}}

	got := buildGSForecast(sched, spNames, 6, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 0 })

	if got[0].Estimated != 7.0/5.0 {
		t.Errorf("estimated = %v, want 1.4 (7 SPs playing / 5-man rotation, under the 6-slot cap)", got[0].Estimated)
	}
}

// The forecast covers today+1 through weekEnd — today itself is counted as used
// GS, not forecast demand.
func TestBuildGSForecast_SpansTomorrowThroughWeekEnd(t *testing.T) {
	today := day(2026, 7, 25)
	got := buildGSForecast(&fakeSchedule{}, nil, 5, today, day(2026, 7, 28),
		func(fantrax.Player) float64 { return 0 })

	want := []time.Time{day(2026, 7, 26), day(2026, 7, 27), day(2026, 7, 28)}
	var gotDates []time.Time
	for _, d := range got {
		gotDates = append(gotDates, d.Date)
	}
	if !reflect.DeepEqual(gotDates, want) {
		t.Errorf("forecast days = %v, want %v", gotDates, want)
	}
}

func TestBuildGSForecast_WeekAlreadyOverIsEmpty(t *testing.T) {
	today := day(2026, 7, 26)
	got := buildGSForecast(&fakeSchedule{}, nil, 5, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 0 })
	if len(got) != 0 {
		t.Errorf("no days remain after today, got %v", got)
	}
}

func sortedDesc(in []float64) []float64 {
	out := append([]float64(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] > out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
