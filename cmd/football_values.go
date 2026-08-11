package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
	"github.com/spf13/cobra"
)

const footballValueLocalDir = ".footballvalue"
const footballValuePrefix = "analysis/football-values/" // S3 prefix under STATE_BUCKET

var footballValuesDate string

var footballValuesCmd = &cobra.Command{
	Use:   "football-values",
	Short: "Append today's per-player dynasty value rows to the Dynasty Value Store",
	Long: `Computes every rostered player's + every team's owned future draft picks'
StatsGuy dynasty value for today and appends date-partitioned NDJSON rows to
the Dynasty Value Store (local .footballvalue/, or the S3
analysis/football-values/ prefix when STATE_BUCKET is set).

Unlike team-values, this store is per-player grain: one row per (date, team,
asset), all four StatsGuy formats carried on every row. Needs no Fantrax
credentials -- only SLEEPER_LEAGUE_ID.`,
	RunE: runFootballValues,
}

func init() {
	footballValuesCmd.Flags().StringVar(&footballValuesDate, "date", "",
		"partition date YYYY-MM-DD (default: today ET). Data is always current Sleeper+StatsGuy; --date only sets which partition it lands in.")
	rootCmd.AddCommand(footballValuesCmd)
}

func runFootballValues(cmd *cobra.Command, args []string) error {
	date := todayET()
	if footballValuesDate != "" {
		d, err := time.Parse("2006-01-02", footballValuesDate)
		if err != nil {
			return fmt.Errorf("bad --date: %w", err)
		}
		date = d
	}

	cfg, sc, err := initFootball()
	if err != nil {
		return err
	}

	league, err := sc.League(cfg.SleeperLeagueID)
	if err != nil {
		return fmt.Errorf("sleeper league: %w", err)
	}
	rosters, err := sc.Rosters(cfg.SleeperLeagueID)
	if err != nil {
		return fmt.Errorf("sleeper rosters: %w", err)
	}
	users, err := sc.Users(cfg.SleeperLeagueID)
	if err != nil {
		return fmt.Errorf("sleeper users: %w", err)
	}
	tradedPicks, err := sc.TradedPicks(cfg.SleeperLeagueID)
	if err != nil {
		return fmt.Errorf("sleeper traded picks: %w", err)
	}
	players, err := sc.PlayersNFL()
	if err != nil {
		return fmt.Errorf("sleeper players: %w", err)
	}

	statsguyCacheDir := ""
	if !noCache {
		statsguyCacheDir = cacheDir
	}
	bundle, err := statsguy.LoadBundle(statsguyCacheDir, cacheTTL(statsguy.CacheTTL))
	if err != nil {
		return fmt.Errorf("statsguy bundle: %w", err)
	}

	rows, coverage := dynasty.Aggregate(date, league, rosters, users, tradedPicks, players, bundle)
	if len(rows) == 0 {
		return fmt.Errorf("football-values: no valued rows produced")
	}

	printFootballValueSummary(date, rows, coverage, cfg.DynastyFormat)

	if dryRun {
		fmt.Printf("football-values (dry-run): computed %d rows for %s; not written\n", len(rows), date.Format("2006-01-02"))
		return nil
	}

	w, dest, err := footballValueWriter()
	if err != nil {
		return err
	}
	if err := w.WriteValues(date, rows); err != nil {
		return fmt.Errorf("write football values: %w", err)
	}
	fmt.Printf("Wrote %d rows to %s (dt=%s)\n", len(rows), dest, date.Format("2006-01-02"))
	return nil
}

// footballValueWriter returns the S3-backed writer when STATE_BUCKET is set
// (Fargate), else a local-filesystem writer, mirroring teamValueWriter.
func footballValueWriter() (dynasty.Writer, string, error) {
	w, err := statestore.FromEnv().FootballValueWriter()
	if err != nil {
		return nil, "", fmt.Errorf("init football-value writer: %w", err)
	}
	dest := footballValueLocalDir
	if b := statestore.Bucket(); b != "" {
		dest = "s3://" + b + "/" + footballValuePrefix
	}
	return w, dest, nil
}

// printFootballValueSummary prints a starters-value-descending table per
// team, with the full-roster total shown alongside but visually demoted --
// the gate pre-commitment from the coverage spike (bd memory
// dynasty-football-coverage-gate-2026-08-10): starters coverage is
// consistently near-complete, full-roster coverage is not, so the headline
// number must be the one the join can actually support.
func printFootballValueSummary(date time.Time, rows []dynasty.Row, coverage []dynasty.Coverage, format string) {
	type teamTotal struct {
		teamName             string
		starters, fullRoster int
		rostered, matched    int
		unmatched            []string
	}
	byTeam := make(map[string]*teamTotal)
	for _, r := range rows {
		t := byTeam[r.TeamID]
		if t == nil {
			t = &teamTotal{teamName: r.TeamName}
			byTeam[r.TeamID] = t
		}
		v := r.ValueFor(format)
		t.fullRoster += v
		if r.AssetType == "player" && r.IsStarter {
			t.starters += v
		}
	}
	for _, c := range coverage {
		t := byTeam[c.TeamID]
		if t == nil {
			t = &teamTotal{teamName: c.TeamName}
			byTeam[c.TeamID] = t
		}
		t.rostered = c.RosteredCount
		t.matched = c.MatchedCount
		t.unmatched = c.Unmatched
	}

	totals := make([]*teamTotal, 0, len(byTeam))
	for _, t := range byTeam {
		totals = append(totals, t)
	}
	sort.Slice(totals, func(i, j int) bool { return totals[i].starters > totals[j].starters })

	fmt.Printf("Dynasty value (%s) — %s\n", format, date.Format("2006-01-02"))
	fmt.Printf("%-24s %10s %12s   %s\n", "TEAM", "STARTERS", "FULL ROSTER", "MATCH")
	var totRostered, totMatched int
	for _, t := range totals {
		fmt.Printf("%-24s %10d %12d   %d/%d\n",
			truncate(t.teamName, 24), t.starters, t.fullRoster, t.matched, t.rostered)
		if len(t.unmatched) > 0 {
			fmt.Printf("%-24s   unmatched: %v\n", "", t.unmatched)
		}
		totRostered += t.rostered
		totMatched += t.matched
	}
	fmt.Printf("league join coverage: %d/%d players matched to StatsGuy\n", totMatched, totRostered)
}
