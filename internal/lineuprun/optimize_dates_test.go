package lineuprun

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// fakeDateRoster resolves periods by simple day math and serves per-period
// rosters. periodRosterErr fails the period fetch so the "fall back to the
// current roster, with a warning" path is reachable.
type fakeDateRoster struct {
	periodFor       map[string]fantrax.DailyPeriod
	periodRoster    []fantrax.Player
	periodRosterErr error

	mu             sync.Mutex
	periodFetches  int
	periodsFetched []fantrax.DailyPeriod
}

func (f *fakeDateRoster) DailyPeriodFor(_, date time.Time) fantrax.DailyPeriod {
	return f.periodFor[date.Format("2006-01-02")]
}

func (f *fakeDateRoster) GetHitterRosterForPeriod(p fantrax.DailyPeriod) ([]fantrax.Player, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.periodFetches++
	f.periodsFetched = append(f.periodsFetched, p)
	return f.periodRoster, f.periodRosterErr
}

func (f *fakeDateRoster) GetPitcherRosterForPeriod(p fantrax.DailyPeriod) ([]fantrax.Player, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.periodRoster, f.periodRosterErr
}

// fakeDateSchedule extends the GS-budget fake with the two lookups the per-date
// pass additionally needs. Errors are per-method so each degraded path can be
// provoked on its own.
type fakeDateSchedule struct {
	fakeSchedule

	venues  map[string]map[string]string
	benched map[string]map[string]bool

	playingErr, lockedErr, probablesErr, venuesErr, benchedErr error

	hook func()
}

func (f *fakeDateSchedule) TeamsPlayingOn(d time.Time) (map[string]bool, error) {
	if f.hook != nil {
		f.hook()
	}
	if f.playingErr != nil {
		return nil, f.playingErr
	}
	return f.fakeSchedule.TeamsPlayingOn(d)
}

func (f *fakeDateSchedule) LockedTeams(d time.Time) (map[string]bool, error) {
	if f.lockedErr != nil {
		return nil, f.lockedErr
	}
	return f.fakeSchedule.LockedTeams(d)
}

func (f *fakeDateSchedule) ProbableStarters(d time.Time) (map[string]string, error) {
	if f.probablesErr != nil {
		return nil, f.probablesErr
	}
	return f.fakeSchedule.ProbableStarters(d)
}

func (f *fakeDateSchedule) GameVenues(d time.Time) (map[string]string, error) {
	if f.venuesErr != nil {
		return nil, f.venuesErr
	}
	return f.venues[d.Format("2006-01-02")], nil
}

func (f *fakeDateSchedule) BenchedPlayers(d time.Time, _ map[string]string) (map[string]bool, error) {
	if f.benchedErr != nil {
		return nil, f.benchedErr
	}
	return f.benched[d.Format("2006-01-02")], nil
}

func optimizeInputs() OptimizeInputs {
	today := day(2026, 7, 25)
	return OptimizeInputs{
		Dates:       []time.Time{today},
		Today:       today,
		SeasonStart: day(2026, 3, 25),
		HitterRoster: []fantrax.Player{
			{ID: "h1", Name: "Star Hitter", MLBTeam: "NYY", Status: "Active", RosterPosition: "012", Positions: []string{"012"}},
			{ID: "h2", Name: "Bench Bat", MLBTeam: "BOS", Status: "Reserve", Positions: []string{"012"}},
		},
		PitcherRoster: []fantrax.Player{
			{ID: "p1", Name: "Ace Arm", MLBTeam: "LAD", Status: "Active", RosterPosition: "020", Positions: []string{"020"}, PosShortNames: "SP"},
		},
		HitterSlots:    []fantrax.Slot{{PosID: "012", PosName: "OF"}},
		PitcherSlots:   []fantrax.Slot{{PosID: "020", PosName: "P"}},
		HitterScoring:  fantrax.ScoringWeights{"HR": 4},
		PitcherScoring: fantrax.ScoringWeights{"K": 2},
		HitterSrc: stubHitterSource{byName: map[string]*projections.Projection{
			"Star Hitter": {G: 100, HR: 30},
			"Bench Bat":   {G: 100, HR: 5},
		}},
		PitcherSrc: stubPitcherSource{byName: map[string]*projections.PitcherProjection{
			"Ace Arm": {G: 30, IP: 180, K: 200},
		}},
	}
}

