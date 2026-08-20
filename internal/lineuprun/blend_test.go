package lineuprun

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// --- The pure core: window bounds and the series collapse ---

// The fetch reaches back further than the window (35d for a 30d window) so the
// window is fully populated at its far edge, and stops at yesterday because
// today's period is still open.
func TestHitterWindowBounds_LooksBackFurtherThanTheWindowAndStopsYesterday(t *testing.T) {
	today := day(2026, 7, 25)
	start, end, err := hitterWindowBounds(today, day(2026, 3, 25))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := today.AddDate(0, 0, -recencyLookbackDays); !start.Equal(want) {
		t.Errorf("start = %v, want %v (%d-day lookback)", start, want, recencyLookbackDays)
	}
	if want := today.AddDate(0, 0, -1); !end.Equal(want) {
		t.Errorf("end = %v, want yesterday %v", end, want)
	}
	if recencyLookbackDays <= recencyWindowDays {
		t.Errorf("lookback (%d) must exceed the window (%d) or the far edge is underfilled",
			recencyLookbackDays, recencyWindowDays)
	}
}

// Early April: the lookback would reach into the preseason, so it clamps.
func TestHitterWindowBounds_ClampsToSeasonStart(t *testing.T) {
	seasonStart := day(2026, 3, 25)
	start, _, err := hitterWindowBounds(day(2026, 4, 2), seasonStart)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !start.Equal(seasonStart) {
		t.Errorf("start = %v, want the season opener %v", start, seasonStart)
	}
}

func TestHitterWindowBounds_NoCompletedDaysIsAnError(t *testing.T) {
	seasonStart := day(2026, 3, 25)
	if _, _, err := hitterWindowBounds(seasonStart, seasonStart); err == nil {
		t.Fatal("expected an error on the season opener — no completed days to window over")
	}
}

// The collapse is the pure core (rosterbot-6om criterion 2): it takes an
// already-fetched series and no client at all. A mistake here does not fail,
// it silently shifts every hitter's Expected Points.
func TestCollapseHitterWindow_AveragesOnlyGamesInsideTheWindow(t *testing.T) {
	today := day(2026, 7, 25)
	days := []fantrax.DayRoster{
		// Well outside the trailing 30 days — must not count, however big.
		hitterDay(today.AddDate(0, 0, -45), "p1", 500),
		hitterDay(today.AddDate(0, 0, -3), "p1", 10),
		hitterDay(today.AddDate(0, 0, -1), "p1", 20),
		// The as-of day itself is excluded by the leakage guard.
		hitterDay(today, "p1", 999),
	}

	got := collapseHitterWindow(days, today)

	rs, ok := got["p1"]
	if !ok {
		t.Fatal("p1 missing from the recency map")
	}
	if rs.GamesPlayed != 2 {
		t.Errorf("GamesPlayed = %d, want 2 (the 45-day-old game and today are both out)", rs.GamesPlayed)
	}
	if math.Abs(rs.FPtsPerGame-15.0) > 1e-9 {
		t.Errorf("FPtsPerGame = %v, want 15 ((10+20)/2)", rs.FPtsPerGame)
	}
}

func TestCollapseHitterWindow_EmptySeriesIsEmpty(t *testing.T) {
	if got := collapseHitterWindow(nil, day(2026, 7, 25)); len(got) != 0 {
		t.Errorf("got %v, want an empty map", got)
	}
}

// --- The phase ---

// fakeBlendClient records what each role's recency fetch was asked for, which
// is how the asymmetry test below observes the two strategies without
// inspecting the resulting sources.
type fakeBlendClient struct {
	days      []fantrax.DayRoster
	daysErr   error
	dailyCall struct {
		called      bool
		start, end  time.Time
		seasonStart time.Time
	}

	pitcherStats map[string]fantrax.RecentStat
	pitcherErr   error
	pitcherCall  struct {
		called bool
		period fantrax.DailyPeriod
	}
}

func (f *fakeBlendClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, errors.New("GetSeasonDateRange should not be called when seasonStart is set")
}

func (f *fakeBlendClient) DailyFantasyPoints(_ string, start, end, seasonStart time.Time, _ string, _ time.Duration) ([]fantrax.DayRoster, error) {
	f.dailyCall.called = true
	f.dailyCall.start, f.dailyCall.end, f.dailyCall.seasonStart = start, end, seasonStart
	return f.days, f.daysErr
}

// These tests exercise BlendSources' fallback policy, not the MLB coverage
// backfill, so every player in f.days is fully covered and this is never called.
func (f *fakeBlendClient) MLBDailyFPts(_ []fantrax.MLBPlayerRef, _, _ time.Time) ([]fantrax.DayRoster, error) {
	return nil, nil
}

func (f *fakeBlendClient) GetRecentPitcherStats(period fantrax.DailyPeriod) (map[string]fantrax.RecentStat, error) {
	f.pitcherCall.called = true
	f.pitcherCall.period = period
	return f.pitcherStats, f.pitcherErr
}

