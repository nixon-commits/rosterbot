package lineuprun

import (
	"strings"
	"testing"
)

// TestAuthorize_RequiresTenantConsent is rosterbot-18wq.
//
// auto_apply is documented as the only field that decides whether this bot
// writes to a roster, and every user-creation path sets it false on purpose.
// Nothing read it. Propose-only — decision 11 of the multiuser design — was
// therefore a claim rather than a behaviour, and the moment a second manager
// connected the bot would have written to their team without them opting in.
//
// The gate goes here because datePlan.authorize() already exists to make
// "apply without checking" a signature error rather than a matter of statement
// order. Consent is the second thing an apply must prove, and it belongs in the
// same proof.
func TestAuthorize_RequiresTenantConsent(t *testing.T) {
	worth := datePlan{Disposition: movesWorthApplying}

	if _, ok := worth.authorize(false); ok {
		t.Error("authorized an apply for a tenant who has not enabled auto_apply; " +
			"the bot would write to a roster nobody consented to")
	}
	if _, ok := worth.authorize(true); !ok {
		t.Error("refused an apply for a consenting tenant with a real net gain")
	}
}

// TestAuthorize_ConsentDoesNotOverrideTheZeroGainCheck. The two conditions are
// independent and both must hold: consent does not make a worthless swap worth
// submitting. Fantrax rejects a payload atomically if any player in it is
// locked, so a zero-gain apply can drop genuinely valuable moves alongside it.
func TestAuthorize_ConsentDoesNotOverrideTheZeroGainCheck(t *testing.T) {
	for _, d := range []moveDisposition{movesZeroGain, movesNone} {
		p := datePlan{Disposition: d}
		if _, ok := p.authorize(true); ok {
			t.Errorf("disposition %v authorized despite failing the delta check", d)
		}
	}
}

// TestAuthorize_ZeroValueIsInert keeps the existing property: Go cannot stop an
// in-package struct literal forging an applyAuthorization, so the zero value
// must be useless and applyLineupFor must refuse it.
func TestAuthorize_ZeroValueIsInert(t *testing.T) {
	var a applyAuthorization
	if a.plan != nil {
		t.Error("the zero applyAuthorization carries a plan")
	}
}

// TestEmit_ProposesButDoesNotApplyWithoutConsent is the wiring test, and it is
// the one that matters more than the unit test above.
//
// Everything found in this codebase this week was a control that was CORRECT
// and simply never consulted: a sealed session with no reader, a ledger reader
// looking where its writer no longer wrote, a guard test CI never ran. A
// correct authorize() that emitDate forgot to pass consent to would be the same
// bug wearing this ticket's name. So this drives the real Emit and asserts on
// the roster write itself.
func TestEmit_ProposesButDoesNotApplyWithoutConsent(t *testing.T) {
	h := newEmitHarness()
	in := h.inputs([]dateResult{movingResult()}, false)
	in.Cfg.AutoApply = false

	Emit(h.ft, in)

	if len(h.ft.applies) != 0 {
		t.Fatalf("applied a lineup for a tenant who never enabled auto_apply: %+v", h.ft.applies)
	}
	if !strings.Contains(h.out.String(), "Proposed only") {
		t.Errorf("no explanation printed; an unchanged roster with a silent log is "+
			"indistinguishable from a broken bot:\n%s", h.out.String())
	}
}

// TestEmit_AppliesWithConsent is the other half: the gate must not be so eager
// that a consenting tenant stops being managed.
func TestEmit_AppliesWithConsent(t *testing.T) {
	h := newEmitHarness()
	in := h.inputs([]dateResult{movingResult()}, false)
	in.Cfg.AutoApply = true

	Emit(h.ft, in)

	if len(h.ft.applies) != 1 {
		t.Fatalf("a consenting tenant's lineup was not applied: %+v", h.ft.applies)
	}
}
