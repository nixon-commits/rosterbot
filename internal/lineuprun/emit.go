package lineuprun

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
)

// emitClient is the Fantrax surface the emit phase needs. It is deliberately
// the smallest interface in this package: these two methods are the only ones
// in the whole run that mutate anything outside the process.
type emitClient interface {
	ApplyLineup(period fantrax.DailyPeriod, active []fantrax.PlayerSlot, reserve []string) error
	InvalidatePeriodRosterCache(period fantrax.DailyPeriod) error
}

// EmitInputs is everything the emit phase consumes: the optimized results plus
// the presentation and destination decisions the caller already made.
type EmitInputs struct {
	Results   []dateResult
	MultiDate bool

	// SlotName maps a slot position ID to its display name; PlayerName maps a
	// player ID to a name, seeded from today's rosters and overlaid per date.
	SlotName   map[string]string
	PlayerName map[string]string

	HitterSlots  []fantrax.Slot
	PitcherSlots []fantrax.Slot

	GSBudget     *optimizer.GSBudget
	ShowPipeline bool

	// Snapshot archival. WriteSnapshots is the caller's single resolved
	// decision (see Options.WriteSnapshots).
	WriteSnapshots bool
	SnapshotStore  backtest.SnapshotStore
	SnapshotRoot   string
	// HitterSystem/PitcherSystem are the RESOLVED systems the loads actually
	// used (post RoS-first fallback), per role since rosterbot-5qvs.
	HitterSystem   string
	PitcherSystem  string
	HittersNoData  bool
	PitchersNoData bool

	// Lineup publication. A nil Publisher means "do not publish".
	PublishLineup bool
	Publisher     lineupapi.Publisher

	// HKB is the dynasty age/value enrichment the published JSON carries,
	// keyed by normalized name (see LoadHKBMeta). Nil publishes without it —
	// the caller's soft-fail path, and what every test that does not care
	// about the enrichment passes.
	HKB map[string]lineupapi.Dynasty

	// Cfg supplies the league/team identity the published JSON carries, and
	// DryRun — the difference between printing a plan and enacting it.
	Cfg *config.Config

	Out io.Writer

	// Notify sends a Pushover. Injected rather than called directly so the
	// phase's tests never reach the network; Run passes a closure over the
	// configured credentials.
	Notify func(message string)
}

// Emit is the tail of the run: archive snapshots, publish today's read-only
// lineup JSON, then print and (unless dry-run) apply each date's moves.
//
// # Ordering
//
// The one ordering that is load-bearing — the zero-gain delta check must
// happen before ApplyLineup — is expressed in the types rather than in
// statement order: applyLineupFor requires an applyAuthorization, and the only
// way to obtain one is datePlan.authorize(), which refuses any disposition
// other than movesWorthApplying. See applyAuthorization for why that matters.
//
// Snapshot archival and lineup publication read the same already-computed
// results and write to unrelated destinations, so despite sitting in a fixed
// order here they have no dependency on each other; neither blocks the other on
// failure. They both precede the apply loop simply because they describe what
// the optimizer decided, and should be durable whether or not the apply lands.
func Emit(ft emitClient, in EmitInputs) {
	writeSnapshots(in)
	publishToday(in)

	for _, dr := range in.Results {
		emitDate(ft, in, dr)
	}
}

func writeSnapshots(in EmitInputs) {
	if !in.WriteSnapshots {
		return
	}
	// A caller that asked for snapshots but supplied no store is a wiring bug,
	// not a runtime condition. Say so once, rather than panicking per date —
	// snapshot archival is best-effort here (every failure below is a warning),
	// so a nil dereference would take down a run that was otherwise fine.
	if in.SnapshotStore == nil {
		fmt.Fprintf(in.Out, "  ⚠ snapshot archive skipped: no snapshot store configured\n")
		return
	}
	for _, dr := range in.Results {
		if err := writeProjectionSnapshot(dr, in.HitterSystem, in.PitcherSystem, in.SlotName, in.HittersNoData, in.PitchersNoData, in.SnapshotStore, in.SnapshotRoot); err != nil {
			fmt.Fprintf(in.Out, "  ⚠ snapshot archive failed for %s: %v\n", dr.date.Format("2006-01-02"), err)
		}
	}
}

