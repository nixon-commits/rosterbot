package lineuprun

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// dateRosterClient is the Fantrax surface the per-date pass needs: resolve a
// calendar date to its daily scoring period, and read that period's rosters.
type dateRosterClient interface {
	DailyPeriodFor(seasonStart, date time.Time) fantrax.DailyPeriod
	GetHitterRosterForPeriod(period fantrax.DailyPeriod) ([]fantrax.Player, error)
	GetPitcherRosterForPeriod(period fantrax.DailyPeriod) ([]fantrax.Player, error)
}

// dateScheduleClient is the MLB-schedule surface the per-date pass needs.
// *schedule.Client satisfies it; a map-backed fake satisfies it in tests.
type dateScheduleClient interface {
	TeamsPlayingOn(ctx context.Context, date time.Time) (map[string]bool, error)
	LockedTeams(ctx context.Context, date time.Time) (map[string]bool, error)
	ProbableStarters(ctx context.Context, date time.Time) (map[string]string, error)
	GameVenues(ctx context.Context, date time.Time) (map[string]string, error)
	BenchedPlayers(ctx context.Context, date time.Time, rosterNames map[string]string) (map[string]bool, error)
}

// OptimizeInputs is everything the per-date optimize pass consumes. It is a
// wide struct because the pass genuinely depends on all of it — the two
// rosters, the two slot sets, the two scoring weight sets, the two blended
// sources, the handedness/FIP data behind matchup adjustment, and the GS
// budget. Naming them as one input type is the point: before this they were
// twelve free variables in scope, and nothing distinguished "the pass reads
// this" from "this happens to be declared above".
type OptimizeInputs struct {
	Dates              []time.Time
	Today, SeasonStart time.Time

	HitterRoster  []fantrax.Player
	PitcherRoster []fantrax.Player

	HitterSlots  []fantrax.Slot
	PitcherSlots []fantrax.Slot

	HitterScoring  fantrax.ScoringWeights
	PitcherScoring fantrax.ScoringWeights

	HitterSrc  projections.Source
	PitcherSrc projections.PitcherSource

	// Matchup-adjustment inputs. Any of them being empty just disables the
	// adjustment for that date; none is required.
	HitterBats        map[string]string
	PitcherHandedness map[string]string
	PitcherFIP        map[string]float64
	LeagueAvgFIP      float64

	// GSBudget gates today's pitcher starts. It is applied to today only —
	// future dates each get their own daily production run.
	GSBudget *optimizer.GSBudget

	ShowPipeline bool
}

// OptimizeDates runs the hitter and pitcher optimizers for every date in
// parallel and returns one dateResult per date, in input order.
//
// Per-date failures are not fatal: each degradation (a period roster that
// won't load, an unavailable schedule, missing probables) is recorded as a
// warning on that date's result and the pass continues with a documented
// fallback. That is deliberate — a single date's upstream hiccup should not
// cost the other dates their lineups, and on the hourly production run it
// should not cost today's.
//
// The results slice is preallocated and each goroutine writes only its own
// index, so the parallelism needs no lock and the output order is the input
// order regardless of completion order.
func OptimizeDates(ctx context.Context, ft dateRosterClient, sched dateScheduleClient, in OptimizeInputs) []dateResult {
	results := make([]dateResult, len(in.Dates))

	var wg sync.WaitGroup
	for i, date := range in.Dates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = optimizeOneDate(ctx, ft, sched, in, date)
		}()
	}
	wg.Wait()
	return results
}

