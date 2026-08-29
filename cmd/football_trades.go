package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/pushover"
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
SLEEPER_LEAGUE_ID.

Every alerted trade is also appended to the durable Football Trade Log
(football/trades/log/, or .football/trades/log/ locally), which is what the
dashboard's Football tab reads. The log stores each asset's value AS PRICED AT
GRADE TIME: StatsGuy publishes no history, so re-deriving an old trade would
price it against today and present that as what it was worth then.`,
	RunE: runFootballTrades,
}

var footballTradesRelog bool

func init() {
	footballTradesCmd.Flags().BoolVar(&footballTradesRelog, "relog", false,
		"one-shot backfill: rebuild durable log rows for ALREADY-ALERTED trades at TODAY's prices, flagged regraded. Sends nothing and writes no markers.")
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

	bundle, err := statsguy.LoadBundle(cacheDir, cacheTTL(statsguy.CacheTTL))
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

	// Soft: a marker store we cannot build disables dedup, not the alert --
	// same policy as optimize.go's IL-start/GS-floor markers. EXCEPTION:
	// --relog reads the marker store AS ITS DATA SOURCE (the marker body is
	// the only surviving grade-time fact for a pre-log trade), so a relog
	// with no store to read from has nothing to backfill and must hard-fail
	// rather than silently rebuild zero rows.
	markers, err := statestore.FromEnv().FootballTradeMarkers()
	if err != nil {
		if footballTradesRelog {
			return fmt.Errorf("init football-trade markers: %w (--relog reads the marker store as its data source and cannot proceed without one)", err)
		}
		warn("football-trades: init markers: %v (alert will repeat until resolved)", err)
		markers = nil
	}

	now := time.Now().UTC()
	if footballTradesRelog {
		return relogFootballTrades(ctx, cfg, markers, now, trades, players, bundle, names)
	}

	res := gradeAndAlertTrades(ctx, tradeRunInputs{
		markers: markers,
		now:     now,
		trades:  trades,
		players: players,
		bundle:  bundle,
		names:   names,
		format:  cfg.DynastyFormat,
		dryRun:  dryRun,
		send:    func(title, body string) error { return sendFootballTradeAlert(ctx, title, body) },
		out:     os.Stdout,
	})

	// Soft-fail, and deliberately WITHOUT the `continue` the send path inside
	// the loop uses: this runs AFTER every send, on the rows that loop returned,
	// precisely so a log failure cannot reach back and suppress an alert that
	// already went out. The durable log is additive reporting; the Pushover
	// alert is the job. Same precedent as cmd/grade.go's lineup gaps never
	// taking down the irreplaceable grades.
	if len(res.LogRows) > 0 {
		if added, err := writeFootballTradeLog(now, res.LogRows); err != nil {
			warn("football-trades: trade log not written: %v (the alerts still went out, and the markers are set, so the next poll will NOT retry these -- recover with `football-trades --relog`)", err)
		} else {
			fmt.Printf("football-trades: logged %d graded trade(s) to dt=%s\n", added, now.Format("2006-01-02"))
		}
	}

	fmt.Printf("football-trades: %d trades found, %d already alerted, %d graded, %d sent\n",
		len(trades), res.Skipped, res.Graded, res.Alerted)
	return nil
}

// tradeRunInputs is everything gradeAndAlertTrades needs, with the two side
// effects it cannot own -- sending, and printing -- injected.
//
// The seam exists because runFootballTrades calls initFootball() and
// statestore.FromEnv() internally and so cannot be driven from a test at all.
// Extracting the decision loop is what makes "the alert still goes out when the
// log write fails" an assertion rather than a claim.
type tradeRunInputs struct {
	markers lineupapi.BlobStore
	now     time.Time
	trades  []sleeper.Transaction
	players map[string]sleeper.Player
	bundle  *statsguy.Bundle
	names   map[int]string
	format  string
	dryRun  bool
	send    func(title, body string) error
	out     io.Writer
}

// tradeRunResult is what one poll decided: the rows to log, and the counts.
type tradeRunResult struct {
	LogRows                  []dynasty.TradeLogRow
	Graded, Alerted, Skipped int
}

// gradeAndAlertTrades runs the check -> send -> mark loop and returns the rows
// worth logging. It never writes the log itself: the caller does that after
// every send has been attempted, which is what keeps a log failure off the
// alerting path.
func gradeAndAlertTrades(ctx context.Context, in tradeRunInputs) tradeRunResult {
	var res tradeRunResult

	// One Marker over the whole poll. in.markers may be nil (the soft-degrade
	// path in runFootballTrades) -- alertmarker.New(nil, ...) is a working
	// configuration: no dedup, alert every run, never silence. Its logf
	// delegates to this package's warn() with the same "football-trades: "
	// prefix every other line here carries.
	m := alertmarker.New(in.markers, alertmarker.WithLogf(func(format string, args ...any) {
		warn("football-trades: "+format, args...)
	}))

	for _, txn := range in.trades {
		// check: already alerted for this transaction_id? A read failure
		// reads as unsent and is logged by the Marker itself.
		if m.Sent(ctx, txn.TransactionID) {
			res.Skipped++
			continue
		}

		sides := dynasty.BuildTradeSides(txn, in.players, in.bundle, in.names, in.format)
		verdict := dynasty.GradeTrade(sides)
		res.Graded++

		title, body := formatTradeAlert(txn, sides, verdict)
		fmt.Fprintln(in.out, title)
		fmt.Fprintln(in.out, body)

		if in.dryRun {
			continue // do not send, mark or log in a dry-run
		}

		// check -> send -> mark, never claim-then-send (rosterbot-chs). A
		// failed send returns its error with nothing marked, so the next
		// poll retries; a marker-write failure after a successful send
		// degrades to a duplicate alert next poll, never to silence.
		if err := m.Send(txn.TransactionID, []byte(dynasty.TradeVerdictSummary(verdict)), func() error {
			return in.send(title, body)
		}); err != nil {
			warn("football-trades: send failed for %s: %v", txn.TransactionID, err)
			continue // do not mark: a failed send must retry, never go silent
		}
		res.Alerted++

		// Logged only once the alert has actually gone out, so the log means
		// exactly "what was reported" -- a send that failed will retry next
		// poll and be logged then, at that run's prices.
		res.LogRows = append(res.LogRows, dynasty.BuildTradeLogRow(in.now, txn, in.players, in.bundle, in.names, in.format))
	}
	return res
}

// relogFootballTrades is the one-shot backfill behind --relog.
//
// Every trade the league has ever made was already alerted and marked before
// the durable log existed, and the main loop skips a marked trade BEFORE it
// grades -- so a log added to that loop alone would ship permanently empty in
// an offseason league. This rebuilds those rows.
//
// What it can and cannot recover is the whole point. Sleeper retains completed
// trades forever, so the IDENTITY (teams, players, picks, FAAB, date) is exact.
// StatsGuy has no history, so the VALUES are today's, which is a different
// number from what the alert reported -- hence Regraded, which the dashboard
// renders as an explicit badge. The one surviving grade-time fact is the dedup
// marker's body, carried across as OriginalSummary.
//
// It sends nothing and writes no markers: this is a log repair, not an alert
// replay, and re-alerting months-old trades would be the worse failure. That is
// enforced by the signature rather than by care -- markers arrives as the
// read-only lineupapi.ObjectStore, so Publish is not reachable from here at all,
// and no send function is passed in.
func relogFootballTrades(ctx context.Context, cfg *FootballConfig, markers lineupapi.ObjectStore, now time.Time, trades []sleeper.Transaction, players map[string]sleeper.Player, bundle *statsguy.Bundle, names map[int]string) error {
	var rows []dynasty.TradeLogRow
	skipped := 0
	for _, txn := range trades {
		// ONLY ALREADY-ALERTED TRADES. The marker is the evidence that this
		// trade predates the log; without one, the next ordinary poll will
		// alert it and capture a genuine grade-time row. Relogging it here
		// would write a re-priced row TODAY that the real capture, landing on a
		// later day, would then have to outrank -- and it is exactly the row
		// whose values are wrong. DedupeTradeLog's captured-beats-regraded rule
		// covers that case too, but not writing the bad row in the first place
		// is the guard that does not depend on a later read getting it right.
		//
		// A marker READ FAILURE skips as well. Absent evidence is not evidence
		// of absence, and the cost of the two mistakes is asymmetric: skipping
		// a trade that was alerted leaves one row missing from a backfill that
		// can simply be re-run, while relogging one that was not permanently
		// substitutes today's prices for a capture that had not happened yet.
		body, found, err := markers.Get(ctx, txn.TransactionID)
		if err != nil {
			warn("football-trades --relog: marker read %s: %v (skipping -- cannot confirm it was alerted)", txn.TransactionID, err)
			skipped++
			continue
		}
		if !found {
			skipped++
			continue
		}

		row := dynasty.BuildTradeLogRow(now, txn, players, bundle, names, cfg.DynastyFormat)
		row.Regraded = true
		row.OriginalSummary = strings.TrimSpace(string(body))
		rows = append(rows, row)
		fmt.Printf("relog %s (%s): %s\n", txn.TransactionID, tradeDateLabel(row), row.Verdicts[cfg.DynastyFormat].Summary)
	}
	if skipped > 0 {
		fmt.Printf("football-trades --relog: skipped %d trade(s) with no dedup marker -- never alerted, so the next poll will capture them properly\n", skipped)
	}

	if dryRun {
		fmt.Printf("football-trades --relog (dry-run): %d trade(s) rebuilt; not written\n", len(rows))
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("football-trades --relog: no already-alerted trades to rebuild; nothing written")
		return nil
	}
	added, err := writeFootballTradeLog(now, rows)
	if err != nil {
		return fmt.Errorf("relog trade log: %w", err)
	}
	fmt.Printf("football-trades --relog: %d trade(s) rebuilt, %d written to dt=%s (%d already captured and left untouched)\n",
		len(rows), added, now.Format("2006-01-02"), len(rows)-added)
	return nil
}

func tradeDateLabel(r dynasty.TradeLogRow) string {
	if r.TradeDate.IsZero() {
		return "date unknown"
	}
	return r.TradeDate.Format("2006-01-02")
}

// writeFootballTradeLog opens the store and appends rows to date's partition.
func writeFootballTradeLog(date time.Time, rows []dynasty.TradeLogRow) (int, error) {
	sel := statestore.FromEnv()
	reader, err := sel.FootballTradeLogReader()
	if err != nil {
		return 0, fmt.Errorf("init trade log reader: %w", err)
	}
	writer, err := sel.FootballTradeLogWriter()
	if err != nil {
		return 0, fmt.Errorf("init trade log writer: %w", err)
	}
	return appendFootballTradeLog(reader, writer, date, rows)
}

// appendFootballTradeLog folds rows into date's existing partition and rewrites
// it. Split from writeFootballTradeLog with no environment or Sleeper client in
// sight so the merge is testable at all, on cmd/grade.go's writeLineupGaps
// precedent.
//
// READ-MODIFY-WRITE IS MANDATORY HERE, and getting it wrong is silent.
// ndjsonstore.Write is a whole-object overwrite and this producer polls every
// six hours, so two trades landing on the same UTC day in different polls are
// disjoint graded sets: a bare second Write deletes the first poll's rows,
// returns no error, and the run prints a success summary over the loss.
//
// It assumes a SINGLE WRITER, and that assumption is load-bearing: the read and
// the write are separate operations, so two overlapping producers would each
// read the same prior set and the later write would drop the earlier one's
// rows. That is safe here because FootballTrades is one scheduled task every
// six hours with no per-tenant fan-out (infra.go's perTenantJobs does not list
// it), so overlap requires a deliberate manual launch alongside the schedule.
// It is deliberately NOT given a compare-and-swap: internal/s3blob documents
// that the only conditional writes in this tree exist for the one artifact that
// is genuinely concurrently mutated (the WebAuthn identity record), and every
// other durable store here -- including cmd/trades.go's offer log, which does
// exactly this -- is write-once-per-key by the same reasoning.
//
// Only THIS date's partition is read back into the merge. ReadAllTrades walks
// every partition, and folding the whole history into today would copy every
// past row forward on every write. That is the deliberate difference from
// internal/tradeboard's offer log, which does carry rows forward: an offer's
// outcome mutates (open -> accepted), so each of its partitions is a cumulative
// snapshot, whereas a graded trade is immutable and each partition here is that
// day's delta.
// It returns how many rows the merge actually ADDED, which is not always
// len(fresh): MergeTradeLog lets a prior row win, so a --relog over a day that
// already holds captured grades adds nothing. Reporting len(fresh) there would
// print "wrote 14 regraded rows" while writing none of them -- a summary that
// does not describe what happened, which is the failure this repo keeps
// guarding against.
func appendFootballTradeLog(reader dynasty.TradeLogReader, writer dynasty.TradeLogWriter, date time.Time, fresh []dynasty.TradeLogRow) (added int, err error) {
	all, err := reader.ReadAllTrades()
	if err != nil {
		return 0, fmt.Errorf("read trade log: %w", err)
	}
	dt := date.UTC().Format("2006-01-02")
	var prior []dynasty.TradeLogRow
	for _, r := range all {
		if r.Dt == dt {
			prior = append(prior, r)
		}
	}
	merged := dynasty.MergeTradeLog(prior, fresh)
	if err := writer.WriteTradeLog(date, merged); err != nil {
		return 0, err
	}
	return len(merged) - len(prior), nil
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

	switch {
	case v.Status == dynasty.TradeFavors:
		title = fmt.Sprintf("Trade: favors %s (+%.0f%%)", v.FavoredTeamName, v.Pct)
	case v.Status == dynasty.TradeDeadEven:
		// Everything priced and both sides carrying real, equal value. The
		// "nothing to compare" wording below would be false here, and would
		// send the operator hunting for missing data that is not missing.
		title = "Trade: dead even, so no verdict"
	case v.UnpricedAssets > 0:
		title = "Trade: too many unpriced assets to grade"
	default:
		// Incomplete with nothing unpriced: everything priced and every side
		// totals zero, or nothing this system models changed hands. Blaming an
		// unpriced asset here would send the operator looking for one that does
		// not exist.
		title = "Trade: nothing to compare, so no verdict"
	}
	// Rune-safe fit to Pushover's limit — the old inline slice could cut a
	// multibyte team or player name mid-rune.
	body = pushover.Truncate(body)
	return title, body
}

// sendFootballTradeAlert routes a graded trade through the notify dispatcher
// (feed record first, then APNs -- and Pushover while the dual-send cutover
// flag is set), the same channel as baseball's "Trade Alert". The returned
// error is a failed FEED write only, which is exactly what the caller's
// do-not-mark-on-failure rule needs: the durable record is the half that must
// exist before a trade may be marked handled. An unconfigured dispatcher is a
// silent no-op, preserving the old print-only behaviour of a credential-less
// run.
func sendFootballTradeAlert(ctx context.Context, title, body string) error {
	return notify.Send(ctx, notify.Event{Kind: "transactions", Title: title, Message: body})
}
