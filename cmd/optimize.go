package cmd

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/progress"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/roster"
	"github.com/nixon-commits/rosterbot/internal/schedule"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
)

const cacheDir = ".cache"

var (
	datesStr         string
	daysAhead        int
	checkRoster      bool
	matchupPeriod    bool
	projectionSystem string
	showBlend        bool

	// projDisplayName maps projection system flag values to display-friendly names.
	projDisplayName = map[string]string{
		"steamer":         "Steamer",
		"depthcharts":     "DepthCharts",
		"thebatx":         "TheBatX",
		"steamer-ros":     "Steamer RoS",
		"depthcharts-ros": "DepthCharts RoS",
		"thebatx-ros":     "TheBatX RoS",
	}
)

var optimizeCmd = &cobra.Command{
	Use:   "optimize",
	Short: "Optimize daily lineup for hitters and pitchers",
	RunE:  runOptimize,
}

func init() {
	optimizeCmd.Flags().StringVar(&datesStr, "dates", "", "date(s) for schedule lookup: YYYY-MM-DD, YYYY-MM-DD:YYYY-MM-DD, or 'all' (default: today)")
	optimizeCmd.Flags().IntVar(&daysAhead, "days", 0, "optimize for the next N days starting from today")
	optimizeCmd.Flags().BoolVar(&matchupPeriod, "matchup", false, "optimize for all remaining days in the current matchup period")
	optimizeCmd.Flags().BoolVar(&checkRoster, "check-roster", true, "check for roster slot mismatches (IL/minors)")
	optimizeCmd.Flags().StringVar(&projectionSystem, "projections", "depthcharts", "projection system: steamer, depthcharts, thebatx, steamer-ros, depthcharts-ros, thebatx-ros")
	optimizeCmd.Flags().BoolVar(&showBlend, "blend", false, "show hitter blend breakdown (projections vs recent)")
	rootCmd.AddCommand(optimizeCmd)
}

func cacheTTL(d time.Duration) time.Duration {
	if noCache {
		return 0
	}
	return d
}