// optimizeOneDate is the whole per-date pipeline for a single day: resolve the
// period, fetch that day's rosters and schedule facts, optimize both roles, and
// (when requested) build the projection-pipeline detail tables.
func optimizeOneDate(ctx context.Context, ft dateRosterClient, sched dateScheduleClient, in OptimizeInputs, date time.Time) dateResult {
	isToday := date.Equal(in.Today)

	// DailyPeriodFor is the ONLY correct resolver here: ApplyLineup and
	// GetHitterRosterForPeriod are keyed by Fantrax's *daily* scoring period.
	// Never resolve this from the weekly `periods` list — every date in a
	// --matchup window falls inside some weekly period's range, so a
	// weekly-keyed lookup hands back one number for the whole span. During a
	// merged week (the All-Star break, weekly period 16 spanning
	// 2026-07-13..26) that collapsed 11 distinct calendar dates onto period 16:
	// the roster fetches returned the identical stale snapshot for all of them,
	// so each date's optimizer never saw its own prior apply, and the same swap
	// re-applied (and re-notified via Pushover) on every hourly run — the
	// rosterbot-z3b flood.
	period := ft.DailyPeriodFor(in.SeasonStart, date)

	var warnings []string
	warnf := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	// Fetch period-specific rosters.
	dateHitterRoster := in.HitterRoster
	datePitcherRoster := in.PitcherRoster
	if !isToday && period > 0 {
		if r, err := ft.GetHitterRosterForPeriod(period); err == nil {
			dateHitterRoster = r
		} else {
			warnf("could not fetch hitter roster for period %d (%v) — using current", period, err)
		}
		if r, err := ft.GetPitcherRosterForPeriod(period); err == nil {
			datePitcherRoster = r
		} else {
			warnf("could not fetch pitcher roster for period %d (%v) — using current", period, err)
		}
	}

	// MLB schedule + probable pitchers.
	playingToday, err := sched.TeamsPlayingOn(ctx, date)
	if err != nil {
		warnf("mlb schedule unavailable for %s (%v) — assuming all teams play", date.Format("2006-01-02"), err)
		allPlayers := append(dateHitterRoster, datePitcherRoster...)
		playingToday = allTeamsPlaying(allPlayers)
	}

	// Detect locked teams (game in progress or final) — only for today.
	if isToday {
		lockedTeams, err := sched.LockedTeams(ctx, date)
		if err != nil {
			warnf("locked teams unavailable (%v) — proceeding without lock detection", err)
		} else if len(lockedTeams) > 0 {
			markLocked(dateHitterRoster, lockedTeams)
			markLocked(datePitcherRoster, lockedTeams)
		}
	}

	probableStarters, err := sched.ProbableStarters(ctx, date)
	if err != nil {
		warnf("probable pitchers unavailable for %s (%v) — SPs default to start", date.Format("2006-01-02"), err)
		probableStarters = map[string]string{} // empty = default to start
	}

	// Fetch game venues for matchup adjustments.
	var venues map[string]string
	if v, err := sched.GameVenues(ctx, date); err != nil {
		warnf("game venues unavailable for %s (%v)", date.Format("2006-01-02"), err)
	} else {
		venues = v
	}

	// Fetch benched hitters (confirmed out of real-life starting lineup).
	// Only for today — lineups aren't posted days in advance.
	var benchedToday map[string]bool
	if isToday {
		rosterNames := make(map[string]string, len(dateHitterRoster))
		for _, p := range dateHitterRoster {
			if !p.InMinors && !p.IsInjured {
				rosterNames[projections.NormalizeName(p.Name)] = p.MLBTeam
			}
		}
		if b, err := sched.BenchedPlayers(ctx, date, rosterNames); err != nil {
			warnf("starting lineups unavailable (%v) — assuming all hitters play", err)
		} else if len(b) > 0 {
			benchedToday = b
		}
	}

	// Optimize hitters (with matchup adjustment if available).
	dateHitterSrc := in.HitterSrc
	var matchupSrc *projections.MatchupAdjustedSource
	if opposing := buildOpposingPitchers(probableStarters, venues, in.PitcherHandedness, in.PitcherFIP, in.LeagueAvgFIP); len(opposing) > 0 {
		matchupSrc = projections.NewMatchupAdjustedSource(dateHitterSrc, opposing, in.HitterBats, in.LeagueAvgFIP)
		dateHitterSrc = matchupSrc
	}
	hitterResult := optimizer.OptimizeLineup(dateHitterRoster, playingToday, dateHitterSrc, in.HitterScoring, in.HitterSlots, benchedToday)

	// GS budget gate only applies to today — for future dates the budget would
	// need to be recomputed per-date, and the daily production run handles each
	// day as it arrives.
	dateBudget := in.GSBudget
	if !isToday {
		dateBudget = nil
	}
	pitcherResult := optimizer.OptimizePitcherLineup(datePitcherRoster, playingToday, probableStarters, in.PitcherSrc, in.PitcherScoring, in.PitcherSlots, dateBudget)

	dr := dateResult{
		date:          date,
		period:        period,
		isToday:       isToday,
		hitterResult:  hitterResult,
		pitcherResult: pitcherResult,
		warnings:      warnings,
		venues:        venues,
		benchedToday:  benchedToday,
		budget:        dateBudget,
	}

	if in.ShowPipeline {
		dr.hitterBreakdowns = hitterBreakdownsFor(hitterResult, in.HitterSrc, in.HitterScoring)
		dr.hitterPipelines = hitterPipelinesFor(hitterResult, in.HitterSrc, in.HitterScoring, dr.hitterBreakdowns, matchupSrc)
		dr.pitcherPipelines = pitcherPipelinesFor(pitcherResult, in.PitcherSrc, in.PitcherScoring)
	}

	return dr
}

