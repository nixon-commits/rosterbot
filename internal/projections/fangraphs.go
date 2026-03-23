package projections

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const fangraphsBattingURL = "https://www.fangraphs.com/api/projections?type=steamer&stats=bat&pos=all&team=0&players=0&lg=all"

// Projection holds per-season projected counting stats for a hitter.
// All values are season totals; the optimizer prorates to a per-game rate.
type Projection struct {
	PA      float64
	H       float64
	Doubles float64
	Triples float64
	HR      float64
	RBI     float64
	R       float64
	BB      float64
	SB      float64
	CS      float64
	HBP     float64
}

// Source can look up a projection for a player by name and team.
type Source interface {
	GetProjection(name, mlbTeam string) (*Projection, bool)
}

// FanGraphsSource fetches Steamer projections from FanGraphs.
type FanGraphsSource struct {
	// key: "lastname, firstname|TEAM" or just name|TEAM
	projections map[string]*Projection
}

// NewFanGraphsSource fetches and parses the FanGraphs batting projections CSV.
func NewFanGraphsSource() (*FanGraphsSource, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(fangraphsBattingURL)
	if err != nil {
		return nil, fmt.Errorf("fangraphs fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fangraphs: status %d", resp.StatusCode)
	}

	r := csv.NewReader(resp.Body)
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("fangraphs csv: %w", err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("fangraphs csv: no data rows")
	}

	header := records[0]
	idx := buildIndex(header)

	src := &FanGraphsSource{projections: make(map[string]*Projection)}
	for _, row := range records[1:] {
		if len(row) < len(header) {
			continue
		}
		name := strings.TrimSpace(getField(row, idx, "Name"))
		team := strings.ToUpper(strings.TrimSpace(getField(row, idx, "Team")))
		if name == "" || team == "" {
			continue
		}

		p := &Projection{
			PA:      parseFloat(getField(row, idx, "PA")),
			H:       parseFloat(getField(row, idx, "H")),
			Doubles: parseFloat(getField(row, idx, "2B")),
			Triples: parseFloat(getField(row, idx, "3B")),
			HR:      parseFloat(getField(row, idx, "HR")),
			RBI:     parseFloat(getField(row, idx, "RBI")),
			R:       parseFloat(getField(row, idx, "R")),
			BB:      parseFloat(getField(row, idx, "BB")),
			SB:      parseFloat(getField(row, idx, "SB")),
			CS:      parseFloat(getField(row, idx, "CS")),
			HBP:     parseFloat(getField(row, idx, "HBP")),
		}
		key := projKey(name, team)
		src.projections[key] = p
	}

	return src, nil
}

// GetProjection looks up a player's projection by name and MLB team.
// Returns nil, false if the player isn't in the dataset.
func (s *FanGraphsSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	key := projKey(name, strings.ToUpper(mlbTeam))
	p, ok := s.projections[key]
	if ok {
		return p, true
	}
	// Try name-only fallback (handles team changes mid-season).
	for k, v := range s.projections {
		if strings.HasPrefix(k, normalizeName(name)+"|") {
			return v, true
		}
	}
	return nil, false
}

func projKey(name, team string) string {
	return normalizeName(name) + "|" + strings.ToUpper(team)
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func buildIndex(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[strings.TrimSpace(h)] = i
	}
	return m
}

func getField(row []string, idx map[string]int, col string) string {
	i, ok := idx[col]
	if !ok || i >= len(row) {
		return ""
	}
	return row[i]
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
