package sleeper

// League is a Sleeper fantasy league's static configuration.
type League struct {
	LeagueID         string         `json:"league_id"`
	Name             string         `json:"name"`
	Season           string         `json:"season"`
	Status           string         `json:"status"` // pre_draft, drafting, in_season, complete
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

// Transaction is one league transaction (trade, waiver claim, or free-agent
// add/drop) for a given week ("round" in Sleeper's API).
type Transaction struct {
	TransactionID string                 `json:"transaction_id"`
	Type          string                 `json:"type"`   // trade, free_agent, waiver
	Status        string                 `json:"status"` // complete, pending, failed
	RosterIDs     []int                  `json:"roster_ids"`
	Adds          map[string]int         `json:"adds"`
	Drops         map[string]int         `json:"drops"`
	DraftPicks    []TransactionDraftPick `json:"draft_picks"`
	WaiverBudget  []WaiverBudgetTransfer `json:"waiver_budget"`
	Created       int64                  `json:"created"` // epoch millis
}

// NFLState is the current NFL week/season as Sleeper sees it.
type NFLState struct {
	Week       int    `json:"week"`
	Season     string `json:"season"`
	SeasonType string `json:"season_type"` // pre, regular, post
	Leg        int    `json:"leg"`
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

	// SearchRank is Sleeper's own depth-chart/relevance ranking (lower =
	// more relevant; undrafted/irrelevant players carry a large sentinel
	// value). Used as the starter-selection fallback when a roster's
	// Starters array is empty or unusable — never StatsGuy value, which
	// would make an unvalued player unselectable by construction.
	SearchRank int `json:"search_rank"`
}