// publishToday writes the read-only API's lineup JSON for today only. The iOS
// thin client reads this precomputed JSON; the hourly non-dry-run run keeps it
// fresh.
//
// A dry run applied nothing, so it publishes to the PREVIEW key instead of
// overwriting the applied-lineup snapshot. Before that split a dry run
// published nothing at all, which made the app's Optimize button a silent
// no-op in every Debug build — APIClient.guardedParams forces dry_run there,
// so the one path a developer can actually exercise produced no observable
// result (rbapp-c56).
//
// --publish-lineup keeps its original narrower meaning: an explicit opt-in for
// a dry run to write the REAL key, so `rosterbot serve` has something to serve
// locally without a live Fantrax mutation.
func publishToday(in EmitInputs) {
	preview := in.Cfg.DryRun && !in.PublishLineup
	for _, dr := range in.Results {
		if !dr.isToday {
			continue
		}
		if err := publishLineup(dr, in.Cfg, in.HitterSlots, in.PitcherSlots, in.HKB, in.Publisher, time.Now(), preview); err != nil {
			fmt.Fprintf(in.Out, "  ⚠ lineup publish failed: %v\n", err)
		}
		return
	}
}

// emitDate prints one date's warnings, board and planned moves, then applies
// them if — and only if — the plan authorizes it.
func emitDate(ft emitClient, in EmitInputs, dr dateResult) {
	for _, w := range dr.warnings {
		fmt.Fprintf(in.Out, "  ⚠ %s\n", w)
	}

	renderDateResult(in.Out, dr, in.MultiDate, in.SlotName, in.ShowPipeline, in.GSBudget)

	plan := planDate(dr, in.PlayerName)
	if plan.Disposition == movesNone {
		fmt.Fprintln(in.Out, "\n  No changes needed.")
		return
	}

	renderPlannedMoves(in.Out, plan, in.SlotName)

	// A zero-gain payload that IS submitted looks like a regression of the
	// rosterbot-6i4 guard unless the run says why, so name the players whose
	// slots forced it.
	if plan.Required {
		names := make([]string, 0, len(plan.RequiredPlayerIDs))
		for _, id := range plan.RequiredPlayerIDs {
			if n := plan.Names[id]; n != "" {
				names = append(names, n)
			} else {
				names = append(names, id)
			}
		}
		fmt.Fprintf(in.Out, "\n  ⚠ Required slot fix: %s in a slot they are no longer eligible for — applying despite ≈0 gain.\n",
			strings.Join(names, ", "))
	}

	if plan.Disposition == movesZeroGain {
		fmt.Fprintln(in.Out, "\n  Net gain ≈ 0 — skipping apply (no change in who plays).")
		return
	}
	if in.Cfg.DryRun {
		fmt.Fprintln(in.Out, "\n[DRY RUN] No changes applied.")
		return
	}

	auth, ok := plan.authorize(in.Cfg.AutoApply)
	if !ok {
		if !in.Cfg.AutoApply {
			// Said out loud, every time. A bot that silently declines to do the
			// thing it was asked to do is indistinguishable from a broken one,
			// and this is the line that explains an unchanged roster.
			fmt.Fprintln(in.Out, "\n  Proposed only: auto-apply is off for this account, "+
				"so no changes were written to the roster.")
			return
		}
		// Otherwise unreachable: the dispositions that fail authorize()
		// returned above. Kept as a guard rather than an assumption, since this
		// is the branch that writes to the live roster.
		return
	}
	applyLineupFor(ft, in, dr, auth)
}

// --- The plan ---

// moveDisposition is what the delta check concluded about one date's combined
// hitter+pitcher move set.
type moveDisposition int

