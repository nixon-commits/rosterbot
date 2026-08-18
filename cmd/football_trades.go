package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
	"github.com/spf13/cobra"
)

var footballTradesCmd = &cobra.Command{
	Use:   "football-trades",
	Short: "Poll Sleeper for newly completed trades and push a graded alert",
	Long: `Polls Sleeper league transactions (week 1 through the current week -- Sleeper's
round-bucketing for offseason/preseason trades is unconfirmed, and re-polling
old weeks is nearly free), grades every completed trade not yet alerted by a
local sum over the StatsGuy bundle (no package decay -- see
internal/dynasty.GradeTrade), and pushes a Pushover alert.

Idempotent: one dedup marker per Sleeper transaction_id, written only after a
confirmed send (check -> send -> mark). Needs no Fantrax credentials -- only
SLEEPER_LEAGUE_ID.`,
	RunE: runFootballTrades,
}

func init() {
	rootCmd.AddCommand(footballTradesCmd)
}

func runFootballTrades(cmd *cobra.Command, args []string) error {
	cfg, sc, err := initFootball()
	if err != nil {
		return err
	}
	ctx := context.Background()

	state, err := sc.State()
	if err != nil {
		return fmt.Errorf("sleeper state: %w", err)
	}
	rosters, err := sc.Rosters(cfg.SleeperLeagueID)
	if err != nil {
		return fmt.Errorf("sleeper rosters: %w", err)
	}
	users, err := sc.Users(cfg.SleeperLeagueID)
	if err != nil {
		return fmt.Errorf("sleeper users: %w", err)
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

	names := dynasty.TeamNames(rosters, users)

	var trades []sleeper.Transaction
	for _, week := range pollWeeks(state) {
		txns, err := sc.Transactions(cfg.SleeperLeagueID, week)
		if err != nil {
			return fmt.Errorf("sleeper transactions (week %d): %w", week, err)
		}
		for _, t := range txns {
			if t.Type == "trade" && t.Status == "complete" {
				trades = append(trades, t)
			}
		}
	}

	markers, err := statestore.FromEnv().FootballTradeMarkers()
	if err != nil {
		return fmt.Errorf("init football-trade markers: %w", err)
	}

	graded, alerted, skipped := 0, 0, 0
	for _, txn := range trades {
		// check: already alerted for this transaction_id?
		if _, found, err := markers.Get(ctx, txn.TransactionID); err != nil {
			warn("football-trades: marker read %s: %v (treating as unsent)", txn.TransactionID, err)
		} else if found {
			skipped++
			continue
		}

		sides := dynasty.BuildTradeSides(txn, players, bundle, names, cfg.DynastyFormat)
		verdict := dynasty.GradeTrade(sides)
		graded++

		title, body := formatTradeAlert(txn, sides, verdict)
		fmt.Println(title)
		fmt.Println(body)

		if dryRun {
			continue // do not send or mark in a dry-run
		}

		if err := sendFootballTradeAlert(cfg, title, body); err != nil {
			warn("football-trades: send failed for %s: %v", txn.TransactionID, err)
			continue // do not mark: a failed send must retry, never go silent
		}
		alerted++

		// mark: check -> send -> mark, never claim-then-send (rosterbot-chs).
		// A marker-write failure here degrades to a duplicate alert on the
		// next poll, never to silence.
		if err := markers.Publish(txn.TransactionID, []byte(verdictSummary(verdict))); err != nil {
			warn("football-trades: marker write %s: %v (a repeat poll will alert again)", txn.TransactionID, err)
		}
	}

	fmt.Printf("football-trades: %d trades found, %d already alerted, %d graded, %d sent\n",
		len(trades), skipped, graded, alerted)
	return nil
}

// pollWeeks returns the Sleeper "round" values to poll for new transactions:
// week 1 through the current week, inclusive -- not just the current week.
// Sleeper's transaction round-bucketing for offseason/preseason trades was an
// open question at design time (state.week was 1 / season_type "pre"), and
// re-polling old weeks is nearly free: the dedup marker keys on
// transaction_id regardless of which week surfaces the record, and each
// week's transactions payload is small.
func pollWeeks(state *sleeper.NFLState) []int {
	week := state.Week
	if week < 1 {
		week = 1
	}
	weeks := make([]int, 0, week)
	for w := 1; w <= week; w++ {
		weeks = append(weeks, w)
	}
	return weeks
}

func formatTradeAlert(txn sleeper.Transaction, sides []dynasty.TradeSide, v dynasty.TradeVerdict) (title, body string) {
	var parts []string
	for _, s := range sides {
		var assets []string
		for _, a := range s.Assets {
			assets = append(assets, a.Name)
		}
		parts = append(parts, fmt.Sprintf("%s gets %s", s.TeamName, strings.Join(assets, ", ")))
	}
	body = strings.Join(parts, " | ")

	switch v.Status {
	case dynasty.TradeFavors:
		title = fmt.Sprintf("Trade: favors %s (+%.0f%%)", v.FavoredTeamName, v.Pct)
	default:
		title = "Trade: too many unpriced assets to grade"
	}
	// formatPushover-style truncation to fit Pushover's 1024-char message limit.
	const maxLen = 1024
	if len(body) > maxLen {
		body = body[:maxLen-1] + "…"
	}
	return title, body
}

func verdictSummary(v dynasty.TradeVerdict) string {
	if v.Status == dynasty.TradeFavors {
		return fmt.Sprintf("favors %s (+%.0f%%)", v.FavoredTeamName, v.Pct)
	}
	return string(v.Status)
}

// sendFootballTradeAlert sends to FootballPushoverUserKey (a personal
// notification, matching internal/transactions.go's baseball "Trade Alert" --
// FootballPushoverGroupKey exists for a future league-wide broadcast use, the
// same role PushoverGroupKey plays for gs-check's league-wide violation
// alert, but is not this command's target). The API token itself is not
// football-specific -- it is the one Pushover app token, read the same way
// initShared reads it for the cache-notify alert.
func sendFootballTradeAlert(cfg *FootballConfig, title, body string) error {
	apiToken := os.Getenv("PUSHOVER_API_TOKEN")
	if cfg.FootballPushoverUserKey == "" || apiToken == "" {
		return nil // no creds configured: print-only, matching other commands' soft-skip
	}
	return notify.SendPushover(cfg.FootballPushoverUserKey, apiToken, title, body)
}
