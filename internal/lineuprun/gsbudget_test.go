package lineuprun

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeSchedule stands in for internal/schedule.Client — the GS-budget phase's
// only network dependency. Keyed by YYYY-MM-DD so a test can describe a whole
// week without touching MLB statsapi.
type fakeSchedule struct {
	playing      map[string]map[string]bool
	probables    map[string]map[string]string
	locked       map[string]map[string]bool
	playingErr   map[string]error
	probablesErr map[string]error
}

func (f *fakeSchedule) TeamsPlayingOn(d time.Time) (map[string]bool, error) {
	k := d.Format("2006-01-02")
	if err := f.playingErr[k]; err != nil {
		return nil, err
	}
	return f.playing[k], nil
}
func (f *fakeSchedule) ProbableStarters(d time.Time) (map[string]string, error) {
	k := d.Format("2006-01-02")
	if err := f.probablesErr[k]; err != nil {
		return nil, err
	}
	return f.probables[k], nil
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

	got, _ := buildGSForecast(sched, spNames, 5, today, day(2026, 7, 26), pts)

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

	got, _ := buildGSForecast(sched, spNames, 2, today, day(2026, 7, 26), pts)

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

	got, _ := buildGSForecast(sched, spNames, 5, today, day(2026, 7, 26),
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

	got, _ := buildGSForecast(sched, spNames, 2, today, day(2026, 7, 26),
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

	got, _ := buildGSForecast(sched, spNames, 6, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 0 })

	if got[0].Estimated != 7.0/5.0 {
		t.Errorf("estimated = %v, want 1.4 (7 SPs playing / 5-man rotation, under the 6-slot cap)", got[0].Estimated)
	}
}

// The forecast covers today+1 through weekEnd — today itself is counted as used
// GS, not forecast demand.
func TestBuildGSForecast_SpansTomorrowThroughWeekEnd(t *testing.T) {
	today := day(2026, 7, 25)
	got, _ := buildGSForecast(&fakeSchedule{}, nil, 5, today, day(2026, 7, 28),
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
	got, _ := buildGSForecast(&fakeSchedule{}, nil, 5, today, day(2026, 7, 26),
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

// --- rosterbot-1jvj: the rotation-math fallback must be per-team ---

// The headline defect: MLB trickles out probables up to a week ahead, so the
// league-wide map is almost never empty on a future day. Gating the estimate on
// len(probs) > 0 therefore meant a single announced probable ANYWHERE in the
// league — for any team, ours or not — zeroed the whole day's forecast.
//
// Measured against statsapi for 2026-08-23: 2 of 30 teams playing had an
// announced probable. Those 2 must not silence the other 28.
func TestBuildGSForecast_UnannouncedTeamsStillEstimateWhenLeagueHasProbables(t *testing.T) {
	today := day(2026, 7, 25)
	playing := map[string]bool{"ZZZ": true} // the one announced team, not ours
	spNames := map[string]fantrax.Player{}
	for i := 0; i < 7; i++ {
		team := string(rune('A' + i))
		playing[team] = true
		spNames[team] = activeSP(team, team, team)
	}
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{"2026-07-26": playing},
		probables: map[string]map[string]string{
			"2026-07-26": {"someone elses ace": "ZZZ"},
		},
	}

	got, _ := buildGSForecast(sched, spNames, 6, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 0 })

	if len(got[0].ConfirmedStarters) != 0 {
		t.Errorf("none of the announced probables are ours, got %v", got[0].ConfirmedStarters)
	}
	if got[0].Estimated != 7.0/5.0 {
		t.Errorf("estimated = %v, want 1.4 (7 of our SP teams play with no probable announced)", got[0].Estimated)
	}
}

// A day mid-announcement carries both regimes at once: the SPs already named
// are confirmed, and the ones whose club has published nothing yet still owe a
// rotation-math estimate. The old either/or branch could only report one.
func TestBuildGSForecast_CombinesConfirmedAndEstimatedOnTheSameDay(t *testing.T) {
	today := day(2026, 7, 25)
	playing := map[string]bool{"AAA": true, "BBB": true}
	spNames := map[string]fantrax.Player{
		"named one": activeSP("1", "Named One", "AAA"),
		"named two": activeSP("2", "Named Two", "BBB"),
	}
	for i := 0; i < 5; i++ {
		team := "U" + string(rune('A'+i))
		playing[team] = true
		spNames[team] = activeSP(team, team, team)
	}
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{"2026-07-26": playing},
		probables: map[string]map[string]string{
			"2026-07-26": {"named one": "AAA", "named two": "BBB"},
		},
	}

	got, _ := buildGSForecast(sched, spNames, 6, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 10 })

	if len(got[0].ConfirmedStarters) != 2 {
		t.Errorf("confirmed = %v, want the 2 announced SPs", got[0].ConfirmedStarters)
	}
	if got[0].Estimated != 1.0 {
		t.Errorf("estimated = %v, want 1.0 (5 unannounced SP teams / 5-man rotation)", got[0].Estimated)
	}
}