// stubHitterSource / stubPitcherSource are one-method base projections, so the
// phase can be exercised without loading FanGraphs. Both Source interfaces are
// a single method, which is what makes that cheap.
type stubHitterSource struct {
	byName map[string]*projections.Projection
}

func (s stubHitterSource) GetProjection(name, _ string) (*projections.Projection, bool) {
	p, ok := s.byName[name]
	return p, ok
}

type stubPitcherSource struct {
	byName map[string]*projections.PitcherProjection
}

func (s stubPitcherSource) GetPitcherProjection(name, _ string) (*projections.PitcherProjection, bool) {
	p, ok := s.byName[name]
	return p, ok
}

func blendInputs() BlendInputs {
	today := day(2026, 7, 25)
	hitterBase := stubHitterSource{byName: map[string]*projections.Projection{
		"Star Hitter": {G: 100, HR: 30},
	}}
	pitcherBase := stubPitcherSource{byName: map[string]*projections.PitcherProjection{
		"Ace Arm": {G: 30, IP: 180, K: 200},
	}}
	return BlendInputs{
		HitterBase:     projections.NewChainedSource(hitterBase, projections.NewRollingSource()),
		PitcherBase:    projections.NewPitcherChainedSource(pitcherBase, projections.NewPitcherRollingSource()),
		HitterRoster:   []fantrax.Player{{ID: "h1", Name: "Star Hitter", Positions: []string{"012"}}},
		PitcherRoster:  []fantrax.Player{{ID: "p1", Name: "Ace Arm", Positions: []string{"020"}, PosShortNames: "SP"}},
		HitterScoring:  fantrax.ScoringWeights{"HR": 4},
		PitcherScoring: fantrax.ScoringWeights{"K": 2},
		HitterAvgFPG:   8,
		PitcherAvgFPG:  12,
		TeamID:         "t1",
		Today:          today,
		SeasonStart:    day(2026, 3, 25),
		CurrentPeriod:  113,
		// 1 so the two-day fixture series clears the threshold; the real run
		// uses cfg.BlendMinGP.
		BlendMinGP: 1,
		SystemName: "DepthCharts RoS",
	}
}

func healthyBlendClient() *fakeBlendClient {
	today := day(2026, 7, 25)
	return &fakeBlendClient{
		days: []fantrax.DayRoster{
			hitterDay(today.AddDate(0, 0, -2), "h1", 10),
			hitterDay(today.AddDate(0, 0, -1), "h1", 20),
		},
		pitcherStats: map[string]fantrax.RecentStat{
			"p1": {FPtsPerGame: 18, GamesPlayed: 20},
		},
	}
}

func TestBlendSources_WrapsBothRolesWhenRecencyIsAvailable(t *testing.T) {
	ft := healthyBlendClient()
	got := BlendSources(ft, blendInputs())

	if _, ok := got.Hitters.(*projections.BlendedSource); !ok {
		t.Errorf("hitters = %T, want a *projections.BlendedSource", got.Hitters)
	}
	if _, ok := got.Pitchers.(*projections.PitcherBlendedSource); !ok {
		t.Errorf("pitchers = %T, want a *projections.PitcherBlendedSource", got.Pitchers)
	}
	if got.RecentHitterCount != 1 || got.RecentPitcherCount != 1 {
		t.Errorf("counts = %d hitters / %d pitchers, want 1 / 1",
			got.RecentHitterCount, got.RecentPitcherCount)
	}
}

// THE asymmetry (rosterbot-6om criterion 3). Hitters are windowed to a bounded
// trailing range; pitchers read one unbounded season-to-date snapshot. This is
// backtest-justified (rosterbot-2nd) and a future "let's make these consistent"
// refactor should have to delete this test on purpose.
func TestBlendSources_HittersAreWindowedAndPitchersAreYTD(t *testing.T) {
	ft := healthyBlendClient()
	in := blendInputs()

	BlendSources(ft, in)

	if !ft.dailyCall.called {
		t.Fatal("hitters must read the per-day FPts series")
	}
	wantStart := in.Today.AddDate(0, 0, -recencyLookbackDays)
	wantEnd := in.Today.AddDate(0, 0, -1)
	if !ft.dailyCall.start.Equal(wantStart) || !ft.dailyCall.end.Equal(wantEnd) {
		t.Errorf("hitter window = %v..%v, want the bounded %v..%v",
			ft.dailyCall.start.Format("2006-01-02"), ft.dailyCall.end.Format("2006-01-02"),
			wantStart.Format("2006-01-02"), wantEnd.Format("2006-01-02"))
	}
	if span := ft.dailyCall.end.Sub(ft.dailyCall.start).Hours() / 24; span >= 365 {
		t.Errorf("hitter recency spans %v days — it must be bounded, not season-to-date", span)
	}

	if !ft.pitcherCall.called {
		t.Fatal("pitchers must read the YTD snapshot")
	}
	if ft.pitcherCall.period != in.CurrentPeriod {
		t.Errorf("pitcher snapshot period = %d, want the current period %d",
			ft.pitcherCall.period, in.CurrentPeriod)
	}
}

