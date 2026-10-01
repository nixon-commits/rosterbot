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
first run writes a baseline and alerts nothing.

The snapshot pointer is the diff baseline, so it advances only after a run
that fully delivered: a failed league, a failed send, or a digest that could
not carry every new item holds it, and the next run re-detects the same events
against the same baseline (the dedup markers keep what was already sent
quiet). The daily archive partition is written either way.

The run exits non-zero, after every league has been processed, when a league
failed to load, a digest send failed, or the archive or pointer write failed,
so the run ledger records a failure rather than a quiet success.`,
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
	archiveWriter := statestore.FromEnv().ArchiveWriter
	if !found {
		return writeFirstBaseline(os.Stdout, dryRun, now, cur, snapStore, archiveWriter)
	}

	// Soft: a marker store we cannot build disables dedup, not the digest.
	markers, err := statestore.FromEnv().FootballPickupMarkers()
	if err != nil {
		warn("football-pickups: init markers: %v (dedup is off this run; an unsent tail will not hold the pointer)", err)
		markers = nil
	}

	total, failed := pollPickups(ctx, leagues,
		func(ctx context.Context, lc leagueContext) (pickupLeagueInputs, error) {
			return loadPickupLeague(ctx, sc, lc.League.LeagueID, state)
		},
		func(lc leagueContext, in pickupLeagueInputs) pickupRunResult {
			det := detectLeaguePickups(prev, cur, lc, in, players, bundle)
			res := alertPickups(ctx, pickupAlertInputs{
				markers: markers, league: lc.Profile, items: det.items, dryRun: dryRun,
				send: func(title, body string) error { return sendFootballPickupAlert(ctx, title, body) },
				out:  os.Stdout,
			})
			res.Roles, res.Drops, res.Chops = det.roles, det.drops, det.chops
			printPickupCoverage(os.Stdout, lc.League.Name, prev.CapturedAt, prev.HasDepthData, cur.HasDepthData,
				len(cur.Players), len(cur.Players)-countRostered(cur, in.rostered), res)
			return res
		})

	// The persist error is remembered, not returned here: every league has
	// already alerted, and the run's verdict is composed once at the end from
	// every cause (pickupRunError).
	persistErr := finishPickupRun(os.Stdout, dryRun, now, cur, prev, total, failed, markers != nil, snapStore, archiveWriter)
	fmt.Printf("football-pickups: %d league(s), prev=%s, %d role, %d drops, %d chops, %d new, %d digest(s) sent\n",
		len(leagues), prev.CapturedAt.Format("2006-01-02"), total.Roles, total.Drops, total.Chops, total.New, total.Sent)
	return pickupRunError(len(leagues), failed, total.SendFailed, persistErr)
}

// pickupRunError is the run's verdict, composed after every league has been
// processed: nil only when no league failed to load, no send failed and the
// persist step (archive write, pointer write) succeeded. Each cause that
// applies is named, so one line says everything that went wrong.
//
// A non-nil error makes the run exit non-zero, which is what lets the run
// ledger record FAILURE and opsalert's Streak fire. Without it a lost archive
// day (NoBackfill, so the history is gone for good), a pointer that would not
// move, or a send that failed would all exit 0: the hold they cause surfaces
// only once the pointer passes its two-day MaxAge. football-trades exits 0 on
// a failed send, but it holds no state; this job holds a diff baseline, and
// that is what makes the failure worth paging.
func pickupRunError(leagues int, failed []string, sendFailed int, persistErr error) error {
	var causes []string
	if len(failed) > 0 {
		causes = append(causes, fmt.Sprintf("%d of %d league(s) failed: %s", len(failed), leagues, strings.Join(failed, ", ")))
	}
	if sendFailed > 0 {
		causes = append(causes, fmt.Sprintf("%d digest send(s) failed", sendFailed))
	}
	if persistErr != nil {
		causes = append(causes, persistErr.Error())
	}
	if len(causes) == 0 {
		return nil
	}
	return fmt.Errorf("football-pickups: %s", strings.Join(causes, "; "))
}

// archiveWriterFunc yields the Daily Archive writer. It is a function rather
// than a value so a dry-run never builds one, and so a test can inject an
// in-memory store (the caller passes statestore.FromEnv().ArchiveWriter).
type archiveWriterFunc func() (archive.Writer, error)

// writeFirstBaseline handles the run with no prior capture: write the
// baseline (archive, then pointer) and alert nothing. The claim is printed
// only after the write has landed, and a dry-run prints its own line INSTEAD
// of the claim, so the output never says a baseline was written that was not.
func writeFirstBaseline(out io.Writer, dryRun bool, now time.Time, cur dynasty.PlayerSnapshot, snapStore lineupapi.BlobStore, archiveWriter archiveWriterFunc) error {
	if dryRun {
		fmt.Fprintf(out, "football-pickups (dry-run): no prior capture; baseline of %d players not written, alerted nothing\n", len(cur.Players))
		return nil
	}
	if err := persistPickupSnapshot(now, cur, snapStore, archiveWriter, true); err != nil {
		return err
	}
	fmt.Fprintf(out, "football-pickups: no prior capture; wrote baseline of %d players and alerted nothing\n", len(cur.Players))
	return nil
}

// finishPickupRun is the persist step after every league has been processed:
// the dry-run guard, the pointer-hold decision and the writes. It returns the
// persist error (archive or pointer write) for the caller to fold into the
// run's verdict; the alerts have already gone out, so a failure here never
// stops them.
//
// A dry-run writes and moves nothing. Otherwise the daily archive partition is
// written regardless, and latest.json moves only when shouldAdvancePointer
// allows it. markersOK is false when the marker store could not be built.
func finishPickupRun(out io.Writer, dryRun bool, now time.Time, cur, prev dynasty.PlayerSnapshot, total pickupRunResult, failed []string, markersOK bool, snapStore lineupapi.BlobStore, archiveWriter archiveWriterFunc) error {
	if dryRun {
		fmt.Fprintln(out, "football-pickups (dry-run): snapshot not archived, pointer not moved")
		return nil
	}
	advance := shouldAdvancePointer(total, failed, markersOK)
	persistErr := persistPickupSnapshot(now, cur, snapStore, archiveWriter, advance)
	switch {
	case persistErr != nil:
		// Soft: the alerts went out and the markers are set. A pointer that
		// did not move means the next run diffs against the older capture and
		// re-detects the same events under the same (baseline-scoped) keys,
		// which the markers keep quiet. The error still fails the run.
		warn("football-pickups: %v (next run diffs against the prior capture)", persistErr)
	case advance:
		fmt.Fprintf(out, "football-pickups: pointer advanced to %s\n", cur.CapturedAt.Format("2006-01-02"))
		if !markersOK && total.Tail > 0 {
			fmt.Fprintf(out, "football-pickups: marker store unavailable: %d unsent item(s) abandoned, pointer not held for them\n", total.Tail)
		}
	}
	if !advance {
		fmt.Fprintf(out, "football-pickups: pointer HELD at %s: %d failed league(s), %d unsent item(s), %d failed send(s)\n",
			prev.CapturedAt.Format("2006-01-02"), len(failed), total.Tail, total.SendFailed)
	}
	return persistErr
}

// shouldAdvancePointer decides whether this run may move the snapshot pointer.
//
// The pointer IS the diff baseline: the next run detects only what changed
// since it. Advancing it past an event this run did not finish delivering
// loses that event for good, because neither detector looks behind its
// baseline (DetectDrops filters on completion time against the baseline's
// capture time less a one-hour overlap, and DetectRoleChanges skips anyone
// already at #1 in it). So the pointer is held
// when any league failed to load (its whole window would be lost), when a
// digest left items out (the tail would be lost), or when a send failed
// (everything it carried would be). Holding keeps the window open: the next
// run re-detects the same events against the same baseline, and the dedup
// markers keep whatever was already sent quiet. The hold is bounded -- a
// digest carries at least one item per run, so the tail shrinks by `shown`
// each time -- and an unhealthy hold shows up as the snapshot pointer's age
// on the Infra page.
//
// One exception: with no marker store (markersOK false) dedup is off, so every
// item reads fresh on every run and a held pointer could only repeat the same
// top digest forever without ever reaching the tail. An unsent tail therefore
// does NOT hold the pointer then (the caller prints the abandoned count). A
// failed league or a failed send still holds: those retries are real work, not
// repetition.
func shouldAdvancePointer(total pickupRunResult, failed []string, markersOK bool) bool {
	if len(failed) > 0 || total.SendFailed > 0 {
		return false
	}
	return total.Tail == 0 || !markersOK
}

// persistPickupSnapshot archives today's capture (history, NoBackfill) and,
// when advance is true, moves the pointer (the next diff's baseline). The
// archive is written either way: it is the daily record of what Sleeper
// served, independent of whether this run's diff finished delivering. Archive
// first, and a failed archive leaves the pointer where it is -- a pointer that
// moved without its history landing is worse than the reverse.
func persistPickupSnapshot(now time.Time, snap dynasty.PlayerSnapshot, snapStore lineupapi.BlobStore, archiveWriter archiveWriterFunc, advance bool) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	w, err := archiveWriter()
	if err != nil {
		return fmt.Errorf("init archive writer: %w", err)
	}
	if err := w.Write(now, pickupSource, []archive.Artifact{{Filename: "players.json", Bytes: body}}); err != nil {
		return fmt.Errorf("archive %s: %w", pickupSource, err)
	}
	if !advance {
		return nil
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
	in := pickupLeagueInputs{rostered: rosteredSet(rosters), names: dynasty.TeamNames(rosters, users)}
	for _, week := range pollWeeks(state) {
		txns, err := sc.Transactions(ctx, leagueID, week)
		if err != nil {
			return pickupLeagueInputs{}, fmt.Errorf("sleeper transactions (week %d): %w", week, err)
		}
		in.txns = append(in.txns, txns...)
	}
	return in, nil
}

// rosteredSet is every player id on any roster in the league: active, taxi
// squad and reserve (IR). All three are unavailable to add, so all three are
// "rostered" for the unrostered filters.
func rosteredSet(rosters []sleeper.Roster) map[string]bool {
	out := map[string]bool{}
	for _, r := range rosters {
		for _, id := range r.Players {
			out[id] = true
		}
		for _, id := range r.Taxi {
			out[id] = true
		}
		for _, id := range r.Reserve {
			out[id] = true
		}
	}
	return out
}

// pickupDetection is one league's detector output: the ordered digest items
// plus the per-detector counts the coverage line reports.
type pickupDetection struct {
	items               []dynasty.PickupItem
	roles, drops, chops int
}

// detectLeaguePickups runs the three detectors for one league and renders the
// ordered digest items.
//
// Role keys are scoped to the BASELINE capture's full UTC timestamp, not
// today's date and not even the baseline's date (two baselines on one UTC
// date must not share keys). The
// pointer is held while a tail or a failure is outstanding, so the next run
// diffs the same baseline against a newer capture and re-detects the same
// events; a key carrying today's date would differ every run, never match its
// own marker, and re-send what already went out. DROP and CHOP keys are
// per-transaction and already stable.
//
// roles are filtered to THIS league's rosterable positions: cur is built from
// the union of every league's slots, so without the filter a league with no K
// slot would receive a K role change because another league rosters kickers.
func detectLeaguePickups(prev, cur dynasty.PlayerSnapshot, lc leagueContext, in pickupLeagueInputs, players map[string]sleeper.Player, bundle *statsguy.Bundle) pickupDetection {
	roles := dynasty.DetectRoleChanges(prev, cur, in.rostered, bundle, lc.Profile.Format)
	roles = filterRolesByPosition(roles, dynasty.RosterablePositions([]sleeper.League{lc.League}))
	drops, chops := dynasty.DetectDrops(in.txns, prev.CapturedAt, in.rostered, players, bundle, lc.Profile.Format, in.names)
	items := dynasty.OrderPickups(dynasty.PickupItems(dynasty.BaselineStamp(prev.CapturedAt), chops, drops, roles), lc.Profile.Superflex)
	return pickupDetection{items: items, roles: len(roles), drops: len(drops), chops: len(chops)}
}

// filterRolesByPosition keeps the role changes at a position the league can
// roster, preserving order.
func filterRolesByPosition(roles []dynasty.RoleChange, positions map[string]bool) []dynasty.RoleChange {
	var out []dynasty.RoleChange
	for _, r := range roles {
		if positions[r.Position] {
			out = append(out, r)
		}
	}
	return out
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
//
// Tail is the number of new items still waiting to be sent after this run's
// digest (fresh items the digest did not carry, or every fresh item when the
// send failed); SendFailed is 1 for a league whose send errored. Neither is
// reporting: shouldAdvancePointer reads them to decide whether the diff
// baseline may move.
type pickupRunResult struct {
	Roles, Drops, Chops, New, Sent, Tail, SendFailed int
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
		total.Tail += r.Tail
		total.SendFailed += r.SendFailed
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
// run.
//
// An item the digest could not fit stays unmarked, but unmarked alone does not
// get it sent later: it gets sent only if the next run detects it again. That
// is why the result reports it (Tail, SendFailed) and runFootballPickups holds
// the snapshot pointer while either is non-zero -- the next run then diffs
// the same baseline, re-detects the same items under the same
// (baseline-scoped) keys, and the markers let it skip what already went out.
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
		// next run -- they are the Tail, which holds the pointer), name it,
		// and send nothing this run.
		warn("%s", unshowableHeadWarning(in.league.Name, fresh[0].Key, in.dryRun))
		if !in.dryRun {
			m.Record(in.league.LeagueID+"-"+fresh[0].Key, []byte(fresh[0].Line))
			res.Tail = len(fresh) - 1
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
		res.SendFailed, res.Tail = 1, len(fresh)
		return res
	}
	res.Sent = 1
	// Mark ONLY the items the digest actually carried. A line the digest
	// refused for space stays unmarked and is reported as the Tail, so the
	// pointer is held and the next run's digest leads with it -- instead of
	// being skipped by an advanced baseline, the silent-loss failure this
	// repo's check -> send -> mark rule exists to prevent.
	for _, it := range fresh[:shown] {
		m.Record(in.league.LeagueID+"-"+it.Key, []byte(it.Line))
	}
	res.Tail = len(fresh) - shown
	return res
}

// unshowableHeadWarning names an item no digest can ever carry. Under
// --dry-run nothing is marked, so the message must not claim it was.
func unshowableHeadWarning(league, key string, dryRun bool) string {
	outcome := "marked without alert"
	if dryRun {
		outcome = "would be marked without alert (dry-run: nothing marked)"
	}
	return fmt.Sprintf("football-pickups: %s: item %s does not fit a digest on its own; %s", league, key, outcome)
}

func sendFootballPickupAlert(ctx context.Context, title, body string) error {
	return notify.Send(ctx, notify.Event{Kind: "waivers", Title: title, Message: body})
}
