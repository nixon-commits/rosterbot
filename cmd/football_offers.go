package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/pushover"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/sleeperauth"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
	"github.com/spf13/cobra"
)

var footballOffersCmd = &cobra.Command{
	Use:   "football-offers",
	Short: "Grade every Sleeper trade offer made to you before you respond",
	Long: `Reads the live trade offers proposed TO the operator's roster in every
non-complete NFL league SLEEPER_USER_ID belongs to, through Sleeper's
undocumented GraphQL API with the operator's own session token
(SLEEPER_TOKEN -- the only job that receives it), grades each with the same
StatsGuy sum football-trades uses, and pushes one alert per offer written
from the operator's side: what you get, what you give, the verdict, when it
expires, and the consent state ("waiting on you", how many have accepted for an
offer between more than two rosters, or that you have already accepted it).

Read-only: nothing is accepted, rejected or countered. Idempotent via one
dedup marker per transaction_id under football/offers/ (check -> send ->
mark); --dry-run sends and marks nothing. The run fails outright when the
token is rejected -- every league would fail the same way -- and ops
alerting escalates on the third consecutive failure.`,
	RunE: runFootballOffers,
}

func init() { rootCmd.AddCommand(footballOffersCmd) }

func runFootballOffers(cmd *cobra.Command, args []string) error {
	cfg, sc, err := initFootball()
	if err != nil {
		return err
	}
	if err := cfg.requireSleeperUserID(); err != nil {
		return err
	}
	auth, err := sleeperauth.FromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()

	// Identity FIRST. A token pasted from the wrong account, or a wrong user
	// id, would otherwise run green forever and never find an offer.
	uid, err := auth.Me(ctx)
	if err != nil {
		return fmt.Errorf("sleeper identity check: %w", err)
	}
	if err := verifyOfferIdentity(uid, cfg.SleeperUserID); err != nil {
		return err
	}
	fmt.Printf("football-offers: token verified for user %s\n", uid)

	state, err := sc.State(ctx)
	if err != nil {
		return fmt.Errorf("sleeper state: %w", err)
	}
	leagues, err := discoverLeagues(ctx, sc, cfg, state, os.Stdout)
	if err != nil {
		// An off-season stop has already printed its one line and recorded
		// the outcome; silence cobra so the exit-0 path prints nothing else,
		// exactly as checkSeasonGate does for the baseball commands.
		if errors.Is(err, errOffSeason) {
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
		}
		return err
	}
	players, err := sc.PlayersNFL(ctx)
	if err != nil {
		return fmt.Errorf("sleeper players: %w", err)
	}
	bundle, err := statsguy.LoadBundle(ctx, cacheDir, cacheTTL(statsguy.CacheTTL))
	if err != nil {
		return fmt.Errorf("statsguy bundle: %w", err)
	}
	// Soft: a marker store we cannot build disables dedup, not the alert --
	// the same policy as football-trades.
	markers, err := statestore.FromEnv().FootballOfferMarkers()
	if err != nil {
		warn("football-offers: init markers: %v (alerts will repeat until resolved)", err)
		markers = nil
	}
	now := time.Now().UTC()

	total, found, failed, err := pollOffers(ctx, leagues, cfg.SleeperUserID, now,
		func(ctx context.Context, lc leagueContext) (leagueOffers, error) {
			return loadLeagueOffers(ctx, sc, auth, cfg.SleeperUserID, lc.League.LeagueID)
		},
		func(lc leagueContext, li leagueOffers, keep []sleeper.Transaction) offerRunResult {
			return gradeAndAlertOffers(ctx, offerRunInputs{
				markers:  markers,
				now:      now,
				offers:   keep,
				myRoster: li.myRoster,
				players:  players,
				bundle:   bundle,
				names:    li.names,
				league:   lc.Profile,
				dryRun:   dryRun,
				send:     func(title, body string) error { return sendFootballOfferAlert(ctx, title, body) },
				out:      os.Stdout,
			})
		},
		os.Stdout,
	)
	if err != nil {
		return err
	}

	fmt.Printf("football-offers: %d league(s), %d proposed, %d already alerted, %d expired, %d already accepted by you, %d sent by you, %d graded, %d sent\n",
		len(leagues), found, total.Skipped, total.Expired, total.Answered, total.SentByMe, total.Graded, total.Alerted)
	if len(failed) > 0 {
		return fmt.Errorf("football-offers: %d of %d league(s) failed: %s", len(failed), len(leagues), strings.Join(failed, ", "))
	}
	return nil
}

