package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pmurley/go-fantrax/models"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/nixon-commits/rosterbot/internal/tradeboard"
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

	// Built before the dry-run gate, not after, so `--dry-run` exercises the
	// whole path bar the write. A producer whose output only ever materializes
	// in production is one that can only be tested in production.
	table := buildTradeValues(date, pool, hkbPlayers, rows, teamNames)
	fmt.Printf("Trade values table: %d players, %d picks, HKB coverage %d/%d\n",
		len(table.Players), len(table.Picks), table.Matched, table.Rostered)

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

	// The Trades tab's league values table. Written here because this command
	// already holds everything it needs — the 10.7 MB player pool, the HKB
	// rankings and the team names — and no other daily job does. Splitting it
	// into its own schedule would re-fetch the pool for no freshness gain:
	// HKB's own cache TTL is 8h, so a values table cannot be fresher than that
	// however often it is rebuilt.
	//
	// Soft-fail: the Team Value Store is NoBackfill and a missed day is
	// permanent (docs/adr/0002), whereas this table is a rewritable snapshot
	// the next run replaces. A trades hiccup must not take the irreplaceable
	// artifact down with it.
	if err := publishTradeValues(table); err != nil {
		warn("team-values: trade values table not written: %v", err)
	}
	return nil
}

// publishTradeValues stores the Trades tab's league values table.
func publishTradeValues(table tradeboard.ValuesTable) error {
	data, err := json.Marshal(table)
	if err != nil {
		return fmt.Errorf("marshal values table: %w", err)
	}
	store, err := statestore.FromEnv().TradeValuesStore()
	if err != nil {
		return fmt.Errorf("open trade values store: %w", err)
	}
	if err := store.Publish(lineupapi.TradeValuesKey, data); err != nil {
		return fmt.Errorf("publish values table: %w", err)
	}
	fmt.Printf("Wrote trade values table (%d players, %d picks)\n", len(table.Players), len(table.Picks))
	return nil
}

// buildTradeValues assembles the Trades tab's league values table.
func buildTradeValues(date time.Time, pool []models.PoolPlayer, hkbPlayers []hkb.Player, rows []teamvalue.Row, teamNames map[string]string) tradeboard.ValuesTable {
	// tradeboard deliberately does not import internal/fantrax (it would drag
	// chromedp in behind it), so the pool is mapped across here. IsPitcher is
	// teamvalue's own, not a reimplementation, so the two-way tiebreak stays
	// stated once.
	players := make([]tradeboard.PoolPlayer, 0, len(pool))
	for _, pp := range pool {
		players = append(players, tradeboard.PoolPlayer{
			Name:           pp.Name,
			Position:       positionDisplay(pp.PosShortNames),
			MLBTeam:        pp.MLBTeamShortName,
			FantasyTeamID:  pp.FantasyTeamID,
			MinorsEligible: pp.MinorsEligible,
			IsPitcher:      teamvalue.IsPitcher(pp.Positions),
		})
	}

	return tradeboard.BuildValuesTable(date, players, hkbPlayers, rows, teamNames)
}

// positionDisplay turns a pool entry's PosShortNames into plain text.
//
// PosShortNames is the right source despite the markup, and the two obvious
// alternatives are both wrong. MultiPositions is populated only for players
// eligible at more than one *named* position -- Juan Soto (OF) and Paul Skenes
// (SP) come back empty, which blanked the Pos column for 340 of 515 rostered
// players. Deriving from the Positions IDs is worse: everyone carries the UT
// flex slot ("014"), and Fantrax hides it except where it is a player's only
// hitting eligibility, so "SS,INF,UT" for Bobby Witt Jr. and "UT,P" for Ohtani
// both have to come out of the same list. PosShortNames already encodes that
// rule.
//
// The tags are stripped rather than tolerated because this string lands in a
// durable JSON artifact. Upstream documents the field as HTML ("<b>UT</b>,SP")
// and the roster endpoints do emit it; this league's pool currently does not,
// which is exactly the kind of thing that changes without notice. Nothing here
// guards against injection -- the value is Fantrax's own and the tab renders it
// through textContent -- it guards against "<b>OF</b>" showing up in a table
// cell the day the payload shifts.
func positionDisplay(short string) string {
	if !strings.ContainsRune(short, '<') {
		return short
	}
	var b strings.Builder
	depth := 0
	for _, r := range short {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
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

// truncate cuts s to at most n runes, appending an ellipsis if it was cut.
// Rune-aware, not byte-aware: a byte-slice cut (s[:n-1]) can land mid-codepoint
// on any multi-byte UTF-8 character (e.g. a curly apostrophe in a Sleeper
// display name), producing the U+FFFD replacement character instead of the
// intended glyph.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