// markLocked flags players whose MLB team's game has started.
//
// ACTIVE players are locked regardless of health: Fantrax refuses to move a
// player out of an active slot once his team's game has started, and an injury
// icon does not release the slot. A health exemption here is exactly the
// 2026-08-29 incident — Cade Cavalli started, was flagged injured mid-game,
// and the optimizer proposed benching him (zero value, exempt from the lock);
// Fantrax rejected that payload on every hourly run for the rest of the day.
//
// RESERVE minors and injured players are skipped: they cannot be moved into an
// active slot anyway, and marking them locked would misreport why.
func markLocked(roster []fantrax.Player, lockedTeams map[string]bool) {
	for i := range roster {
		p := &roster[i]
		if !lockedTeams[p.MLBTeam] {
			continue
		}
		// Status alone, not Status+RosterPosition: the optimizers' bench
		// predicate is Status=="Active" alone, so anything benchable must be
		// lockable or the exemption reopens through the gap.
		if p.Status == "Active" || (!p.InMinors && !p.IsInjured) {
			p.Locked = true
		}
	}
}

// buildOpposingPitchers pairs each team with the pitcher it faces, by matching
// two teams that share a home venue on the date. Returns nil when there is not
// enough data to adjust (no probables, no venues, or no league-average FIP to
// normalize quality against), which disables matchup adjustment for the date.
func buildOpposingPitchers(
	probableStarters map[string]string,
	venues map[string]string,
	handedness map[string]string,
	fip map[string]float64,
	leagueAvgFIP float64,
) map[string]projections.OpposingPitcher {
	if len(probableStarters) == 0 || leagueAvgFIP <= 0 || venues == nil {
		return nil
	}
	opposing := make(map[string]projections.OpposingPitcher)
	for pitcherName, pitcherTeam := range probableStarters {
		pitcherHome := venues[pitcherTeam]
		for team, homeTeam := range venues {
			if team == pitcherTeam {
				continue
			}
			if homeTeam != pitcherHome {
				continue
			}
			opp := projections.OpposingPitcher{Name: pitcherName, Team: pitcherTeam}
			if h, ok := handedness[pitcherName]; ok {
				opp.Throws = h
			}
			if f, ok := fip[pitcherName]; ok {
				opp.FIP = f
			}
			opposing[team] = opp
			break
		}
	}
	return opposing
}

// hitterBreakdownsFor extracts each scored hitter's blend breakdown, when the
// source is actually a blended one (it is not before the season opener, or when
// the recency fetch failed).
func hitterBreakdownsFor(res optimizer.Result, src projections.Source, scoring fantrax.ScoringWeights) map[string]*projections.HitterBreakdown {
	blended, ok := src.(*projections.BlendedSource)
	if !ok {
		return nil
	}
	out := make(map[string]*projections.HitterBreakdown)
	for _, sp := range res.Scored {
		if bd := blended.GetHitterBreakdown(sp.Player.Name, sp.Player.MLBTeam, scoring); bd != nil {
			out[sp.Player.ID] = bd
		}
	}
	return out
}

// hitterPipelinesFor builds the base → blend → platoon → quality → final
// breakdown the --pipeline table renders, one row per hitter with a game and a
// usable projection.
func hitterPipelinesFor(
	res optimizer.Result,
	src projections.Source,
	scoring fantrax.ScoringWeights,
	breakdowns map[string]*projections.HitterBreakdown,
	matchupSrc *projections.MatchupAdjustedSource,
) map[string]*projections.HitterPipelineDetail {
	out := make(map[string]*projections.HitterPipelineDetail)
	for _, sp := range res.Scored {
		if !sp.HasGame {
			continue
		}
		proj, ok := src.GetProjection(sp.Player.Name, sp.Player.MLBTeam)
		if !ok || proj.G <= 0 {
			continue
		}
		pd := &projections.HitterPipelineDetail{
			PlayerName:     sp.Player.Name,
			PlayerID:       sp.Player.ID,
			MLBTeam:        sp.Player.MLBTeam,
			BasePtsPerGame: projections.ExpectedPtsFromProj(proj, scoring),
			PlatoonMult:    1.0,
			QualityMult:    1.0,
		}

		// Stage 2: Blend
		if bd, ok := breakdowns[sp.Player.ID]; ok {
			pd.BlendedPtsPerGame = bd.BlendedPts
			pd.HasRecent = bd.HasRecent
			pd.BaseWt = bd.BaseWt
			pd.RecentFPG = bd.RecentFPG
			pd.GamesPlayed = bd.GamesPlayed
		} else {
			pd.BlendedPtsPerGame = pd.BasePtsPerGame
		}
		pd.BlendDelta = pd.BlendedPtsPerGame - pd.BasePtsPerGame

		// Stage 3: Matchup (platoon + quality)
		afterPlatoon := pd.BlendedPtsPerGame
		if matchupSrc != nil {
			md := matchupSrc.GetMatchupDetail(sp.Player.Name, sp.Player.MLBTeam)
			pd.PlatoonMult = md.PlatoonMult
			pd.PlatoonFavorable = md.Favorable
			pd.QualityMult = md.QualityMult
			pd.OpposingPitcher = md.OpposingPitcher
			pd.OpposingFIP = md.OpposingFIP
			pd.LeagueAvgFIP = md.LeagueAvgFIP
			afterPlatoon = pd.BlendedPtsPerGame * md.PlatoonMult
			pd.FinalPtsPerGame = pd.BlendedPtsPerGame * md.CombinedMult
		} else {
			pd.FinalPtsPerGame = pd.BlendedPtsPerGame
		}
		pd.PlatoonDelta = afterPlatoon - pd.BlendedPtsPerGame
		pd.QualityDelta = pd.FinalPtsPerGame - afterPlatoon

		out[sp.Player.ID] = pd
	}
	return out
}

