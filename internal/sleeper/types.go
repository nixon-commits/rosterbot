package sleeper

import "time"

// League is a Sleeper fantasy league's static configuration.
type League struct {
	LeagueID string `json:"league_id"`
	Name     string `json:"name"`
	Season   string `json:"season"`
	Status   string `json:"status"` // pre_draft, drafting, in_season, complete
	// Avatar is an opaque id, not a URL: the image lives at
	// https://sleepercdn.com/avatars/thumbs/<avatar>. Empty for a league that
	// never set one, which is ordinary and not an error.
	Avatar           string         `json:"avatar"`
	TotalRosters     int            `json:"total_rosters"`
	RosterPositions  []string       `json:"roster_positions"`
	PreviousLeagueID string         `json:"previous_league_id"`
	Settings         map[string]int `json:"settings"`
}

// Roster is one team's roster within a league.
type Roster struct {
	RosterID int      `json:"roster_id"`
	OwnerID  string   `json:"owner_id"`
	LeagueID string   `json:"league_id"`
	Players  []string `json:"players"` // Sleeper player_ids
	Starters []string `json:"starters"`
	Reserve  []string `json:"reserve"`
	Taxi     []string `json:"taxi"`
}

// User is a league member.
type User struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Metadata    struct {
		TeamName string `json:"team_name"`
	} `json:"metadata"`
}

// TradedPick is one draft pick that has changed hands from its original
// owner, as tracked by Sleeper's traded_picks endpoint.
type TradedPick struct {
	Season          string `json:"season"`
	Round           int    `json:"round"`
	RosterID        int    `json:"roster_id"` // original owner (pick identity)
	PreviousOwnerID int    `json:"previous_owner_id"`
	OwnerID         int    `json:"owner_id"` // current owner
}

// TransactionDraftPick is a draft pick asset attached to a transaction.
type TransactionDraftPick struct {
	Season          string `json:"season"`
	Round           int    `json:"round"`
	RosterID        int    `json:"roster_id"`
	PreviousOwnerID int    `json:"previous_owner_id"`
	OwnerID         int    `json:"owner_id"`
}

// WaiverBudgetTransfer is one FAAB budget movement attached to a transaction
// (a trade can include cash considerations alongside or instead of players
// and picks).
type WaiverBudgetTransfer struct {
	Sender   int `json:"sender"`
	Receiver int `json:"receiver"`
	Amount   int `json:"amount"`
}

// Transaction is one league transaction (trade, waiver claim, free-agent
// add/drop, or — in a guillotine league — a chop) for a given week ("round"
// in Sleeper's API). The same shape is returned by the public REST feed and,
// with the two trailing fields populated, by the authenticated GraphQL
// league_transactions_by_status query that Plan 2 reads pending offers from.
type Transaction struct {
	TransactionID string                 `json:"transaction_id"`
	Type          string                 `json:"type"`   // trade, free_agent, waiver, chopped
	Status        string                 `json:"status"` // complete, failed; GraphQL also returns proposed (a live trade offer) and rejected
	RosterIDs     []int                  `json:"roster_ids"`
	Adds          map[string]int         `json:"adds"`
	Drops         map[string]int         `json:"drops"`
	DraftPicks    []TransactionDraftPick `json:"draft_picks"`
	WaiverBudget  []WaiverBudgetTransfer `json:"waiver_budget"`
	Created       int64                  `json:"created"` // epoch millis

	// Creator is the Sleeper USER id (not roster id) that proposed the
	// transaction. For a trade it is who sent the offer, which is what
	// separates "an offer made to me" from "an offer I made".
	Creator string `json:"creator"`
	// ConsenterIDs are the ROSTER ids that have accepted so far. On a
	// completed trade it holds every party; on a pending one it shows who is
	// still being waited on.
	ConsenterIDs []int `json:"consenter_ids"`

	// Settings is Sleeper's free-form per-transaction map: waiver_bid and seq on
	// a waiver claim, expires_at on a trade offer. Decoded as any because the
	// values are not all one type and a single mistyped key would fail the
	// whole row's decode. Read expires_at through ExpiresAt.
	Settings map[string]any `json:"settings"`
}

// ExpiresAt returns settings.expires_at as a UTC time, when present.
//
// Sleeper stores it as epoch SECONDS while Created is epoch MILLIS; reading
// one as the other is off by a factor of a thousand, which is why the unit is
// pinned here and in the test. Absent, non-numeric or zero reads as "no
// expiry": an offer without one never lapses on its own, so the safe default
// is to keep it.
func (t Transaction) ExpiresAt() (time.Time, bool) {
	v, ok := t.Settings["expires_at"]
	if !ok {
		return time.Time{}, false
	}
	f, ok := v.(float64) // encoding/json decodes every JSON number into any as float64
	if !ok || f <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0).UTC(), true
}

// NFLState is the current NFL week/season as Sleeper sees it.
type NFLState struct {
	Week       int    `json:"week"`
	Season     string `json:"season"`
	SeasonType string `json:"season_type"` // pre, regular, post
	Leg        int    `json:"leg"`
	// LeagueCreateSeason is the season NEW leagues are created into. Sleeper
	// flips it ahead of Season in December, so between then and the next
	// season's start the renewed leagues live only under this value.
	LeagueCreateSeason string `json:"league_create_season"`
}

// Player is one entry from Sleeper's full NFL player dump.
type Player struct {
	PlayerID         string   `json:"player_id"`
	FirstName        string   `json:"first_name"`
	LastName         string   `json:"last_name"`
	Position         string   `json:"position"`
	Team             string   `json:"team"`
	FantasyPositions []string `json:"fantasy_positions"`
	Status           string   `json:"status"`
	InjuryStatus     string   `json:"injury_status"`

	// DepthChartPosition/DepthChartOrder are Sleeper's editorial depth chart:
	// the slot name ("QB", "RB", "LWR", "RWR", "SWR", "TE", ...) and 1 for the
	// starter. Order is a pointer because Sleeper sends null for most of the
	// pool; nil means "no recorded slot", which the pickups detector treats as
	// distinct from 2+. Measured 2026-09-21: Drew Lock and Carson Wentz both
	// read QB/1 the week they were picked up for real FAAB.
	DepthChartPosition string `json:"depth_chart_position"`
	DepthChartOrder    *int   `json:"depth_chart_order"`

	// SearchRank is Sleeper's own depth-chart/relevance ranking (lower =
	// more relevant; undrafted/irrelevant players carry a large sentinel
	// value). Used as the starter-selection fallback when a roster's
	// Starters array is empty or unusable — never StatsGuy value, which
	// would make an unvalued player unselectable by construction.
	SearchRank int `json:"search_rank"`
}
