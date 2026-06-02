package pipeline

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/schedule"
)

// Input configures a single-date pipeline run.
type Input struct {
	Date             time.Time
	ProjectionSystem string // "steamer", "depthcharts", "thebatx"
}

// HitterDetail holds per-player projection data for the web GUI.
type HitterDetail struct {
	Player      fantrax.Player
	ExpectedPts float64
	HasGame     bool
	Breakdown   *projections.HitterBreakdown // nil if no projection available
	ParkFactor  float64                      // 1.0 if unavailable
	MatchupMult float64                      // 1.0 if unavailable
}

// PitcherDetail holds per-pitcher projection data for the web GUI.
type PitcherDetail struct {
	Player      fantrax.Player
	ExpectedPts float64
	HasGame     bool
	IsStarter   bool
	SteamerPts  float64
	RecentFPG   float64
	SteamerWt   float64
	RecentWt    float64
	GamesPlayed int
	IsSP        bool
}

// LineupChange describes a single roster move.
type LineupChange struct {
	PlayerName string
	Direction  string  // "activate" or "bench"
	FromSlot   string  // display name of current slot (empty if from bench)
	ToSlot     string  // display name of target slot ("BN" if benching)
	PtsDelta   float64 // positive for activations, negative for benchings
}

// Result holds the complete output of a single-date pipeline run.
type Result struct {
	Date             time.Time
	ProjectionSystem string
	Hitters          []HitterDetail
	Pitchers         []PitcherDetail
	HitterSlots      []fantrax.Slot
	PitcherSlots     []fantrax.Slot
	HitterResult     optimizer.Result
	PitcherResult    optimizer.PitcherResult
	Changes          []LineupChange
	TotalDelta       float64
	Warnings         []string
}

