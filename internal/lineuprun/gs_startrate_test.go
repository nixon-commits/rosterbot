package lineuprun

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// pitcherDay builds a row that CLEARS every one of tallyStartRates' filters, so
// each test below can switch off exactly the one it is about.
func pitcherDay(name, team string, d time.Time, started bool) fantrax.PitcherDay {
	return fantrax.PitcherDay{
		PitcherName:   name,
		MLBTeam:       team,
		Date:          d,
		Started:       started,
		StatusID:      fantrax.StatusActive,
		PosShortNames: "SP",
	}
}

// days returns n consecutive dates starting at from.
func daysFrom(from time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, from.AddDate(0, 0, i))
	}
	return out
}

// rostered builds one row per date for a single pitcher, marking the listed
// date offsets as starts.
func rostered(name, team string, dates []time.Time, startedOn ...int) []fantrax.PitcherDay {
	started := map[int]bool{}
	for _, i := range startedOn {
		started[i] = true
	}
	out := make([]fantrax.PitcherDay, 0, len(dates))
	for i, d := range dates {
		out = append(out, pitcherDay(name, team, d, started[i]))
	}
	return out
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

// shrunk is the rate tallyStartRates reports for a clean tally, before the cap.
func shrunk(starts, opps int) float64 {
	return (float64(starts) + gsStartRatePrior/RotationSize) / (float64(opps) + gsStartRatePrior)
}

// --- computeStartRates ---

// The denominator is club-game-days the pitcher was ROSTERED, not days in the
// window. A pitcher acquired two days ago has two opportunities, not the whole
// window's worth of days he was somebody else's problem.
func TestComputeStartRates_DenominatorIsClubGameDaysWhileRostered(t *testing.T) {
	seasonStart := day(2026, 7, 11)
	today := day(2026, 7, 25)
	window := daysFrom(seasonStart, 14)

	// Rostered all 14 days, started once; plus a second arm rostered for the
	// last 8, who also started once.
	rows := rostered("Ace Pitcher", "LAD", window, 2)
	rows = append(rows, rostered("Late Arm", "LAD", window[6:], 3)...)
	ft := &fakeGSFantrax{pitcherDays: rows}
	sched := scheduleFor([]string{"LAD"}, window...)

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}

	if got, want := res.Opportunities["ace pitcher"], 14; got != want {
		t.Errorf("ace opportunities = %d, want %d", got, want)
	}
	if got, want := res.Opportunities["late arm"], 8; got != want {
		t.Errorf("late arm opportunities = %d, want %d (only the days he was ours)", got, want)
	}
	if got, want := res.Rate["ace pitcher"], shrunk(1, 14); !nearly(got, want) {
		t.Errorf("ace rate = %v, want %v", got, want)
	}
	// The point of the roster-scoped denominator: the newcomer is graded on his
	// eight days, not on the fourteen, so the same single start reads HIGHER
	// for him. Grading him against days he was not ours would make the two
	// identical.
	if res.Rate["late arm"] <= res.Rate["ace pitcher"] {
		t.Errorf("1-in-8 must outrank 1-in-14: late=%v ace=%v", res.Rate["late arm"], res.Rate["ace pitcher"])
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
	seasonStart := day(2026, 7, 11)
	today := day(2026, 7, 25)
	window := daysFrom(seasonStart, 14)

	ft := &fakeGSFantrax{pitcherDays: rostered("Ace Pitcher", "LAD", window, 1)}
	// LAD played only 6 of the 14 days; the rest belong to another club.
	sched := scheduleFor([]string{"LAD"}, window[:6]...)
	for _, d := range window[6:] {
		sched.playing[d.Format("2006-01-02")] = map[string]bool{"NYY": true}
	}

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	if got, want := res.Opportunities["ace pitcher"], 6; got != want {
		t.Errorf("opportunities = %d, want %d (only days his club played)", got, want)
	}
	if got, want := res.Rate["ace pitcher"], shrunk(1, 6); !nearly(got, want) {
		t.Errorf("ace rate = %v, want %v", got, want)
	}
}

// IL and minor-league days are dropped from the denominator. Unlike a club
// off-day they are not shared across the roster, so counting them prices
// exactly the pitchers coming back from injury lowest — measured live, Gerrit
// Cole read 0.086 over 26 "opportunities" of which 24 were days he could not
// have pitched for us, against 0.600 over the two that were real.
//
// This is the presence/absence pair for the exclusion: the same tally with and
// without the unavailable days must produce different rates, and the one WITH
// them must be the higher.
func TestComputeStartRates_UnavailableDaysAreNotOpportunities(t *testing.T) {
	seasonStart := day(2026, 7, 11)
	today := day(2026, 7, 25)
	window := daysFrom(seasonStart, 14)

	for _, status := range []string{fantrax.StatusIL, fantrax.StatusMinors} {
		t.Run("status "+status, func(t *testing.T) {
			// Started on both of the six days he was available; the other
			// eight he was on the IL (or in the minors) and could not have.
			rows := rostered("Recovering Ace", "LAD", window, 1, 4)
			for i := 6; i < len(rows); i++ {
				rows[i].StatusID = status
			}
			ft := &fakeGSFantrax{pitcherDays: rows}
			sched := scheduleFor([]string{"LAD"}, window...)

			res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
			if err != nil {
				t.Fatalf("computeStartRates: %v", err)
			}
			if got, want := res.Opportunities["recovering ace"], 6; got != want {
				t.Errorf("opportunities = %d, want %d — the %d unavailable days must not be in the denominator",
					got, want, len(window)-6)
			}
			// The uncapped rate over the real six days is 2.4/8 = 0.30, above
			// the cap; over all fourteen it would be 2.4/16 = 0.15, below it.
			// So the cap is what this asserts against, and a regression that
			// re-admits the unavailable days lands at 0.15 and fails.
			if got, want := res.Rate["recovering ace"], gsStartRateCap; !nearly(got, want) {
				t.Errorf("rate = %v, want %v; counting the unavailable days would give %v",
					got, want, shrunk(2, len(window)))
			}
		})
	}
}

// A day on which the pitcher was not SP-eligible is out of scope: the forecast
// only ever consults this rate for a pitcher rosterSPNames put in the
// unannounced bucket, and rosterSPNames tests the same PosShortNames string.
func TestComputeStartRates_NonSPDaysAreNotOpportunities(t *testing.T) {
	seasonStart := day(2026, 7, 11)
	today := day(2026, 7, 25)
	window := daysFrom(seasonStart, 14)

	rows := rostered("Converted Arm", "LAD", window, 1)
	for i := 6; i < len(rows); i++ {
		rows[i].PosShortNames = "RP"
	}
	ft := &fakeGSFantrax{pitcherDays: rows}
	sched := scheduleFor([]string{"LAD"}, window...)

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	if got, want := res.Opportunities["converted arm"], 6; got != want {
		t.Errorf("opportunities = %d, want %d (only the SP-eligible days)", got, want)
	}
}

// A first-appearance row carries a Started flag derived from a capped delta
// against no baseline, so it can report a start belonging to another team's
// season — and a genuine start on such a day is indistinguishable from a bench
// day. It is dropped in BOTH directions.
func TestComputeStartRates_FirstAppearanceRowsAreDropped(t *testing.T) {
	seasonStart := day(2026, 7, 11)
	today := day(2026, 7, 25)
	window := daysFrom(seasonStart, 14)

	rows := rostered("Ace Pitcher", "LAD", window, 0, 5)
	// The walk saw him for the first time on day 0, where it also reported a
	// start: exactly the asymmetric row.
	rows[0].FirstAppearance = true
	ft := &fakeGSFantrax{pitcherDays: rows}
	sched := scheduleFor([]string{"LAD"}, window...)

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	if got, want := res.Opportunities["ace pitcher"], 13; got != want {
		t.Errorf("opportunities = %d, want %d — the first-appearance day is not a denominator either", got, want)
	}
	if got, want := res.Rate["ace pitcher"], shrunk(1, 13); !nearly(got, want) {
		t.Errorf("rate = %v, want %v — the first-appearance start must not be credited", got, want)
	}
}

// The minimum-opportunity guard. A pitcher seen on one club-game-day carries no
// measurement, whatever the shrinkage prior does to the arithmetic: he falls
// back to the flat rate exactly as if he had no history at all.
func TestComputeStartRates_ShortHistoryFallsBackToFlat(t *testing.T) {
	seasonStart := day(2026, 7, 11)
	today := day(2026, 7, 25)
	window := daysFrom(seasonStart, 14)

	rows := rostered("Ace Pitcher", "LAD", window, 3)
	// One day on the roster, and he started it — the 0.467 case.
	rows = append(rows, pitcherDay("One Day Wonder", "LAD", window[13], true))
	// FOUR days and no start yet — one below gsStartRateMinOpportunities, so
	// this is the guard's boundary from the refusing side: the "rotation
	// regular acquired last week" case that used to be priced at 0.067, a
	// third of flat, on no evidence at all.
	rows = append(rows, rostered("New Regular", "LAD", window[10:14])...)
	// Exactly gsStartRateMinOpportunities and no start — the boundary from the
	// other side, which must be PRICED. Without it the guard could be widened
	// by one and the suite would stay green.
	rows = append(rows, rostered("Just Enough", "LAD", window[9:14])...)

	ft := &fakeGSFantrax{pitcherDays: rows}
	sched := scheduleFor([]string{"LAD"}, window...)

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	for _, name := range []string{"one day wonder", "new regular"} {
		if r, ok := res.Rate[name]; ok {
			t.Errorf("%q was priced at %v on %d opportunities; below %d it must fall back to the flat rate",
				name, r, res.Opportunities[name], gsStartRateMinOpportunities)
		}
		// Still counted as SEEN, which is what lets the coverage line
		// distinguish "too little history" from "no history".
		if _, ok := res.Opportunities[name]; !ok {
			t.Errorf("%q must still be reported as seen", name)
		}
	}
	if _, ok := res.Rate["ace pitcher"]; !ok {
		t.Error("a pitcher with a full window of history must still be priced")
	}
	if got := res.Opportunities["just enough"]; got != gsStartRateMinOpportunities {
		t.Fatalf("fixture built %d opportunities, want exactly the guard's %d",
			got, gsStartRateMinOpportunities)
	}
	if _, ok := res.Rate["just enough"]; !ok {
		t.Errorf("a pitcher on exactly %d opportunities must be priced; the guard is a minimum, not a threshold to clear",
			gsStartRateMinOpportunities)
	}
}

// The cap. A rate above 1/RotationSize is not a fast starter — a five-man
// rotation cannot give any member more than one turn in five — so it is a short
// sample or a snapshot artefact and is clamped.
func TestComputeStartRates_RateIsCappedAtRotationMathsCeiling(t *testing.T) {
	seasonStart := day(2026, 7, 11)
	today := day(2026, 7, 25)
	window := daysFrom(seasonStart, 14)

	// Six starts in eight club-game-days: physically impossible as a standing
	// rate, and 0.775 uncapped.
	ft := &fakeGSFantrax{pitcherDays: rostered("Impossible Arm", "LAD", window[:8], 0, 1, 2, 3, 4, 5)}
	sched := scheduleFor([]string{"LAD"}, window...)

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	if got := res.Rate["impossible arm"]; !nearly(got, gsStartRateCap) {
		t.Errorf("rate = %v, want the %v cap (uncapped it reads %v)", got, gsStartRateCap, shrunk(6, 8))
	}
}

// A pitcher with no observed opportunity is ABSENT from both maps rather than
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

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err != nil {
		t.Fatalf("computeStartRates: %v", err)
	}
	if _, ok := res.Rate["idle arm"]; ok {
		t.Errorf("a pitcher with zero club-game-days must be absent, got %v", res.Rate["idle arm"])
	}
	if _, ok := res.Opportunities["idle arm"]; ok {
		t.Error("a pitcher with zero club-game-days must not be reported as seen either")
	}
}

