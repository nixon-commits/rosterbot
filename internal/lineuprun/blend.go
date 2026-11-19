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

	// HitterBlend enables the hitter recency blend. False is the production
	// default (rosterbot-jyt0.3): hitters run on the base projection alone and
	// no hitter recency data is fetched. The pitcher blend ignores it.
	HitterBlend bool

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
// # The hitter blend is OFF by default (rosterbot-jyt0.3)
//
// Unless BlendInputs.HitterBlend is set, hitters are valued from the base
// projection alone. The full-season replay of `backtest --recency-experiment`
// (173 days, depthcharts-ros base) ranked realized points base 8,512 > w14
// 8,453 > w30 8,450 > decay21 8,394 > ytd 8,338: every window lost to no
// blend, because a rest-of-season projection already carries the in-season
// signal and blending on top double-counts it. That supersedes rosterbot-2nd's
// June reading, which compared windows against each other and held (w30 still
// beats ytd) but never showed a window beating the base.
//
// The blend code, the switch and the harness all stay, so the mid-May re-run
// on a steamer-ros base (rosterbot-jyt0.4) is one command and reverting is one
// env var (HITTER_RECENCY_BLEND=true). Everything below about the hitter
// window describes the path that switch re-enables.
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
//
// # A zero-row success IS missing recency data (rosterbot-l56d)
//
// The guard above is not only about the error return. A fetch that succeeds
// with an EMPTY map is the same absence reached by a different route, and it
// used to take the success branch: the run logged "loaded: 0 players with
// data", built a blend over nothing, exited 0, and the ledger recorded
// SUCCESS — the soft-failure the ledger cannot see. Zero rows and a failed
// fetch are therefore reported identically, because they mean the same thing
// to every consumer downstream.
//
// This stays a WARNING plus fallback rather than a hard error, because the
// surrounding contract is degrade-not-fail: a lineup run must still produce a
// lineup. What it must not do is produce one whose numbers moved for a reason
// nothing reported.
func BlendSources(ft blendClient, in BlendInputs) BlendResult {
	var r BlendResult

	// Before the opener — or with no idea what period it is — there is no
	// in-season production to blend, for either role.
	seasonUnderway := in.PeriodErr == nil && in.CurrentPeriod > 1

	// --- Hitters: base projection only, unless the blend is switched on ---
	if !seasonUnderway {
		if in.CurrentPeriod <= 1 {
			r.logf("season not started (period %d) — using %s only", in.CurrentPeriod, in.SystemName)
		}
		r.Hitters = in.HitterBase
	} else if !in.HitterBlend {
		// Stated on every in-season run, so "no hitter recency line" can never
		// be mistaken for a blend that silently failed to load.
		r.logf("hitter recency blend off — hitters valued from %s only (HITTER_RECENCY_BLEND=true to enable)", in.SystemName)
		r.Hitters = in.HitterBase
	} else {
		r.logf("fetching recent hitter stats (trailing %dd window as of %s)...",
			recencyWindowDays, in.Today.Format("2006-01-02"))
		recentStats, err := windowedHitterRecent(ft, in.TeamID, in.Today, in.SeasonStart, in.NoCache)
		switch {
		case err != nil:
			r.logf("WARNING: recent hitter stats unavailable (%v) — using %s only", err, in.SystemName)
			r.Hitters = in.HitterBase
		case len(recentStats) == 0:
			r.logf("WARNING: recent hitter stats unavailable (zero rows) — using %s only", in.SystemName)
			r.Hitters = in.HitterBase
		default:
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
		switch {
		case err != nil:
			r.logf("WARNING: recent pitcher stats unavailable (%v) — using %s only", err, in.SystemName)
			r.Pitchers = in.PitcherBase
		case len(recentPitStats) == 0:
			r.logf("WARNING: recent pitcher stats unavailable (zero rows) — using %s only", in.SystemName)
			r.Pitchers = in.PitcherBase
		default:
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