// Run executes the full optimization pipeline for a single date and returns
// detailed results suitable for the web GUI. It handles its own config loading,
// client creation, and data fetching.
func Run(input Input) (*Result, error) {
	if err := projections.SetProjectionSystem(input.ProjectionSystem); err != nil {
		return nil, err
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	date := input.Date
	if date.IsZero() {
		date = today
	}
	isToday := date.Equal(today)

	// Load config and create client.
	cfg, ft, err := initPipeline(date)
	if err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}

	result := &Result{
		Date:             date,
		ProjectionSystem: input.ProjectionSystem,
	}

	// --- Fetch shared data ---
	hitterRoster, err := ft.GetHitterRoster()
	if err != nil {
		return nil, fmt.Errorf("get hitter roster: %w", err)
	}

	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		return nil, fmt.Errorf("get hitter slots: %w", err)
	}
	result.HitterSlots = hitterSlots

	hitterScoring, err := ft.GetScoringWeights()
	if err != nil {
		return nil, fmt.Errorf("get hitter scoring: %w", err)
	}

	pitcherRoster, err := ft.GetPitcherRoster()
	if err != nil {
		return nil, fmt.Errorf("get pitcher roster: %w", err)
	}

	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		return nil, fmt.Errorf("get pitcher slots: %w", err)
	}
	result.PitcherSlots = pitcherSlots

	pitcherScoring, err := ft.GetPitcherScoringWeights()
	if err != nil {
		return nil, fmt.Errorf("get pitcher scoring: %w", err)
	}

	// --- Current period + recent stats ---
	currentPeriod, periodErr := ft.GetCurrentPeriod()
	if periodErr != nil {
		log.Printf("WARNING: could not get current period (%v)", periodErr)
	}

	// --- Hitter projections ---
	var hitterProjSrc projections.Source
	fgSrc, err := projections.NewFanGraphsSourceFromCSV("fangraphs-leaderboard-projections_batters.csv")
	if err != nil {
		fgSrc, err = projections.NewFanGraphsSource()
	}
	if err != nil {
		return nil, fmt.Errorf("batting projections unavailable: %w", err)
	}

	rolling := projections.NewRollingSource()
	baseSrc := projections.NewChainedSource(fgSrc, rolling)

	if periodErr != nil || currentPeriod <= 1 {
		hitterProjSrc = baseSrc
	} else {
		lookback := currentPeriod - 1
		if lookback > 60 {
			lookback = 60
		}
		if lookback < 10 {
			lookback = 10
		}
		recentStats, err := ft.GetRecentStats(currentPeriod, lookback)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("recent hitter stats unavailable: %v", err))
			hitterProjSrc = baseSrc
		} else {
			nameToID := make(map[string]string)
			for _, p := range hitterRoster {
				nameToID[projections.NormalizeName(p.Name)] = p.ID
			}
			hitterProjSrc = projections.NewBlendedSource(baseSrc, recentStats, hitterScoring, nameToID, cfg.BlendMinGP)
		}
	}

	// MLBAM IDs for handedness lookup.
	var hitterMLBAMIDs map[string]int
	if fgSrc != nil {
		hitterMLBAMIDs = fgSrc.MLBAMIDs()
	}

	// --- Pitcher projections ---
	var pitcherProjSrc projections.PitcherSource
	fgPitSrc, err := projections.NewFanGraphsPitcherSourceFromCSV("fangraphs-leaderboard-projections_pitchers.csv")
	if err != nil {
		fgPitSrc, err = projections.NewFanGraphsPitcherSource()
	}
	if err != nil {
		return nil, fmt.Errorf("pitching projections unavailable: %w", err)
	}

	pitRolling := projections.NewPitcherRollingSource()
	pitBaseSrc := projections.NewPitcherChainedSource(fgPitSrc, pitRolling)

	if periodErr != nil || currentPeriod <= 1 {
		pitcherProjSrc = pitBaseSrc
	} else {
		pitLookback := currentPeriod - 1
		if pitLookback > 60 {
			pitLookback = 60
		}
		if pitLookback < 10 {
			pitLookback = 10
		}
		recentPitStats, err := ft.GetRecentPitcherStats(currentPeriod, pitLookback)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("recent pitcher stats unavailable: %v", err))
			pitcherProjSrc = pitBaseSrc
		} else {
			pitNameToID := make(map[string]string)
			pitPlayerPos := make(map[string][]string)
			for _, p := range pitcherRoster {
				pitNameToID[projections.NormalizeName(p.Name)] = p.ID
				pitPlayerPos[p.ID] = p.Positions
			}
			pitcherProjSrc = projections.NewPitcherBlendedSource(pitBaseSrc, recentPitStats, pitcherScoring, pitNameToID, pitPlayerPos, cfg.BlendMinGP)
		}
	}

	// Pitcher FIP for matchup adjustments.
	var pitcherFIP map[string]float64
	var leagueAvgFIP float64
	var pitcherMLBAMIDs map[string]int
	if fgPitSrc != nil {
		pitcherFIP, leagueAvgFIP = fgPitSrc.PitcherInfo()
		pitcherMLBAMIDs = fgPitSrc.MLBAMIDs()
	}

	// Fetch handedness.
	var hitterBats map[string]string
	var pitcherHandedness map[string]string
	allMLBAMIDs := make(map[string]int)
	for k, v := range hitterMLBAMIDs {
		allMLBAMIDs[k] = v
	}
	for k, v := range pitcherMLBAMIDs {
		allMLBAMIDs[k] = v
	}
	if len(allMLBAMIDs) > 0 {
		bats, throws, err := projections.FetchMLBHandedness(allMLBAMIDs)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("MLB handedness unavailable: %v", err))
		} else {
			hitterBats = bats
			pitcherHandedness = throws
		}
	}

	schedClient := schedule.NewClient()

	// Season start for period calculation.
	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not get season start: %v", err))
	}

	// --- Per-date optimization ---
	period := fantrax.PeriodForDate(seasonStart, date)

	dateHitterRoster := hitterRoster
	datePitcherRoster := pitcherRoster
	if !isToday && period > 0 {
		if r, err := ft.GetHitterRosterForPeriod(period); err == nil {
			dateHitterRoster = r
		} else {
			result.Warnings = append(result.Warnings, fmt.Sprintf("could not fetch hitter roster for period %d: %v", period, err))
		}
		if r, err := ft.GetPitcherRosterForPeriod(period); err == nil {
			datePitcherRoster = r
		} else {
			result.Warnings = append(result.Warnings, fmt.Sprintf("could not fetch pitcher roster for period %d: %v", period, err))
		}
	}

	// Schedule data.
	playingToday, err := schedClient.TeamsPlayingOn(date)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("schedule unavailable: %v", err))
		allPlayers := append(dateHitterRoster, datePitcherRoster...)
		playingToday = allTeamsPlaying(allPlayers)
	}

	// Locked teams (today only).
	if isToday {
		lockedTeams, err := schedClient.LockedTeams(date)
		if err == nil && len(lockedTeams) > 0 {
			for i := range dateHitterRoster {
				if lockedTeams[dateHitterRoster[i].MLBTeam] && !dateHitterRoster[i].InMinors && !dateHitterRoster[i].IsInjured {
					dateHitterRoster[i].Locked = true
				}
			}
			for i := range datePitcherRoster {
				if lockedTeams[datePitcherRoster[i].MLBTeam] && !datePitcherRoster[i].InMinors && !datePitcherRoster[i].IsInjured {
					datePitcherRoster[i].Locked = true
				}
			}
		}
	}

	probableStarters, err := schedClient.ProbableStarters(date)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("probable pitchers unavailable: %v", err))
		probableStarters = map[string]string{}
	}

	var venues map[string]string
	v, err := schedClient.GameVenues(date)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("game venues unavailable: %v", err))
	} else {
		venues = v
	}

	// Benched hitters (today only).
	var benchedToday map[string]bool
	if isToday {
		rosterNames := make(map[string]string, len(dateHitterRoster))
		for _, p := range dateHitterRoster {
			if !p.InMinors && !p.IsInjured {
				rosterNames[projections.NormalizeName(p.Name)] = p.MLBTeam
			}
		}
		if b, err := schedClient.BenchedPlayers(date, rosterNames); err == nil && len(b) > 0 {
			benchedToday = b
		}
	}

	// Build per-date projection source with matchup adjustments.
	dateHitterSrc := hitterProjSrc

	var matchupSrc *projections.MatchupAdjustedSource
	if len(probableStarters) > 0 && leagueAvgFIP > 0 && venues != nil {
		opposingPitchers := buildOpposingPitchers(probableStarters, venues, pitcherHandedness, pitcherFIP)
		if len(opposingPitchers) > 0 {
			matchupSrc = projections.NewMatchupAdjustedSource(dateHitterSrc, opposingPitchers, hitterBats, leagueAvgFIP)
			dateHitterSrc = matchupSrc
		}
	}

	// --- Run optimizers ---
	hitterResult := optimizer.OptimizeLineup(dateHitterRoster, playingToday, dateHitterSrc, hitterScoring, hitterSlots, benchedToday)
	result.HitterResult = hitterResult

	pitcherResult := optimizer.OptimizePitcherLineup(datePitcherRoster, playingToday, probableStarters, pitcherProjSrc, pitcherScoring, pitcherSlots, nil)
	result.PitcherResult = pitcherResult

	// --- Build detailed hitter data ---
	slotName := buildSlotNameMap(hitterSlots, pitcherSlots)

	for _, sp := range hitterResult.Scored {
		detail := HitterDetail{
			Player:      sp.Player,
			ExpectedPts: sp.ExpectedPts,
			HasGame:     sp.HasGame,
			ParkFactor:  1.0,
			MatchupMult: 1.0,
		}

		// Get blend breakdown (always, not gated by a flag).
		if blended, ok := hitterProjSrc.(*projections.BlendedSource); ok {
			detail.Breakdown = blended.GetHitterBreakdown(sp.Player.Name, sp.Player.MLBTeam, hitterScoring)
		}

		// Get matchup multiplier.
		if matchupSrc != nil {
			detail.MatchupMult = matchupSrc.MatchupMultiplier(sp.Player.Name, sp.Player.MLBTeam)
		}

		result.Hitters = append(result.Hitters, detail)
	}

	// --- Build detailed pitcher data ---
	for _, sp := range pitcherResult.Scored {
		detail := PitcherDetail{
			Player:      sp.Player,
			ExpectedPts: sp.ExpectedPts,
			HasGame:     sp.HasGame,
			IsStarter:   sp.IsStarter,
			IsSP:        strings.Contains(sp.Player.PosShortNames, "SP"),
		}

		// Get pitcher blend breakdown.
		if pitBlended, ok := pitcherProjSrc.(*projections.PitcherBlendedSource); ok {
			proj, projOK := pitBlended.GetPitcherProjection(sp.Player.Name, sp.Player.MLBTeam)
			if projOK && proj.G > 0 {
				detail.SteamerPts = projections.PitcherExpectedPtsFromProj(proj, pitcherScoring)
			}

			playerID, idOK := findPlayerID(pitcherRoster, sp.Player.Name)
			if idOK {
				if recent, ok := getRecentPitcherStat(pitBlended, playerID); ok {
					detail.RecentFPG = recent.FPtsPerGame
					detail.GamesPlayed = recent.GamesPlayed
				}
			}

			detail.SteamerWt, detail.RecentWt = projections.PitcherBlendWeightsForDisplay(detail.GamesPlayed, detail.IsSP)
		}

		result.Pitchers = append(result.Pitchers, detail)
	}

	// --- Build lineup changes ---
	allActivate := append(hitterResult.ToActivate, pitcherResult.ToActivate...)
	allBench := append(hitterResult.ToBench, pitcherResult.ToBench...)

	ptsMap := buildPtsMap(hitterResult, pitcherResult)

	for _, ps := range allActivate {
		name := playerNameByID(dateHitterRoster, datePitcherRoster, ps.PlayerID)
		result.Changes = append(result.Changes, LineupChange{
			PlayerName: name,
			Direction:  "activate",
			ToSlot:     slotName[ps.PosID],
			PtsDelta:   ptsMap[ps.PlayerID],
		})
		result.TotalDelta += ptsMap[ps.PlayerID]
	}
	for _, id := range allBench {
		name := playerNameByID(dateHitterRoster, datePitcherRoster, id)
		result.Changes = append(result.Changes, LineupChange{
			PlayerName: name,
			Direction:  "bench",
			ToSlot:     "BN",
			PtsDelta:   -ptsMap[id],
		})
		result.TotalDelta -= ptsMap[id]
	}

	return result, nil
}

