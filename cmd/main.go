package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nixon-commits/fantrax-optimizer/internal/config"
	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/optimizer"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
	"github.com/nixon-commits/fantrax-optimizer/internal/roster"
	"github.com/nixon-commits/fantrax-optimizer/internal/schedule"
	"golang.org/x/sync/errgroup"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print planned moves without applying them")
	datesStr := flag.String("dates", "", "date(s) for schedule lookup: YYYY-MM-DD, YYYY-MM-DD:YYYY-MM-DD, or 'all' (default: today)")
	checkRoster := flag.Bool("check-roster", true, "check for roster slot mismatches (IL/minors)")
	flag.Parse()

	today := time.Now().Truncate(24 * time.Hour)

	// Parse dates early for non-"all" cases; "all" needs the Fantrax client.
	var dates []time.Time
	needsSeasonLookup := *datesStr == "all"
	if !needsSeasonLookup {
		var err error
		dates, err = parseDates(*datesStr, today)
		if err != nil {
			log.Fatalf("invalid --dates: %v", err)
		}
	}

	cfg, err := config.Load(*dryRun, dates)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// --- Fantrax client ---
	ft, err := fantrax.NewClient(cfg.LeagueID, cfg.TeamID)
	if err != nil {
		log.Fatalf("fantrax client: %v", err)
	}

	// Resolve "all" now that the client is available.
	if needsSeasonLookup {
		start, end, err := ft.GetSeasonDateRange()
		if err != nil {
			log.Fatalf("get season date range: %v", err)
		}
		if start.Before(today) {
			start = today
		}
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			cfg.Dates = append(cfg.Dates, d)
		}
		log.Printf("season range: %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	log.Printf("dates=%s dry-run=%v", formatDates(cfg.Dates), cfg.DryRun)

	// --- Roster alerts (if requested) ---
	if *checkRoster {
		fullRoster, counts, err := ft.GetFullHitterRoster()
		if err != nil {
			log.Fatalf("get full roster: %v", err)
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
	hitterRoster, err := ft.GetHitterRoster()
	if err != nil {
		log.Fatalf("get hitter roster: %v", err)
	}
	log.Printf("hitter roster: %d hitters (%d active)", len(hitterRoster), countActive(hitterRoster))

	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		log.Fatalf("get hitter slots: %v", err)
	}
	log.Printf("hitter active slots: %d", len(hitterSlots))

	hitterScoring, err := ft.GetScoringWeights()
	if err != nil {
		log.Fatalf("get hitter scoring: %v", err)
	}
	log.Printf("hitter scoring weights: %d categories", len(hitterScoring))

	// --- Fetch pitcher roster, slots, scoring (shared across dates) ---
	pitcherRoster, err := ft.GetPitcherRoster()
	if err != nil {
		log.Fatalf("get pitcher roster: %v", err)
	}
	log.Printf("pitcher roster: %d pitchers (%d active)", len(pitcherRoster), countActive(pitcherRoster))

	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		log.Fatalf("get pitcher slots: %v", err)
	}
	log.Printf("pitcher active slots: %d", len(pitcherSlots))

	pitcherScoring, err := ft.GetPitcherScoringWeights()
	if err != nil {
		log.Fatalf("get pitcher scoring: %v", err)
	}
	log.Printf("pitcher scoring weights: %d categories", len(pitcherScoring))

	// --- Current period (shared by hitter + pitcher blending) ---
	currentPeriod, periodErr := ft.GetCurrentPeriod()
	if periodErr != nil {
		log.Printf("WARNING: could not get current period (%v) — using Steamer only", periodErr)
	} else {
		log.Printf("current period: %d", currentPeriod)
	}

	// --- Hitter projections (shared across dates) ---
	var hitterProjSrc projections.Source
	fgSrc, err := projections.NewFanGraphsSource()
	if err != nil {
		log.Printf("WARNING: fangraphs batting unavailable (%v) — using rolling stats only", err)
		hitterProjSrc = projections.NewRollingSource()
	} else {
		log.Printf("fangraphs batting projections loaded")
		rolling := projections.NewRollingSource()
		baseSrc := projections.NewChainedSource(fgSrc, rolling)

		if periodErr != nil || currentPeriod <= 1 {
			if currentPeriod <= 1 {
				log.Printf("season not started (period %d) — using Steamer only", currentPeriod)
			}
			hitterProjSrc = baseSrc
		} else {
			log.Printf("fetching last 10 hitter periods...")
			recentStats, err := ft.GetRecentStats(currentPeriod, 10)
			if err != nil {
				log.Printf("WARNING: recent hitter stats unavailable (%v) — using Steamer only", err)
				hitterProjSrc = baseSrc
			} else {
				log.Printf("recent hitter stats loaded: %d players with data", len(recentStats))
				nameToID := make(map[string]string)
				for _, p := range hitterRoster {
					nameToID[projections.NormalizeName(p.Name)] = p.ID
				}
				hitterProjSrc = projections.NewBlendedSource(baseSrc, recentStats, hitterScoring, nameToID)
			}
		}
	}

	// --- Pitcher projections (shared across dates) ---
	var pitcherProjSrc projections.PitcherSource
	fgPitSrc, err := projections.NewFanGraphsPitcherSource()
	if err != nil {
		log.Printf("WARNING: fangraphs pitching unavailable (%v) — using rolling stats only", err)
		pitcherProjSrc = projections.NewPitcherRollingSource()
	} else {
		log.Printf("fangraphs pitching projections loaded")
		pitRolling := projections.NewPitcherRollingSource()
		pitBaseSrc := projections.NewPitcherChainedSource(fgPitSrc, pitRolling)

		if periodErr != nil || currentPeriod <= 1 {
			pitcherProjSrc = pitBaseSrc
		} else {
			recentPitStats, err := ft.GetRecentPitcherStats(currentPeriod, 10)
			if err != nil {
				log.Printf("WARNING: recent pitcher stats unavailable (%v) — using Steamer only", err)
				pitcherProjSrc = pitBaseSrc
			} else {
				log.Printf("recent pitcher stats loaded: %d players with data", len(recentPitStats))
				pitNameToID := make(map[string]string)
				pitPlayerPos := make(map[string][]string)
				for _, p := range pitcherRoster {
					pitNameToID[projections.NormalizeName(p.Name)] = p.ID
					pitPlayerPos[p.ID] = p.Positions
				}
				pitcherProjSrc = projections.NewPitcherBlendedSource(pitBaseSrc, recentPitStats, pitcherScoring, pitNameToID, pitPlayerPos)
			}
		}
	}

	multiDate := len(cfg.Dates) > 1
	schedClient := schedule.NewClient()

	// Get season start date for period calculation.
	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		log.Printf("WARNING: could not get season start (%v) — only today's lineup can be set", err)
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
		date          time.Time
		period        int
		isToday       bool
		hitterResult  optimizer.Result
		pitcherResult optimizer.PitcherResult
		warnings      []string
	}

	results := make([]dateResult, len(cfg.Dates))

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

			probableStarters, err := schedClient.ProbableStarters(date)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("probable pitchers unavailable for %s (%v) — SPs default to start", date.Format("2006-01-02"), err))
				probableStarters = map[string]string{} // empty = default to start
			}

			// Optimize hitters.
			hitterResult := optimizer.OptimizeLineup(dateHitterRoster, playingToday, hitterProjSrc, hitterScoring, hitterSlots)

			// Optimize pitchers.
			pitcherResult := optimizer.OptimizePitcherLineup(datePitcherRoster, playingToday, probableStarters, pitcherProjSrc, pitcherScoring, pitcherSlots)

			results[i] = dateResult{
				date:          date,
				period:        period,
				isToday:       isToday,
				hitterResult:  hitterResult,
				pitcherResult: pitcherResult,
				warnings:      warnings,
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		log.Fatalf("parallel optimize: %v", err)
	}

	// --- Sequential print + apply ---
	for _, dr := range results {
		for _, w := range dr.warnings {
			log.Printf("WARNING: %s", w)
		}

		if multiDate {
			header := dr.date.Format("2006-01-02")
			if dr.isToday {
				header += " (today)"
			}
			fmt.Printf("\n=== %s ===\n", header)
		}

		// --- Print hitter ranking ---
		fmt.Println("\n=== Hitter Ranking ===")
		fmt.Printf("%-25s %-6s %-8s %s\n", "Player", "Team", "Pts/G", "Game?")
		fmt.Println(repeatStr("-", 55))
		for _, sp := range dr.hitterResult.Scored {
			game := "no"
			if sp.HasGame {
				game = "YES"
			}
			fmt.Printf("%-25s %-6s %-8.2f %s\n", sp.Player.Name, sp.Player.MLBTeam, sp.ExpectedPts, game)
		}

		// --- Print pitcher ranking ---
		fmt.Println("\n=== Pitcher Ranking ===")
		fmt.Printf("%-25s %-6s %-8s %-6s %s\n", "Player", "Team", "Pts/G", "Role", "Game?")
		fmt.Println(repeatStr("-", 65))
		for _, sp := range dr.pitcherResult.Scored {
			game := "no"
			if sp.HasGame {
				game = "YES"
			}
			role := "RP"
			if sp.IsStarter {
				role = "SP"
			}
			fmt.Printf("%-25s %-6s %-8.2f %-6s %s\n", sp.Player.Name, sp.Player.MLBTeam, sp.ExpectedPts, role, game)
		}

		// --- Combine changes ---
		allActivate := append(dr.hitterResult.ToActivate, dr.pitcherResult.ToActivate...)
		allBench := append(dr.hitterResult.ToBench, dr.pitcherResult.ToBench...)

		// --- Print planned moves ---
		fmt.Println("\n=== Planned Lineup Changes ===")
		if len(allActivate) == 0 && len(allBench) == 0 {
			fmt.Println("No changes needed.")
			if !multiDate {
				os.Exit(0)
			}
			continue
		}

		for _, ps := range allActivate {
			fmt.Printf("  ACTIVATE  %-25s → %s\n", playerName[ps.PlayerID], slotName[ps.PosID])
		}
		for _, id := range allBench {
			fmt.Printf("  BENCH     %s\n", playerName[id])
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
			log.Fatalf("apply lineup: %v", err)
		}
		fmt.Println("Lineup applied successfully.")
	}
}