const (
	// movesNone: the optimizer produced no changes for this date.
	movesNone moveDisposition = iota
	// movesZeroGain: changes exist but net out to ~0 points. The optimizer can
	// construct cosmetic swaps among equally-valued bench players (two
	// zero-projection players trading UT slots); Fantrax rejects a payload
	// atomically if any player in it is per-player-locked, so submitting one
	// risks dropping genuinely valuable moves for no gain.
	movesZeroGain
	// movesWorthApplying: a real net gain. The only disposition authorize()
	// will issue an applyAuthorization for.
	movesWorthApplying
)

// datePlan is one date's analysed move set: what to change, what it is worth,
// and the lookup tables needed to describe it to a human.
type datePlan struct {
	Disposition moveDisposition
	Delta       float64
	Activate    []fantrax.PlayerSlot
	Bench       []string

	// Names resolves player IDs for this date specifically. Pts is the
	// effective point value each move is credited with.
	Names map[string]string
	Pts   map[string]float64

	// SlotOnly marks the Activate entries whose player already occupied an
	// active slot, i.e. the move changes WHICH slot he fills, not whether he
	// plays. Those entries stay in the payload — Fantrax has to be told the new
	// slot — but they are worth zero points and are rendered as such.
	SlotOnly map[string]bool

	// Required marks a move set that must be submitted even though it is worth
	// nothing, because the CURRENT assignment is not legal — a player occupies
	// a slot his positions no longer permit.
	//
	// Correcting that is slot-only by construction (nobody enters or leaves the
	// lineup), so it nets zero and would otherwise be classified movesZeroGain
	// and dropped on every hourly run forever, with the lineup never converging
	// and nothing saying so (rosterbot-uhq). Names the players so the run
	// output can explain why a zero-gain payload was sent.
	Required          bool
	RequiredPlayerIDs []string
}

// planDate combines the hitter and pitcher move sets, values them, and decides
// whether the result is worth submitting. Pure — no I/O, no client.
func planDate(dr dateResult, playerName map[string]string) datePlan {
	activate := append(append([]fantrax.PlayerSlot(nil), dr.hitterResult.ToActivate...), dr.pitcherResult.ToActivate...)
	bench := append(append([]string(nil), dr.hitterResult.ToBench...), dr.pitcherResult.ToBench...)

	if len(activate) == 0 && len(bench) == 0 {
		return datePlan{Disposition: movesNone}
	}

	// Resolve change-line names from THIS date's rosters. The global playerName
	// map is built from today's base roster only, but future dates optimize
	// against period-specific rosters that can include pitchers not on today's
	// active roster (reserve/IL SPs surfaced for a merged matchup week).
	// Without this, activating such a pitcher renders a blank name in the
	// notification. Seed from the global map, then overlay the date's Scored
	// players, which carry the period-specific names.
	names := make(map[string]string, len(playerName))
	for id, n := range playerName {
		names[id] = n
	}
	for _, sp := range dr.hitterResult.Scored {
		names[sp.Player.ID] = sp.Player.Name
	}
	for _, sp := range dr.pitcherResult.Scored {
		names[sp.Player.ID] = sp.Player.Name
	}

	// Effective-pts lookup for the optimization delta. Hitters contribute full
	// ExpectedPts when they have a game. Pitchers: RPs and confirmed starters
	// contribute full pts; non-starting SPs are discounted by
	// optimizer.NonStarterSPDiscount, using optimizer.SPEligiblePlayer — the
	// same combined SP-eligibility predicate the optimizer itself uses — so
	// the delta reflects what the optimizer actually optimized on, not a
	// hand-copied approximation of it.
	pts := make(map[string]float64)
	for _, sp := range dr.hitterResult.Scored {
		if sp.HasGame {
			pts[sp.Player.ID] = sp.ExpectedPts
		}
	}
	for _, sp := range dr.pitcherResult.Scored {
		if !sp.HasGame {
			continue
		}
		spEligible := optimizer.SPEligiblePlayer(sp.Player)
		if sp.IsStarter || !spEligible {
			pts[sp.Player.ID] = sp.ExpectedPts
		} else {
			pts[sp.Player.ID] = sp.ExpectedPts * optimizer.NonStarterSPDiscount
		}
	}

	// Which activations are slot-only. Taken from the optimizers' own
	// current-assignment maps rather than re-derived from Scored: the predicate
	// for "already in an active slot" is the same one that decided the entry
	// belonged in ToActivate at all, so the two cannot drift apart.
	slotOnly := make(map[string]bool)
	for _, ps := range activate {
		_, wasHitterActive := dr.hitterResult.ActiveBefore[ps.PlayerID]
		_, wasPitcherActive := dr.pitcherResult.ActiveBefore[ps.PlayerID]
		if wasHitterActive || wasPitcherActive {
			slotOnly[ps.PlayerID] = true
		}
	}

	// A lineup whose current assignment is illegal has to be fixed, and the fix
	// is worth exactly zero by the delta above — nobody's active membership
	// changes. Without this the zero-gain guard would suppress it on every run.
	required := append(append([]string(nil), dr.hitterResult.IneligibleBefore...), dr.pitcherResult.IneligibleBefore...)

	delta := combinedMovesDelta(activate, bench, pts, slotOnly)
	disposition := movesWorthApplying
	if isZeroGainDelta(delta) && len(required) == 0 {
		disposition = movesZeroGain
	}

	return datePlan{
		Disposition:       disposition,
		Delta:             delta,
		Activate:          activate,
		Bench:             bench,
		Names:             names,
		Pts:               pts,
		SlotOnly:          slotOnly,
		Required:          len(required) > 0,
		RequiredPlayerIDs: required,
	}
}

