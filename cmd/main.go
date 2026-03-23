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
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print planned moves without applying them")
	datesStr := flag.String("dates", "", "date(s) for schedule lookup: YYYY-MM-DD, YYYY-MM-DD:YYYY-MM-DD, or 'all' (default: today)")
	checkRoster := flag.Bool("check-roster", false, "check for roster slot mismatches (IL/minors)")
	flag.Parse()

	today := time.Now().Truncate(24 * time.Hour)
	dates, err := parseDates(*datesStr, today)
	if err != nil {
		log.Fatalf("invalid --dates: %v", err)
	}

	cfg, err := config.Load(*dryRun, dates)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("dates=%s dry-run=%v", formatDates(cfg.Dates), cfg.DryRun)

	// --- Fantrax client ---
	ft, err := fantrax.NewClient(cfg.LeagueID, cfg.TeamID)
	if err != nil {
		log.Fatalf("fantrax client: %v", err)
	}

	// --- Roster alerts (if requested) ---
	if *checkRoster {
		fullRoster, err := ft.GetFullHitterRoster()
		if err != nil {
			log.Fatalf("get full roster: %v", err)
		}
		alerts := roster.CheckRoster(fullRoster)
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

	playerName := make(map[string]string)
	for _, p := range hitterRoster {
		playerName[p.ID] = p.Name
	}
	slotName := make(map[string]string)
	for _, s := range slots {
		slotName[s.PosID] = s.PosName
	}

	// --- Per-date loop ---
	for _, date := range cfg.Dates {
		isToday := date.Equal(today)

		if multiDate {
			header := date.Format("2006-01-02")
			if isToday {
				header += " (today)"
			}
			fmt.Printf("\n=== %s ===\n", header)
		}

		// --- MLB schedule ---
		playingToday, err := schedClient.TeamsPlayingOn(date)
		if err != nil {
			log.Printf("WARNING: mlb schedule unavailable for %s (%v) — assuming all teams play", date.Format("2006-01-02"), err)
			playingToday = allTeamsPlaying(hitterRoster)
		}
		if !multiDate {
			log.Printf("teams playing today: %d", len(playingToday))
		}

		// --- Optimize ---
		result := optimizer.OptimizeLineup(hitterRoster, playingToday, projSrc, scoring, slots)

		// --- Print ranking ---
		fmt.Println("\n=== Hitter Ranking ===")
		fmt.Printf("%-25s %-6s %-8s %s\n", "Player", "Team", "Pts/G", "Game?")
		fmt.Println(repeatStr("-", 55))
		for _, sp := range result.Scored {
			game := "no"
			if sp.HasGame {
				game = "YES"
			}
			fmt.Printf("%-25s %-6s %-8.2f %s\n", sp.Player.Name, sp.Player.MLBTeam, sp.ExpectedPts, game)
		}

		// --- Print planned moves ---
		fmt.Println("\n=== Planned Lineup Changes ===")
		if len(result.ToActivate) == 0 && len(result.ToBench) == 0 {
			fmt.Println("No changes needed.")
			if !multiDate {
				os.Exit(0)
			}
			continue
		}

		for _, ps := range result.ToActivate {
			fmt.Printf("  ACTIVATE  %-25s → %s\n", playerName[ps.PlayerID], slotName[ps.PosID])
		}
		for _, id := range result.ToBench {
			fmt.Printf("  BENCH     %s\n", playerName[id])
		}

		if cfg.DryRun {
			fmt.Println("\n[DRY RUN] No changes applied.")
			continue
		}

		if multiDate && !isToday {
			fmt.Println("\n[DRY RUN] Future date — changes not applied.")
			continue
		}

		// --- Apply ---
		fmt.Println("\nApplying lineup...")
		if err := ft.ApplyLineup(result.ToActivate, result.ToBench); err != nil {
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
