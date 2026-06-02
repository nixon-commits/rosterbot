package schedule

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

var mlbScheduleURL = "https://statsapi.mlb.com/api/v1/schedule?sportId=1&hydrate=team&date=%s"
var mlbLineupsURL = "https://statsapi.mlb.com/api/v1/schedule?sportId=1&hydrate=lineups,team&date=%s"

// pastScheduleTTL is the on-disk lifetime for cached schedule responses for
// past dates. Past games are immutable, so a long TTL is safe.
const pastScheduleTTL = 30 * 24 * time.Hour

// Client fetches the MLB game schedule.
type Client struct {
	http http.Client
	// CacheDir enables two file caches under the same directory:
	//   - sticky per-date probable-starters cache (always on when set), so
	//     a previously-confirmed probable isn't lost when the MLB statsapi
	//     intermittently drops probablePitcher data during the pre-game window
	//   - per-date schedule cache for PAST dates only, key
	//     `mlb-schedule-<YYYY-MM-DD>` (immutable, 30-day TTL)
	// Today and future dates are always fetched fresh.
	// Leave empty to disable both.
	CacheDir string
}

func NewClient() *Client {
	return &Client{http: http.Client{Timeout: 10 * time.Second}}
}

// schedulePayload is the decoded MLB schedule API response.
type schedulePayload struct {
	Dates []struct {
		Games []struct {
			Teams struct {
				Away struct {
					Team struct {
						Abbreviation string `json:"abbreviation"`
					} `json:"team"`
				} `json:"away"`
				Home struct {
					Team struct {
						Abbreviation string `json:"abbreviation"`
					} `json:"team"`
				} `json:"home"`
			} `json:"teams"`
			Status struct {
				AbstractGameState string `json:"abstractGameState"` // "Preview", "Live", "Final"
			} `json:"status"`
		} `json:"games"`
	} `json:"dates"`
}

func (c *Client) fetchSchedule(date time.Time) (*schedulePayload, error) {
	if c.CacheDir != "" && isPastDate(date) {
		fc := cache.New[*schedulePayload](c.CacheDir, pastScheduleTTL)
		key := cache.Key("mlb-schedule", date.Format("2006-01-02"))
		return fc.Get(key, func() (*schedulePayload, error) {
			return c.fetchScheduleUncached(date)
		})
	}
	return c.fetchScheduleUncached(date)
}

// isPastDate reports whether date's UTC YMD is strictly before today's UTC
// YMD. Past-date schedules are immutable and safe to cache aggressively;
// today/future are not.
func isPastDate(date time.Time) bool {
	return date.UTC().Format("2006-01-02") < time.Now().UTC().Format("2006-01-02")
}

func (c *Client) fetchScheduleUncached(date time.Time) (*schedulePayload, error) {
	url := fmt.Sprintf(mlbScheduleURL, date.Format("2006-01-02"))
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("mlb schedule fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mlb schedule: status %d", resp.StatusCode)
	}

	var payload schedulePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("mlb schedule decode: %w", err)
	}
	return &payload, nil
}

// TeamsPlayingOn returns the set of MLB team abbreviations with a game on the given date.
func (c *Client) TeamsPlayingOn(date time.Time) (map[string]bool, error) {
	payload, err := c.fetchSchedule(date)
	if err != nil {
		return nil, err
	}

	playing := make(map[string]bool)
	for _, d := range payload.Dates {
		for _, g := range d.Games {
			playing[projections.NormalizeTeam(g.Teams.Away.Team.Abbreviation)] = true
			playing[projections.NormalizeTeam(g.Teams.Home.Team.Abbreviation)] = true
		}
	}
	return playing, nil
}

// OpponentsOn returns a map of MLB team abbreviation → opponent abbreviation
// for every game on the given date. A team that plays a doubleheader will
// only have its last opponent in the map (good enough for award-style "facing
// X" labels; we don't model doubleheaders elsewhere either).
func (c *Client) OpponentsOn(date time.Time) (map[string]string, error) {
	payload, err := c.fetchSchedule(date)
	if err != nil {
		return nil, err
	}
	opp := make(map[string]string)
	for _, d := range payload.Dates {
		for _, g := range d.Games {
			away := projections.NormalizeTeam(g.Teams.Away.Team.Abbreviation)
			home := projections.NormalizeTeam(g.Teams.Home.Team.Abbreviation)
			if away != "" {
				opp[away] = home
			}
			if home != "" {
				opp[home] = away
			}
		}
	}
	return opp, nil
}

