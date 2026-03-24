package schedule

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/projections"
)

var mlbScheduleURL = "https://statsapi.mlb.com/api/v1/schedule?sportId=1&hydrate=team&date=%s"

// Client fetches the MLB game schedule.
type Client struct {
	http http.Client
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
		} `json:"games"`
	} `json:"dates"`
}

func (c *Client) fetchSchedule(date time.Time) (*schedulePayload, error) {
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
