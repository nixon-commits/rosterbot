package lineuprun

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func pitcherDay(name, team string, d time.Time, started bool) fantrax.PitcherDay {
	return fantrax.PitcherDay{PitcherName: name, MLBTeam: team, Date: d, Started: started}
}

// scheduleFor builds a fake schedule on which every listed club plays on every
// listed date.
func scheduleFor(clubs []string, dates ...time.Time) *fakeSchedule {
	f := &fakeSchedule{playing: map[string]map[string]bool{}}
	for _, d := range dates {
		m := map[string]bool{}
		for _, c := range clubs {
			m[c] = true
		}
		f.playing[d.Format("2006-01-02")] = m
	}
	return f
}

func nearly(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// --- computeStartRates ---

// The denominator is club-game-days the pitcher was ROSTERED, not days in the
// window. A pitcher acquired two days ago has two opportunities, not the whole
// window's worth of days he was somebody else's problem.
func TestComputeStartRates_DenominatorIsClubGameDaysWhileRostered(t *testing.T) {
	today := day(2026, 7, 25)
	seasonStart := day(2026, 7, 21)
	window := []time.Time{day(2026, 7, 21), day(2026, 7, 22), day(2026, 7, 23), day(2026, 7, 24)}

	ft := &fakeGSFantrax{pitcherDays: []fantrax.PitcherDay{
		// Rostered all four days, started once.
		pitcherDay("Ace Pitcher", "LAD", window[0], false),
		pitcherDay("Ace Pitcher", "LAD", window[1], true),
		pitcherDay("Ace Pitcher", "LAD", window[2], false),
		pitcherDay("Ace Pitcher", "LAD", window[3], false),
		// Acquired on the last day, started immediately.
		pitcherDay("New Arm", "LAD", window[3], true),
	}}
	sched := scheduleFor([]string{"LAD"}, window...)

	rates, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}

	wantAce := (1 + gsStartRatePrior/RotationSize) / (4 + gsStartRatePrior)
	if !nearly(rates["ace pitcher"], wantAce) {
		t.Errorf("ace rate = %v, want %v (1 start over 4 rostered club-game-days)", rates["ace pitcher"], wantAce)
	}
	wantNew := (1 + gsStartRatePrior/RotationSize) / (1 + gsStartRatePrior)
	if !nearly(rates["new arm"], wantNew) {
		t.Errorf("new arm rate = %v, want %v (1 start over his 1 rostered day, not over the window)",
			rates["new arm"], wantNew)
	}
	// The whole point of the roster-scoped denominator: the newcomer must not
	// be graded against days he was not on the team.
	if rates["new arm"] <= rates["ace pitcher"] {
		t.Errorf("a newcomer who started on his only rostered day must not read below a 1-in-4 starter: new=%v ace=%v",
			rates["new arm"], rates["ace pitcher"])
	}
	// The window is asked for as [today-28, yesterday], clamped at the season
	// start — never including today, whose starts are not settled.
	if !ft.pitcherDayFrom.Equal(seasonStart) || !ft.pitcherDayTo.Equal(today.AddDate(0, 0, -1)) {
		t.Errorf("walked %s..%s, want %s..%s",
			ft.pitcherDayFrom.Format("2006-01-02"), ft.pitcherDayTo.Format("2006-01-02"),
			seasonStart.Format("2006-01-02"), today.AddDate(0, 0, -1).Format("2006-01-02"))
	}
}

// A day the club did not play is not an opportunity declined. Counting it would
// price every pitcher down by the league's off-day rate and say nothing about
// any of them.
func TestComputeStartRates_ClubOffDaysAreNotOpportunities(t *testing.T) {
	today := day(2026, 7, 25)
	seasonStart := day(2026, 7, 21)
	window := []time.Time{day(2026, 7, 21), day(2026, 7, 22), day(2026, 7, 23), day(2026, 7, 24)}

	ft := &fakeGSFantrax{pitcherDays: []fantrax.PitcherDay{
		pitcherDay("Ace Pitcher", "LAD", window[0], false),
		pitcherDay("Ace Pitcher", "LAD", window[1], true),
		pitcherDay("Ace Pitcher", "LAD", window[2], false),
		pitcherDay("Ace Pitcher", "LAD", window[3], false),
	}}
	// LAD played only two of the four days.
	sched := scheduleFor([]string{"LAD"}, window[1], window[2])
	sched.playing[window[0].Format("2006-01-02")] = map[string]bool{"NYY": true}
	sched.playing[window[3].Format("2006-01-02")] = map[string]bool{"NYY": true}

	rates, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	want := (1 + gsStartRatePrior/RotationSize) / (2 + gsStartRatePrior)
	if !nearly(rates["ace pitcher"], want) {
		t.Errorf("ace rate = %v, want %v (1 start over the 2 days his club actually played)",
			rates["ace pitcher"], want)
	}
}