// initPipeline loads config and creates a Fantrax client.
func initPipeline(date time.Time) (*config.Config, *fantrax.Client, error) {
	cfg, err := config.Load(true, []time.Time{date}) // always dry-run for web GUI
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	ft, err := fantrax.NewClient(cfg.LeagueID, cfg.TeamID)
	if err != nil {
		return nil, nil, fmt.Errorf("fantrax client: %w", err)
	}
	return cfg, ft, nil
}

// buildOpposingPitchers creates the opposing pitcher map from probable starters and venues.
func buildOpposingPitchers(probableStarters map[string]string, venues map[string]string, pitcherHandedness map[string]string, pitcherFIP map[string]float64) map[string]projections.OpposingPitcher {
	opposingPitchers := make(map[string]projections.OpposingPitcher)
	for pitcherName, pitcherTeam := range probableStarters {
		pitcherHome := venues[pitcherTeam]
		if pitcherHome == "" {
			continue
		}
		for team, homeTeam := range venues {
			if team == pitcherTeam {
				continue
			}
			if homeTeam == pitcherHome {
				opp := projections.OpposingPitcher{
					Name: pitcherName,
					Team: pitcherTeam,
				}
				if h, ok := pitcherHandedness[pitcherName]; ok {
					opp.Throws = h
				}
				if f, ok := pitcherFIP[pitcherName]; ok {
					opp.FIP = f
				}
				opposingPitchers[team] = opp
				break
			}
		}
	}
	return opposingPitchers
}