// A failed schedule day ABORTS. Skipping it would shrink the denominator for
// every pitcher and therefore INFLATE every rate, which makes the gate suppress
// MORE on worse information — the opposite of a safe degradation.
func TestComputeStartRates_ScheduleFailureAbortsRatherThanInflating(t *testing.T) {
	today := day(2026, 7, 25)
	seasonStart := day(2026, 7, 21)
	window := daysFrom(seasonStart, 4)

	ft := &fakeGSFantrax{pitcherDays: rostered("Ace Pitcher", "LAD", window, 0)}
	sched := scheduleFor([]string{"LAD"}, window...)
	sched.playingErr = map[string]error{window[2].Format("2006-01-02"): errors.New("statsapi down")}

	res, err := computeStartRates(t.Context(), ft, sched, "t1", seasonStart, today, 0)
	if err == nil {
		t.Fatalf("want an error, got rates %v", res.Rate)
	}
	if res.Rate != nil || res.Opportunities != nil {
		t.Errorf("a partial denominator must not be returned, got %+v", res)
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
	res, err := computeStartRates(t.Context(), &fakeGSFantrax{}, scheduleFor(nil), "t1", seasonStart, seasonStart, 0)
	if err != nil {
		t.Fatalf("opening day must not error: %v", err)
	}
	if len(res.Rate) != 0 {
		t.Errorf("want no rates on opening day, got %v", res.Rate)
	}
}

// --- StartRateCache ---

// The cache exists so `shadow`'s four per-system passes share one walk. A
// second Run over identical inputs must not re-measure.
func TestStartRateCache_MeasuresOnce(t *testing.T) {
	c := NewStartRateCache()
	calls := 0
	measure := func() (startRateResult, error) {
		calls++
		return startRateResult{Rate: map[string]float64{"a": 0.1}}, nil
	}
	for i := 0; i < 4; i++ {
		res, err := c.get(measure)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if res.Rate["a"] != 0.1 {
			t.Fatalf("pass %d returned %v", i, res.Rate)
		}
	}
	if calls != 1 {
		t.Errorf("measured %d times, want 1", calls)
	}
}

// A nil cache is the ordinary single-Run path and must measure every time
// rather than panicking or silently sharing.
func TestStartRateCache_NilMeasuresEveryTime(t *testing.T) {
	var c *StartRateCache
	calls := 0
	for i := 0; i < 3; i++ {
		if _, err := c.get(func() (startRateResult, error) {
			calls++
			return startRateResult{}, nil
		}); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("measured %d times, want 3", calls)
	}
}

// The failure is memoized with the result. Retrying a walk over settled
// snapshots that just failed would pay the whole cost again to reach the same
// answer, and would report the warning once per pass.
func TestStartRateCache_MemoizesTheFailureToo(t *testing.T) {
	c := NewStartRateCache()
	calls := 0
	for i := 0; i < 3; i++ {
		_, err := c.get(func() (startRateResult, error) {
			calls++
			return startRateResult{}, errors.New("fantrax 500")
		})
		if err == nil {
			t.Fatalf("pass %d: want the memoized error", i)
		}
	}
	if calls != 1 {
		t.Errorf("measured %d times, want 1", calls)
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
	// Implausibly high rates, so only the cap can hold the number down. (The
	// estimator itself can no longer produce these — see gsStartRateCap — but
	// buildGSForecast must not depend on its caller for a bound.)
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

func gsInputsForRateTest(_ *fakeSchedule) GSInputs {
	return GSInputs{
		TeamID:      "t1",
		Today:       day(2026, 7, 25),
		SeasonStart: day(2026, 7, 11),
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
	sched := scheduleFor([]string{"LAD"}, daysFrom(day(2026, 7, 11), 16)...)
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
	if !strings.Contains(logs, "1 with no history") {
		t.Errorf("the coverage line must say WHY the pitcher fell back; logs:\n%s", logs)
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
	window := daysFrom(day(2026, 7, 11), 16)
	sched := scheduleFor([]string{"LAD"}, window...)
	sched.probables = map[string]map[string]string{}

	minGS, maxGS := 10, 12
	ft := &fakeGSFantrax{
		weekStart: day(2026, 7, 21), weekEnd: day(2026, 7, 26),
		limitMin: &minGS, limitMax: &maxGS,
		pitcherDays: rostered("Ace Pitcher", "LAD", window[:14]),
	}

	d := ComputeGSBudget(t.Context(), ft, sched, gsInputsForRateTest(sched))
	if d.Budget == nil {
		t.Fatal("gate unexpectedly disabled")
	}
	logs := strings.Join(d.Logs, "\n")
	if !strings.Contains(logs, "1 of 1 rostered SPs priced") {
		t.Errorf("coverage line must report the priced pitcher; logs:\n%s", logs)
	}
	if !strings.Contains(logs, "median 14 opportunities") {
		t.Errorf("coverage line must say how much history the price rests on; logs:\n%s", logs)
	}
	if strings.Contains(logs, "start-rate history unavailable") {
		t.Errorf("a healthy read must not report a failure; logs:\n%s", logs)
	}
	// Fourteen club-game-days, no starts: the measured rate is well under the
	// flat 0.20, so the estimate must be too.
	want := shrunk(0, 14)
	if len(d.Budget.Forecast) != 1 || !nearly(d.Budget.Forecast[0].Estimated, want) {
		t.Errorf("forecast = %+v, want a single day estimated at %v", d.Budget.Forecast, want)
	}
	if d.Budget.Forecast[0].Estimated >= 1/RotationSize {
		t.Errorf("a pitcher who never started must forecast below the flat rate, got %v",
			d.Budget.Forecast[0].Estimated)
	}
}

// A pitcher seen but priced off too little history is reported SEPARATELY from
// one never seen at all. Collapsing the two is what made the old line unable to
// distinguish a month of history from a single day.
func TestComputeGSBudget_CoverageLineSeparatesTooLittleHistoryFromNone(t *testing.T) {
	window := daysFrom(day(2026, 7, 11), 16)
	sched := scheduleFor([]string{"LAD"}, window...)
	sched.probables = map[string]map[string]string{}

	minGS, maxGS := 10, 12
	rows := rostered("Ace Pitcher", "LAD", window[:14], 2)
	rows = append(rows, rostered("New Arm", "LAD", window[11:14])...)
	ft := &fakeGSFantrax{
		weekStart: day(2026, 7, 21), weekEnd: day(2026, 7, 26),
		limitMin: &minGS, limitMax: &maxGS,
		pitcherDays: rows,
	}

	in := gsInputsForRateTest(sched)
	in.PitcherRoster = []fantrax.Player{
		activeSP("1", "Ace Pitcher", "LAD"),
		activeSP("2", "New Arm", "LAD"),
		activeSP("3", "Never Seen", "LAD"),
	}

	d := ComputeGSBudget(t.Context(), ft, sched, in)
	logs := strings.Join(d.Logs, "\n")
	if !strings.Contains(logs, "1 of 3 rostered SPs priced") {
		t.Errorf("want 1 of 3 priced; logs:\n%s", logs)
	}
	if !strings.Contains(logs, "1 under the 5-opportunity minimum") {
		t.Errorf("the short-history pitcher must be named as such; logs:\n%s", logs)
	}
	if !strings.Contains(logs, "1 with no history") {
		t.Errorf("the unseen pitcher must be named as such; logs:\n%s", logs)
	}
}