// applyAuthorization is proof that a date's move set cleared the zero-gain
// delta check.
//
// applyLineupFor requires one, and the only way to obtain a non-inert value is
// datePlan.authorize() — so "apply without checking the delta" stops being a
// matter of keeping two statements in the right order and becomes something the
// signature will not accept (rosterbot-k0n criterion 2). Within the package a
// struct literal could still forge one, which Go cannot prevent; the zero value
// is therefore inert, and applyLineupFor refuses a nil plan. That refusal is
// pinned by TestApplyLineupFor_RefusesAnUnauthorizedApply.
type applyAuthorization struct{ plan *datePlan }

// authorize issues proof only when BOTH conditions hold: the move set is worth
// applying, and the tenant has consented to the bot writing to their roster.
//
// autoApply is a required argument rather than a field on datePlan so that a
// caller cannot obtain an authorization without having stated a position on
// consent. It is the same reasoning that made the delta check a signature
// requirement instead of a statement above the call: the branch this guards
// writes to somebody's real team.
//
// Until rosterbot-18wq this second condition did not exist. auto_apply was
// documented as "the only field that decides whether this bot writes to" a
// roster, every creation path set it false deliberately, and nothing read it —
// so propose-only was a claim, and a second manager connecting would have had
// their lineup applied without ever opting in.
func (p datePlan) authorize(autoApply bool) (applyAuthorization, bool) {
	if p.Disposition != movesWorthApplying {
		return applyAuthorization{}, false
	}
	if !autoApply {
		return applyAuthorization{}, false
	}
	return applyAuthorization{plan: &p}, true
}

// --- Rendering and applying ---

func renderPlannedMoves(out io.Writer, plan datePlan, slotName map[string]string) {
	fmt.Fprintf(out, "\n  Changes (%+.2f pts) %s\n", plan.Delta, strings.Repeat("─", 35))
	for _, ps := range plan.Activate {
		// ↔ is a slot change for a player who was already active: submitted, but
		// worth nothing, so the lines sum to the header.
		marker, gain := "↑", plan.Pts[ps.PlayerID]
		if plan.SlotOnly[ps.PlayerID] {
			marker, gain = "↔", 0
		}
		fmt.Fprintf(out, "    %s %-24s → %-4s  %+6.2f\n", marker, plan.Names[ps.PlayerID], slotName[ps.PosID], gain)
	}
	for _, id := range plan.Bench {
		fmt.Fprintf(out, "    ↓ %-24s → BN    %+6.2f\n", plan.Names[id], -plan.Pts[id])
	}
}