// A pitcher with no observed opportunity is ABSENT from the map rather than
// present at zero — absence is what routes him to the flat rate in
// buildGSForecast, and a zero would confidently claim he never starts.
func TestComputeStartRates_NoOpportunitiesMeansAbsentNotZero(t *testing.T) {
	today := day(2026, 7, 25)
	seasonStart := day(2026, 7, 21)
	d := day(2026, 7, 22)

	ft := &fakeGSFantrax{pitcherDays: []fantrax.PitcherDay{
		pitcherDay("Idle Arm", "COL", d, false),
	}}
	// COL never plays in the window.
	sched := scheduleFor([]string{"LAD"}, day(2026, 7, 21), d, day(2026, 7, 23), day(2026, 7, 24))

	rates, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	if _, ok := rates["idle arm"]; ok {
		t.Errorf("a pitcher with zero club-game-days must be absent, got %v", rates["idle arm"])
	}
}

// A failed schedule day ABORTS. Skipping it would shrink the denominator for
// every pitcher and therefore INFLATE every rate, which makes the gate suppress
// MORE on worse information — the opposite of a safe degradation.
func TestComputeStartRates_ScheduleFailureAbortsRatherThanInflating(t *testing.T) {
	today := day(2026, 7, 25)
	seasonStart := day(2026, 7, 21)
	window := []time.Time{day(2026, 7, 21), day(2026, 7, 22), day(2026, 7, 23), day(2026, 7, 24)}

	ft := &fakeGSFantrax{pitcherDays: []fantrax.PitcherDay{
		pitcherDay("Ace Pitcher", "LAD", window[0], true),
		pitcherDay("Ace Pitcher", "LAD", window[1], false),
		pitcherDay("Ace Pitcher", "LAD", window[2], false),
		pitcherDay("Ace Pitcher", "LAD", window[3], false),
	}}
	sched := scheduleFor([]string{"LAD"}, window...)
	sched.playingErr = map[string]error{window[2].Format("2006-01-02"): errors.New("statsapi down")}

	rates, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err == nil {
		t.Fatalf("want an error, got rates %v", rates)
	}
	if rates != nil {
		t.Errorf("a partial denominator must not be returned, got %v", rates)
	}
}

func TestComputeStartRates_HistoryFailureIsReported(t *testing.T) {
	ft := &fakeGSFantrax{pitcherDaysErr: errors.New("fantrax 500")}
	_, err := computeStartRates(t.Context(), ft, scheduleFor(nil), "t1", day(2026, 7, 21), day(2026, 7, 25), 0)
	if err == nil {
		t.Fatal("want an error from a failed day walk")
	}
}

// Before the season's second day there is no settled history at all. That is an
// ordinary state, not a failure: every SP falls back to the flat rate and
// nothing is logged as broken.
func TestComputeStartRates_NoSettledHistoryIsNotAnError(t *testing.T) {
	seasonStart := day(2026, 3, 25)
	rates, err := computeStartRates(t.Context(), &fakeGSFantrax{}, scheduleFor(nil), "t1", seasonStart, seasonStart, 0)
	if err != nil {
		t.Fatalf("opening day must not error: %v", err)
	}
	if len(rates) != 0 {
		t.Errorf("want no rates on opening day, got %v", rates)
	}
}

// --- buildGSForecast weighting ---

