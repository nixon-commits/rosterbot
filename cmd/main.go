package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
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
		// Start from today (past dates are irrelevant for lineup setting).
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

	// --- Fetch roster, slots, scoring (shared across dates) ---
	hitterRoster, err := ft.GetHitterRoster()
	if err != nil {
		log.Fatalf("get roster: %v", err)
	}
	log.Printf("roster: %d hitters (%d active)", countActive(hitterRoster), len(hitterRoster))

	slots, err := ft.GetActiveSlots()
	if err != nil {
		log.Fatalf("get slots: %v", err)
	}
	log.Printf("active slots: %d", len(slots))

	scoring, err := ft.GetScoringWeights()
	if err != nil {
		log.Fatalf("get scoring: %v", err)
	}
	log.Printf("scoring weights: %d categories", len(scoring))

	// --- Projections (shared across dates) ---
	var projSrc projections.Source
	fgSrc, err := projections.NewFanGraphsSource()
	if err != nil {
		log.Printf("WARNING: fangraphs unavailable (%v) — using rolling stats only", err)
		projSrc = projections.NewRollingSource()
	} else {
		log.Printf("fangraphs projections loaded")
		rolling := projections.NewRollingSource()
		baseSrc := projections.NewChainedSource(fgSrc, rolling)

		currentPeriod, err := ft.GetCurrentPeriod()
		if err != nil {
			log.Printf("WARNING: could not get current period (%v) — using Steamer only", err)
			projSrc = baseSrc
		} else if currentPeriod <= 1 {
			log.Printf("season not started (period %d) — using Steamer only", currentPeriod)
			projSrc = baseSrc
		} else {
			log.Printf("current period: %d, fetching last 10 periods...", currentPeriod)
			recentStats, err := ft.GetRecentStats(currentPeriod, 10)
			if err != nil {
				log.Printf("WARNING: recent stats unavailable (%v) — using Steamer only", err)
				projSrc = baseSrc
			} else {
				log.Printf("recent stats loaded: %d players with data", len(recentStats))
				nameToID := make(map[string]string)
				for _, p := range hitterRoster {
					nameToID[projections.NormalizeName(p.Name)] = p.ID
				}
				projSrc = projections.NewBlendedSource(baseSrc, recentStats, scoring, nameToID)
			}
		}
	}

	multiDate := len(cfg.Dates) > 1
	schedClient := schedule.NewClient()

	// Get season start date for period calculation (period 1 = season start day).
	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		log.Printf("WARNING: could not get season start (%v) — only today's lineup can be set", err)
	}

	playerName := make(map[string]string)
	for _, p := range hitterRoster {
		playerName[p.ID] = p.Name
	}
	slotName := make(map[string]string)
	for _, s := range slots {
		slotName[s.PosID] = s.PosName
	}

	// --- Parallel fetch + optimize for all dates ---
	type dateResult struct {
		date     time.Time
		period   int
		isToday  bool
		result   optimizer.Result
		warnings []string
	}

	results := make([]dateResult, len(cfg.Dates))
	var mu sync.Mutex // protects log output during parallel fetch

	var g errgroup.Group
	for i, date := range cfg.Dates {
		i, date := i, date
		g.Go(func() error {
			isToday := date.Equal(today)
			period := fantrax.PeriodForDate(seasonStart, date)

			var warnings []string

			// Fetch roster for this period so we see what's already been set.
			dateRoster := hitterRoster
			if !isToday && period > 0 {
				if r, err := ft.GetHitterRosterForPeriod(period); err == nil {
					dateRoster = r
				} else {
					mu.Lock()
					warnings = append(warnings, fmt.Sprintf("could not fetch roster for period %d (%v) — using current roster", period, err))
					mu.Unlock()
				}
			}

			// MLB schedule.
			playingToday, err := schedClient.TeamsPlayingOn(date)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("mlb schedule unavailable for %s (%v) — assuming all teams play", date.Format("2006-01-02"), err))
				playingToday = allTeamsPlaying(dateRoster)
			}

			// Optimize.
			result := optimizer.OptimizeLineup(dateRoster, playingToday, projSrc, scoring, slots)

			results[i] = dateResult{
				date:     date,
				period:   period,
				isToday:  isToday,
				result:   result,
				warnings: warnings,
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		log.Fatalf("parallel optimize: %v", err)
	}

	if !multiDate && len(results) == 1 {
		log.Printf("teams playing today: %d", len(results[0].result.Scored))
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

		// --- Print ranking ---
		fmt.Println("\n=== Hitter Ranking ===")
		fmt.Printf("%-25s %-6s %-8s %s\n", "Player", "Team", "Pts/G", "Game?")
		fmt.Println(repeatStr("-", 55))
		for _, sp := range dr.result.Scored {
			game := "no"
			if sp.HasGame {
				game = "YES"
			}
			fmt.Printf("%-25s %-6s %-8.2f %s\n", sp.Player.Name, sp.Player.MLBTeam, sp.ExpectedPts, game)
		}

		// --- Print planned moves ---
		fmt.Println("\n=== Planned Lineup Changes ===")
		if len(dr.result.ToActivate) == 0 && len(dr.result.ToBench) == 0 {
			fmt.Println("No changes needed.")
			if !multiDate {
				os.Exit(0)
			}
			continue
		}

		for _, ps := range dr.result.ToActivate {
			fmt.Printf("  ACTIVATE  %-25s → %s\n", playerName[ps.PlayerID], slotName[ps.PosID])
		}
		for _, id := range dr.result.ToBench {
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

		// --- Apply (sequential — Fantrax API is not concurrent-safe) ---
		fmt.Printf("\nApplying lineup for %s (period %d)...\n", dateKey, dr.period)
		if err := ft.ApplyLineup(dr.period, dr.result.ToActivate, dr.result.ToBench); err != nil {
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
	if s == "all" {
		dates := make([]time.Time, 14)
		for i := range dates {
			dates[i] = today.AddDate(0, 0, i)
		}
		return dates, nil
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