// verifyOfferIdentity compares the Sleeper user the token authenticates as
// against SLEEPER_USER_ID. Split out of runFootballOffers (which calls
// initFootball, sleeperauth.FromEnv and statestore.FromEnv and so cannot be
// driven from a test) so that dropping the comparison fails a test rather than
// quietly turning a wrong-account token into a run that is green forever.
func verifyOfferIdentity(got, want string) error {
	if got != want {
		return fmt.Errorf("SLEEPER_TOKEN belongs to Sleeper user %s but SLEEPER_USER_ID is %s: the token is from another account, or the user id is wrong", got, want)
	}
	return nil
}

// offerLoader fetches one league's offer inputs.
type offerLoader func(ctx context.Context, lc leagueContext) (leagueOffers, error)

// offerRunner grades and alerts one league's kept offers.
type offerRunner func(lc leagueContext, li leagueOffers, keep []sleeper.Transaction) offerRunResult

// pollOffers runs load -> select -> run for every league: the extracted body of
// runFootballOffers' loop, on pollLeagues' precedent, so the four rules that
// live in it are asserted rather than read.
//
//   - ErrUnauthorized aborts the run at once and no further league is loaded:
//     every league would fail the same way, and six warnings read as six
//     problems where there is one.
//   - Any other load error is warned, names the league in failed, and the
//     loop continues, so one league's hiccup never silences the rest.
//   - A league where the operator owns no roster prints a skip line.
//   - Every other league prints the coverage line unconditionally, zero case
//     included: a league with no offers and a query that returns nothing look
//     identical without it.
//   - Every row the query returned is accounted for. A row that fails the
//     filters the server was asked to apply is the one in-band sign that the
//     query semantics differ from what the code assumes, so it is named in a
//     warning rather than dropped.
func pollOffers(ctx context.Context, leagues []leagueContext, myUserID string, now time.Time, load offerLoader, run offerRunner, out io.Writer) (total offerRunResult, found int, failed []string, err error) {
	for _, lc := range leagues {
		li, lerr := load(ctx, lc)
		if lerr != nil {
			if errors.Is(lerr, sleeperauth.ErrUnauthorized) {
				return total, found, failed, fmt.Errorf("football-offers: %s: %w", lc.League.Name, lerr)
			}
			warn("football-offers: %s: %v (continuing with the other leagues)", lc.League.Name, lerr)
			failed = append(failed, lc.League.Name)
			continue
		}
		if li.myRoster == 0 {
			fmt.Fprintf(out, "football-offers: %s: no roster owned by %s; skipped\n", lc.League.Name, myUserID)
			continue
		}
		sel := selectOffers(li.offers, li.myRoster, myUserID, now)
		found += len(li.offers)
		total.Expired += sel.Expired
		total.Answered += sel.Answered
		total.SentByMe += sel.SentByMe
		if len(sel.Unexpected) > 0 {
			warn("football-offers: %s: %d unexpected row(s) from the proposed-trades query (ids %s) — the server filter did not hold; check the query",
				lc.League.Name, len(sel.Unexpected), strings.Join(sel.Unexpected, ","))
		}
		fmt.Fprintf(out, "football-offers: %s: %d proposed for roster %d, %d to me and live\n", lc.League.Name, len(li.offers), li.myRoster, len(sel.Keep))

		res := run(lc, li, sel.Keep)
		total.Graded += res.Graded
		total.Alerted += res.Alerted
		total.Skipped += res.Skipped
	}
	return total, found, failed, nil
}

// leagueOffers is one league's inputs: the operator's roster id there (0 when
// they own none), the proposed trades involving it, and the team names.
type leagueOffers struct {
	myRoster int
	offers   []sleeper.Transaction
	names    map[int]string
}

func loadLeagueOffers(ctx context.Context, sc *sleeper.Client, auth *sleeperauth.Client, userID, leagueID string) (leagueOffers, error) {
	rosters, err := sc.Rosters(ctx, leagueID)
	if err != nil {
		return leagueOffers{}, fmt.Errorf("sleeper rosters: %w", err)
	}
	users, err := sc.Users(ctx, leagueID)
	if err != nil {
		return leagueOffers{}, fmt.Errorf("sleeper users: %w", err)
	}
	li := leagueOffers{names: dynasty.TeamNames(rosters, users)}
	for _, r := range rosters {
		if r.OwnerID == userID {
			li.myRoster = r.RosterID
		}
	}
	if li.myRoster == 0 {
		return li, nil
	}
	li.offers, err = auth.ProposedTrades(ctx, leagueID, li.myRoster)
	if err != nil {
		return leagueOffers{}, fmt.Errorf("proposed trades: %w", err)
	}
	return li, nil
}

