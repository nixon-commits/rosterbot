package sleeperauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// MeQuery selects the caller's user_id and NOTHING ELSE. The me object also
// carries email, phone and the account's own token; a decoded field is one
// step from a log line, so they are never requested. TestMeQuery_SelectsUserIDOnly
// pins this.
const MeQuery = `{ me { user_id } }`

// ProposedTradesQuery lists the live trade offers involving one roster.
//
// status "proposed" is the live-offer status (not "pending", which matches
// nothing); type "trade" excludes pending waiver claims, which share the
// status; roster_id filters server-side, and league_transactions needs no
// leg, unlike league_transactions_by_status. The selection is the REST
// transaction's fields plus creator, consenter_ids and settings.
const ProposedTradesQuery = `query($league_id: Snowflake!, $roster_id: Int!) {
  league_transactions(league_id: $league_id, status: "proposed", type: "trade", roster_id: $roster_id) {
    transaction_id type status roster_ids adds drops draft_picks waiver_budget
    created creator consenter_ids settings
  }
}`

// Me returns the Sleeper user id the token belongs to. The offers command
// compares it with SLEEPER_USER_ID before doing anything else, so a token
// from the wrong account is a loud failure rather than a job that runs green
// and never finds an offer.
func (c *Client) Me(ctx context.Context) (string, error) {
	var out struct {
		Me struct {
			UserID string `json:"user_id"`
		} `json:"me"`
	}
	if err := c.Query(ctx, MeQuery, nil, &out); err != nil {
		return "", err
	}
	if out.Me.UserID == "" {
		return "", errors.New("sleeperauth: me returned no user_id")
	}
	return out.Me.UserID, nil
}

// wireTransaction is sleeper.Transaction with draft_picks held raw, because
// the GraphQL API has been observed to return picks either as objects (the
// REST shape) or as "roster_id,season,round,owner_id,previous_owner_id"
// strings. Decoding straight into []TransactionDraftPick would fail the whole
// row on the string form, and a failed row is an offer that never alerts.
type wireTransaction struct {
	sleeper.Transaction
	DraftPicks json.RawMessage `json:"draft_picks"`
}

// ProposedTrades returns every live trade offer in leagueID that involves
// rosterID, in the REST transaction shape.
func (c *Client) ProposedTrades(ctx context.Context, leagueID string, rosterID int) ([]sleeper.Transaction, error) {
	var out struct {
		Rows []wireTransaction `json:"league_transactions"`
	}
	vars := map[string]any{"league_id": leagueID, "roster_id": rosterID}
	if err := c.Query(ctx, ProposedTradesQuery, vars, &out); err != nil {
		return nil, err
	}
	txns := make([]sleeper.Transaction, 0, len(out.Rows))
	for _, w := range out.Rows {
		picks, err := decodeDraftPicks(w.DraftPicks)
		if err != nil {
			return nil, fmt.Errorf("sleeperauth: transaction %s: %w", w.TransactionID, err)
		}
		t := w.Transaction
		t.DraftPicks = picks
		txns = append(txns, t)
	}
	return txns, nil
}

// decodeDraftPicks accepts the object form, the string form, null, or absent.
func decodeDraftPicks(raw json.RawMessage) ([]sleeper.TransactionDraftPick, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var objs []sleeper.TransactionDraftPick
	if err := json.Unmarshal(raw, &objs); err == nil {
		return objs, nil
	}
	var strs []string
	if err := json.Unmarshal(raw, &strs); err != nil {
		return nil, fmt.Errorf("draft_picks is neither objects nor strings: %s", string(raw))
	}
	picks := make([]sleeper.TransactionDraftPick, 0, len(strs))
	for _, s := range strs {
		f := strings.Split(s, ",")
		if len(f) != 5 {
			return nil, fmt.Errorf("draft pick %q: want 5 comma-separated fields (roster_id,season,round,owner_id,previous_owner_id)", s)
		}
		var p sleeper.TransactionDraftPick
		var err error
		if p.RosterID, err = strconv.Atoi(f[0]); err != nil {
			return nil, fmt.Errorf("draft pick %q: roster_id: %w", s, err)
		}
		p.Season = f[1]
		if p.Round, err = strconv.Atoi(f[2]); err != nil {
			return nil, fmt.Errorf("draft pick %q: round: %w", s, err)
		}
		if p.OwnerID, err = strconv.Atoi(f[3]); err != nil {
			return nil, fmt.Errorf("draft pick %q: owner_id: %w", s, err)
		}
		if p.PreviousOwnerID, err = strconv.Atoi(f[4]); err != nil {
			return nil, fmt.Errorf("draft pick %q: previous_owner_id: %w", s, err)
		}
		picks = append(picks, p)
	}
	return picks, nil
}
