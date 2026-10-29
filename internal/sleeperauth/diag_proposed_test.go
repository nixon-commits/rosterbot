//go:build diag

// Live probe for the offers query. Build-tagged so it never runs in CI: it
// needs the operator's SLEEPER_TOKEN and answers a question only live data
// can — does league_transactions(status:"proposed", roster_id) return every
// offer the Sleeper app shows for the operator's roster, and what shape do
// the rows have? Run by the OPERATOR:
//
//	set -a; source .env; set +a
//	DIAG_LEAGUE_ID=<league with a live offer> go test -tags diag -run TestDiagProposed ./internal/sleeperauth/ -v
//
// It prints ids, counts, statuses and raw draft_picks JSON. It never prints
// the token.
package sleeperauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func TestDiagProposed(t *testing.T) {
	c, err := FromEnv()
	if err != nil {
		t.Skipf("%v — set it in .env and source it", err)
	}
	userID := os.Getenv("SLEEPER_USER_ID")
	leagueID := os.Getenv("DIAG_LEAGUE_ID")
	if userID == "" || leagueID == "" {
		t.Skip("SLEEPER_USER_ID and DIAG_LEAGUE_ID are required")
	}
	ctx := context.Background()

	me, err := c.Me(ctx)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	fmt.Printf("me.user_id=%s matches SLEEPER_USER_ID=%v\n", me, me == userID)

	pub := sleeper.NewClient()
	rosters, err := pub.Rosters(ctx, leagueID)
	if err != nil {
		t.Fatalf("rosters: %v", err)
	}
	myRoster := 0
	for _, r := range rosters {
		if r.OwnerID == userID {
			myRoster = r.RosterID
		}
	}
	fmt.Printf("league %s: my roster_id=%d (0 = none found)\n", leagueID, myRoster)
	// Without a roster the roster-scoped query below would run with roster_id=0
	// and read as "the query returns 0 rows" -- the exact signal that would
	// wrongly switch the job to the leg-scoped fallback. A wrong league id, a
	// wrong user id or a co-owned roster is a setup mistake, not a finding.
	if myRoster == 0 {
		t.Skipf("no roster in league %s is owned by SLEEPER_USER_ID %s — wrong DIAG_LEAGUE_ID or SLEEPER_USER_ID", leagueID, userID)
	}

	// Compare the output below against the Sleeper app, for an offer made TO you:
	fmt.Println("CHECKLIST, for a live offer made TO you (read the rows below against the app):")
	fmt.Println("  (1) the offer TO you appears in the roster-scoped count;")
	fmt.Println("  (2) `consenters` does NOT include your roster before you act;")
	fmt.Println("  (3) `creator` is the proposer's USER id, not a roster id;")
	fmt.Println("  (4) `expires` is a plausible near-future date (seconds, not millis);")
	fmt.Println("  (5) each pick's owner_id is the roster RECEIVING it.")

	// 1. The query the job will use.
	mine, err := c.ProposedTrades(ctx, leagueID, myRoster)
	if err != nil {
		t.Fatalf("ProposedTrades: %v", err)
	}
	fmt.Printf("league_transactions(status proposed, type trade, roster_id=%d): %d row(s)\n", myRoster, len(mine))
	for _, x := range mine {
		exp, ok := x.ExpiresAt()
		fmt.Printf("  id=%s status=%s roster_ids=%v creator=%s consenters=%v created=%d expires=%v(%v) picks=%d faab=%d\n",
			x.TransactionID, x.Status, x.RosterIDs, x.Creator, x.ConsenterIDs, x.Created, exp, ok, len(x.DraftPicks), len(x.WaiverBudget))
	}

	// 2. The same query without roster_id, RAW, to see every proposed trade in
	//    the league and the raw draft_picks shape.
	var raw struct {
		Rows []map[string]json.RawMessage `json:"league_transactions"`
	}
	const all = `query($league_id: Snowflake!) { league_transactions(league_id: $league_id, status: "proposed", type: "trade") { transaction_id roster_ids draft_picks settings status } }`
	if err := c.Query(ctx, all, map[string]any{"league_id": leagueID}, &raw); err != nil {
		t.Fatalf("league-wide proposed: %v", err)
	}
	fmt.Printf("league_transactions(status proposed, type trade, no roster_id): %d row(s)\n", len(raw.Rows))
	for _, r := range raw.Rows {
		fmt.Printf("  id=%s roster_ids=%s draft_picks=%s settings=%s\n", r["transaction_id"], r["roster_ids"], r["draft_picks"], r["settings"])
	}

	// 3. The leg-scoped alternative for the current week, for comparison.
	st, err := pub.State(ctx)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	var byStatus struct {
		Rows []struct {
			TransactionID string `json:"transaction_id"`
			RosterIDs     []int  `json:"roster_ids"`
		} `json:"league_transactions_by_status"`
	}
	const legQ = `query($league_id: Snowflake!, $leg: Int!) { league_transactions_by_status(league_id: $league_id, status: "proposed", leg: $leg) { transaction_id roster_ids } }`
	if err := c.Query(ctx, legQ, map[string]any{"league_id": leagueID, "leg": st.Week}, &byStatus); err != nil {
		t.Fatalf("by_status: %v", err)
	}
	fmt.Printf("league_transactions_by_status(status proposed, leg=%d): %d row(s)\n", st.Week, len(byStatus.Rows))
	fmt.Println("COMPARE the roster_id count with what the Sleeper app shows under your league's Trades tab (offers to you + offers from you).")
}