// applyLineupFor submits one date's authorized moves to Fantrax.
//
// An apply failure is logged and notified but not returned: aborting here would
// drop any subsequent dates' work on a multi-date run and turn a partial
// success into a failed run with no daily summary.
func applyLineupFor(ft emitClient, in EmitInputs, dr dateResult, auth applyAuthorization) {
	if auth.plan == nil {
		fmt.Fprintf(in.Out, "  ⚠ internal: unauthorized apply for %s — skipped\n", dr.date.Format("2006-01-02"))
		return
	}
	plan := auth.plan

	dateKey := dr.date.Format("2006-01-02")
	if dr.period == 0 && !dr.isToday {
		fmt.Fprintf(in.Out, "\n[SKIP] No scoring period found for %s — changes not applied.\n", dateKey)
		return
	}

	// Sequential — the Fantrax apply API is not concurrent-safe.
	fmt.Fprintf(in.Out, "\nApplying lineup for %s (period %d)...\n", dateKey, dr.period)
	if err := ft.ApplyLineup(dr.period, plan.Activate, plan.Bench); err != nil {
		fmt.Fprintf(in.Out, "  ⚠ apply lineup failed for %s: %v\n", dateKey, err)
		in.Notify(fmt.Sprintf("⚠ %s: apply failed — %v", dr.date.Format("Mon Jan 2"), err))
		return
	}
	fmt.Fprintln(in.Out, "Lineup applied successfully.")
	// A failed invalidation cannot be recovered from here — the apply has
	// already landed and is not undoable — so it is printed rather than acted
	// on. What it costs is the next run within the 15-minute todayTTL reading
	// the pre-apply snapshot and re-proposing moves that are already in place;
	// naming it here is what separates that from the unexplained churn
	// rosterbot-sza presented as. No Pushover: the window is short and
	// self-healing, and the operator has no action to take.
	if err := ft.InvalidatePeriodRosterCache(dr.period); err != nil {
		fmt.Fprintf(in.Out, "  ⚠ roster cache not invalidated for %s: %v — the next run may read the pre-apply roster\n", dateKey, err)
	}

	in.Notify(applySummary(dr, *plan, in.SlotName))
}

// applySummary is the Pushover body for a successful apply: what changed, worth
// how much, one line per move. Pure, so its wording is testable.
func applySummary(dr dateResult, plan datePlan, slotName map[string]string) string {
	nHitter := len(dr.hitterResult.ToActivate) + len(dr.hitterResult.ToBench)
	nPitcher := len(dr.pitcherResult.ToActivate) + len(dr.pitcherResult.ToBench)
	var parts []string
	if nHitter > 0 {
		parts = append(parts, fmt.Sprintf("%d hitter", nHitter))
	}
	if nPitcher > 0 {
		parts = append(parts, fmt.Sprintf("%d pitcher", nPitcher))
	}

	summary := fmt.Sprintf("%s: %s changes (%+.2f pts)",
		dr.date.Format("Mon Jan 2"), strings.Join(parts, " + "), plan.Delta)
	for _, ps := range plan.Activate {
		marker := "↑"
		if plan.SlotOnly[ps.PlayerID] {
			marker = "↔"
		}
		summary += fmt.Sprintf("\n  %s %s → %s", marker, plan.Names[ps.PlayerID], slotName[ps.PosID])
	}
	for _, id := range plan.Bench {
		summary += fmt.Sprintf("\n  ↓ %s → BN", plan.Names[id])
	}
	return summary
}
