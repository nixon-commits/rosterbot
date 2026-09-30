package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
from the operator's side: what you get, what you give, the verdict, who has
already accepted, and when it expires.

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
	if uid != cfg.SleeperUserID {
		return fmt.Errorf("SLEEPER_TOKEN belongs to Sleeper user %s but SLEEPER_USER_ID is %s: the token is from another account, or the user id is wrong", uid, cfg.SleeperUserID)
	}
	fmt.Printf("football-offers: token verified for user %s\n", uid)

	state, err := sc.State(ctx)
	if err != nil {
		return fmt.Errorf("sleeper state: %w", err)
	}
	leagues, err := discoverLeagues(ctx, sc, cfg, state.Season, os.Stdout)
	if err != nil {
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

	var (
		failed []string
		total  offerRunResult
		found  int
	)
	for _, lc := range leagues {
		li, err := loadLeagueOffers(ctx, sc, auth, cfg.SleeperUserID, lc.League.LeagueID)
		if err != nil {
			if errors.Is(err, sleeperauth.ErrUnauthorized) {
				// Every league would fail the same way; fail the run so ops
				// alerting sees a real outage rather than six warnings.
				return fmt.Errorf("football-offers: %s: %w", lc.League.Name, err)
			}
			warn("football-offers: %s: %v (continuing with the other leagues)", lc.League.Name, err)
			failed = append(failed, lc.League.Name)
			continue
		}
		if li.myRoster == 0 {
			fmt.Printf("football-offers: %s: no roster owned by %s; skipped\n", lc.League.Name, cfg.SleeperUserID)
			continue
		}
		keep, expired, answered := selectOffers(li.offers, li.myRoster, cfg.SleeperUserID, now)
		found += len(li.offers)
		total.Expired += expired
		total.Answered += answered
		// Unconditional coverage line, zero case included: a league with no
		// offers and a query that returns nothing look identical without it.
		fmt.Printf("football-offers: %s: %d proposed for roster %d, %d to me and live\n", lc.League.Name, len(li.offers), li.myRoster, len(keep))

		res := gradeAndAlertOffers(ctx, offerRunInputs{
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
		total.Graded += res.Graded
		total.Alerted += res.Alerted
		total.Skipped += res.Skipped
	}

	fmt.Printf("football-offers: %d league(s), %d proposed, %d already alerted, %d expired, %d already answered, %d graded, %d sent\n",
		len(leagues), found, total.Skipped, total.Expired, total.Answered, total.Graded, total.Alerted)
	if len(failed) > 0 {
		return fmt.Errorf("football-offers: %d of %d league(s) failed: %s", len(failed), len(leagues), strings.Join(failed, ", "))
	}
	return nil
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

// selectOffers keeps the offers worth alerting: live trades proposed by
// someone else that involve my roster and still await my consent. The two
// dropped classes worth counting are returned so the summary can say why a
// proposed offer produced no alert.
func selectOffers(txns []sleeper.Transaction, myRoster int, myUserID string, now time.Time) (keep []sleeper.Transaction, expired, answered int) {
	for _, t := range txns {
		if t.Type != "trade" || t.Status != "proposed" || t.Creator == myUserID || !containsInt(t.RosterIDs, myRoster) {
			continue
		}
		if exp, ok := t.ExpiresAt(); ok && !exp.After(now) {
			expired++
			continue
		}
		if containsInt(t.ConsenterIDs, myRoster) {
			answered++ // I already accepted; it is waiting on someone else
			continue
		}
		keep = append(keep, t)
	}
	return keep, expired, answered
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
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
	Graded, Alerted, Skipped, Expired, Answered int
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
// you". A three-team offer names each giving side instead of "You give".
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
	accepted := len(txn.ConsenterIDs)
	if accepted > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d accepted, waiting on you", accepted, len(txn.RosterIDs)))
	} else {
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
