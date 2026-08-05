package lineuprun

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/pmurley/go-fantrax/auth_client"
)

// fakeEmitClient records every remote mutation the emit phase attempts. The
// whole point of this phase's tests is what does and does not reach these two
// methods — they are the only calls in the run that change state outside the
// process.
type fakeEmitClient struct {
	applies  []appliedLineup
	applyErr error

	invalidated []fantrax.DailyPeriod
}

type appliedLineup struct {
	period   fantrax.DailyPeriod
	activate []fantrax.PlayerSlot
	bench    []string
}

func (f *fakeEmitClient) ApplyLineup(period fantrax.DailyPeriod, active []fantrax.PlayerSlot, reserve []string) error {
	f.applies = append(f.applies, appliedLineup{period: period, activate: active, bench: reserve})
	return f.applyErr
}

func (f *fakeEmitClient) InvalidatePeriodRosterCache(period fantrax.DailyPeriod) {
	f.invalidated = append(f.invalidated, period)
}

// emitHarness bundles the client, the captured output and the captured
// notifications, since almost every assertion below looks at more than one.
type emitHarness struct {
	ft     *fakeEmitClient
	out    bytes.Buffer
	notify []string
}

func newEmitHarness() *emitHarness { return &emitHarness{ft: &fakeEmitClient{}} }

func (h *emitHarness) inputs(results []dateResult, dryRun bool) EmitInputs {
	return EmitInputs{
		Results:    results,
		SlotName:   goldenSlotNames(),
		PlayerName: map[string]string{},
		Cfg:        &config.Config{DryRun: dryRun, LeagueID: "L1", TeamID: "T1"},
		Out:        &h.out,
		Notify:     func(m string) { h.notify = append(h.notify, m) },
	}
}

// movingResult is a date whose optimizer produced a real, positive-value swap:
// activate a 12-point hitter, bench a 2-point one.
func movingResult() dateResult {
	dr := goldenResult()
	dr.hitterResult.ToActivate = []fantrax.PlayerSlot{{PlayerID: "Star Hitter", PosID: "012"}}
	dr.hitterResult.ToBench = []string{"Bench Bat"}
	return dr
}

// cosmeticResult is the shape the zero-gain guard exists for: two equally
// valued players trading places, netting zero.
func cosmeticResult() dateResult {
	dr := goldenResult()
	// Both carry identical ExpectedPts and both have a game, so the delta is
	// exactly zero.
	dr.hitterResult.Scored = []optimizer.ScoredPlayer{
		scoredHitter("Swap A", "NYY", "012", 5.00, true, true),
		scoredHitter("Swap B", "BOS", "", 5.00, false, true),
	}
	dr.pitcherResult.Scored = nil
	dr.hitterResult.ToActivate = []fantrax.PlayerSlot{{PlayerID: "Swap B", PosID: "012"}}
	dr.hitterResult.ToBench = []string{"Swap A"}
	return dr
}

// --- planDate: pure ---

func TestPlanDate_NoMovesIsMovesNone(t *testing.T) {
	got := planDate(goldenResult(), nil)
	if got.Disposition != movesNone {
		t.Errorf("disposition = %v, want movesNone", got.Disposition)
	}
}

func TestPlanDate_RealGainIsWorthApplying(t *testing.T) {
	got := planDate(movingResult(), nil)
	if got.Disposition != movesWorthApplying {
		t.Fatalf("disposition = %v, want movesWorthApplying", got.Disposition)
	}
	// 12.40 activated − 4.50 benched.
	if want := 12.40 - 4.50; got.Delta < want-1e-9 || got.Delta > want+1e-9 {
		t.Errorf("delta = %v, want %v", got.Delta, want)
	}
}

func TestPlanDate_EquallyValuedSwapIsZeroGain(t *testing.T) {
	got := planDate(cosmeticResult(), nil)
	if got.Disposition != movesZeroGain {
		t.Errorf("disposition = %v (delta %v), want movesZeroGain", got.Disposition, got.Delta)
	}
}

// A non-starting SP is credited at the optimizer's NonStarterSPDiscount, so the delta
// reflects what the optimizer actually optimized on rather than the raw
// projection.
func TestPlanDate_NonStartingSPIsDiscounted(t *testing.T) {
	dr := goldenResult()
	dr.hitterResult.Scored = nil
	dr.pitcherResult.Scored = []optimizer.ScoredPitcher{
		scoredPitcher("Idle Ace", "LAD", "020", "SP", 20.00, false, true, false),
	}
	dr.pitcherResult.ToActivate = []fantrax.PlayerSlot{{PlayerID: "Idle Ace", PosID: "020"}}

	got := planDate(dr, nil)
	want := 20.00 * optimizer.NonStarterSPDiscount
	if got.Delta < want-1e-9 || got.Delta > want+1e-9 {
		t.Errorf("delta = %v, want %v (20.00 × optimizer.NonStarterSPDiscount)", got.Delta, want)
	}
}