// numPSlots bounds how many starts can accrue in a day, so it has to bound the
// DAY, not each regime separately — otherwise confirmed + estimated can exceed
// the slot count and forecast starts the roster could never field.
func TestBuildGSForecast_CombinedDayTotalCapsAtPitcherSlots(t *testing.T) {
	today := day(2026, 7, 25)
	playing := map[string]bool{"AAA": true, "BBB": true}
	spNames := map[string]fantrax.Player{
		"named one": activeSP("1", "Named One", "AAA"),
		"named two": activeSP("2", "Named Two", "BBB"),
	}
	for i := 0; i < 20; i++ {
		team := "U" + string(rune('A'+i))
		playing[team] = true
		spNames[team] = activeSP(team, team, team)
	}
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{"2026-07-26": playing},
		probables: map[string]map[string]string{
			"2026-07-26": {"named one": "AAA", "named two": "BBB"},
		},
	}

	// 2 confirmed + 20/5 = 4 estimated = 6, against 3 pitcher slots.
	got, _ := buildGSForecast(sched, spNames, 3, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 10 })

	total := float64(len(got[0].ConfirmedStarters)) + got[0].Estimated
	if total != 3 {
		t.Errorf("confirmed %d + estimated %v = %v, want the day capped at 3 P slots",
			len(got[0].ConfirmedStarters), got[0].Estimated, total)
	}
}

// A club that HAS named its starter is settled: if it isn't our pitcher, our
// other arm on that club is not starting either, so the day owes no estimate
// for them. This is the boundary that separates the fix from simply always
// estimating.
func TestBuildGSForecast_OurSPOnAClubThatNamedSomeoneElseContributesNothing(t *testing.T) {
	today := day(2026, 7, 25)
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{"2026-07-26": {"AAA": true}},
		probables: map[string]map[string]string{
			"2026-07-26": {"their ace": "AAA"},
		},
	}
	spNames := map[string]fantrax.Player{
		"our arm": activeSP("1", "Our Arm", "AAA"),
	}

	got, _ := buildGSForecast(sched, spNames, 6, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 10 })

	if len(got[0].ConfirmedStarters) != 0 || got[0].Estimated != 0 {
		t.Errorf("club already named a different starter: got confirmed=%v estimated=%v, want both empty",
			got[0].ConfirmedStarters, got[0].Estimated)
	}
}

// A failed probables fetch must not read as "every club has settled and none of
// them picked us" — that is indistinguishable from a genuine zero and is
// exactly how an upstream outage would silently zero the week's forecast. An
// unanswerable question about who is announced leaves every playing club
// unannounced, which is the honest reading, and says so.
func TestBuildGSForecast_ProbablesFetchErrorWarnsAndStillEstimates(t *testing.T) {
	today := day(2026, 7, 25)
	playing := map[string]bool{}
	spNames := map[string]fantrax.Player{}
	for i := 0; i < 7; i++ {
		team := string(rune('A' + i))
		playing[team] = true
		spNames[team] = activeSP(team, team, team)
	}
	sched := &fakeSchedule{
		playing:      map[string]map[string]bool{"2026-07-26": playing},
		probablesErr: map[string]error{"2026-07-26": errors.New("statsapi 503")},
	}

	got, warnings := buildGSForecast(sched, spNames, 6, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 0 })

	if got[0].Estimated != 7.0/5.0 {
		t.Errorf("estimated = %v, want 1.4 — a fetch failure must not read as zero demand", got[0].Estimated)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "statsapi 503") {
		t.Errorf("warnings = %v, want one naming the underlying error", warnings)
	}
}

// The healthy path stays silent, so a warning in the run log always means
// something actually failed.
func TestBuildGSForecast_HealthyDaysWarnAboutNothing(t *testing.T) {
	today := day(2026, 7, 25)
	sched := &fakeSchedule{
		playing:   map[string]map[string]bool{"2026-07-26": {"AAA": true}},
		probables: map[string]map[string]string{"2026-07-26": {"our arm": "AAA"}},
	}
	spNames := map[string]fantrax.Player{"our arm": activeSP("1", "Our Arm", "AAA")}

	_, warnings := buildGSForecast(sched, spNames, 6, today, day(2026, 7, 26),
		func(fantrax.Player) float64 { return 10 })

	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none on a healthy day", warnings)
	}
}