// pitcherPipelinesFor builds the pitcher-side pipeline table: base → blend →
// GS gate → final. Gate attribution comes from res.GateReport, which is the
// gate's own account of what it declined. It used to be re-derived here by
// inference (probable ∧ !IsStarter ∧ gated), which was both fragile and, in
// the case of the discount multiplier, wrong.
func pitcherPipelinesFor(
	res optimizer.PitcherResult,
	src projections.PitcherSource,
	scoring fantrax.ScoringWeights,
) map[string]*projections.PitcherPipelineDetail {
	var breakdowns map[string]*projections.PitcherBreakdown
	if blended, ok := src.(*projections.PitcherBlendedSource); ok {
		breakdowns = make(map[string]*projections.PitcherBreakdown)
		for _, sp := range res.Scored {
			if bd := blended.GetPitcherBreakdown(sp.Player.Name, sp.Player.MLBTeam, scoring); bd != nil {
				breakdowns[sp.Player.ID] = bd
			}
		}
	}

	suppressed := make(map[string]bool, len(res.GateReport.Suppressed))
	for _, s := range res.GateReport.Suppressed {
		suppressed[s.PlayerID] = true
	}

	out := make(map[string]*projections.PitcherPipelineDetail)
	for _, sp := range res.Scored {
		if !sp.HasGame {
			continue
		}

		// Determine base pts from breakdown or direct projection lookup.
		var basePts float64
		if bd, ok := breakdowns[sp.Player.ID]; ok {
			basePts = bd.BasePts
		} else if proj, ok := src.GetPitcherProjection(sp.Player.Name, sp.Player.MLBTeam); ok && proj.G > 0 {
			basePts = projections.PitcherExpectedPtsFromProj(proj, scoring)
		} else {
			continue
		}

		spEligible := strings.Contains(sp.Player.PosShortNames, "SP")
		role := "RP"
		if spEligible {
			role = "SP"
		}

		pd := &projections.PitcherPipelineDetail{
			PlayerName:     sp.Player.Name,
			PlayerID:       sp.Player.ID,
			MLBTeam:        sp.Player.MLBTeam,
			Role:           role,
			BasePtsPerGame: basePts,
		}

		// Stage 2: Blend
		if bd, ok := breakdowns[sp.Player.ID]; ok {
			pd.BlendedPtsPerGame = bd.BlendedPts
			pd.HasRecent = bd.HasRecent
			pd.BaseWt = bd.BaseWt
			pd.RecentFPG = bd.RecentFPG
			pd.GamesPlayed = bd.GamesPlayed
		} else {
			pd.BlendedPtsPerGame = basePts
		}
		pd.BlendDelta = pd.BlendedPtsPerGame - pd.BasePtsPerGame

		// Stage 3: GS Gate — the gate's own report says which starts it declined.
		// The suppressed starter carries the optimizer's NonStarterSPDiscount.
		pd.FinalPtsPerGame = pd.BlendedPtsPerGame
		if suppressed[sp.Player.ID] {
			pd.WasGated = true
			pd.FinalPtsPerGame = pd.BlendedPtsPerGame * optimizer.NonStarterSPDiscount
			pd.GateDelta = pd.FinalPtsPerGame - pd.BlendedPtsPerGame
		}

		out[sp.Player.ID] = pd
	}
	return out
}
