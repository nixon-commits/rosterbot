package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nixon-commits/fantrax-optimizer/internal/config"
	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/optimizer"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
	"github.com/nixon-commits/fantrax-optimizer/internal/schedule"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print planned moves without applying them")
	dateStr := flag.String("date", "", "override date for schedule lookup (YYYY-MM-DD, default: today)")
	flag.Parse()

	date := time.Now()
	if *dateStr != "" {
		var err error
		date, err = time.Parse("2006-01-02", *dateStr)
		if err != nil {
			log.Fatalf("invalid --date: %v", err)
		}
	}

	cfg, err := config.Load(*dryRun, date)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("date=%s dry-run=%v", cfg.Date.Format("2006-01-02"), cfg.DryRun)

	// --- Fantrax client ---
	ft, err := fantrax.NewClient(cfg.LeagueID, cfg.TeamID)
	if err != nil {
		log.Fatalf("fantrax client: %v", err)
	}

	// --- Fetch roster, slots, scoring in parallel-ish ---
	roster, err := ft.GetHitterRoster()
	if err != nil {
		log.Fatalf("get roster: %v", err)
	}
	log.Printf("roster: %d hitters (%d active)", countActive(roster), len(roster))

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

	// --- MLB schedule ---
	schedClient := schedule.NewClient()
	playingToday, err := schedClient.TeamsPlayingOn(cfg.Date)
	if err != nil {
		log.Printf("WARNING: mlb schedule unavailable (%v) — assuming all teams play", err)
		playingToday = allTeamsPlaying(roster)
	}
	log.Printf("teams playing today: %d", len(playingToday))

	// --- Projections ---
	var projSrc projections.Source
	fgSrc, err := projections.NewFanGraphsSource()
	if err != nil {
		log.Printf("WARNING: fangraphs unavailable (%v) — using rolling stats only", err)
		projSrc = projections.NewRollingSource()
	} else {
		log.Printf("fangraphs projections loaded")
		rolling := projections.NewRollingSource()
		projSrc = projections.NewChainedSource(fgSrc, rolling)
	}

	// --- Optimize ---
	result := optimizer.OptimizeLineup(roster, playingToday, projSrc, scoring, slots)

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
	}

	playerName := make(map[string]string)
	for _, p := range roster {
		playerName[p.ID] = p.Name
	}
	slotName := make(map[string]string)
	for _, s := range slots {
		slotName[s.PosID] = s.PosName
	}

	for _, ps := range result.ToActivate {
		fmt.Printf("  ACTIVATE  %-25s → %s\n", playerName[ps.PlayerID], slotName[ps.PosID])
	}
	for _, id := range result.ToBench {
		fmt.Printf("  BENCH     %s\n", playerName[id])
	}

	if cfg.DryRun {
		fmt.Println("\n[DRY RUN] No changes applied.")
		os.Exit(0)
	}

	// --- Apply ---
	fmt.Println("\nApplying lineup...")
	if err := ft.ApplyLineup(result.ToActivate, result.ToBench); err != nil {
		log.Fatalf("apply lineup: %v", err)
	}
	fmt.Println("Lineup applied successfully.")
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