// A pitcher can be SP-eligible via Player.Positions (the "015" position ID)
// without PosShortNames containing "SP" — e.g. a league using a single
// generic "P" slot, where PosShortNames may read "P" instead of "SP". The
// optimizer's own discount test (pitcher_lineup.go) matches on
// optimizer.IsSPEligible(Positions) OR PosShortNames contains "SP"; this
// pins that emit's delta calculation uses the identical combined predicate,
// so the two never disagree about whether a non-starter is discounted.
func TestPlanDate_NonStartingSPEligibleViaPositionsIsDiscounted(t *testing.T) {
	dr := goldenResult()
	dr.hitterResult.Scored = nil
	idleAce := scoredPitcher("Idle Ace", "LAD", "020", "P", 20.00, false, true, false)
	idleAce.Player.Positions = []string{auth_client.PosSP}
	dr.pitcherResult.Scored = []optimizer.ScoredPitcher{idleAce}
	dr.pitcherResult.ToActivate = []fantrax.PlayerSlot{{PlayerID: "Idle Ace", PosID: "020"}}

	got := planDate(dr, nil)
	want := 20.00 * optimizer.NonStarterSPDiscount
	if got.Delta < want-1e-9 || got.Delta > want+1e-9 {
		t.Errorf("delta = %v, want %v (20.00 × optimizer.NonStarterSPDiscount, eligible via Positions not PosShortNames)", got.Delta, want)
	}
}

// Future dates optimize against period-specific rosters that can surface
// players absent from today's. Their names must still resolve, or the apply
// notification renders a blank.
func TestPlanDate_ResolvesNamesFromThisDatesRoster(t *testing.T) {
	dr := movingResult()
	got := planDate(dr, map[string]string{"someone-else": "Yesterday Guy"})

	if got.Names["Star Hitter"] != "Star Hitter" {
		t.Errorf("this date's player name did not resolve: %v", got.Names)
	}
	if got.Names["someone-else"] != "Yesterday Guy" {
		t.Error("the global name map should still seed the lookup")
	}
}

func TestPlanDate_DoesNotAliasTheOptimizerSlices(t *testing.T) {
	dr := movingResult()
	plan := planDate(dr, nil)
	plan.Activate = append(plan.Activate, fantrax.PlayerSlot{PlayerID: "intruder"})

	if len(dr.hitterResult.ToActivate) != 1 {
		t.Errorf("planning wrote back into the optimizer's slice: %v", dr.hitterResult.ToActivate)
	}
}

// --- Authorization: the ordering constraint, expressed in types ---

// The criterion this phase exists for (rosterbot-k0n #2): apply is gated on a
// value only the delta check can produce.
func TestAuthorize_OnlyIssuedForAWorthwhileGain(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan datePlan
		want bool
	}{
		{"no moves", datePlan{Disposition: movesNone}, false},
		{"cosmetic swap", datePlan{Disposition: movesZeroGain}, false},
		{"real gain", datePlan{Disposition: movesWorthApplying}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, ok := tc.plan.authorize()
			if ok != tc.want {
				t.Fatalf("authorize() ok = %v, want %v", ok, tc.want)
			}
			if ok != (auth.plan != nil) {
				t.Errorf("an unauthorized value must be inert, got plan=%v", auth.plan)
			}
		})
	}
}

// The zero value cannot be used to sneak past the check. Go cannot stop a
// struct literal inside the package from forging one, so the guard is a runtime
// refusal — pinned here because it is the last line before the live roster.
func TestApplyLineupFor_RefusesAnUnauthorizedApply(t *testing.T) {
	h := newEmitHarness()
	dr := movingResult()

	applyLineupFor(h.ft, h.inputs([]dateResult{dr}, false), dr, applyAuthorization{})

	if len(h.ft.applies) != 0 {
		t.Fatalf("an unauthorized apply reached Fantrax: %+v", h.ft.applies)
	}
	if !strings.Contains(h.out.String(), "unauthorized apply") {
		t.Errorf("the refusal should be visible in the output, got %q", h.out.String())
	}
}

// --- Emit end to end ---

// The headline safety property: a cosmetic swap is printed but never submitted.
// Fantrax rejects a payload atomically if any player in it is locked, so
// submitting a zero-gain set risks dropping valuable moves for nothing.
func TestEmit_ZeroGainMoveSetIsNeverApplied(t *testing.T) {
	h := newEmitHarness()

	Emit(h.ft, h.inputs([]dateResult{cosmeticResult()}, false))

	if len(h.ft.applies) != 0 {
		t.Fatalf("a zero-gain move set was applied: %+v", h.ft.applies)
	}
	if len(h.notify) != 0 {
		t.Errorf("nothing was applied, so nothing should notify: %v", h.notify)
	}
	if !strings.Contains(h.out.String(), "Net gain ≈ 0 — skipping apply (cosmetic swap).") {
		t.Errorf("expected the skip to be explained in the output, got:\n%s", h.out.String())
	}
}

func TestEmit_DryRunNeverApplies(t *testing.T) {
	h := newEmitHarness()

	Emit(h.ft, h.inputs([]dateResult{movingResult()}, true))

	if len(h.ft.applies) != 0 {
		t.Fatalf("dry-run applied a lineup: %+v", h.ft.applies)
	}
	if len(h.notify) != 0 {
		t.Errorf("dry-run should not notify: %v", h.notify)
	}
	if !strings.Contains(h.out.String(), "[DRY RUN] No changes applied.") {
		t.Errorf("missing dry-run notice in:\n%s", h.out.String())
	}
}

