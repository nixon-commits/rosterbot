package lineuprun

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
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
		// AutoApply true because these fixtures exercise the APPLY path.
		// config.Load defaults it true for the single-tenant env deployment;
		// only a tenant's own profile lowers it (rosterbot-18wq). A struct
		// literal gets Go's false, so stating it here is what keeps these tests
		// about what they are actually testing.
		Cfg:    &config.Config{DryRun: dryRun, LeagueID: "L1", TeamID: "T1", AutoApply: true},
		Out:    &h.out,
		Notify: func(m string) { h.notify = append(h.notify, m) },
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

// liveSlotMoveResult reproduces the 2026-08-14 case in rosterbot-6i4: an
// already-active hitter slides OF→3B so a bench bat can take the OF slot, and a
// third hitter drops to the bench. Only two of the three moves change who
// scores; the reported gain was +5.28 when the truth is +0.04.
func liveSlotMoveResult() dateResult {
	dr := goldenResult()
	dr.hitterResult.Scored = []optimizer.ScoredPlayer{
		scoredHitter("Antonacci", "CWS", "012", 5.24, true, true),
		scoredHitter("Bauers", "MIL", "", 4.32, false, true),
		scoredHitter("Genao", "CLE", "012", 4.28, true, true),
	}
	dr.pitcherResult.Scored = nil
	dr.hitterResult.ToActivate = []fantrax.PlayerSlot{
		{PlayerID: "Antonacci", PosID: "004"},
		{PlayerID: "Bauers", PosID: "012"},
	}
	dr.hitterResult.ToBench = []string{"Genao"}
	dr.hitterResult.ActiveBefore = map[string]string{"Antonacci": "012", "Genao": "012"}
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

// rosterbot-6i4: the delta must measure change in ACTIVE MEMBERSHIP, not change
// in slot assignment. Two of these three moves change who scores; the third only
// moves a player who was already scoring.
func TestPlanDate_SlotOnlyMoveEarnsNothing(t *testing.T) {
	got := planDate(liveSlotMoveResult(), nil)

	if want := 4.32 - 4.28; got.Delta < want-1e-9 || got.Delta > want+1e-9 {
		t.Errorf("delta = %v, want %v (the slot mover's 5.24 must not be credited)", got.Delta, want)
	}
	if got.Disposition != movesWorthApplying {
		t.Errorf("disposition = %v, want movesWorthApplying — the genuine swap still stands", got.Disposition)
	}
	if !got.SlotOnly["Antonacci"] {
		t.Error("the already-active player should be marked slot-only")
	}
	if got.SlotOnly["Bauers"] {
		t.Error("a bench→active promotion is not a slot-only move")
	}
	// The payload still needs every entry — Fantrax has to be told the new slot.
	if len(got.Activate) != 2 {
		t.Errorf("activate payload = %v, want both entries retained", got.Activate)
	}
}

// The headline consequence: a move set that only reshuffles slots is worth
// nothing and must not reach the live roster.
func TestPlanDate_PureSlotShuffleIsZeroGain(t *testing.T) {
	dr := goldenResult()
	dr.hitterResult.Scored = []optimizer.ScoredPlayer{
		scoredHitter("Shuffle A", "NYY", "012", 5.00, true, true),
		scoredHitter("Shuffle B", "BOS", "014", 3.00, true, true),
	}
	dr.pitcherResult.Scored = nil
	dr.hitterResult.ToActivate = []fantrax.PlayerSlot{
		{PlayerID: "Shuffle A", PosID: "014"},
		{PlayerID: "Shuffle B", PosID: "012"},
	}
	dr.hitterResult.ActiveBefore = map[string]string{"Shuffle A": "012", "Shuffle B": "014"}

	got := planDate(dr, nil)
	if got.Disposition != movesZeroGain {
		t.Errorf("disposition = %v (delta %v), want movesZeroGain", got.Disposition, got.Delta)
	}
}

// The pitcher optimizer emits slot-only activations from the identical
// `currentAssign[id] != PosID` test, so the same rule has to hold there.
func TestPlanDate_PitcherSlotOnlyMoveEarnsNothing(t *testing.T) {
	dr := goldenResult()
	dr.hitterResult = optimizer.Result{}
	dr.pitcherResult.Scored = []optimizer.ScoredPitcher{
		scoredPitcher("Settled Ace", "LAD", "020", "SP", 22.00, true, true, true),
	}
	dr.pitcherResult.ToActivate = []fantrax.PlayerSlot{{PlayerID: "Settled Ace", PosID: "021"}}
	dr.pitcherResult.ActiveBefore = map[string]string{"Settled Ace": "020"}

	got := planDate(dr, nil)
	if got.Delta != 0 {
		t.Errorf("delta = %v, want 0 — the ace was already in an active slot", got.Delta)
	}
	if got.Disposition != movesZeroGain {
		t.Errorf("disposition = %v, want movesZeroGain", got.Disposition)
	}
}

// The wiring, end to end: the map the OPTIMIZER populates has to be the one
// planDate reads. Every other test here hand-builds ActiveBefore, so none of
// them would notice if the optimizer stopped setting it — the delta would
// silently revert to crediting slot shuffles at full value, which is precisely
// how this bug stayed invisible the first time. This test drives the real
// optimizer instead, so the two sides cannot drift apart.
func TestPlanDate_ReadsTheRealOptimizersActiveBeforeMap(t *testing.T) {
	scoring := fantrax.ScoringWeights{"HR": 4.0}
	src := stubHitterSource{byName: map[string]*projections.Projection{
		"Slot Mover": {G: 100, HR: 30},
	}}
	// One player, already active at OF, and one 1B slot he is also eligible for:
	// the optimizer must move him, and the move must be worth nothing.
	roster := []fantrax.Player{{
		ID: "mover", Name: "Slot Mover", MLBTeam: "NYY",
		Positions: []string{"002", "012"}, Status: "Active", RosterPosition: "012",
	}}
	result := optimizer.OptimizeLineup(roster, map[string]bool{"NYY": true}, src, scoring,
		[]fantrax.Slot{{PosID: "002", PosName: "1B"}}, nil)

	if len(result.ToActivate) != 1 {
		t.Fatalf("precondition: expected the optimizer to emit a slot-only move, got %+v", result.ToActivate)
	}

	got := planDate(dateResult{hitterResult: result}, nil)
	if got.Delta != 0 {
		t.Errorf("delta = %v, want 0 — the player was already in an active slot", got.Delta)
	}
	if got.Disposition != movesZeroGain {
		t.Errorf("disposition = %v, want movesZeroGain", got.Disposition)
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
			// Consent held true so this case tests the DELTA dimension alone;
			// the consent dimension is TestAuthorize_RequiresTenantConsent.
			auth, ok := tc.plan.authorize(true)
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
	if !strings.Contains(h.out.String(), "Net gain ≈ 0 — skipping apply (no change in who plays).") {
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

// The printed move lines have to add up to the printed header, or the fix to
// the total (rosterbot-6i4) just relocates the inflation one line down: a
// slot-only move showing +5.24 under a +0.04 heading reads as an arithmetic
// error rather than as a move that is genuinely worth nothing.
func TestRenderPlannedMoves_SlotOnlyMoveShowsNoGain(t *testing.T) {
	var buf bytes.Buffer
	renderPlannedMoves(&buf, planDate(liveSlotMoveResult(), nil), goldenSlotNames())
	out := buf.String()

	if !strings.Contains(out, "Changes (+0.04 pts)") {
		t.Errorf("header should carry the true net gain:\n%s", out)
	}
	if strings.Contains(out, "+5.24") {
		t.Errorf("the slot mover's projection is still being credited:\n%s", out)
	}
	if !strings.Contains(out, "↔ Antonacci") {
		t.Errorf("a slot change should be marked as one, not as an activation:\n%s", out)
	}
	if !strings.Contains(out, "↑ Bauers") {
		t.Errorf("a genuine promotion should still render as one:\n%s", out)
	}
}

// The Pushover body is the same statement in a different medium and must not
// disagree with the terminal.
func TestApplySummary_MarksSlotOnlyMoves(t *testing.T) {
	dr := liveSlotMoveResult()
	got := applySummary(dr, planDate(dr, nil), goldenSlotNames())

	if !strings.Contains(got, "↔ Antonacci") {
		t.Errorf("slot change not marked in the notification:\n%s", got)
	}
	if !strings.Contains(got, "(+0.04 pts)") {
		t.Errorf("notification should carry the true net gain:\n%s", got)
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

// requiredSlotMoveResult is a move set consisting ONLY of a slot change, where
// the current assignment is no longer legal: the player sits in a slot his
// positions do not permit. Nobody enters or leaves the lineup, so the delta is
// correctly 0 — and 0 is precisely why it must not be suppressed.
func requiredSlotMoveResult() dateResult {
	dr := goldenResult()
	dr.hitterResult.Scored = []optimizer.ScoredPlayer{
		scoredHitter("Stranded", "NYY", "001", 6.00, true, true),
	}
	dr.pitcherResult.Scored = nil
	// Moving from the 1B slot he can no longer fill to the C slot he can.
	dr.hitterResult.ToActivate = []fantrax.PlayerSlot{{PlayerID: "Stranded", PosID: "001"}}
	dr.hitterResult.ToBench = nil
	dr.hitterResult.ActiveBefore = map[string]string{"Stranded": "002"}
	dr.hitterResult.IneligibleBefore = []string{"Stranded"}
	return dr
}

// rosterbot-6i4 correctly made a slot-only move set worth 0, and rosterbot-uhq
// is the residual: the suppression was unconditional. A shuffle that is
// REQUIRED — a player sitting in a slot he is no longer eligible for — nets 0
// too, so it was suppressed on every hourly run forever and the lineup never
// converged.
func TestPlanDate_RequiredSlotShuffleIsNotSuppressed(t *testing.T) {
	got := planDate(requiredSlotMoveResult(), nil)

	// Precondition: this really is the zero-gain shape, not a move that escapes
	// suppression by being worth something.
	if !isZeroGainDelta(got.Delta) {
		t.Fatalf("fixture must net zero to exercise the guard, got delta %v", got.Delta)
	}
	if got.Disposition != movesWorthApplying {
		t.Errorf("disposition = %v, want movesWorthApplying: the current assignment is "+
			"ILLEGAL, so this shuffle is required, not cosmetic — suppressing it leaves "+
			"the lineup permanently unconverged", got.Disposition)
	}
	if !got.Required {
		t.Error("plan should be marked Required so the run output can say why a " +
			"zero-gain move set was submitted")
	}
}

// The case rosterbot-6i4 was actually about must still be suppressed: an equal
// swap among legally assigned players is worth nothing and risks Fantrax
// rejecting a whole payload atomically over a per-player lock.
func TestPlanDate_CosmeticShuffleIsStillSuppressed(t *testing.T) {
	got := planDate(cosmeticResult(), nil)
	if got.Disposition != movesZeroGain {
		t.Errorf("disposition = %v, want movesZeroGain", got.Disposition)
	}
	if got.Required {
		t.Error("a legal lineup must never be marked Required")
	}
}

// The run output has to explain a zero-gain payload that WAS submitted, or the
// next person reading it sees the rosterbot-6i4 guard apparently regressing.
func TestEmitDate_RequiredSlotFixExplainsItselfInTheOutput(t *testing.T) {
	h := newEmitHarness()
	dr := requiredSlotMoveResult()
	emitDate(h.ft, h.inputs([]dateResult{dr}, true), dr)
	out := h.out.String()
	if !strings.Contains(out, "Required slot fix") {
		t.Errorf("run output does not explain the required slot fix:\n%s", out)
	}
	if !strings.Contains(out, "Stranded") {
		t.Errorf("run output does not name the stranded player:\n%s", out)
	}
	if strings.Contains(out, "skipping apply") {
		t.Errorf("required fix was suppressed:\n%s", out)
	}
}

// The converse: a cosmetic shuffle must not raise the alarm.
func TestEmitDate_CosmeticShuffleStaysQuiet(t *testing.T) {
	h := newEmitHarness()
	dr := cosmeticResult()
	emitDate(h.ft, h.inputs([]dateResult{dr}, true), dr)
	out := h.out.String()
	if strings.Contains(out, "Required slot fix") {
		t.Errorf("a legal lineup raised the required-fix warning:\n%s", out)
	}
	if !strings.Contains(out, "skipping apply") {
		t.Errorf("cosmetic shuffle should still be suppressed:\n%s", out)
	}
}