func healthyDateSchedule() *fakeDateSchedule {
	return &fakeDateSchedule{
		fakeSchedule: fakeSchedule{
			playing: map[string]map[string]bool{
				"2026-07-25": {"NYY": true, "BOS": true, "LAD": true},
			},
			probables: map[string]map[string]string{
				"2026-07-25": {"ace arm": "LAD"},
			},
			locked: map[string]map[string]bool{},
		},
		venues: map[string]map[string]string{
			"2026-07-25": {"NYY": "NYY", "BOS": "NYY", "LAD": "LAD"},
		},
	}
}

func healthyDateRoster() *fakeDateRoster {
	return &fakeDateRoster{periodFor: map[string]fantrax.DailyPeriod{
		"2026-07-25": 113,
		"2026-07-26": 114,
		"2026-07-27": 115,
	}}
}

func TestOptimizeDates_ProducesOneResultPerDateInInputOrder(t *testing.T) {
	in := optimizeInputs()
	in.Dates = []time.Time{day(2026, 7, 25), day(2026, 7, 26), day(2026, 7, 27)}
	sched := healthyDateSchedule()
	sched.playing["2026-07-26"] = map[string]bool{"NYY": true}
	sched.playing["2026-07-27"] = map[string]bool{"NYY": true}

	got, err := OptimizeDates(healthyDateRoster(), sched, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	// Completion order is nondeterministic; output order must not be.
	for i, want := range in.Dates {
		if !got[i].date.Equal(want) {
			t.Errorf("result %d is for %v, want %v", i, got[i].date, want)
		}
	}
	if !got[0].isToday || got[1].isToday || got[2].isToday {
		t.Errorf("isToday flags = %v/%v/%v, want true/false/false",
			got[0].isToday, got[1].isToday, got[2].isToday)
	}
	if got[0].period != 113 || got[1].period != 114 || got[2].period != 115 {
		t.Errorf("periods = %d/%d/%d, want 113/114/115", got[0].period, got[1].period, got[2].period)
	}
}

// Today optimizes against the live roster already in hand; only future dates
// need the period-specific snapshot. Fetching one for today would both waste a
// request and risk optimizing against a stale view of the day in progress.
func TestOptimizeDates_OnlyFutureDatesFetchPeriodRosters(t *testing.T) {
	in := optimizeInputs()
	in.Dates = []time.Time{day(2026, 7, 25), day(2026, 7, 26)}
	sched := healthyDateSchedule()
	sched.playing["2026-07-26"] = map[string]bool{"NYY": true}
	ft := healthyDateRoster()
	ft.periodRoster = in.HitterRoster

	if _, err := OptimizeDates(ft, sched, in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ft.periodFetches != 1 {
		t.Errorf("hitter period fetches = %d, want 1 (tomorrow only)", ft.periodFetches)
	}
	if len(ft.periodsFetched) != 1 || ft.periodsFetched[0] != 114 {
		t.Errorf("fetched periods = %v, want [114]", ft.periodsFetched)
	}
}

// Every per-date upstream failure degrades that date rather than the run: a
// warning is recorded and a documented fallback applies.
func TestOptimizeDates_UpstreamFailuresBecomeWarningsNotErrors(t *testing.T) {
	cases := []struct {
		name    string
		break_  func(*fakeDateSchedule, *fakeDateRoster)
		future  bool
		wantSub string
	}{
		{
			name:    "period roster unavailable",
			break_:  func(_ *fakeDateSchedule, r *fakeDateRoster) { r.periodRosterErr = errors.New("snapshot down") },
			future:  true,
			wantSub: "could not fetch hitter roster for period 114 (snapshot down) — using current",
		},
		{
			name:    "mlb schedule unavailable",
			break_:  func(s *fakeDateSchedule, _ *fakeDateRoster) { s.playingErr = errors.New("statsapi down") },
			wantSub: "assuming all teams play",
		},
		{
			name:    "locked teams unavailable",
			break_:  func(s *fakeDateSchedule, _ *fakeDateRoster) { s.lockedErr = errors.New("no lock data") },
			wantSub: "proceeding without lock detection",
		},
		{
			name:    "probables unavailable",
			break_:  func(s *fakeDateSchedule, _ *fakeDateRoster) { s.probablesErr = errors.New("no probables") },
			wantSub: "SPs default to start",
		},
		{
			name:    "venues unavailable",
			break_:  func(s *fakeDateSchedule, _ *fakeDateRoster) { s.venuesErr = errors.New("no venues") },
			wantSub: "game venues unavailable",
		},
		{
			name:    "starting lineups unavailable",
			break_:  func(s *fakeDateSchedule, _ *fakeDateRoster) { s.benchedErr = errors.New("no lineups") },
			wantSub: "assuming all hitters play",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := optimizeInputs()
			sched := healthyDateSchedule()
			ft := healthyDateRoster()
			if tc.future {
				in.Dates = []time.Time{day(2026, 7, 26)}
				sched.playing["2026-07-26"] = map[string]bool{"NYY": true}
			}
			tc.break_(sched, ft)

			got, err := OptimizeDates(ft, sched, in)
			if err != nil {
				t.Fatalf("a per-date failure must not fail the run: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d results, want 1", len(got))
			}
			joined := strings.Join(got[0].warnings, "\n")
			if !strings.Contains(joined, tc.wantSub) {
				t.Errorf("warnings = %q, want one containing %q", joined, tc.wantSub)
			}
			// The date still produced a lineup.
			if len(got[0].hitterResult.Scored) == 0 {
				t.Error("the date should still have been optimized")
			}
		})
	}
}

// A locked team's healthy players are flagged so the optimizer leaves them
// alone; minors and injured players are not, since they were never movable and
// flagging them would misreport why.
func TestOptimizeDates_LocksOnlyHealthyPlayersOnLockedTeams(t *testing.T) {
	in := optimizeInputs()
	in.HitterRoster = []fantrax.Player{
		{ID: "h1", Name: "Playing Guy", MLBTeam: "NYY", Status: "Active", Positions: []string{"012"}},
		{ID: "h2", Name: "Hurt Guy", MLBTeam: "NYY", Status: "Active", Positions: []string{"012"}, IsInjured: true},
		{ID: "h3", Name: "Farm Guy", MLBTeam: "NYY", Status: "Active", Positions: []string{"012"}, InMinors: true},
	}
	sched := healthyDateSchedule()
	sched.locked["2026-07-25"] = map[string]bool{"NYY": true}

	got, err := OptimizeDates(healthyDateRoster(), sched, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	locked := map[string]bool{}
	for _, sp := range got[0].hitterResult.Scored {
		locked[sp.Player.ID] = sp.Player.Locked
	}
	if !locked["h1"] {
		t.Error("a healthy player on a locked team should be locked")
	}
	if locked["h2"] || locked["h3"] {
		t.Errorf("injured/minors players must not be marked locked: %v", locked)
	}
}

// Lock detection is a today-only concern — a future date's games have not
// started, so there is nothing to lock and no reason to ask.
func TestOptimizeDates_FutureDatesSkipLockAndBenchLookups(t *testing.T) {
	in := optimizeInputs()
	in.Dates = []time.Time{day(2026, 7, 26)}
	sched := healthyDateSchedule()
	sched.playing["2026-07-26"] = map[string]bool{"NYY": true}
	// If either lookup is consulted for a future date, these errors surface as
	// warnings and the assertion below catches it.
	sched.lockedErr = errors.New("should not be called")
	sched.benchedErr = errors.New("should not be called")

	got, err := OptimizeDates(healthyDateRoster(), sched, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got[0].warnings) != 0 {
		t.Errorf("future date consulted today-only lookups: %v", got[0].warnings)
	}
}

// The GS budget gates today only. A future date optimized under today's budget
// would double-count starts the daily run for that day will re-forecast anyway.
func TestOptimizeDates_GSBudgetAppliesToTodayOnly(t *testing.T) {
	in := optimizeInputs()
	in.Dates = []time.Time{day(2026, 7, 25), day(2026, 7, 26)}
	// A fully consumed budget: today's probable SP must be suppressed.
	in.GSBudget = &optimizer.GSBudget{Limit: 1, Used: 1, Today: in.Today, WeekEnd: day(2026, 7, 26)}
	sched := healthyDateSchedule()
	sched.playing["2026-07-26"] = map[string]bool{"LAD": true}
	sched.probables["2026-07-26"] = map[string]string{"ace arm": "LAD"}
	ft := healthyDateRoster()
	ft.periodRoster = in.PitcherRoster

	got, err := OptimizeDates(ft, sched, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	starterOn := func(dr dateResult) bool {
		for _, sp := range dr.pitcherResult.Scored {
			if sp.Player.ID == "p1" {
				return sp.IsStarter
			}
		}
		return false
	}
	if starterOn(got[0]) {
		t.Error("today's exhausted budget should have suppressed the start")
	}
	if !starterOn(got[1]) {
		t.Error("a future date must not be gated by today's budget")
	}
}

// Pipeline tables are opt-in; without --pipeline the maps stay nil so the extra
// per-player work is skipped entirely.
func TestOptimizeDates_PipelineDetailIsOptIn(t *testing.T) {
	in := optimizeInputs()

	off, err := OptimizeDates(healthyDateRoster(), healthyDateSchedule(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if off[0].hitterPipelines != nil || off[0].pitcherPipelines != nil {
		t.Error("pipeline detail should be absent without ShowPipeline")
	}

	in.ShowPipeline = true
	on, err := OptimizeDates(healthyDateRoster(), healthyDateSchedule(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(on[0].hitterPipelines) == 0 {
		t.Error("expected hitter pipeline rows with ShowPipeline")
	}
}

// --- buildOpposingPitchers: pure ---

func TestBuildOpposingPitchers_PairsTeamsSharingAVenue(t *testing.T) {
	got := buildOpposingPitchers(
		map[string]string{"ace arm": "LAD"},
		map[string]string{"LAD": "LAD", "SFG": "LAD", "NYY": "NYY"},
		map[string]string{"ace arm": "L"},
		map[string]float64{"ace arm": 2.80},
		4.00,
	)

	opp, ok := got["SFG"]
	if !ok {
		t.Fatalf("the visiting team should face the pitcher, got %v", got)
	}
	if opp.Team != "LAD" || opp.Throws != "L" || opp.FIP != 2.80 {
		t.Errorf("opposing pitcher = %+v", opp)
	}
	if _, self := got["LAD"]; self {
		t.Error("a team must not be listed as facing its own pitcher")
	}
	if _, unrelated := got["NYY"]; unrelated {
		t.Error("a team at a different venue must not be paired")
	}
}

func TestBuildOpposingPitchers_MissingInputsDisableAdjustment(t *testing.T) {
	venues := map[string]string{"LAD": "LAD", "SFG": "LAD"}
	probs := map[string]string{"ace arm": "LAD"}

	if got := buildOpposingPitchers(nil, venues, nil, nil, 4.00); got != nil {
		t.Errorf("no probables should disable adjustment, got %v", got)
	}
	if got := buildOpposingPitchers(probs, nil, nil, nil, 4.00); got != nil {
		t.Errorf("no venues should disable adjustment, got %v", got)
	}
	if got := buildOpposingPitchers(probs, venues, nil, nil, 0); got != nil {
		t.Errorf("no league-average FIP should disable adjustment, got %v", got)
	}
}

// The dates are optimized in parallel, not one after another. Each date's pass
// parks on a barrier that only opens once every date has arrived.
func TestOptimizeDates_RunsDatesConcurrently(t *testing.T) {
	const nDates = 4
	in := optimizeInputs()
	in.Dates = nil
	sched := healthyDateSchedule()
	for i := 0; i < nDates; i++ {
		d := day(2026, 7, 25).AddDate(0, 0, i)
		in.Dates = append(in.Dates, d)
		sched.playing[d.Format("2006-01-02")] = map[string]bool{"NYY": true}
	}

	var (
		mu      sync.Mutex
		arrived int
		opened  = make(chan struct{})
		once    sync.Once
	)
	sched.hook = func() {
		mu.Lock()
		arrived++
		full := arrived == nDates
		mu.Unlock()
		if full {
			once.Do(func() { close(opened) })
		}
		select {
		case <-opened:
		case <-time.After(5 * time.Second):
		}
	}

	ft := healthyDateRoster()
	ft.periodFor["2026-07-28"] = 116
	done := make(chan error, 1)
	go func() {
		_, err := OptimizeDates(ft, sched, in)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OptimizeDates did not finish — the dates are running sequentially")
	}

	select {
	case <-opened:
	default:
		t.Error("barrier never opened: the dates were not optimized concurrently")
	}
}
