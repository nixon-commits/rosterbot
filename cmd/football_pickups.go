package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
	"github.com/spf13/cobra"
)

var footballPickupsCmd = &cobra.Command{
	Use:   "football-pickups",
	Short: "Daily per-league digest of Sleeper pickup opportunities from public data",
	Long: `Snapshots Sleeper's player dump once a day (players on an NFL club at a
position some league can roster), archives it as source sleeper-players, and
diffs it against the most recent prior capture. Three detectors feed one
ranked digest per league, sent only when non-empty:

  ROLE  an unrostered player who became a depth-chart starter (#1) since the
        prior capture -- the signal that caught Lock and Wentz the week they
        drew real FAAB while StatsGuy's value sat still
  DROP  a priced player released by a completed waiver or free-agent move
        since the prior capture and still unrostered (trades excluded)
  CHOP  a guillotine elimination: the roster and its priced players

Public data only: no token. Dedup is one marker per event under
football/pickups/ (check -> send -> mark); --dry-run sends nothing, marks
nothing, archives nothing and leaves the snapshot pointer untouched. The
first run writes a baseline and alerts nothing.`,
	RunE: runFootballPickups,
}

func init() { rootCmd.AddCommand(footballPickupsCmd) }

// pickupSource is the Daily Archive source name; pickupSnapshotKey is the one
// object the diff reads and overwrites.
const (
	pickupSource      = "sleeper-players"
	pickupSnapshotKey = "latest.json"
)

func runFootballPickups(cmd *cobra.Command, args []string) error {
	cfg, sc, err := initFootball()
	if err != nil {
		return err
	}
	if err := cfg.requireSleeperUserID(); err != nil {
		return err
	}
	ctx := context.Background()

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
	// FATAL, deliberately: a drop's value and a role change's valuation both
	// read the bundle, and a digest of unvalued lines would rank by nothing.
	bundle, err := statsguy.LoadBundle(ctx, cacheDir, cacheTTL(statsguy.CacheTTL))
	if err != nil {
		return fmt.Errorf("statsguy bundle: %w", err)
	}
	now := time.Now().UTC()

	var lgs []sleeper.League
	for _, lc := range leagues {
		lgs = append(lgs, lc.League)
	}
	cur := dynasty.BuildPlayerSnapshot(now, players, dynasty.RosterablePositions(lgs))

	// The pointer store is a HARD requirement: without it there is nothing to
	// diff against and nothing to save, and a run that silently alerted on a
	// full-dump "diff" would page on every starter in the league.
	snapStore, err := statestore.FromEnv().FootballPickupSnapshot()
	if err != nil {
		return fmt.Errorf("init pickup snapshot store: %w", err)
	}
	prev, found, err := loadPickupSnapshot(ctx, snapStore)
	if err != nil {
		return fmt.Errorf("load prior snapshot: %w", err)
	}
	if !found {
		fmt.Printf("football-pickups: no prior capture; wrote baseline of %d players and alerted nothing\n", len(cur.Players))
		if dryRun {
			fmt.Println("football-pickups (dry-run): baseline not written")
			return nil
		}
		return persistPickupSnapshot(now, cur, snapStore)
	}

	// Soft: a marker store we cannot build disables dedup, not the digest.
	markers, err := statestore.FromEnv().FootballPickupMarkers()
	if err != nil {
		warn("football-pickups: init markers: %v (digests will repeat until resolved)", err)
		markers = nil
	}
	captureDate := now.Format("2006-01-02")

	total, failed := pollPickups(ctx, leagues,
		func(ctx context.Context, lc leagueContext) (pickupLeagueInputs, error) {
			return loadPickupLeague(ctx, sc, lc.League.LeagueID, state)
		},
		func(lc leagueContext, in pickupLeagueInputs) pickupRunResult {
			roles := dynasty.DetectRoleChanges(prev, cur, in.rostered, bundle, lc.Profile.Format)
			drops, chops := dynasty.DetectDrops(in.txns, prev.CapturedAt, in.rostered, players, bundle, lc.Profile.Format, in.names)
			items := dynasty.OrderPickups(dynasty.PickupItems(captureDate, chops, drops, roles), lc.Profile.Superflex)
			res := alertPickups(ctx, pickupAlertInputs{
				markers: markers, league: lc.Profile, items: items, dryRun: dryRun,
				send: func(title, body string) error { return sendFootballPickupAlert(ctx, title, body) },
				out:  os.Stdout,
			})
			res.Roles, res.Drops, res.Chops = len(roles), len(drops), len(chops)
			printPickupCoverage(os.Stdout, lc.League.Name, prev.CapturedAt, prev.HasDepthData, cur.HasDepthData,
				len(cur.Players), len(cur.Players)-countRostered(cur, in.rostered), res)
			return res
		})

	if dryRun {
		fmt.Println("football-pickups (dry-run): snapshot not archived, pointer not moved")
	} else if err := persistPickupSnapshot(now, cur, snapStore); err != nil {
		// Soft: the alerts went out and the markers are set; the next run
		// diffs against the older capture and the markers keep it quiet.
		warn("football-pickups: %v (next run diffs against the prior capture)", err)
	}
	fmt.Printf("football-pickups: %d league(s), prev=%s, %d role, %d drops, %d chops, %d new, %d digest(s) sent\n",
		len(leagues), prev.CapturedAt.Format("2006-01-02"), total.Roles, total.Drops, total.Chops, total.New, total.Sent)
	if len(failed) > 0 {
		return fmt.Errorf("football-pickups: %d of %d league(s) failed: %s", len(failed), len(leagues), strings.Join(failed, ", "))
	}
	return nil
}

