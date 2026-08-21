package lineuprun

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// blendClient is the fetch surface the BlendSources phase needs: the daily
// FPts series behind the hitter window (via recentStatsClient) and the pitcher
// YTD snapshot. Two roles, two different shapes of recency — see BlendSources.
type blendClient interface {
	recentStatsClient
	GetRecentPitcherStats(currentPeriod fantrax.DailyPeriod) (map[string]fantrax.RecentStat, error)
}

// BlendInputs is everything BlendSources needs to wrap the base projection
// sources with recent Fantrax production.
//
// The base sources arrive already chained (FanGraphs → rolling fallback) and
// the league-average FPG arrives precomputed, so the phase never touches a
// concrete FanGraphs source type — it only needs something satisfying the two
// Source interfaces plus the numbers the blend weights are built from.
type BlendInputs struct {
	HitterBase  projections.Source
	PitcherBase projections.PitcherSource

	HitterRoster  []fantrax.Player
	PitcherRoster []fantrax.Player

	HitterScoring  fantrax.ScoringWeights
	PitcherScoring fantrax.ScoringWeights

	// HitterAvgFPG / PitcherAvgFPG are the roster-wide mean projections used as
	// the blend's regression target.
	HitterAvgFPG  float64
	PitcherAvgFPG float64

	TeamID             string
	Today, SeasonStart time.Time

	// CurrentPeriod / PeriodErr come from LoadInputs. Either an unknown period
	// or a pre-opener period (<= 1) means there is no in-season signal to blend
	// and both roles fall through to their base source.
	CurrentPeriod fantrax.DailyPeriod
	PeriodErr     error

	BlendMinGP int
	NoCache    bool

	// SystemName is the display name of the projection system, used only in the
	// degraded-mode log lines ("… — using DepthCharts RoS only").
	SystemName string
}

// BlendResult is the phase's output: the two projection sources the optimizers
// consume, the per-role counts the progress display reports, and the log lines
// to emit. Logs are returned rather than printed for the same reason
// ComputeGSBudget returns its alert — it keeps the phase assertable without a
// progress display (rosterbot-6om criterion 1).
type BlendResult struct {
	Hitters  projections.Source
	Pitchers projections.PitcherSource

	RecentHitterCount  int
	RecentPitcherCount int

	Logs []string
}

func (r *BlendResult) logf(format string, args ...any) {
	r.Logs = append(r.Logs, fmt.Sprintf(format, args...))
}

// BlendSources wraps the base projection sources with recent Fantrax
// production, one strategy per role.
//
// # The asymmetry is deliberate — do not unify it
//
// Hitters blend against a trailing 30-day window; pitchers blend against
// season-to-date YTD. That is not an oversight, it is the result of
// rosterbot-2nd's `backtest --recency-experiment`: over both the full season
// and the last eight weeks, a bounded 30-day window beat unbounded YTD by
// roughly 1 realized pt/game/day of hitter production, because raw YTD
// double-counts the in-season signal already regressed into the depthcharts-ros
// base. The same harness found pitchers produced *identical* lineup outcomes
// across every strategy — pitcher slotting is game-availability-bound, not
// projection-bound — so they stay on the cheaper single-snapshot YTD read.
//
// The two paths therefore look different on purpose: hitters reconstruct a
// per-day FPts series and collapse it (see windowedHitterRecent), while
// pitchers take one roster snapshot whose FantasyPointsPerGame is cumulative
// YTD regardless of the period requested.
//
// Both roles degrade the same way: any failure falls back to the base source
// rather than a partial blend, since a blend built from missing recency data
// would quietly shift every player's expected points.
func BlendSources(ft blendClient, in BlendInputs) BlendResult {
	var r BlendResult

	// Before the opener — or with no idea what period it is — there is no
	// in-season production to blend, for either role.
	seasonUnderway := in.PeriodErr == nil && in.CurrentPeriod > 1

	// --- Hitters: trailing 30-day window ---
	if !seasonUnderway {
		if in.CurrentPeriod <= 1 {
			r.logf("season not started (period %d) — using %s only", in.CurrentPeriod, in.SystemName)
		}
		r.Hitters = in.HitterBase
	} else {
		r.logf("fetching recent hitter stats (trailing %dd window as of %s)...",
			recencyWindowDays, in.Today.Format("2006-01-02"))
		recentStats, err := windowedHitterRecent(ft, in.TeamID, in.Today, in.SeasonStart, in.NoCache)
		if err != nil {
			r.logf("WARNING: recent hitter stats unavailable (%v) — using %s only", err, in.SystemName)
			r.Hitters = in.HitterBase
		} else {
			r.RecentHitterCount = len(recentStats)
			r.logf("recent hitter stats loaded: %d players with data", len(recentStats))
			r.Hitters = projections.NewBlendedSource(
				in.HitterBase, recentStats,
				nameToID(in.HitterRoster), in.BlendMinGP, in.HitterAvgFPG)
		}
	}

	// --- Pitchers: season-to-date YTD ---
	if !seasonUnderway {
		r.Pitchers = in.PitcherBase
	} else {
		recentPitStats, err := ft.GetRecentPitcherStats(in.CurrentPeriod)
		if err != nil {
			r.logf("WARNING: recent pitcher stats unavailable (%v) — using %s only", err, in.SystemName)
			r.Pitchers = in.PitcherBase
		} else {
			r.RecentPitcherCount = len(recentPitStats)
			r.logf("recent pitcher stats loaded: %d players with data", len(recentPitStats))
			r.Pitchers = projections.NewPitcherBlendedSource(
				in.PitcherBase, recentPitStats,
				nameToID(in.PitcherRoster), playerPositions(in.PitcherRoster),
				in.BlendMinGP, in.PitcherAvgFPG)
		}
	}

	return r
}

// nameToID maps normalized player name → Fantrax player ID, the join both
// blended sources use to attach a recency row to a projection.
func nameToID(roster []fantrax.Player) map[string]string {
	m := make(map[string]string, len(roster))
	for _, p := range roster {
		m[projections.NormalizeName(p.Name)] = p.ID
	}
	return m
}

// playerPositions maps player ID → position eligibility, which the pitcher
// blend needs to pick a stabilization threshold (SP stabilizes at 15 GP, RP at
// 25).
func playerPositions(roster []fantrax.Player) map[string][]string {
	m := make(map[string][]string, len(roster))
	for _, p := range roster {
		m[p.ID] = p.Positions
	}
	return m
}