// A hitter game outside the 30-day window must not reach the blend even though
// the fetch deliberately over-reads to fill the window's far edge.
func TestBlendSources_HitterWindowExcludesStaleGamesTheFetchPulledIn(t *testing.T) {
	today := day(2026, 7, 25)
	ft := healthyBlendClient()
	ft.days = append(ft.days, hitterDay(today.AddDate(0, 0, -34), "h1", 400))

	got := BlendSources(ft, blendInputs())

	blended, ok := got.Hitters.(*projections.BlendedSource)
	if !ok {
		t.Fatalf("hitters = %T, want a blended source", got.Hitters)
	}
	bd := blended.GetHitterBreakdown("Star Hitter", "", fantrax.ScoringWeights{"HR": 4})
	if bd == nil {
		t.Fatal("no breakdown for the rostered hitter")
	}
	// (10 + 20) / 2 — the 34-day-old 400-point game is inside the 35-day fetch
	// but outside the 30-day window.
	if math.Abs(bd.RecentFPG-15.0) > 1e-9 {
		t.Errorf("RecentFPG = %v, want 15 — a game older than the window leaked into the blend", bd.RecentFPG)
	}
}

// Before the opener there is no in-season production for either role.
func TestBlendSources_PreSeasonUsesBaseSourcesForBothRoles(t *testing.T) {
	ft := healthyBlendClient()
	in := blendInputs()
	in.CurrentPeriod = 1

	got := BlendSources(ft, in)

	if got.Hitters != in.HitterBase || got.Pitchers != in.PitcherBase {
		t.Error("pre-season must pass the base sources through untouched")
	}
	if ft.dailyCall.called || ft.pitcherCall.called {
		t.Error("pre-season must not fetch recency at all")
	}
	if want := "season not started (period 1) — using DepthCharts RoS only"; strings.Join(got.Logs, "\n") != want {
		t.Errorf("logs = %q, want %q", got.Logs, want)
	}
}

// An unknown period is the other no-blend case — and unlike the pre-season one
// it logs nothing here, because LoadInputs already warned about it.
func TestBlendSources_UnknownPeriodUsesBaseSourcesSilently(t *testing.T) {
	ft := healthyBlendClient()
	in := blendInputs()
	in.PeriodErr = errors.New("current period unavailable")

	got := BlendSources(ft, in)

	if got.Hitters != in.HitterBase || got.Pitchers != in.PitcherBase {
		t.Error("an unknown period must fall back to the base sources")
	}
	if len(got.Logs) != 0 {
		t.Errorf("logs = %v, want none (LoadInputs already warned)", got.Logs)
	}
}

// The two roles degrade independently: one recency fetch failing must not cost
// the other role its blend.
func TestBlendSources_RolesDegradeIndependently(t *testing.T) {
	t.Run("hitter fetch fails", func(t *testing.T) {
		ft := healthyBlendClient()
		ft.daysErr = errors.New("daily series down")
		in := blendInputs()

		got := BlendSources(ft, in)

		if got.Hitters != in.HitterBase {
			t.Error("hitters should fall back to base, not a partial blend")
		}
		if got.RecentHitterCount != 0 {
			t.Errorf("RecentHitterCount = %d, want 0", got.RecentHitterCount)
		}
		if _, ok := got.Pitchers.(*projections.PitcherBlendedSource); !ok {
			t.Error("a hitter failure must not cost pitchers their blend")
		}
		if !strings.Contains(strings.Join(got.Logs, "\n"),
			"WARNING: recent hitter stats unavailable (daily series down) — using DepthCharts RoS only") {
			t.Errorf("missing degraded-mode warning in %v", got.Logs)
		}
	})

	t.Run("pitcher fetch fails", func(t *testing.T) {
		ft := healthyBlendClient()
		ft.pitcherErr = errors.New("snapshot down")
		in := blendInputs()

		got := BlendSources(ft, in)

		if got.Pitchers != in.PitcherBase {
			t.Error("pitchers should fall back to base")
		}
		if got.RecentPitcherCount != 0 {
			t.Errorf("RecentPitcherCount = %d, want 0", got.RecentPitcherCount)
		}
		if _, ok := got.Hitters.(*projections.BlendedSource); !ok {
			t.Error("a pitcher failure must not cost hitters their blend")
		}
		if !strings.Contains(strings.Join(got.Logs, "\n"),
			"WARNING: recent pitcher stats unavailable (snapshot down) — using DepthCharts RoS only") {
			t.Errorf("missing degraded-mode warning in %v", got.Logs)
		}
	})
}