// parseDates parses the --dates flag value into a slice of dates.
func parseDates(s string, today time.Time) ([]time.Time, error) {
	if s == "" {
		return []time.Time{today}, nil
	}
	if parts := strings.SplitN(s, ":", 2); len(parts) == 2 {
		start, err := time.Parse("2006-01-02", parts[0])
		if err != nil {
			return nil, fmt.Errorf("start date: %w", err)
		}
		end, err := time.Parse("2006-01-02", parts[1])
		if err != nil {
			return nil, fmt.Errorf("end date: %w", err)
		}
		if end.Before(start) {
			return nil, fmt.Errorf("end date %s is before start date %s", parts[1], parts[0])
		}
		var dates []time.Time
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			dates = append(dates, d)
		}
		return dates, nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, err
	}
	return []time.Time{d}, nil
}

func formatDates(dates []time.Time) string {
	if len(dates) == 1 {
		return dates[0].Format("2006-01-02")
	}
	return fmt.Sprintf("%s..%s (%d days)",
		dates[0].Format("2006-01-02"),
		dates[len(dates)-1].Format("2006-01-02"),
		len(dates))
}

func alertLabel(t roster.AlertType) string {
	switch t {
	case roster.HealthyInIL:
		return "Healthy but in IL slot"
	case roster.CalledUpInMinors:
		return "Called up but in Minors slot"
	case roster.InjuredInActive:
		return "Injured but in Active/Reserve slot"
	case roster.MinorInActive:
		return "Minor leaguer but in Active/Reserve slot"
	default:
		return string(t)
	}
}

func countActive(roster []fantrax.Player) int {
	n := 0
	for _, p := range roster {
		if p.Status == "Active" {
			n++
		}
	}
	return n
}

// allTeamsPlaying returns a map treating all roster players as having games —
// used as a safe fallback when the MLB schedule API is unavailable.
func allTeamsPlaying(roster []fantrax.Player) map[string]bool {
	m := make(map[string]bool)
	for _, p := range roster {
		m[p.MLBTeam] = true
	}
	return m
}

func repeatStr(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
