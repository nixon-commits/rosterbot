// Package recap produces a Sleeper-style weekly recap of the league: per-team
// efficiency vs hindsight-optimal lineup, head-to-head matchup results, league
// awards, and player highlights. The output is rendered to HTML for hosting on
// GitHub Pages.
package recap

import "time"

// Recap is the full data model for a single matchup-week recap. It's
// JSON-serializable for debugging and feeds the HTML template.
type Recap struct {
	Season      int             `json:"season"`
	WeekNumber  int             `json:"week_number"`
	WeekLabel   string          `json:"week_label"`
	StartDate   time.Time       `json:"start_date"`
	EndDate     time.Time       `json:"end_date"`
	GeneratedAt time.Time       `json:"generated_at"`
	Teams       []TeamWeek      `json:"teams"`
	Matchups    []MatchupResult `json:"matchups"`
	Awards      Awards          `json:"awards"`
}

// TeamWeek is a single team's aggregated weekly performance.
type TeamWeek struct {
	TeamID     string  `json:"team_id"`
	TeamName   string  `json:"team_name"`
	ActualPts  float64 `json:"actual_pts"`
	OptimalPts float64 `json:"optimal_pts"`
	// Efficiency is ActualPts / OptimalPts, in [0, 1]. Zero if OptimalPts <= 0.
	Efficiency float64 `json:"efficiency"`
}

// MatchupResult records a single H2H matchup outcome for the week.
type MatchupResult struct {
	HomeTeamID   string  `json:"home_team_id"`
	HomeTeamName string  `json:"home_team_name"`
	HomePts      float64 `json:"home_pts"`
	AwayTeamID   string  `json:"away_team_id"`
	AwayTeamName string  `json:"away_team_name"`
	AwayPts      float64 `json:"away_pts"`
	Margin       float64 `json:"margin"` // |home - away|
	WinnerID     string  `json:"winner_id,omitempty"`
	LoserID      string  `json:"loser_id,omitempty"`
	IsTie        bool    `json:"is_tie,omitempty"`
}

// PlayerLine is one player-day scoring entry, used for "Players of the Week"
// and "Benchwarmers of the Week" awards.
type PlayerLine struct {
	PlayerID  string    `json:"player_id"`
	Name      string    `json:"name"`
	MLBTeam   string    `json:"mlb_team"`
	Slot      string    `json:"slot,omitempty"`
	FPts      float64   `json:"fpts"`
	Date      time.Time `json:"date"`
	OwnerTeam string    `json:"owner_team"`
}

// PitcherStartLine is one SP game-start record for the week, used for
// best/worst single-game-start awards.
type PitcherStartLine struct {
	Name      string    `json:"name"`
	Date      time.Time `json:"date"`
	FPts      float64   `json:"fpts"`
	OwnerTeam string    `json:"owner_team"`
}

// MatchupTeamSide is one side of an H2H matchup, suitable for "Highest Pts in
// Loss" / "Lowest Pts in Win" awards where we care about a single team's
// outcome more than the matchup itself.
type MatchupTeamSide struct {
	TeamID   string  `json:"team_id"`
	TeamName string  `json:"team_name"`
	Pts      float64 `json:"pts"`
	OppName  string  `json:"opp_name"`
	OppPts   float64 `json:"opp_pts"`
}

// Awards bundles all league-wide awards for the week. Pointer fields are nil
// when the award has no qualifying entry (e.g., no losses → no "Highest Pts in
// Loss"; no SP starts → no best/worst single start).
type Awards struct {
	MostEfficient      *TeamWeek         `json:"most_efficient,omitempty"`
	LeastEfficient     *TeamWeek         `json:"least_efficient,omitempty"`
	BiggestBlowout     *MatchupResult    `json:"biggest_blowout,omitempty"`
	NarrowVictory      *MatchupResult    `json:"narrow_victory,omitempty"`
	HighestPtsInLoss   *MatchupTeamSide  `json:"highest_pts_in_loss,omitempty"`
	LowestPtsInWin     *MatchupTeamSide  `json:"lowest_pts_in_win,omitempty"`
	BestSingleStart    *PitcherStartLine `json:"best_single_start,omitempty"`
	WorstSingleStart   *PitcherStartLine `json:"worst_single_start,omitempty"`
	PlayersOfWeek      []PlayerLine      `json:"players_of_week,omitempty"`
	BenchwarmersOfWeek []PlayerLine      `json:"benchwarmers_of_week,omitempty"`
}