// persistPickupSnapshot archives today's capture (history, NoBackfill) and
// moves the pointer (the next diff's baseline). Archive first: a pointer
// that moved without its history landing is worse than the reverse.
func persistPickupSnapshot(now time.Time, snap dynasty.PlayerSnapshot, snapStore lineupapi.BlobStore) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	w, err := statestore.FromEnv().ArchiveWriter()
	if err != nil {
		return fmt.Errorf("init archive writer: %w", err)
	}
	if err := w.Write(now, pickupSource, []archive.Artifact{{Filename: "players.json", Bytes: body}}); err != nil {
		return fmt.Errorf("archive %s: %w", pickupSource, err)
	}
	if err := savePickupSnapshot(snapStore, snap); err != nil {
		return fmt.Errorf("save snapshot pointer: %w", err)
	}
	return nil
}

func loadPickupSnapshot(ctx context.Context, store lineupapi.ObjectStore) (dynasty.PlayerSnapshot, bool, error) {
	body, found, err := store.Get(ctx, pickupSnapshotKey)
	if err != nil || !found {
		return dynasty.PlayerSnapshot{}, false, err
	}
	var snap dynasty.PlayerSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return dynasty.PlayerSnapshot{}, false, fmt.Errorf("decode %s: %w", pickupSnapshotKey, err)
	}
	return snap, true, nil
}

func savePickupSnapshot(store lineupapi.BlobStore, snap dynasty.PlayerSnapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return store.Publish(pickupSnapshotKey, body)
}

// pickupLeagueInputs is one league's public inputs.
type pickupLeagueInputs struct {
	rostered map[string]bool
	txns     []sleeper.Transaction
	names    map[int]string
}

func loadPickupLeague(ctx context.Context, sc *sleeper.Client, leagueID string, state *sleeper.NFLState) (pickupLeagueInputs, error) {
	rosters, err := sc.Rosters(ctx, leagueID)
	if err != nil {
		return pickupLeagueInputs{}, fmt.Errorf("sleeper rosters: %w", err)
	}
	users, err := sc.Users(ctx, leagueID)
	if err != nil {
		return pickupLeagueInputs{}, fmt.Errorf("sleeper users: %w", err)
	}
	in := pickupLeagueInputs{rostered: map[string]bool{}, names: dynasty.TeamNames(rosters, users)}
	for _, r := range rosters {
		for _, id := range r.Players {
			in.rostered[id] = true
		}
		for _, id := range r.Taxi {
			in.rostered[id] = true
		}
		for _, id := range r.Reserve {
			in.rostered[id] = true
		}
	}
	for _, week := range pollWeeks(state) {
		txns, err := sc.Transactions(ctx, leagueID, week)
		if err != nil {
			return pickupLeagueInputs{}, fmt.Errorf("sleeper transactions (week %d): %w", week, err)
		}
		in.txns = append(in.txns, txns...)
	}
	return in, nil
}

func countRostered(snap dynasty.PlayerSnapshot, rostered map[string]bool) int {
	n := 0
	for id := range snap.Players {
		if rostered[id] {
			n++
		}
	}
	return n
}

type pickupLoader func(ctx context.Context, lc leagueContext) (pickupLeagueInputs, error)
type pickupRunner func(lc leagueContext, in pickupLeagueInputs) pickupRunResult

