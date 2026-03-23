package schedule

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var mlbScheduleURL = "https://statsapi.mlb.com/api/v1/schedule?sportId=1&date=%s"

// Client fetches the MLB game schedule.
type Client struct {
	http http.Client
}

func NewClient() *Client {
	return &Client{http: http.Client{Timeout: 10 * time.Second}}
}

// TeamsPlayingOn returns the set of MLB team abbreviations with a game on the given date.
func (c *Client) TeamsPlayingOn(date time.Time) (map[string]bool, error) {
	url := fmt.Sprintf(mlbScheduleURL, date.Format("2006-01-02"))
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("mlb schedule fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mlb schedule: status %d", resp.StatusCode)
	}

	var payload struct {
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

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("mlb schedule decode: %w", err)
	}

	playing := make(map[string]bool)
	for _, d := range payload.Dates {
		for _, g := range d.Games {
			playing[g.Teams.Away.Team.Abbreviation] = true
			playing[g.Teams.Home.Team.Abbreviation] = true
		}
	}
	return playing, nil
}
