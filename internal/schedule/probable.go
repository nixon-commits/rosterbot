package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/teams"
)

var mlbProbablePitcherURL = "https://statsapi.mlb.com/api/v1/schedule?sportId=1&hydrate=probablePitcher,team&date=%s"

// ProbableStarters returns a map of normalized pitcher name → team abbreviation
// for all probable starters on the given date.
//
// When Client.CacheDir is set, the fresh API result is merged with any cached
// entries for the same date. API entries win for teams they cover; cached
// entries for teams the API didn't cover are preserved. This defends against
// transient MLB statsapi gaps where probablePitcher temporarily goes null for
// a game in the pre-game window.
//
// Returns an empty map (not an error) when no data is available.
func (c *Client) ProbableStarters(date time.Time) (map[string]string, error) {
	apiResult, err := c.fetchProbableStarters(date)
	if err != nil {
		if cached, ok := c.loadProbablesCache(date); ok && len(cached) > 0 {
			return cached, nil
		}
		return nil, err
	}

	if c.CacheDir == "" {
		return apiResult, nil
	}

	merged := make(map[string]string, len(apiResult))
	apiTeams := make(map[string]bool, len(apiResult))
	for name, team := range apiResult {
		merged[name] = team
		apiTeams[team] = true
	}
	if cached, ok := c.loadProbablesCache(date); ok {
		for name, team := range cached {
			if !apiTeams[team] {
				merged[name] = team
			}
		}
	}

	if err := c.saveProbablesCache(date, merged); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save probables cache for %s: %v\n",
			date.Format("2006-01-02"), err)
	}

	return merged, nil
}

func (c *Client) fetchProbableStarters(date time.Time) (map[string]string, error) {
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
				starters[normalizePitcherName(pp.FullName)] = teams.Normalize(g.Teams.Away.Team.Abbreviation)
			}
			if pp := g.Teams.Home.ProbablePitcher; pp != nil && pp.FullName != "" {
				starters[normalizePitcherName(pp.FullName)] = teams.Normalize(g.Teams.Home.Team.Abbreviation)
			}
		}
	}
	return starters, nil
}

type probablesCacheFile struct {
	Date      string            `json:"date"`
	Probables map[string]string `json:"probables"`
}

func (c *Client) probablesCachePath(date time.Time) string {
	return filepath.Join(c.CacheDir, "probables-"+date.Format("2006-01-02")+".json")
}

func (c *Client) loadProbablesCache(date time.Time) (map[string]string, bool) {
	if c.CacheDir == "" {
		return nil, false
	}
	raw, err := os.ReadFile(c.probablesCachePath(date))
	if err != nil {
		return nil, false
	}
	var f probablesCacheFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, false
	}
	if len(f.Probables) == 0 {
		return nil, false
	}
	return f.Probables, true
}

func (c *Client) saveProbablesCache(date time.Time, probables map[string]string) error {
	if c.CacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.CacheDir, 0o755); err != nil {
		return err
	}
	f := probablesCacheFile{
		Date:      date.Format("2006-01-02"),
		Probables: probables,
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.probablesCachePath(date), raw, 0o644)
}

func normalizePitcherName(name string) string {
	return playername.Normalize(name)
}
