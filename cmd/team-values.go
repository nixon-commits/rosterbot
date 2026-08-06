package cmd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/spf13/cobra"
)

const (
	teamValueLocalDir = ".teamvalue"
	teamValuePrefix   = "analysis/team-values/" // S3 prefix under STATE_BUCKET
)

var teamValuesDate string

var teamValuesCmd = &cobra.Command{
	Use:   "team-values",
	Short: "Append today's per-team aggregate HKB dynasty value to the Team Value Store",
	Long: `Computes each fantasy team's aggregate HKB dynasty value for today and appends
one date-partitioned NDJSON record to the Team Value Store (local .teamvalue/,
or the S3 analysis/team-values/ prefix when STATE_BUCKET is set).

Value is summed over each team's rostered players (joined to HKB by normalized
name) and broken out into hitter/pitcher x MLB/minors leaves. HKB serves only
current values, so the series accumulates forward: run this once per day (before
projection-site) and the dashboard's native Value tab (reading value.json)
grows a point per day.`,
	RunE: runTeamValues,
}

func init() {
	teamValuesCmd.Flags().StringVar(&teamValuesDate, "date", "",
		"partition date YYYY-MM-DD (default: today ET). Data is always current HKB+roster; --date only sets which partition it lands in.")
	rootCmd.AddCommand(teamValuesCmd)
}

func runTeamValues(cmd *cobra.Command, args []string) error {
	date := todayET()
	if teamValuesDate != "" {
		d, err := time.Parse("2006-01-02", teamValuesDate)
		if err != nil {
			return fmt.Errorf("bad --date: %w", err)
		}
		date = d
	}

	_, ft, err := initApp([]time.Time{date})
	if err != nil {
		return err
	}

	hkbPlayers, err := hkb.GetPlayers(cacheDir)
	if err != nil {
		return fmt.Errorf("get HKB players: %w", err)
	}
	pool, err := ft.GetFullPlayerPool()
	if err != nil {
		return fmt.Errorf("get player pool: %w", err)
	}
	// Team names + logos are denormalized into each row so the read+render path
	// (projection-site) needs no Fantrax call. Cosmetic — a failure degrades to
	// the pool's own FantasyTeamName rather than aborting the daily capture.
	_, teamNames, teamLogos, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		warn("team-values: standings fetch failed, using pool team names: %v", err)
		teamNames, teamLogos = nil, nil
	}

	rows := teamvalue.Aggregate(date, pool, hkbPlayers, teamNames, teamLogos)
	if len(rows) == 0 {
		return fmt.Errorf("team-values: no rostered players found in pool")
	}

	printTeamValueSummary(date, rows)

	if dryRun {
		fmt.Printf("team-values (dry-run): computed %d team rows for %s; not written\n", len(rows), date.Format("2006-01-02"))
		return nil
	}

	w, dest, err := teamValueWriter()
	if err != nil {
		return err
	}
	if err := w.WriteValues(date, rows); err != nil {
		return fmt.Errorf("write team values: %w", err)
	}
	fmt.Printf("Wrote %d team rows to %s (dt=%s)\n", len(rows), dest, date.Format("2006-01-02"))
	return nil
}

// teamValueWriter returns the S3-backed writer when STATE_BUCKET is set (Fargate),
// else a local-filesystem writer, mirroring how grade/projection-site choose.
func teamValueWriter() (teamvalue.Writer, string, error) {
	w, err := statestore.FromEnv().TeamValueWriter()
	if err != nil {
		return nil, "", fmt.Errorf("init team-value writer: %w", err)
	}
	dest := teamValueLocalDir
	if b := statestore.Bucket(); b != "" {
		dest = "s3://" + b + "/" + teamValuePrefix
	}
	return w, dest, nil
}

// printTeamValueSummary prints a total-descending table with a join-coverage
// line, so a dry-run or log shows at a glance who leads and whether the
// HKB↔Fantrax name join covered the rosters. Any team with a shortfall gets
// its unmatched player names printed on a follow-up line — the dashboard's
// Matched column can only show the ratio (it reads back rows from the store,
// where Unmatched never persists, see teamvalue.Row), so this stdout summary
// is the one place the actual names surface, for whoever reads the job log.
func printTeamValueSummary(date time.Time, rows []teamvalue.Row) {
	sorted := make([]teamvalue.Row, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TotalValue() > sorted[j].TotalValue() })

	fmt.Printf("Team HKB value — %s\n", date.Format("2006-01-02"))
	fmt.Printf("%-24s %8s %8s %8s   %s\n", "TEAM", "TOTAL", "MLB", "MINORS", "MATCH")
	var totRostered, totMatched int
	for _, r := range sorted {
		fmt.Printf("%-24s %8d %8d %8d   %d/%d\n",
			truncate(r.TeamName, 24), r.TotalValue(), r.MLBValue(), r.MinorsValue(), r.MatchedCount, r.RosteredCount)
		if len(r.Unmatched) > 0 {
			fmt.Printf("%-24s   unmatched: %s\n", "", strings.Join(r.Unmatched, ", "))
		}
		totRostered += r.RosteredCount
		totMatched += r.MatchedCount
	}
	fmt.Printf("league join coverage: %d/%d players matched to HKB\n", totMatched, totRostered)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