// offerSelection is selectOffers' account of EVERY row the proposed-trades
// query returned: Keep + Expired + SentByMe + len(Unexpected) == the row count.
// Answered is not a fifth class -- it counts the kept offers whose consenter_ids
// already holds my roster.
type offerSelection struct {
	Keep []sleeper.Transaction
	// Expired: live-status offers whose expires_at has passed.
	Expired int
	// Answered: KEPT offers whose consenter_ids includes my roster. Counted for
	// the summary only; they still alert (see selectOffers).
	Answered int
	// SentByMe: offers I proposed, a separate and deferred feature.
	SentByMe int
	// Unexpected: ids of rows that failed a filter the server was asked to
	// apply (type, status, my roster in roster_ids).
	Unexpected []string
}

// selectOffers keeps the offers worth alerting: live trades proposed by
// someone else that involve my roster. Every row is accounted for in the
// returned offerSelection, so the run summary sums to the row count.
//
// It deliberately does NOT drop an offer because consenter_ids holds my roster.
// What that field means on a PROPOSED offer is unverified live, and the two
// readings pull in opposite directions: "the roster ids that have accepted so
// far" (my roster present = I already accepted, nothing left for me to do) or
// "who is still being waited on" (my roster present = waiting on ME, the very
// offer this job exists to surface). Dropping on the first reading, were the
// second true, would discard every offer made to the operator on a green run
// forever -- the failure this job's spec names, and one that raises no error.
// Keeping costs at worst one alert, deduped by transaction id, for an offer the
// operator has in fact accepted; formatOfferAlert renders the state from the
// data so the alert says what it saw, and Answered counts how often it happens.
func selectOffers(txns []sleeper.Transaction, myRoster int, myUserID string, now time.Time) offerSelection {
	var sel offerSelection
	for _, t := range txns {
		// Re-check the predicates the server was asked to apply. A row that
		// fails them means the query did not mean what we think it means.
		if t.Type != "trade" || t.Status != "proposed" || !slices.Contains(t.RosterIDs, myRoster) {
			sel.Unexpected = append(sel.Unexpected, t.TransactionID)
			continue
		}
		if t.Creator == myUserID {
			sel.SentByMe++
			continue
		}
		if exp, ok := t.ExpiresAt(); ok && !exp.After(now) {
			sel.Expired++
			continue
		}
		if slices.Contains(t.ConsenterIDs, myRoster) {
			sel.Answered++
		}
		sel.Keep = append(sel.Keep, t)
	}
	return sel
}

// offerRunInputs is everything gradeAndAlertOffers needs for ONE league, with
// sending and printing injected -- the same seam tradeRunInputs provides for
// football-trades, so the check -> send -> mark rule is asserted, not read.
type offerRunInputs struct {
	markers  lineupapi.BlobStore
	now      time.Time
	offers   []sleeper.Transaction
	myRoster int
	players  map[string]sleeper.Player
	bundle   *statsguy.Bundle
	names    map[int]string
	league   dynasty.LeagueProfile
	dryRun   bool
	send     func(title, body string) error
	out      io.Writer
}

// offerRunResult is what one league's poll decided.
type offerRunResult struct {
	Graded, Alerted, Skipped, Expired, Answered, SentByMe int
}

// gradeAndAlertOffers runs check -> send -> mark over one league's live
// offers. Nothing is logged durably: the activity feed record notify.Send
// writes first is the durable trace, and an offer's outcome (accepted,
// rejected, countered) is not this job's to record -- an accepted one
// surfaces through football-trades as a completed trade.
func gradeAndAlertOffers(ctx context.Context, in offerRunInputs) offerRunResult {
	var res offerRunResult
	m := alertmarker.New(in.markers, alertmarker.WithLogf(func(format string, args ...any) {
		warn("football-offers: "+format, args...)
	}))
	for _, txn := range in.offers {
		if m.Sent(ctx, txn.TransactionID) {
			res.Skipped++
			continue
		}
		sides := dynasty.BuildTradeSides(txn, in.players, in.bundle, in.names, in.league.Format)
		verdict := dynasty.GradeTrade(sides)
		res.Graded++

		title, body := formatOfferAlert(in.league, in.myRoster, txn, sides, verdict)
		fmt.Fprintln(in.out, title)
		fmt.Fprintln(in.out, body)
		if in.dryRun {
			continue
		}
		// check -> send -> mark (rosterbot-chs): a failed send marks nothing
		// and retries next poll; a marker-write failure after a successful
		// send degrades to a duplicate, never silence.
		if err := m.Send(txn.TransactionID, []byte(dynasty.TradeVerdictSummary(verdict)), func() error {
			return in.send(title, body)
		}); err != nil {
			warn("football-offers: send failed for %s: %v", txn.TransactionID, err)
			continue
		}
		res.Alerted++
	}
	return res
}

