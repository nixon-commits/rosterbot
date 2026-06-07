// Package recap produces a Sleeper-style weekly recap of the league: per-team
// efficiency vs hindsight-optimal lineup, head-to-head matchup results, league
// awards, and player highlights. The output is rendered to HTML for hosting on
// GitHub Pages.
package recap

import "time"

// Recap is the full data model for a single matchup-week recap. It's
// JSON-serializable for debugging and feeds the HTML template.
type Recap struct {
	Season      int               `json:"season"`
	WeekNumber  int               `json:"week_number"`
	WeekLabel   string            `json:"week_label"`
	StartDate   time.Time         `json:"start_date"`
	EndDate     time.Time         `json:"end_date"`
	GeneratedAt time.Time         `json:"generated_at"`
	Teams       []TeamWeek        `json:"teams"`
	Matchups    []MatchupResult   `json:"matchups"`
	Awards      Awards            `json:"awards"`
	WPCurves    []MatchupWPCurve  `json:"wp_curves,omitempty"`
	LogoURLs    map[string]string `json:"logo_urls,omitempty"`
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

// PlayerLine is one player-day scoring entry, used for the top batter / top
// pitcher leaderboards.
type PlayerLine struct {
	PlayerID    string    `json:"player_id"`
	Name        string    `json:"name"`
	MLBTeam     string    `json:"mlb_team"`
	Slot        string    `json:"slot,omitempty"`
	FPts        float64   `json:"fpts"`
	Date        time.Time `json:"date"`
	OwnerTeam   string    `json:"owner_team"`
	OwnerTeamID string    `json:"owner_team_id,omitempty"`
	IsPitcher   bool      `json:"is_pitcher,omitempty"`
}

// LeaderLine is one rostered player's season-to-date rate-stat standing, used
// for the league wOBA / FIP leaderboards. Value carries the metric (wOBA for
// hitters, FIP for pitchers); rank is implied by slice order.
type LeaderLine struct {
	Name        string  `json:"name"`
	MLBTeam     string  `json:"mlb_team,omitempty"`
	OwnerTeam   string  `json:"owner_team"`
	OwnerTeamID string  `json:"owner_team_id,omitempty"`
	Value       float64 `json:"value"`
}

// PitcherStartLine is one SP game-start record for the week, used for
// best/worst single-game-start awards.
type PitcherStartLine struct {
	Name       string    `json:"name"`
	Date       time.Time `json:"date"`
	FPts       float64   `json:"fpts"`
	OwnerTeam  string    `json:"owner_team"`
	MLBTeam    string    `json:"mlb_team,omitempty"`
	Opponent   string    `json:"opponent,omitempty"`
	WeekNumber int       `json:"week_number,omitempty"` // populated by season aggregator
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
	MostEfficient    *TeamWeek         `json:"most_efficient,omitempty"`
	LeastEfficient   *TeamWeek         `json:"least_efficient,omitempty"`
	HighestScore     *TeamWeek         `json:"highest_score,omitempty"`
	LowestScore      *TeamWeek         `json:"lowest_score,omitempty"`
	BiggestBlowout   *MatchupResult    `json:"biggest_blowout,omitempty"`
	NarrowVictory    *MatchupResult    `json:"narrow_victory,omitempty"`
	HighestPtsInLoss *MatchupTeamSide  `json:"highest_pts_in_loss,omitempty"`
	LowestPtsInWin   *MatchupTeamSide  `json:"lowest_pts_in_win,omitempty"`
	BestSingleStart  *PitcherStartLine `json:"best_single_start,omitempty"`
	WorstSingleStart *PitcherStartLine `json:"worst_single_start,omitempty"`
	TopBatters       []PlayerLine      `json:"top_batters,omitempty"`
	TopPitchers      []PlayerLine      `json:"top_pitchers,omitempty"`
	WOBALeaders      []LeaderLine      `json:"woba_leaders,omitempty"`
	FIPLeaders       []LeaderLine      `json:"fip_leaders,omitempty"`
	Comeback         *MatchupTeamSide  `json:"comeback,omitempty"`
	GameOfWeek       *MatchupResult    `json:"game_of_week,omitempty"`
}

// WeekLink is one entry in the cross-week navigation dropdown rendered into
// site mode. The dropdown navigates to Filename via window.location, so paths
// must be relative to the current page (same directory).
type WeekLink struct {
	WeekNumber int    `json:"week_number"`
	WeekLabel  string `json:"week_label"`
	Filename   string `json:"filename"`
	IsCurrent  bool   `json:"is_current,omitempty"`
}

// SeasonAwardTeam is one team's cumulative count for a particular award.
type SeasonAwardTeam struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	Count    int    `json:"count"`
}

// SeasonAwardCategory groups all teams that have earned a given weekly award
// at least once during the season.
type SeasonAwardCategory struct {
	AwardName string            `json:"award_name"`
	Teams     []SeasonAwardTeam `json:"teams"` // sorted by Count desc, then TeamID asc
}

// SeasonAwards is the season-to-date leaderboard of weekly awards collected,
// rendered at the bottom of each weekly recap page in site mode. Each
// category lists every team that has earned that award and how many times.
// Shellings is a separate season-wide list of the worst pitcher starts
// (across all teams) — the per-week WorstSingleStart awards re-ranked by
// FPts ascending.
type SeasonAwards struct {
	ThroughWeek      int                   `json:"through_week"`
	Categories       []SeasonAwardCategory `json:"categories"`
	Shellings        []PitcherStartLine    `json:"shellings,omitempty"`
	StandingsHistory []WeekStandings       `json:"standings_history,omitempty"`
}

// MatchupWPCurve is the per-matchup win-probability trace produced by Monte
// Carlo simulation. Points has length 8: index 0 is the pre-week baseline
// (both teams' WP starts at 0.5 in the absence of observed data); indices
// 1..7 are the WP at end of each day in the matchup week.
type MatchupWPCurve struct {
	HomeTeamID  string    `json:"home_team_id"`
	AwayTeamID  string    `json:"away_team_id"`
	Points      []WPPoint `json:"points"`
	LeadChanges int       `json:"lead_changes"`
}

// WPPoint is one snapshot in a matchup's WP curve. HomeRunning and
// AwayRunning are the cumulative actual FPts each team has scored through
// this point in time.
type WPPoint struct {
	Date        time.Time `json:"date"`
	HomeWP      float64   `json:"home_wp"`      // home win probability in [0, 1]
	HomeRunning float64   `json:"home_running"` // home cumulative FPts through Date
	AwayRunning float64   `json:"away_running"` // away cumulative FPts through Date
}
