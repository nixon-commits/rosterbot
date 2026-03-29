package schedule

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

var statcastParkFactorsURL = "https://baseballsavant.mlb.com/leaderboard/statcast-park-factors?type=year&year=%d&batSide=&stat=index_wOBA&condition=All&rolling="

// mlbTeamIDToAbbr maps MLB Stats API team IDs to canonical abbreviations.
var mlbTeamIDToAbbr = map[int]string{
	108: "LAA", 109: "ARI", 110: "BAL", 111: "BOS", 112: "CHC",
	113: "CIN", 114: "CLE", 115: "COL", 116: "DET", 117: "HOU",
	118: "KC", 119: "LAD", 120: "WSH", 121: "NYM", 133: "ATH",
	134: "PIT", 135: "SD", 136: "SEA", 137: "SF", 138: "STL",
	139: "TB", 140: "TEX", 141: "TOR", 142: "MIN", 143: "PHI",
	144: "ATL", 145: "CHW", 146: "MIA", 147: "NYY", 158: "MIL",
}

// statcastRow represents a single park factor entry from the embedded JSON.
type statcastRow struct {
	MainTeamID string `json:"main_team_id"`
	IndexHR    string `json:"index_hr"`
	IndexHits  string `json:"index_hits"`
	IndexRuns  string `json:"index_runs"`
	IndexBB    string `json:"index_bb"`
	IndexSO    string `json:"index_so"`
	Index1B    string `json:"index_1b"`
	Index2B    string `json:"index_2b"`
	Index3B    string `json:"index_3b"`
}

var dataRegexp = regexp.MustCompile(`var data = (\[.*?\]);`)

// FetchParkFactors fetches Statcast park factors for the given season year.
// The data is embedded as JSON in the HTML page's inline script.
func (c *Client) FetchParkFactors(year int) (map[string]projections.ParkFactors, error) {
	url := fmt.Sprintf(statcastParkFactorsURL, year)
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("statcast park factors fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("statcast park factors: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("statcast park factors read: %w", err)
	}

	// Extract "var data = [...];" from the HTML page.
	match := dataRegexp.FindSubmatch(body)
	if match == nil {
		return nil, fmt.Errorf("statcast park factors: could not find embedded data")
	}

	var rows []statcastRow
	if err := json.Unmarshal(match[1], &rows); err != nil {
		return nil, fmt.Errorf("statcast park factors json: %w", err)
	}

	factors := make(map[string]projections.ParkFactors, 30)
	for _, row := range rows {
		teamID, err := strconv.Atoi(row.MainTeamID)
		if err != nil {
			continue
		}
		abbr, ok := mlbTeamIDToAbbr[teamID]
		if !ok {
			continue
		}

		pf := projections.ParkFactors{
			Team: abbr,
			HR:   parseFactorStr(row.IndexHR),
			H:    parseFactorStr(row.IndexHits),
			R:    parseFactorStr(row.IndexRuns),
			BB:   parseFactorStr(row.IndexBB),
			SO:   parseFactorStr(row.IndexSO),
			H1B:  parseFactorStr(row.Index1B),
			H2B:  parseFactorStr(row.Index2B),
			H3B:  parseFactorStr(row.Index3B),
		}
		factors[abbr] = pf
	}

	if len(factors) == 0 {
		return nil, fmt.Errorf("statcast park factors: parsed 0 teams")
	}
	return factors, nil
}

// FetchParkFactorsWithFallback tries the current year first, then falls back
// to the previous year if data isn't available yet (early in the season).
func (c *Client) FetchParkFactorsWithFallback() (map[string]projections.ParkFactors, error) {
	year := time.Now().Year()
	factors, err := c.FetchParkFactors(year)
	if err == nil && len(factors) >= 20 {
		return factors, nil
	}
	// Fallback to previous year.
	return c.FetchParkFactors(year - 1)
}

// FetchParkFactorsWithFallbackCached is like FetchParkFactorsWithFallback but uses a file cache.
func (c *Client) FetchParkFactorsWithFallbackCached(cacheDir string, ttl time.Duration) (map[string]projections.ParkFactors, error) {
	fc := cache.New[map[string]projections.ParkFactors](cacheDir, ttl)
	year := time.Now().Year()
	key := cache.Key("park-factors", strconv.Itoa(year))
	factors, err := fc.Get(key, func() (map[string]projections.ParkFactors, error) {
		return c.FetchParkFactors(year)
	})
	if err == nil && len(factors) >= 20 {
		return factors, nil
	}
	// Fallback to previous year (separate cache key).
	prevKey := cache.Key("park-factors", strconv.Itoa(year-1))
	return fc.Get(prevKey, func() (map[string]projections.ParkFactors, error) {
		return c.FetchParkFactors(year - 1)
	})
}

// parseFactorStr parses a Statcast index value string (centered at 100) into a
// multiplier (centered at 1.00). Returns 1.0 on parse failure.
func parseFactorStr(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 1.0
	}
	return v / 100.0
}