func TestBuildGSForecast_WeightsTheEstimateByEachPitchersOwnStartRate(t *testing.T) {
	today := day(2026, 7, 25)
	tomorrow := day(2026, 7, 26)
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{
			"2026-07-26": {"LAD": true, "NYY": true, "BOS": true},
		},
	}
	spNames := map[string]fantrax.Player{
		"regular":  activeSP("1", "Regular", "LAD"),
		"swingman": activeSP("2", "Swingman", "NYY"),
		"stashed":  reserveSP("3", "Stashed", "BOS"),
	}
	pts := func(fantrax.Player) float64 { return 0 }

	cases := []struct {
		name  string
		rates map[string]float64
		want  float64
	}{
		{
			// Every pitcher measured at the flat rate reproduces the flat
			// forecast, so the weighting cannot move a roster that really is
			// three interchangeable 1-in-5 starters.
			name:  "all measured at the flat rate",
			rates: map[string]float64{"regular": 0.2, "swingman": 0.2, "stashed": 0.2},
			want:  0.6,
		},
		{
			// The bead's case: a rotation regular and two arms that almost
			// never take an active-slot turn. Flat says 0.60; their own
			// history says 0.24.
			name:  "one regular and two rarely-used arms",
			rates: map[string]float64{"regular": 0.20, "swingman": 0.03, "stashed": 0.01},
			want:  0.24,
		},
		{
			// A partially-measured roster mixes both: the unmeasured pitcher
			// keeps the flat rate rather than dropping out.
			// The measured rate is deliberately NOT 0.20 here: at 0.20 the
			// weighted total would coincide with the flat one and the case
			// would pass against the unweighted code too.
			name:  "partially measured",
			rates: map[string]float64{"regular": 0.05},
			want:  0.05 + 2.0/RotationSize,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildGSForecast(t.Context(), sched, spNames, 6, today, tomorrow, pts, tc.rates)
			if err != nil {
				t.Fatalf("buildGSForecast: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("want one forecast day, got %d", len(got))
			}
			if !nearly(got[0].Estimated, tc.want) {
				t.Errorf("estimated = %v, want %v", got[0].Estimated, tc.want)
			}
		})
	}
}

// The default has to be EXACT, not approximately flat: a roster with no
// measured history must produce the same float the pre-weighting code did, so
// that shipping the weighting changes nothing for anyone it cannot yet price.
func TestBuildGSForecast_NoMeasuredRateReproducesTheFlatDivisorExactly(t *testing.T) {
	today := day(2026, 7, 25)
	tomorrow := day(2026, 7, 26)
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{
			"2026-07-26": {"LAD": true, "NYY": true, "BOS": true},
		},
	}
	spNames := map[string]fantrax.Player{
		"a": activeSP("1", "a", "LAD"),
		"b": activeSP("2", "b", "NYY"),
		"c": activeSP("3", "c", "BOS"),
	}
	pts := func(fantrax.Player) float64 { return 0 }

	for _, rates := range []map[string]float64{nil, {}} {
		got, err := buildGSForecast(t.Context(), sched, spNames, 6, today, tomorrow, pts, rates)
		if err != nil {
			t.Fatalf("buildGSForecast: %v", err)
		}
		if want := 3 / RotationSize; got[0].Estimated != want {
			t.Errorf("estimated = %v, want exactly %v", got[0].Estimated, want)
		}
	}
}

// The per-day slot cap applies to the weighted total exactly as it did to the
// flat one, and it still bounds the day's COMBINED confirmed + estimated
// figure.
func TestBuildGSForecast_WeightedEstimateStillCapsAtRemainingSlots(t *testing.T) {
	today := day(2026, 7, 25)
	tomorrow := day(2026, 7, 26)
	sched := &fakeSchedule{
		playing: map[string]map[string]bool{
			"2026-07-26": {"LAD": true, "NYY": true, "BOS": true},
		},
	}
	spNames := map[string]fantrax.Player{
		"a": activeSP("1", "a", "LAD"),
		"b": activeSP("2", "b", "NYY"),
		"c": activeSP("3", "c", "BOS"),
	}
	// Implausibly high rates, so only the cap can hold the number down.
	rates := map[string]float64{"a": 0.9, "b": 0.9, "c": 0.9}

	got, err := buildGSForecast(t.Context(), sched, spNames, 2, today, tomorrow, func(fantrax.Player) float64 { return 0 }, rates)
	if err != nil {
		t.Fatalf("buildGSForecast: %v", err)
	}
	if got[0].Estimated != 2 {
		t.Errorf("estimated = %v, want the 2-slot cap", got[0].Estimated)
	}
}

// --- ComputeGSBudget wiring ---

func gsInputsForRateTest(sched *fakeSchedule) GSInputs {
	return GSInputs{
		TeamID:      "t1",
		Today:       day(2026, 7, 25),
		SeasonStart: day(2026, 7, 21),
		Periods: []fantrax.ScoringPeriod{{
			Number: 5, StartDate: day(2026, 7, 21), EndDate: day(2026, 7, 26),
		}},
		PitcherRoster:   []fantrax.Player{activeSP("1", "Ace Pitcher", "LAD")},
		NumPitcherSlots: 6,
		ProjPts:         func(fantrax.Player) float64 { return 10 },
	}
}

