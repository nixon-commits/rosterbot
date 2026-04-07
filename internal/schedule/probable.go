package schedule

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

var mlbProbablePitcherURL = "https://statsapi.mlb.com/api/v1/schedule?sportId=1&hydrate=probablePitcher,team&date=%s"

// ProbableStarters returns a map of normalized pitcher name → team abbreviation
// for all probable starters on the given date.
// Returns an empty map (not an error) when no data is available.
func (c *Client) ProbableStarters(date time.Time) (map[string]string, error) {
	url := fmt.Sprintf(mlbProbablePitcherURL, date.Format("2006-01-02"))
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("mlb probable pitchers fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("mlb probable pitchers: status %d", resp.StatusCode)
	}

	var payload struct {
		Dates []struct {
			Games []struct {
				Teams struct {
					Away struct {
						Team struct {
							Abbreviation string `json:"abbreviation"`
						} `json:"team"`
						ProbablePitcher *struct {
							FullName string `json:"fullName"`
						} `json:"probablePitcher"`
					} `json:"away"`
					Home struct {
						Team struct {
							Abbreviation string `json:"abbreviation"`
						} `json:"team"`
						ProbablePitcher *struct {
							FullName string `json:"fullName"`
						} `json:"probablePitcher"`
					} `json:"home"`
				} `json:"teams"`
			} `json:"games"`
		} `json:"dates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("mlb probable pitchers decode: %w", err)
	}

	starters := make(map[string]string)
	for _, d := range payload.Dates {
		for _, g := range d.Games {
			if pp := g.Teams.Away.ProbablePitcher; pp != nil && pp.FullName != "" {
				starters[normalizePitcherName(pp.FullName)] = projections.NormalizeTeam(g.Teams.Away.Team.Abbreviation)
			}
			if pp := g.Teams.Home.ProbablePitcher; pp != nil && pp.FullName != "" {
				starters[normalizePitcherName(pp.FullName)] = projections.NormalizeTeam(g.Teams.Home.Team.Abbreviation)
			}
		}
	}
	return starters, nil
}

// normalizePitcherName normalizes a pitcher name for matching.
func normalizePitcherName(name string) string {
	return playername.Normalize(name)
}