// formatOfferAlert renders one offer from the operator's side.
//
// Title: "[League] Offer from <proposer>: favors you (+N%)" -- or favors
// them, dead even, or the two no-verdict reasons formatTradeAlert names.
// Body: "You get: ... | You give: ... | <column> | expires ... | waiting on
// you" (or "you have already accepted, waiting on others" when my roster is in
// consenter_ids). A three-team offer names each other team and what it gets
// instead of "You give".
func formatOfferAlert(league dynasty.LeagueProfile, myRoster int, txn sleeper.Transaction, sides []dynasty.TradeSide, v dynasty.TradeVerdict) (title, body string) {
	me := strconv.Itoa(myRoster)
	var mine *dynasty.TradeSide
	var others []dynasty.TradeSide
	for i := range sides {
		if sides[i].TeamID == me {
			mine = &sides[i]
		} else {
			others = append(others, sides[i])
		}
	}

	from := "Offer"
	switch {
	case len(others) == 1:
		from = "Offer from " + others[0].TeamName
	case len(others) > 1:
		from = fmt.Sprintf("Offer (%d teams)", len(sides))
	}

	var verdict string
	switch {
	case v.Status == dynasty.TradeFavors && v.FavoredTeamID == me:
		verdict = fmt.Sprintf("favors you (+%.0f%%)", v.Pct)
	case v.Status == dynasty.TradeFavors:
		verdict = fmt.Sprintf("favors %s (+%.0f%%)", v.FavoredTeamName, v.Pct)
	case v.Status == dynasty.TradeDeadEven:
		verdict = "dead even"
	case v.UnpricedAssets > 0:
		verdict = "too many unpriced assets to grade"
	default:
		verdict = "nothing to compare"
	}
	title = fmt.Sprintf("[%s] %s: %s", league.Name, from, verdict)

	var parts []string
	if mine != nil {
		parts = append(parts, "You get: "+assetList(mine.Assets))
	}
	if len(others) == 1 {
		parts = append(parts, "You give: "+assetList(others[0].Assets))
	} else {
		for _, o := range others {
			parts = append(parts, o.TeamName+" gets: "+assetList(o.Assets))
		}
	}
	parts = append(parts, league.Format)
	if exp, ok := txn.ExpiresAt(); ok {
		parts = append(parts, "expires "+exp.Format("2006-01-02 15:04 UTC"))
	}
	// The consent state is rendered from the data, never assumed: selectOffers
	// keeps an offer whose consenter_ids holds my roster, so this is where the
	// two cases part. This copy reads consenter_ids as "accepted so far"; the
	// live probe (internal/sleeperauth/diag_proposed_test.go) settles whether
	// that is right.
	//
	// Otherwise, ConsenterIDs includes the proposer's own consent, so in a
	// two-team offer "1 of 2 accepted" is always true and says nothing; only a
	// multi-team offer has a consent count worth naming.
	switch {
	case slices.Contains(txn.ConsenterIDs, myRoster):
		parts = append(parts, "you have already accepted, waiting on others")
	case len(txn.RosterIDs) > 2:
		parts = append(parts, fmt.Sprintf("%d of %d accepted, waiting on you", len(txn.ConsenterIDs), len(txn.RosterIDs)))
	default:
		parts = append(parts, "waiting on you")
	}
	return title, pushover.Truncate(strings.Join(parts, " | "))
}

func assetList(assets []dynasty.TradeAsset) string {
	if len(assets) == 0 {
		return "nothing"
	}
	names := make([]string, 0, len(assets))
	for _, a := range assets {
		if a.Priced {
			names = append(names, fmt.Sprintf("%s (%d)", a.Name, a.Value))
		} else {
			names = append(names, a.Name+" (unpriced)")
		}
	}
	return strings.Join(names, ", ")
}

// sendFootballOfferAlert routes through the notify dispatcher with the same
// kind football-trades uses; the title's "Offer" is what tells them apart. A
// dedicated kind would be an iOS-visible change and is not bundled here.
func sendFootballOfferAlert(ctx context.Context, title, body string) error {
	return notify.Send(ctx, notify.Event{Kind: "transactions", Title: title, Message: body})
}