// The history read is SOFT. A failed one must leave the gate RUNNING on the
// flat rate — that is exactly the behaviour that shipped all season, and
// degrading to the status quo ante is never worse than the status quo ante.
// The schedule seam inside buildGSForecast is fatal for the opposite reason:
// losing it moves the forecast in an unknown direction.
func TestComputeGSBudget_StartRateFailureDoesNotDisableTheGate(t *testing.T) {
	sched := scheduleFor([]string{"LAD"},
		day(2026, 7, 21), day(2026, 7, 22), day(2026, 7, 23), day(2026, 7, 24),
		day(2026, 7, 25), day(2026, 7, 26))
	sched.probables = map[string]map[string]string{}

	minGS, maxGS := 10, 12
	ft := &fakeGSFantrax{
		weekStart: day(2026, 7, 21), weekEnd: day(2026, 7, 26),
		limitMin: &minGS, limitMax: &maxGS,
		pitcherDaysErr: errors.New("fantrax 500"),
	}

	d := ComputeGSBudget(t.Context(), ft, sched, gsInputsForRateTest(sched))
	if d.Budget == nil {
		t.Fatal("a failed start-rate read must not disable the gate")
	}
	logs := strings.Join(d.Logs, "\n")
	if !strings.Contains(logs, "start-rate history unavailable") {
		t.Errorf("the failure must be named on the console; logs:\n%s", logs)
	}
	if !strings.Contains(logs, "0 of 1 rostered SPs priced") {
		t.Errorf("the coverage line must report zero priced; logs:\n%s", logs)
	}
	// Fell back to flat: one unannounced SP whose club plays tomorrow.
	if len(d.Budget.Forecast) != 1 || !nearly(d.Budget.Forecast[0].Estimated, 1/RotationSize) {
		t.Errorf("forecast = %+v, want a single day estimated at the flat rate", d.Budget.Forecast)
	}
}

// The healthy path must be visibly different from the degraded one: the
// coverage line reports what was priced, and the failure line is ABSENT. A
// diagnostic that reads the same either way cannot tell a weighted run from an
// unweighted one, which is how the forecast reported 0.0 all season
// (rosterbot-1jvj).
func TestComputeGSBudget_StartRateCoverageLineReportsPricedPitchers(t *testing.T) {
	window := []time.Time{day(2026, 7, 21), day(2026, 7, 22), day(2026, 7, 23), day(2026, 7, 24)}
	sched := scheduleFor([]string{"LAD"}, append(window, day(2026, 7, 25), day(2026, 7, 26))...)
	sched.probables = map[string]map[string]string{}

	minGS, maxGS := 10, 12
	ft := &fakeGSFantrax{
		weekStart: day(2026, 7, 21), weekEnd: day(2026, 7, 26),
		limitMin: &minGS, limitMax: &maxGS,
		pitcherDays: []fantrax.PitcherDay{
			pitcherDay("Ace Pitcher", "LAD", window[0], false),
			pitcherDay("Ace Pitcher", "LAD", window[1], false),
			pitcherDay("Ace Pitcher", "LAD", window[2], false),
			pitcherDay("Ace Pitcher", "LAD", window[3], false),
		},
	}

	d := ComputeGSBudget(t.Context(), ft, sched, gsInputsForRateTest(sched))
	if d.Budget == nil {
		t.Fatal("gate unexpectedly disabled")
	}
	logs := strings.Join(d.Logs, "\n")
	if !strings.Contains(logs, "1 of 1 rostered SPs priced") {
		t.Errorf("coverage line must report the priced pitcher; logs:\n%s", logs)
	}
	if strings.Contains(logs, "start-rate history unavailable") {
		t.Errorf("a healthy read must not report a failure; logs:\n%s", logs)
	}
	// Four club-game-days, no starts: the measured rate is well under the flat
	// 0.20, so the estimate must be too.
	want := (0 + gsStartRatePrior/RotationSize) / (4 + gsStartRatePrior)
	if len(d.Budget.Forecast) != 1 || !nearly(d.Budget.Forecast[0].Estimated, want) {
		t.Errorf("forecast = %+v, want a single day estimated at %v", d.Budget.Forecast, want)
	}
	if d.Budget.Forecast[0].Estimated >= 1/RotationSize {
		t.Errorf("a pitcher who never started must forecast below the flat rate, got %v",
			d.Budget.Forecast[0].Estimated)
	}
}
