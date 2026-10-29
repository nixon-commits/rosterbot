package sleeperauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMeQuery_SelectsUserIDOnly(t *testing.T) {
	// me can return email, phone and the account's own token. None of them
	// may be requested, because a decoded field is one step from a log line.
	for _, banned := range []string{"token", "email", "phone", "cookies", "real_name"} {
		if strings.Contains(MeQuery, banned) {
			t.Errorf("MeQuery selects %q: %s", banned, MeQuery)
		}
	}
	if !strings.Contains(MeQuery, "user_id") {
		t.Errorf("MeQuery does not select user_id: %s", MeQuery)
	}
}

func TestMe_DecodesUserID(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"me":{"user_id":"738883211463155712"}}}`))
	})
	got, err := New(testToken).Me(context.Background())
	if err != nil || got != "738883211463155712" {
		t.Fatalf("Me = %q, %v", got, err)
	}
}

func TestMe_EmptyUserIDIsAnError(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"me":{"user_id":""}}}`))
	})
	if _, err := New(testToken).Me(context.Background()); err == nil {
		t.Fatal("want an error for an empty user_id")
	}
}

func TestProposedTrades_QueryShapeAndVariables(t *testing.T) {
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[]}}`))
	})
	if _, err := New(testToken).ProposedTrades(context.Background(), "1312135439800356864", 8); err != nil {
		t.Fatal(err)
	}
	doc, _ := body["query"].(string)
	for _, want := range []string{`status: "proposed"`, `type: "trade"`, "roster_id: $roster_id", "league_id: $league_id", "consenter_ids", "creator", "settings", "created", "draft_picks", "waiver_budget"} {
		if !strings.Contains(doc, want) {
			t.Errorf("query lacks %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "leg:") {
		t.Errorf("query passes a leg; the roster_id form needs none:\n%s", doc)
	}
	vars := body["variables"].(map[string]any)
	if vars["league_id"] != "1312135439800356864" || vars["roster_id"] != float64(8) {
		t.Errorf("variables = %v", vars)
	}
}

func TestProposedTrades_DecodesRows(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[{
		  "transaction_id":"1234","type":"trade","status":"proposed","roster_ids":[3,8],
		  "adds":{"5012":8,"9487":3},"drops":{"5012":3,"9487":8},
		  "draft_picks":[{"season":"2027","round":2,"roster_id":3,"previous_owner_id":3,"owner_id":8}],
		  "waiver_budget":[{"sender":3,"receiver":8,"amount":10}],
		  "created":1788371562251,"creator":"111","consenter_ids":[3],
		  "settings":{"expires_at":1788630762}}]}}`))
	})
	rows, err := New(testToken).ProposedTrades(context.Background(), "L", 8)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	r := rows[0]
	if r.TransactionID != "1234" || r.Status != "proposed" || r.Creator != "111" || len(r.ConsenterIDs) != 1 {
		t.Errorf("row = %+v", r)
	}
	if len(r.DraftPicks) != 1 || r.DraftPicks[0].Round != 2 || r.DraftPicks[0].OwnerID != 8 {
		t.Errorf("draft picks = %+v", r.DraftPicks)
	}
	if exp, ok := r.ExpiresAt(); !ok || exp.Unix() != 1788630762 {
		t.Errorf("ExpiresAt = %v, %v", exp, ok)
	}
}

func TestProposedTrades_ToleratesStringFormDraftPicks(t *testing.T) {
	// A community reference reports draft picks arriving as comma-separated
	// strings on some GraphQL transaction reads:
	// "roster_id,season,round,owner_id,previous_owner_id". Until the diag probe
	// settles which form league_transactions uses, accept both.
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[{
		  "transaction_id":"1","type":"trade","status":"proposed","roster_ids":[3,8],
		  "draft_picks":["3,2027,2,8,3"],"created":1,"creator":"111"}]}}`))
	})
	rows, err := New(testToken).ProposedTrades(context.Background(), "L", 8)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	p := rows[0].DraftPicks
	if len(p) != 1 || p[0].RosterID != 3 || p[0].Season != "2027" || p[0].Round != 2 || p[0].OwnerID != 8 || p[0].PreviousOwnerID != 3 {
		t.Errorf("draft picks = %+v", p)
	}
}

func TestProposedTrades_MalformedStringPickIsAnError(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[{"transaction_id":"1","draft_picks":["3,2027"]}]}}`))
	})
	if _, err := New(testToken).ProposedTrades(context.Background(), "L", 8); err == nil {
		t.Fatal("a pick string with the wrong field count must be an error, not a silently dropped asset")
	}
}