// pickupRunResult is one league's counts (or the run's totals).
type pickupRunResult struct {
	Roles, Drops, Chops, New, Sent int
}

// pollPickups runs load -> run per league with the same isolation as
// pollLeagues/pollOffers: a league whose load fails is warned, named in
// failed and skipped; the others still run.
func pollPickups(ctx context.Context, leagues []leagueContext, load pickupLoader, run pickupRunner) (total pickupRunResult, failed []string) {
	for _, lc := range leagues {
		in, err := load(ctx, lc)
		if err != nil {
			warn("football-pickups: %s: %v (continuing with the other leagues)", lc.League.Name, err)
			failed = append(failed, lc.League.Name)
			continue
		}
		r := run(lc, in)
		total.Roles += r.Roles
		total.Drops += r.Drops
		total.Chops += r.Chops
		total.New += r.New
		total.Sent += r.Sent
	}
	return total, failed
}

// printPickupCoverage is the unconditional per-league line, zero case
// included: without it a quiet league and a blind diff look identical.
//
// depth=<prev>/<cur> is what separates "no role changes" from "the role
// detector did not run": DetectRoleChanges is silent when either capture
// carries no depth-chart data (a stale 24 h player-dump cache written by an
// older binary), and role=0 reads identically in both cases.
func printPickupCoverage(out io.Writer, league string, prevAt time.Time, prevDepth, curDepth bool, players, unrostered int, r pickupRunResult) {
	fmt.Fprintf(out, "football-pickups: %s: prev=%s depth=%s/%s players=%d unrostered=%d role=%d drops=%d chops=%d new=%d\n",
		league, prevAt.Format("2006-01-02"), yesNo(prevDepth), yesNo(curDepth), players, unrostered, r.Roles, r.Drops, r.Chops, r.New)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// pickupAlertInputs is one league's digest inputs with the side effects
// injected — the tradeRunInputs/offerRunInputs seam again.
type pickupAlertInputs struct {
	markers lineupapi.BlobStore
	league  dynasty.LeagueProfile
	items   []dynasty.PickupItem
	dryRun  bool
	send    func(title, body string) error
	out     io.Writer
}

// alertPickups sends ONE digest per league carrying only the items with no
// marker, then marks the items the digest showed: check -> send -> mark. A
// failed send marks nothing; a marker-write failure degrades to a repeat next
// run; an item the digest could not fit stays unmarked for the next run.
func alertPickups(ctx context.Context, in pickupAlertInputs) pickupRunResult {
	var res pickupRunResult
	m := alertmarker.New(in.markers, alertmarker.WithLogf(func(format string, args ...any) {
		warn("football-pickups: "+format, args...)
	}))
	var fresh []dynasty.PickupItem
	for _, it := range in.items {
		if m.Sent(ctx, in.league.LeagueID+"-"+it.Key) {
			continue
		}
		fresh = append(fresh, it)
	}
	res.New = len(fresh)
	if len(fresh) == 0 {
		return res
	}
	title, body, shown := dynasty.FormatPickupDigest(in.league, fresh)
	if shown == 0 {
		// The head item's line alone exceeds the Pushover budget, so no digest
		// can ever carry it. Leaving it unmarked would wedge the league: every
		// run would lead with it, show nothing, and never reach the items
		// behind it. Mark that ONE item (the rest stay unmarked and lead the
		// next run), name it, and send nothing this run.
		warn("football-pickups: %s: item %s does not fit a digest on its own; marked without alert", in.league.Name, fresh[0].Key)
		if !in.dryRun {
			m.Record(in.league.LeagueID+"-"+fresh[0].Key, []byte(fresh[0].Line))
		}
		return res
	}
	fmt.Fprintln(in.out, title)
	fmt.Fprintln(in.out, body)
	if in.dryRun {
		return res
	}
	if err := in.send(title, body); err != nil {
		warn("football-pickups: %s: send failed: %v (nothing marked; next run retries)", in.league.Name, err)
		return res
	}
	res.Sent = 1
	// Mark ONLY the items the digest actually carried. A line the digest
	// refused for space is still unmarked, so it leads the next run's digest
	// instead of being muted forever -- the silent-loss failure this repo's
	// check -> send -> mark rule exists to prevent.
	for _, it := range fresh[:shown] {
		m.Record(in.league.LeagueID+"-"+it.Key, []byte(it.Line))
	}
	return res
}

func sendFootballPickupAlert(ctx context.Context, title, body string) error {
	return notify.Send(ctx, notify.Event{Kind: "waivers", Title: title, Message: body})
}