// buildSlotNameMap creates a posID → display name map from both hitter and pitcher slots.
func buildSlotNameMap(hitterSlots, pitcherSlots []fantrax.Slot) map[string]string {
	m := make(map[string]string)
	for _, s := range hitterSlots {
		m[s.PosID] = s.PosName
	}
	for _, s := range pitcherSlots {
		m[s.PosID] = s.PosName
	}
	return m
}

// buildPtsMap creates a playerID → effective pts map for delta calculation.
func buildPtsMap(hitterResult optimizer.Result, pitcherResult optimizer.PitcherResult) map[string]float64 {
	m := make(map[string]float64)
	for _, sp := range hitterResult.Scored {
		if sp.HasGame {
			m[sp.Player.ID] = sp.ExpectedPts
		}
	}
	for _, sp := range pitcherResult.Scored {
		if !sp.HasGame {
			continue
		}
		isRP := !strings.Contains(sp.Player.PosShortNames, "SP")
		if sp.IsStarter || isRP {
			m[sp.Player.ID] = sp.ExpectedPts
		} else {
			m[sp.Player.ID] = sp.ExpectedPts * 0.10
		}
	}
	return m
}

// allTeamsPlaying returns a map marking all players' teams as playing.
func allTeamsPlaying(players []fantrax.Player) map[string]bool {
	m := make(map[string]bool)
	for _, p := range players {
		m[p.MLBTeam] = true
	}
	return m
}

// playerNameByID finds a player name from hitter or pitcher rosters.
func playerNameByID(hitters, pitchers []fantrax.Player, id string) string {
	for _, p := range hitters {
		if p.ID == id {
			return p.Name
		}
	}
	for _, p := range pitchers {
		if p.ID == id {
			return p.Name
		}
	}
	return id
}

// findPlayerID looks up a player's ID by name in a roster.
func findPlayerID(roster []fantrax.Player, name string) (string, bool) {
	norm := projections.NormalizeName(name)
	for _, p := range roster {
		if projections.NormalizeName(p.Name) == norm {
			return p.ID, true
		}
	}
	return "", false
}

// getRecentPitcherStat accesses the recent stats from a PitcherBlendedSource.
// This uses the exported recent field accessor if available.
func getRecentPitcherStat(src *projections.PitcherBlendedSource, playerID string) (fantrax.RecentStat, bool) {
	recent := src.RecentStats()
	if recent == nil {
		return fantrax.RecentStat{}, false
	}
	stat, ok := recent[playerID]
	return stat, ok
}