func runOptimize(cmd *cobra.Command, args []string) error {
	if err := projections.SetProjectionSystem(projectionSystem); err != nil {
		return err
	}
	// Set up progress display.
	var prog *progress.Progress
	if verbose {
		prog = progress.NewVerbose()
	} else {
		interactive := term.IsTerminal(int(os.Stdout.Fd()))
		prog = progress.New(interactive, os.Stdout)
	}

	// Cache TTLs (0 when --no-cache is set).
	projTTL := cacheTTL(12 * time.Hour)
	staticTTL := cacheTTL(7 * 24 * time.Hour)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Parse dates early for non-"all" cases; "all" and "matchup" need the Fantrax client.
	flagCount := 0
	if daysAhead > 0 {
		flagCount++
	}
	if datesStr != "" {
		flagCount++
	}
	if matchupPeriod {
		flagCount++
	}
	if flagCount > 1 {
		return fmt.Errorf("--days, --dates, and --matchup are mutually exclusive")
	}
	var dates []time.Time
	needsSeasonLookup := datesStr == "all"
	needsMatchupLookup := matchupPeriod
	if daysAhead > 0 {
		for i := 0; i < daysAhead; i++ {
			dates = append(dates, today.AddDate(0, 0, i))
		}
	} else if !needsSeasonLookup && !needsMatchupLookup {
		var err error
		dates, err = parseDates(datesStr, today)
		if err != nil {
			return fmt.Errorf("invalid --dates: %w", err)
		}
	}

	cfg, ft, err := initApp(dates)
	if err != nil {
		return err
	}

	// Resolve "all" or "--matchup" now that the client is available.
	var seasonStart time.Time // used later for period calculation
	if needsSeasonLookup || needsMatchupLookup {
		start, end, err := ft.GetSeasonDateRange()
		if err != nil {
			return fmt.Errorf("get season date range: %w", err)
		}
		seasonStart = start

		if needsMatchupLookup {
			weekStart, weekEnd, err := ft.GetMatchupWeekBounds(today, seasonStart)
			if err != nil {
				return fmt.Errorf("get matchup week: %w", err)
			}
			if weekStart.IsZero() {
				return fmt.Errorf("no matchup week found for today")
			}
			// Start from today (skip past days in the matchup).
			mStart := weekStart
			if mStart.Before(today) {
				mStart = today
			}
			for d := mStart; !d.After(weekEnd); d = d.AddDate(0, 0, 1) {
				cfg.Dates = append(cfg.Dates, d)
			}
			prog.Logf("matchup period: %s to %s (%d days remaining)",
				weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"), len(cfg.Dates))
		} else {
			if start.Before(today) {
				start = today
			}
			for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
				cfg.Dates = append(cfg.Dates, d)
			}
			prog.Logf("season range: %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
		}
	}

	// --- Load projections early to determine system for header ---
	fgSrc, batLoadResult, err := projections.LoadBattingProjections(projectionSystem, cacheDir, projTTL)
	if err != nil {
		msg := fmt.Sprintf("batting projections unavailable: %v", err)
		sendOptimizeNotify(cfg.PushoverUserKey, cfg.PushoverAPIToken, msg)
		return fmt.Errorf("batting projections unavailable: %w", err)
	}
	fgPitSrc, pitLoadResult, err := projections.LoadPitcherProjections(projectionSystem, cacheDir, projTTL)
	if err != nil {
		msg := fmt.Sprintf("pitching projections unavailable: %v", err)
		sendOptimizeNotify(cfg.PushoverUserKey, cfg.PushoverAPIToken, msg)
		return fmt.Errorf("pitching projections unavailable: %w", err)
	}

	prog.Header(projDisplayName[batLoadResult.System], formatDates(cfg.Dates), cfg.DryRun)

	// --- Roster alerts (if requested) ---
	if checkRoster {
		fullRoster, counts, err := ft.GetFullHitterRoster()
		if err != nil {
			return fmt.Errorf("get full roster: %w", err)
		}
		counts.ILCapacity = cfg.ILSlots
		counts.MinorsCapacity = cfg.MinorsSlots
		alerts := roster.CheckRoster(fullRoster, counts)
		if len(alerts) > 0 {
			fmt.Println("\n=== Roster Alerts ===")
			for _, a := range alerts {
				label := alertLabel(a.Type)
				fmt.Printf("  ⚠ %-25s (%s)  %s → %s\n", a.Player.Name, a.Player.MLBTeam, label, a.Suggestion)
			}
			fmt.Println()
		}
	}

	// --- Fetch hitter roster, slots, scoring (shared across dates) ---
	prog.Start("Roster")
	hitterRoster, err := ft.GetHitterRoster()
	if err != nil {
		return fmt.Errorf("get hitter roster: %w", err)
	}
	prog.Logf("hitter roster: %d hitters (%d active)", len(hitterRoster), countActive(hitterRoster))

	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		return fmt.Errorf("get hitter slots: %w", err)
	}
	prog.Logf("hitter active slots: %d", len(hitterSlots))

	hitterScoring, err := ft.GetScoringWeights()
	if err != nil {
		return fmt.Errorf("get hitter scoring: %w", err)
	}
	prog.Logf("hitter scoring weights: %d categories", len(hitterScoring))

	// --- Fetch pitcher roster, slots, scoring (shared across dates) ---
	pitcherRoster, err := ft.GetPitcherRoster()
	if err != nil {
		return fmt.Errorf("get pitcher roster: %w", err)
	}
	prog.Logf("pitcher roster: %d pitchers (%d active)", len(pitcherRoster), countActive(pitcherRoster))

	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		return fmt.Errorf("get pitcher slots: %w", err)
	}
	prog.Logf("pitcher active slots: %d", len(pitcherSlots))

	pitcherScoring, err := ft.GetPitcherScoringWeights()
	if err != nil {
		return fmt.Errorf("get pitcher scoring: %w", err)
	}
	prog.Logf("pitcher scoring weights: %d categories", len(pitcherScoring))
	prog.Done("Roster", fmt.Sprintf("%d hitters (%d active) · %d pitchers (%d active)",
		len(hitterRoster), countActive(hitterRoster),
		len(pitcherRoster), countActive(pitcherRoster)))

	// --- Current period (shared by hitter + pitcher blending) ---
	currentPeriod, periodErr := ft.GetCurrentPeriod()
	if periodErr != nil {
		prog.Logf("WARNING: could not get current period (%v) — using %s only", periodErr, projDisplayName[projectionSystem])
	} else {
		prog.Logf("current period: %d", currentPeriod)
	}

	// --- Hitter projections (shared across dates) ---
	prog.Start("Projections")
	if batLoadResult.FellBack {
		prog.Logf("WARNING: %s RoS projections unavailable — using %s preseason", projDisplayName[projectionSystem], projDisplayName[projectionSystem])
	}
	if batLoadResult.FromCSV {
		prog.Logf("WARNING: API projections unavailable — using CSV file")
	}
	prog.Logf("fangraphs batting projections loaded (%s, %d players)", projDisplayName[batLoadResult.System], fgSrc.Len())
	if pitLoadResult.FellBack {
		prog.Logf("WARNING: %s RoS pitching projections unavailable — using %s preseason", projDisplayName[projectionSystem], projDisplayName[projectionSystem])
	}
	if pitLoadResult.FromCSV {
		prog.Logf("WARNING: API pitching projections unavailable — using CSV file")
	}
	prog.Logf("fangraphs pitching projections loaded (%s, %d players)", projDisplayName[pitLoadResult.System], fgPitSrc.Len())
	var recentHitterCount, recentPitcherCount int
	var hitterProjSrc projections.Source
	rolling := projections.NewRollingSource()
	baseSrc := projections.NewChainedSource(fgSrc, rolling)

	if periodErr != nil || currentPeriod <= 1 {
		if currentPeriod <= 1 {
			prog.Logf("season not started (period %d) — using %s only", currentPeriod, projDisplayName[projectionSystem])
		}
		hitterProjSrc = baseSrc
	} else {
		prog.Logf("fetching recent hitter stats (YTD through period %d)...", currentPeriod-1)
		recentStats, err := ft.GetRecentStats(currentPeriod, 0)
		if err != nil {
			prog.Logf("WARNING: recent hitter stats unavailable (%v) — using %s only", err, projDisplayName[projectionSystem])
			hitterProjSrc = baseSrc
		} else {
			recentHitterCount = len(recentStats)
			prog.Logf("recent hitter stats loaded: %d players with data", len(recentStats))
			nameToID := make(map[string]string)
			for _, p := range hitterRoster {
				nameToID[projections.NormalizeName(p.Name)] = p.ID
			}
			hitterProjSrc = projections.NewBlendedSource(baseSrc, recentStats, hitterScoring, nameToID, cfg.BlendMinGP)
		}
	}

	// Collect MLBAM IDs for handedness lookup.
	var hitterMLBAMIDs map[string]int
	if fgSrc != nil {
		hitterMLBAMIDs = fgSrc.MLBAMIDs()
	}

	// --- Pitcher projections (shared across dates) ---
	var pitcherProjSrc projections.PitcherSource
	pitRolling := projections.NewPitcherRollingSource()
	pitBaseSrc := projections.NewPitcherChainedSource(fgPitSrc, pitRolling)

	if periodErr != nil || currentPeriod <= 1 {
		pitcherProjSrc = pitBaseSrc
	} else {
		recentPitStats, err := ft.GetRecentPitcherStats(currentPeriod, 0)
		if err != nil {
			prog.Logf("WARNING: recent pitcher stats unavailable (%v) — using %s only", err, projDisplayName[projectionSystem])
			pitcherProjSrc = pitBaseSrc
		} else {
			recentPitcherCount = len(recentPitStats)
			prog.Logf("recent pitcher stats loaded: %d players with data", len(recentPitStats))
			pitNameToID := make(map[string]string)
			pitPlayerPos := make(map[string][]string)
			for _, p := range pitcherRoster {
				pitNameToID[projections.NormalizeName(p.Name)] = p.ID
				pitPlayerPos[p.ID] = p.Positions
			}
			pitcherProjSrc = projections.NewPitcherBlendedSource(pitBaseSrc, recentPitStats, pitcherScoring, pitNameToID, pitPlayerPos, cfg.BlendMinGP)
		}
	}
	prog.Done("Projections", "batting + pitching loaded")

	prog.Start("Recent stats")
	prog.Done("Recent stats", fmt.Sprintf("%d hitters · %d pitchers", recentHitterCount, recentPitcherCount))

	// Extract pitcher FIP for matchup adjustments.
	prog.Start("Pitcher info")
	var pitcherFIP map[string]float64
	var leagueAvgFIP float64
	var pitcherMLBAMIDs map[string]int
	if fgPitSrc != nil {
		pitcherFIP, leagueAvgFIP = fgPitSrc.PitcherInfo()
		pitcherMLBAMIDs = fgPitSrc.MLBAMIDs()
		prog.Logf("pitcher info loaded: %d FIP, league avg FIP=%.2f", len(pitcherFIP), leagueAvgFIP)
		prog.Done("Pitcher info", fmt.Sprintf("%d FIP entries · league avg %.2f", len(pitcherFIP), leagueAvgFIP))
	} else {
		prog.Done("Pitcher info", "skipped")
	}

	// Fetch handedness from MLB Stats API using MLBAM IDs from FanGraphs.
	var hitterBats map[string]string
	var pitcherHandedness map[string]string
	allMLBAMIDs := make(map[string]int)
	for k, v := range hitterMLBAMIDs {
		allMLBAMIDs[k] = v
	}
	for k, v := range pitcherMLBAMIDs {
		allMLBAMIDs[k] = v
	}
	prog.Start("Handedness")
	if len(allMLBAMIDs) > 0 {
		bats, throws, err := projections.FetchMLBHandednessCached(allMLBAMIDs, cacheDir, staticTTL)
		if err != nil {
			prog.Logf("WARNING: MLB handedness unavailable (%v) — matchup adjustments disabled", err)
			prog.Warn("Handedness", "unavailable — matchup adjustments disabled")
		} else {
			hitterBats = bats
			pitcherHandedness = throws
			prog.Logf("handedness loaded: %d hitter bats, %d pitcher throws", len(hitterBats), len(pitcherHandedness))
			prog.Done("Handedness", fmt.Sprintf("%d bats · %d throws", len(hitterBats), len(pitcherHandedness)))
		}
	} else {
		prog.Done("Handedness", "skipped — no MLBAM IDs")
	}

	multiDate := len(cfg.Dates) > 1
	schedClient := schedule.NewClient()

	// --- Fetch park factors from Statcast (shared across dates) ---
	prog.Start("Park factors")
	var parkFactors map[string]projections.ParkFactors
	pf, err := schedClient.FetchParkFactorsWithFallbackCached(cacheDir, staticTTL)
	if err != nil {
		prog.Logf("WARNING: statcast park factors unavailable (%v) — using neutral park", err)
		prog.Warn("Park factors", "unavailable — using neutral park")
	} else {
		parkFactors = pf
		prog.Logf("statcast park factors loaded: %d parks", len(parkFactors))
		prog.Done("Park factors", fmt.Sprintf("%d parks", len(parkFactors)))
	}

	// Get season start date for period calculation.
	// If we already fetched the season range for --dates all, reuse seasonStart from above.
	if !needsSeasonLookup {
		s, _, err := ft.GetSeasonDateRange()
		if err != nil {
			prog.Logf("WARNING: could not get season start (%v) — only today's lineup can be set", err)
		} else {
			seasonStart = s
		}
	}

	// Skip optimization if today is before the season start.
	if !seasonStart.IsZero() && today.Before(seasonStart) && !multiDate {
		prog.Logf("season starts %s — nothing to optimize yet", seasonStart.Format("2006-01-02"))
		fmt.Printf("\nSeason starts %s. No games to optimize for today.\n", seasonStart.Format("2006-01-02"))
		return nil
	}

	// --- GS Budget (weekly game-start limit awareness) ---
	var gsBudget *optimizer.GSBudget
	if cfg.GSCap > 0 {
		prog.Start("GS budget")
	}
	if cfg.GSCap > 0 && !seasonStart.IsZero() {
		weekStart, weekEnd, err := ft.GetMatchupWeekBounds(today, seasonStart)
		if err != nil {
			prog.Logf("WARNING: could not determine matchup week (%v) — GS limit disabled", err)
		} else if weekStart.IsZero() {
			prog.Logf("WARNING: no matchup week found for today — GS limit disabled")
		} else {
			prog.Logf("GS limit: %d per week (%s to %s)",
				cfg.GSCap,
				weekStart.Format("2006-01-02"),
				weekEnd.Format("2006-01-02"))

			// Count past GS: for each past day in the week, check probable starters
			// and count how many of our rostered SPs started.
			spNames := rosterSPNames(pitcherRoster)
			usedGS := 0
			for d := weekStart; d.Before(today); d = d.AddDate(0, 0, 1) {
				probs, err := schedClient.ProbableStarters(d)
				if err != nil || len(probs) == 0 {
					continue
				}
				for normName, team := range probs {
					if p, ours := spNames[normName]; ours && p.MLBTeam == team {
						usedGS++
					}
				}
			}

			// Build forecast for remaining days (today+1 through weekEnd).
			// Cap SP counts at the number of active P slots since bench SPs
			// don't accrue GS — only pitchers in active lineup slots do.
			numPSlots := len(pitcherSlots)
			var forecast []optimizer.DayForecast
			for d := today.AddDate(0, 0, 1); !d.After(weekEnd); d = d.AddDate(0, 0, 1) {
				playing, _ := schedClient.TeamsPlayingOn(d)
				probs, _ := schedClient.ProbableStarters(d)

				df := optimizer.DayForecast{Date: d}
				if len(probs) > 0 {
					// Confirmed probables available — count our roster SPs,
					// capped at active P slots (bench SPs don't consume GS).
					for normName, team := range probs {
						if p, ours := spNames[normName]; ours && p.MLBTeam == team {
							df.Confirmed++
						}
					}
					if df.Confirmed > numPSlots {
						df.Confirmed = numPSlots
					}
				} else {
					// No probables — estimate: roster SPs whose team plays / 5 (standard rotation),
					// capped at active P slots since only active-slot SPs consume GS.
					var spPlaying float64
					for _, p := range spNames {
						if playing[p.MLBTeam] {
							spPlaying++
						}
					}
					if spPlaying > float64(numPSlots) {
						spPlaying = float64(numPSlots)
					}
					df.Estimated = spPlaying / 5.0
				}
				forecast = append(forecast, df)
			}

			// Count today's locked active SP starters toward used GS.
			// If their game is in progress or final, their GS is consumed.
			lockedTeams, lockErr := schedClient.LockedTeams(today)
			if lockErr == nil {
				for _, p := range pitcherRoster {
					if p.Status != "Active" || p.InMinors || p.IsInjured {
						continue
					}
					if !lockedTeams[p.MLBTeam] {
						continue
					}
					if strings.Contains(p.PosShortNames, "SP") {
						usedGS++
					}
				}
			}

			gsBudget = &optimizer.GSBudget{
				Limit:    cfg.GSCap,
				Used:     usedGS,
				Today:    today,
				WeekEnd:  weekEnd,
				Forecast: forecast,
			}
			prog.Logf("GS budget: %d/%d used, %.1f projected future starts",
				usedGS, cfg.GSCap, gsBudget.FutureDemand())
		}
	}
	if cfg.GSCap > 0 {
		if gsBudget != nil {
			prog.Done("GS budget", fmt.Sprintf("%d/%d used · %.1f projected", gsBudget.Used, gsBudget.Limit, gsBudget.FutureDemand()))
		} else {
			prog.Warn("GS budget", "unavailable — limit disabled")
		}
	}

	// Build name/slot lookup maps for display.
	playerName := make(map[string]string)
	for _, p := range hitterRoster {
		playerName[p.ID] = p.Name
	}
	for _, p := range pitcherRoster {
		playerName[p.ID] = p.Name
	}
	slotName := make(map[string]string)
	for _, s := range hitterSlots {
		slotName[s.PosID] = s.PosName
	}
	for _, s := range pitcherSlots {
		slotName[s.PosID] = s.PosName
	}

	// --- Parallel fetch + optimize for all dates ---
	type dateResult struct {
		date             time.Time
		period           int
		isToday          bool
		hitterResult     optimizer.Result
		pitcherResult    optimizer.PitcherResult
		warnings         []string
		venues           map[string]string                       // team → home team (for park factor display)
		benchedToday     map[string]bool                         // normalized names confirmed out of real-life lineup
		hitterBreakdowns map[string]*projections.HitterBreakdown // playerID → breakdown
	}

	results := make([]dateResult, len(cfg.Dates))

	prog.Start("Optimize")
	var g errgroup.Group
	for i, date := range cfg.Dates {
		i, date := i, date
		g.Go(func() error {
			isToday := date.Equal(today)
			period := fantrax.PeriodForDate(seasonStart, date)

			var warnings []string

			// Fetch period-specific rosters.
			dateHitterRoster := hitterRoster
			datePitcherRoster := pitcherRoster
			if !isToday && period > 0 {
				if r, err := ft.GetHitterRosterForPeriod(period); err == nil {
					dateHitterRoster = r
				} else {
					warnings = append(warnings, fmt.Sprintf("could not fetch hitter roster for period %d (%v) — using current", period, err))
				}
				if r, err := ft.GetPitcherRosterForPeriod(period); err == nil {
					datePitcherRoster = r
				} else {
					warnings = append(warnings, fmt.Sprintf("could not fetch pitcher roster for period %d (%v) — using current", period, err))
				}
			}

			// MLB schedule + probable pitchers.
			playingToday, err := schedClient.TeamsPlayingOn(date)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("mlb schedule unavailable for %s (%v) — assuming all teams play", date.Format("2006-01-02"), err))
				allPlayers := append(dateHitterRoster, datePitcherRoster...)
				playingToday = allTeamsPlaying(allPlayers)
			}

			// Detect locked teams (game in progress or final) — only for today.
			if isToday {
				lockedTeams, err := schedClient.LockedTeams(date)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("locked teams unavailable (%v) — proceeding without lock detection", err))
				} else if len(lockedTeams) > 0 {
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
				warnings = append(warnings, fmt.Sprintf("probable pitchers unavailable for %s (%v) — SPs default to start", date.Format("2006-01-02"), err))
				probableStarters = map[string]string{} // empty = default to start
			}

			// Fetch game venues for park factor and matchup adjustments.
			var venues map[string]string
			v, err := schedClient.GameVenues(date)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("game venues unavailable for %s (%v)", date.Format("2006-01-02"), err))
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
				if b, err := schedClient.BenchedPlayers(date, rosterNames); err != nil {
					warnings = append(warnings, fmt.Sprintf("starting lineups unavailable (%v) — assuming all hitters play", err))
				} else if len(b) > 0 {
					benchedToday = b
				}
			}

			// Optimize hitters (with park factor adjustment if available).
			dateHitterSrc := hitterProjSrc
			if venues != nil && parkFactors != nil {
				dateHitterSrc = projections.NewParkAdjustedSource(hitterProjSrc, parkFactors, venues)
			}

			// Build opposing pitcher map for matchup adjustments.
			if len(probableStarters) > 0 && leagueAvgFIP > 0 && venues != nil {
				opposingPitchers := make(map[string]projections.OpposingPitcher)
				for pitcherName, pitcherTeam := range probableStarters {
					pitcherHome := venues[pitcherTeam]
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
				if len(opposingPitchers) > 0 {
					dateHitterSrc = projections.NewMatchupAdjustedSource(dateHitterSrc, opposingPitchers, hitterBats, leagueAvgFIP)
				}
			}
			hitterResult := optimizer.OptimizeLineup(dateHitterRoster, playingToday, dateHitterSrc, hitterScoring, hitterSlots, benchedToday)

			// Compute hitter blending breakdowns if --blend flag is set.
			var hitterBreakdowns map[string]*projections.HitterBreakdown
			if showBlend {
				if blended, ok := hitterProjSrc.(*projections.BlendedSource); ok {
					hitterBreakdowns = make(map[string]*projections.HitterBreakdown)
					for _, sp := range hitterResult.Scored {
						if bd := blended.GetHitterBreakdown(sp.Player.Name, sp.Player.MLBTeam, hitterScoring); bd != nil {
							hitterBreakdowns[sp.Player.ID] = bd
						}
					}
				}
			}

			// Optimize pitchers.
			// GS budget gate only applies to today — for future dates the budget
			// would need to be recomputed per-date, and the daily GHA run handles
			// each day as it arrives.
			dateBudget := gsBudget
			if !isToday {
				dateBudget = nil
			}
			pitcherResult := optimizer.OptimizePitcherLineup(datePitcherRoster, playingToday, probableStarters, pitcherProjSrc, pitcherScoring, pitcherSlots, dateBudget)

			results[i] = dateResult{
				date:             date,
				period:           period,
				isToday:          isToday,
				hitterResult:     hitterResult,
				pitcherResult:    pitcherResult,
				warnings:         warnings,
				venues:           venues,
				benchedToday:     benchedToday,
				hitterBreakdowns: hitterBreakdowns,
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("parallel optimize: %w", err)
	}
	prog.Done("Optimize", "done")
	prog.Finish()

	// --- Sequential print + apply ---
	for _, dr := range results {
		for _, w := range dr.warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}

		// --- Build side-by-side hitter/pitcher display ---
		const (
			colL = 43 // hitter column width (runes)
			colR = 48 // pitcher column width (runes)
		)

		// Date header
		dateLabel := dr.date.Format("Mon Jan 2")
		if dr.isToday {
			dateLabel += " (today)"
		}
		if multiDate {
			boxW := colL + 3 + colR
			fmt.Printf("\n  ╔%s╗\n", strings.Repeat("═", boxW))
			fmt.Printf("  ║  %-*s║\n", boxW-2, dateLabel)
			fmt.Printf("  ╚%s╝\n", strings.Repeat("═", boxW))
		}

		// --- Hitter lines ---
		var hLines []string
		var hGreen []bool // parallel: true = render line in green (minor leaguer)
		hLines = append(hLines, "Hitters "+strings.Repeat("─", colL-8))
		hGreen = append(hGreen, false)
		hLines = append(hLines, "  "+padRight("Player", 19)+" "+padRight("Team", 4)+" "+fmt.Sprintf("%6s", "Pts/G")+" "+padRight("Slot", 4)+" Game")
		hGreen = append(hGreen, false)
		hLines = append(hLines, strings.Repeat("─", colL))
		hGreen = append(hGreen, false)

		var hitterStartingPts float64
		var hActive, hBench []optimizer.ScoredPlayer
		for _, sp := range dr.hitterResult.Scored {
			if sp.Player.Status == "Active" {
				hActive = append(hActive, sp)
				if sp.HasGame {
					hitterStartingPts += sp.ExpectedPts
				}
			} else {
				hBench = append(hBench, sp)
			}
		}

		for _, sp := range hActive {
			slot := ""
			if name, ok := slotName[sp.Player.RosterPosition]; ok {
				slot = name
			}
			game := " "
			if sp.Player.Locked {
				game = "🔒"
			} else if dr.benchedToday[projections.NormalizeName(sp.Player.Name)] {
				game = "❌"
			} else if sp.HasGame {
				game = "✓"
			}
			line := padRight("▸", 1) + " " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
				padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) +
				" " + padRight(slot, 4) + " " + game
			hLines = append(hLines, line)
			hGreen = append(hGreen, sp.Player.InMinors)
		}
		if len(hBench) > 0 {
			hLines = append(hLines, strings.Repeat("·", colL))
			hGreen = append(hGreen, false)
			for _, sp := range hBench {
				game := " "
				if sp.Player.Locked {
					game = "🔒"
				} else if dr.benchedToday[projections.NormalizeName(sp.Player.Name)] {
					game = "❌"
				} else if sp.HasGame {
					game = "✓"
				}
				line := "  " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
					padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) +
					" " + padRight("", 4) + " " + game
				hLines = append(hLines, line)
				hGreen = append(hGreen, sp.Player.InMinors)
			}
		}

		// --- Pitcher lines ---
		var pLines []string
		var pGreen []bool // parallel: true = render line in green (minor leaguer)
		pLines = append(pLines, "Pitchers "+strings.Repeat("─", colR-9))
		pGreen = append(pGreen, false)
		pLines = append(pLines, "  "+padRight("Player", 19)+" "+padRight("Team", 4)+" "+fmt.Sprintf("%6s", "Pts/G")+" "+padRight("Slot", 4)+" "+padRight("Pos", 4)+" Game")
		pGreen = append(pGreen, false)
		pLines = append(pLines, strings.Repeat("─", colR))
		pGreen = append(pGreen, false)

		var pitcherStartingPts float64
		var pActive, pBench []optimizer.ScoredPitcher
		for _, sp := range dr.pitcherResult.Scored {
			if sp.Player.Status == "Active" {
				pActive = append(pActive, sp)
				isRP := !strings.Contains(sp.Player.PosShortNames, "SP")
				if sp.HasGame && (sp.IsStarter || isRP) {
					pitcherStartingPts += sp.ExpectedPts
				}
			} else {
				pBench = append(pBench, sp)
			}
		}
		for _, sp := range pActive {
			slot := ""
			if name, ok := slotName[sp.Player.RosterPosition]; ok {
				slot = name
			}
			role := sp.Player.PosShortNames
			if role == "" {
				role = "P"
			}
			if sp.IsStarter {
				role += "★"
			}
			game := " "
			if sp.Player.Locked {
				game = "🔒"
			} else if sp.HasGame {
				game = "✓"
			}
			line := padRight("▸", 1) + " " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
				padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) + " " +
				padRight(slot, 4) + " " + padRight(role, 4) + " " + game
			pLines = append(pLines, line)
			pGreen = append(pGreen, sp.Player.InMinors)
		}
		if len(pBench) > 0 {
			pLines = append(pLines, strings.Repeat("·", colR))
			pGreen = append(pGreen, false)
			for _, sp := range pBench {
				role := sp.Player.PosShortNames
				if role == "" {
					role = "P"
				}
				if sp.IsStarter {
					role += "★"
				}
				game := " "
				if sp.Player.Locked {
					game = "🔒"
				} else if sp.HasGame {
					game = "✓"
				}
				line := "  " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
					padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) + " " +
					padRight("", 4) + " " + padRight(role, 4) + " " + game
				pLines = append(pLines, line)
				pGreen = append(pGreen, sp.Player.InMinors)
			}
		}
		// Pad data sections to same height so totals align.
		for len(hLines) < len(pLines) {
			hLines = append(hLines, "")
			hGreen = append(hGreen, false)
		}
		for len(pLines) < len(hLines) {
			pLines = append(pLines, "")
			pGreen = append(pGreen, false)
		}

		// Append footer lines (separator + total) — now on the same row.
		hLines = append(hLines, strings.Repeat("─", colL))
		hGreen = append(hGreen, false)
		hLines = append(hLines, "  "+padRight("Total", 19)+" "+padRight("", 4)+" "+fmt.Sprintf("%6.2f", hitterStartingPts))
		hGreen = append(hGreen, false)

		pLines = append(pLines, strings.Repeat("─", colR))
		pGreen = append(pGreen, false)
		pLines = append(pLines, "  "+padRight("Total", 19)+" "+padRight("", 4)+" "+fmt.Sprintf("%6.2f", pitcherStartingPts))
		pGreen = append(pGreen, false)
		if gsBudget != nil {
			remaining := gsBudget.Remaining()
			hLines = append(hLines, "")
			hGreen = append(hGreen, false)
			pLines = append(pLines, fmt.Sprintf("GS: %d/%d used (%d rem, %.1f future)",
				gsBudget.Used, gsBudget.Limit, remaining, gsBudget.FutureDemand()))
			pGreen = append(pGreen, false)
		}

		// Print side by side.
		fmt.Println()
		for i := range hLines {
			left := padRight(hLines[i], colL)
			right := padRight(pLines[i], colR)
			if hGreen[i] {
				left = "\033[32m" + left + "\033[0m"
			}
			if pGreen[i] {
				right = "\033[32m" + right + "\033[0m"
			}
			fmt.Printf("  %s │ %s\n", left, right)
		}

		// Combined total.
		fmt.Printf("\n  %-26s %6.2f\n", "Combined Expected", hitterStartingPts+pitcherStartingPts)

		// --- Hitter blend breakdown ---
		if len(dr.hitterBreakdowns) > 0 {
			fmt.Println()
			projLabel := projDisplayName[projectionSystem]
			lw := len(projLabel)
			if lw < 6 {
				lw = 6
			}
			// Row width: "    %-19s  %*s  Recent  ProjWt  Blended  GP" = 4+19+2+lw+2+6+2+6+2+7+2+2 = lw+54
			rowWidth := lw + 54
			titlePrefix := "  Hitter Blend Breakdown "
			fmt.Printf("%s%s╮\n", titlePrefix, strings.Repeat("─", rowWidth-len(titlePrefix)+4))
			fmt.Printf("    %-19s  %*s  Recent  ProjWt  Blended  GP    │\n", "Player", lw, projLabel)
			fmt.Printf("  %s╯\n", strings.Repeat("─", rowWidth+2))

			// Sort by blended score descending.
			blendSorted := make([]optimizer.ScoredPlayer, 0, len(dr.hitterBreakdowns))
			for _, sp := range dr.hitterResult.Scored {
				if _, ok := dr.hitterBreakdowns[sp.Player.ID]; ok {
					blendSorted = append(blendSorted, sp)
				}
			}
			sort.Slice(blendSorted, func(i, j int) bool {
				bi := dr.hitterBreakdowns[blendSorted[i].Player.ID]
				bj := dr.hitterBreakdowns[blendSorted[j].Player.ID]
				return bi.BlendedPts > bj.BlendedPts
			})

			for _, sp := range blendSorted {
				bd := dr.hitterBreakdowns[sp.Player.ID]
				if bd.HasRecent {
					fmt.Printf("    %-19s  %*.2f  %6.2f  %5.0f%%  %7.2f  %2d\n",
						truncName(sp.Player.Name, 19),
						lw, bd.SteamerPts, bd.RecentFPG,
						bd.SteamerWt*100,
						bd.BlendedPts, bd.GamesPlayed)
				} else {
					fmt.Printf("    %-19s  %*.2f  %6s  %5s%%  %7.2f  %2s\n",
						truncName(sp.Player.Name, 19),
						lw, bd.SteamerPts, "—", "100", bd.BlendedPts, "—")
				}
			}
		}

		// --- Combine changes ---
		allActivate := append(dr.hitterResult.ToActivate, dr.pitcherResult.ToActivate...)
		allBench := append(dr.hitterResult.ToBench, dr.pitcherResult.ToBench...)

		// --- Print planned moves ---
		if len(allActivate) == 0 && len(allBench) == 0 {
			fmt.Println("\n  No changes needed.")
			continue
		}

		// Build effective-pts lookup for optimization delta.
		// Hitters contribute full ExpectedPts when they have a game.
		// Pitchers: RPs and confirmed starters contribute full pts;
		// non-starting SPs contribute 10% (the optimizer's discount).
		ptsMap := make(map[string]float64)
		for _, sp := range dr.hitterResult.Scored {
			if sp.HasGame {
				ptsMap[sp.Player.ID] = sp.ExpectedPts
			}
		}
		for _, sp := range dr.pitcherResult.Scored {
			if !sp.HasGame {
				continue
			}
			isRP := !strings.Contains(sp.Player.PosShortNames, "SP")
			if sp.IsStarter || isRP {
				ptsMap[sp.Player.ID] = sp.ExpectedPts
			} else {
				ptsMap[sp.Player.ID] = sp.ExpectedPts * 0.10
			}
		}
		var delta float64
		for _, ps := range allActivate {
			delta += ptsMap[ps.PlayerID]
		}
		for _, id := range allBench {
			delta -= ptsMap[id]
		}

		fmt.Printf("\n  Changes (%+.2f pts) %s\n", delta, strings.Repeat("─", 35))
		for _, ps := range allActivate {
			fmt.Printf("    ↑ %-22s → %-4s  %+6.2f\n", playerName[ps.PlayerID], slotName[ps.PosID], ptsMap[ps.PlayerID])
		}
		for _, id := range allBench {
			fmt.Printf("    ↓ %-22s → BN    %+6.2f\n", playerName[id], -ptsMap[id])
		}

		if cfg.DryRun {
			fmt.Println("\n[DRY RUN] No changes applied.")
			continue
		}

		// --- Resolve period for this date ---
		dateKey := dr.date.Format("2006-01-02")
		if dr.period == 0 && !dr.isToday {
			fmt.Printf("\n[SKIP] No scoring period found for %s — changes not applied.\n", dateKey)
			continue
		}

		// --- Apply combined lineup (sequential — Fantrax API is not concurrent-safe) ---
		fmt.Printf("\nApplying lineup for %s (period %d)...\n", dateKey, dr.period)
		if err := ft.ApplyLineup(dr.period, allActivate, allBench); err != nil {
			return fmt.Errorf("apply lineup: %w", err)
		}
		fmt.Println("Lineup applied successfully.")

		// Send Pushover notification summarizing the changes.
		nHitter := len(dr.hitterResult.ToActivate) + len(dr.hitterResult.ToBench)
		nPitcher := len(dr.pitcherResult.ToActivate) + len(dr.pitcherResult.ToBench)
		var parts []string
		if nHitter > 0 {
			parts = append(parts, fmt.Sprintf("%d hitter", nHitter))
		}
		if nPitcher > 0 {
			parts = append(parts, fmt.Sprintf("%d pitcher", nPitcher))
		}
		summary := fmt.Sprintf("%s: %s changes (%+.2f pts)",
			dr.date.Format("Mon Jan 2"), strings.Join(parts, " + "), delta)
		for _, ps := range allActivate {
			summary += fmt.Sprintf("\n  ↑ %s → %s", playerName[ps.PlayerID], slotName[ps.PosID])
		}
		for _, id := range allBench {
			summary += fmt.Sprintf("\n  ↓ %s → BN", playerName[id])
		}
		sendOptimizeNotify(cfg.PushoverUserKey, cfg.PushoverAPIToken, summary)
	}

	return nil
}

// sendOptimizeNotify sends a Pushover notification if credentials are configured.
func sendOptimizeNotify(userKey, apiToken, message string) {
	if userKey == "" || apiToken == "" {
		return
	}
	if err := notify.SendPushover(userKey, apiToken, "Fantrax Lineup", message); err != nil {
		log.Printf("WARNING: pushover notification failed: %v", err)
	}
}
