//go:build diag

package fantrax

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// TestDiagPendingTradesDraftPickIdentity captures a LIVE pending-trade offer
// via auth_client.GetLeagueHomeInfoRaw and checks whether the pending-offer
// payload encodes draft-pick identity (year/round/original team) the same
// way the TRADE-view history endpoint does via draftPickDisplayParts
// (rosterbot-uc3, internal/transactions.newDraftPickTradePlayer). Never runs
// in CI.
//
// This settles rosterbot-1o5, which rosterbot-uc3 deliberately left open:
// tradeboard.BuildOffers still falls back to "Draft pick (unidentified)" for
// every pick in a live offer, and at 2026-08-11 investigation time the
// league had zero pending trades — nothing to capture, so the pending
// payload's shape (internal/fantrax.PendingTrade only has
// PlayerName/Position/FromTeam/ToTeam/TradeID, parsed from a scorerMap keyed
// by scorerId — a different response shape than the TRADE-view history
// endpoint) was, and still is, unconfirmed either way.
//
// Prerequisite: a live pending trade offer that includes at least one draft
// pick, visible to the credentialed team (FANTRAX_TEAM_ID) as either the
// source or destination side.
//
//	DIAG_PENDING_TRADES=1 \
//	go test -tags diag -run TestDiagPendingTradesDraftPickIdentity -v ./internal/fantrax/
//
// Optionally set DIAG_PENDING_OUT=/path/to/file.json to save the raw
// pretty-printed response to a file instead of the test log.
//
// The one command Jon runs when a pick-bearing pending offer exists is the
// invocation above (add DIAG_PENDING_OUT to also save the payload). Its
// output — the per-offer summary plus any "candidate draft-pick field(s)"
// lines from scanForDraftPickKeys — is what turns this from "unconfirmed"
// into either "extend PendingTrade and wire tradevalue.NewDraftPickAsset in
// tradeboard.BuildOffers" (fields found) or "the pending offer view genuinely
// omits pick identity, close as not-fixable" (nothing found after checking
// the raw JSON by hand for anything scanForDraftPickKeys' needle list
// missed).
func TestDiagPendingTradesDraftPickIdentity(t *testing.T) {
	if os.Getenv("DIAG_PENDING_TRADES") == "" {
		t.Skip("set DIAG_PENDING_TRADES=1 to run")
	}

	// go test runs in the package dir; the session cookie cache
	// (.fantrax-cache) and the data cache (.cache) both live at the repo root.
	t.Chdir("../..")

	c, err := NewClient(os.Getenv("FANTRAX_LEAGUE_ID"), os.Getenv("FANTRAX_TEAM_ID"))
	if err != nil {
		t.Fatalf("fantrax client: %v", err)
	}

	// Bypass GetPendingTrades' cache (todayTTL) so this always reads the
	// live offer list rather than a copy from an earlier run this hour.
	summary, err := c.fetchPendingTrades()
	if err != nil {
		t.Fatalf("fetch pending trades: %v", err)
	}
	if len(summary) == 0 {
		t.Skip("no pending trades right now — this harness needs a live offer with a pick to capture; rerun once one exists")
	}

	raw, err := c.auth.GetLeagueHomeInfoRaw()
	if err != nil {
		t.Fatalf("get league home info: %v", err)
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatalf("indent raw JSON: %v", err)
	}

	t.Logf("%d pending offer row(s):", len(summary))
	for _, p := range summary {
		t.Logf("  trade=%s %s (%s): %s -> %s", p.TradeID, p.PlayerName, p.Position, p.FromTeam, p.ToTeam)
	}

	candidates := scanForDraftPickKeys(raw)
	if len(candidates) == 0 {
		t.Log("no draft-pick-shaped keys found (needles: draftpick/pick/round/displayparts) — " +
			"either this offer has no pick, or Fantrax names the field something the needle list " +
			"doesn't cover; read the raw JSON below by hand before concluding it's absent")
	} else {
		t.Logf("%d candidate draft-pick field(s):", len(candidates))
		for _, k := range candidates {
			t.Logf("  %s", k)
		}
	}

	if out := os.Getenv("DIAG_PENDING_OUT"); out != "" {
		if err := os.WriteFile(out, pretty.Bytes(), 0o600); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
		t.Logf("wrote raw response to %s", out)
		return
	}
	t.Logf("raw response:\n%s", pretty.String())
}