// LockedTeams returns the set of teams whose game is currently in progress or final.
// Players on these teams cannot be moved in Fantrax for that scoring period.
func (c *Client) LockedTeams(date time.Time) (map[string]bool, error) {
	payload, err := c.fetchSchedule(date)
	if err != nil {
		return nil, err
	}

	locked := make(map[string]bool)
	for _, d := range payload.Dates {
		for _, g := range d.Games {
			state := g.Status.AbstractGameState
			if state == "Live" || state == "Final" {
				locked[projections.NormalizeTeam(g.Teams.Away.Team.Abbreviation)] = true
				locked[projections.NormalizeTeam(g.Teams.Home.Team.Abbreviation)] = true
			}
		}
	}
	return locked, nil
}

// lineupsPayload is the decoded MLB schedule API response with lineup hydration.
type lineupsPayload struct {
	Dates []struct {
		Games []struct {
			Teams struct {
				Away struct {
					Team struct {
						Abbreviation string `json:"abbreviation"`
					} `json:"team"`
				} `json:"away"`
				Home struct {
					Team struct {
						Abbreviation string `json:"abbreviation"`
					} `json:"team"`
				} `json:"home"`
			} `json:"teams"`
			Lineups *struct {
				HomePlayers []struct {
					FullName string `json:"fullName"`
				} `json:"homePlayers"`
				AwayPlayers []struct {
					FullName string `json:"fullName"`
				} `json:"awayPlayers"`
			} `json:"lineups"`
		} `json:"games"`
	} `json:"dates"`
}

// BenchedPlayers returns the set of normalized player names who are confirmed
// NOT in today's starting lineup. rosterPlayers maps normalized name → team
// abbreviation for rostered hitters. A player is only marked benched when
// their team's game has lineups posted and the player is absent from the lineup.
// If lineups are not yet posted for a game, no players from those teams are affected.
func (c *Client) BenchedPlayers(date time.Time, rosterPlayers map[string]string) (map[string]bool, error) {
	url := fmt.Sprintf(mlbLineupsURL, date.Format("2006-01-02"))
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("mlb lineups fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mlb lineups: status %d", resp.StatusCode)
	}

	var payload lineupsPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("mlb lineups decode: %w", err)
	}

	// Collect confirmed starters by team. Only teams with posted lineups
	// (non-empty player lists) are included.
	teamStarters := make(map[string]map[string]bool) // team abbr → set of normalized names
	for _, d := range payload.Dates {
		for _, g := range d.Games {
			if g.Lineups == nil {
				continue
			}
			homeTeam := projections.NormalizeTeam(g.Teams.Home.Team.Abbreviation)
			awayTeam := projections.NormalizeTeam(g.Teams.Away.Team.Abbreviation)

			if len(g.Lineups.HomePlayers) > 0 {
				starters := make(map[string]bool)
				for _, p := range g.Lineups.HomePlayers {
					starters[normalizePitcherName(p.FullName)] = true
				}
				teamStarters[homeTeam] = starters
			}
			if len(g.Lineups.AwayPlayers) > 0 {
				starters := make(map[string]bool)
				for _, p := range g.Lineups.AwayPlayers {
					starters[normalizePitcherName(p.FullName)] = true
				}
				teamStarters[awayTeam] = starters
			}
		}
	}

	benched := make(map[string]bool)
	for normalizedName, team := range rosterPlayers {
		starters, hasLineup := teamStarters[team]
		if !hasLineup {
			continue // lineups not posted for this team — assume player plays
		}
		if !starters[normalizedName] {
			benched[normalizedName] = true
		}
	}
	return benched, nil
}

// GameVenues returns a map of team abbreviation → home team abbreviation
// for every team playing on the given date. The home team determines the park.
func (c *Client) GameVenues(date time.Time) (map[string]string, error) {
	payload, err := c.fetchSchedule(date)
	if err != nil {
		return nil, err
	}

	venues := make(map[string]string)
	for _, d := range payload.Dates {
		for _, g := range d.Games {
			home := projections.NormalizeTeam(g.Teams.Home.Team.Abbreviation)
			away := projections.NormalizeTeam(g.Teams.Away.Team.Abbreviation)
			venues[home] = home
			venues[away] = home
		}
	}
	return venues, nil
}