func TestEmit_AppliesAndNotifiesOnARealGain(t *testing.T) {
	h := newEmitHarness()
	dr := movingResult()

	Emit(h.ft, h.inputs([]dateResult{dr}, false))

	if len(h.ft.applies) != 1 {
		t.Fatalf("expected exactly one apply, got %+v", h.ft.applies)
	}
	got := h.ft.applies[0]
	if got.period != dr.period {
		t.Errorf("applied against period %d, want %d", got.period, dr.period)
	}
	if len(got.activate) != 1 || got.activate[0].PlayerID != "Star Hitter" {
		t.Errorf("activate = %+v", got.activate)
	}
	if len(got.bench) != 1 || got.bench[0] != "Bench Bat" {
		t.Errorf("bench = %+v", got.bench)
	}
	// The period roster cache must be invalidated, or the next date in a
	// multi-date run re-reads the pre-apply snapshot.
	if len(h.ft.invalidated) != 1 || h.ft.invalidated[0] != dr.period {
		t.Errorf("invalidated = %v, want [%d]", h.ft.invalidated, dr.period)
	}
	if len(h.notify) != 1 || !strings.Contains(h.notify[0], "↑ Star Hitter → OF") {
		t.Errorf("notification = %v", h.notify)
	}
}

// An apply failure must not abort the run: a later date's lineup is still worth
// setting, and the daily summary is still worth sending.
func TestEmit_ApplyFailureNotifiesAndContinuesToTheNextDate(t *testing.T) {
	h := newEmitHarness()
	h.ft.applyErr = errors.New("fantrax said no")

	first := movingResult()
	second := movingResult()
	second.date = day(2026, 7, 26)
	second.period = 114
	second.isToday = false

	Emit(h.ft, h.inputs([]dateResult{first, second}, false))

	if len(h.ft.applies) != 2 {
		t.Fatalf("a failure on date one stopped date two: %+v", h.ft.applies)
	}
	if len(h.notify) != 2 {
		t.Fatalf("expected a failure notification per date, got %v", h.notify)
	}
	for _, n := range h.notify {
		if !strings.Contains(n, "apply failed — fantrax said no") {
			t.Errorf("notification does not report the cause: %q", n)
		}
	}
	if len(h.ft.invalidated) != 0 {
		t.Errorf("a failed apply must not invalidate the roster cache: %v", h.ft.invalidated)
	}
}

// A future date whose period could not be resolved is skipped rather than
// applied against period 0 — which Fantrax would interpret as some other day.
func TestEmit_UnresolvedPeriodOnAFutureDateIsSkipped(t *testing.T) {
	h := newEmitHarness()
	dr := movingResult()
	dr.period = 0
	dr.isToday = false

	Emit(h.ft, h.inputs([]dateResult{dr}, false))

	if len(h.ft.applies) != 0 {
		t.Fatalf("applied against an unresolved period: %+v", h.ft.applies)
	}
	if !strings.Contains(h.out.String(), "[SKIP] No scoring period found") {
		t.Errorf("expected a skip notice, got:\n%s", h.out.String())
	}
}

func TestEmit_NoChangesSaysSoAndAppliesNothing(t *testing.T) {
	h := newEmitHarness()

	Emit(h.ft, h.inputs([]dateResult{goldenResult()}, false))

	if len(h.ft.applies) != 0 {
		t.Fatalf("applied with no planned moves: %+v", h.ft.applies)
	}
	if !strings.Contains(h.out.String(), "No changes needed.") {
		t.Errorf("missing the no-changes line in:\n%s", h.out.String())
	}
}

// Per-date warnings from the optimize pass surface above that date's board.
func TestEmit_PrintsPerDateWarnings(t *testing.T) {
	h := newEmitHarness()
	dr := goldenResult()
	dr.warnings = []string{"mlb schedule unavailable — assuming all teams play"}

	Emit(h.ft, h.inputs([]dateResult{dr}, false))

	out := h.out.String()
	if !strings.Contains(out, "⚠ mlb schedule unavailable") {
		t.Errorf("warning not printed:\n%s", out)
	}
	if strings.Index(out, "⚠ mlb schedule") > strings.Index(out, "Hitters ") {
		t.Error("warnings should precede the board")
	}
}

// movingResult activates one hitter and benches another (2 hitter moves); the
// test adds a pitcher bench on top, so the summary must read "2 hitter + 1
// pitcher" — the count is moves, not players promoted.
func TestApplySummary_CountsBothRolesAndListsEveryMove(t *testing.T) {
	dr := movingResult()
	dr.pitcherResult.ToBench = []string{"Rel Iever"}
	plan := planDate(dr, nil)

	got := applySummary(dr, plan, goldenSlotNames())

	for _, want := range []string{"2 hitter + 1 pitcher", "↑ Star Hitter → OF", "↓ Bench Bat → BN", "↓ Rel Iever → BN"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}
