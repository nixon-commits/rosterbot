package projections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var fangraphsBattingURL = "https://www.fangraphs.com/api/projections?type=fangraphsdc&stats=bat&pos=all&team=0&players=0&lg=all"

// Projection holds projected season counting stats for a hitter.
// All values are season totals; per-game rates are derived by dividing by G.
type Projection struct {
	G       float64
	PA      float64
	H       float64
	Singles float64
	Doubles float64
	Triples float64
	HR      float64
	RBI     float64
	R       float64
	BB      float64
	SB      float64
	CS      float64
	HBP     float64
	SO      float64
	GIDP    float64
	Bats    string // "R", "L", or "S" (switch)
}

// Source can look up a projection for a player.
type Source interface {
	GetProjection(name, mlbTeam string) (*Projection, bool)
}

type fgRow struct {
	PlayerName string  `json:"PlayerName"`
	Team       string  `json:"Team"`
	G          float64 `json:"G"`
	PA         float64 `json:"PA"`
	H          float64 `json:"H"`
	Singles    float64 `json:"1B"`
	Doubles    float64 `json:"2B"`
	Triples    float64 `json:"3B"`
	HR         float64 `json:"HR"`
	RBI        float64 `json:"RBI"`
	R          float64 `json:"R"`
	BB         float64 `json:"BB"`
	SB         float64 `json:"SB"`
	CS         float64 `json:"CS"`
	HBP        float64 `json:"HBP"`
	SO         float64 `json:"SO"`
	GIDP       float64 `json:"GDP"`
	Bats       string  `json:"Bats"`
}

// FanGraphsSource fetches Steamer projections from FanGraphs.
type FanGraphsSource struct {
	projections map[string]*Projection
}

// NewFanGraphsSource fetches and parses the FanGraphs batting projections JSON.
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

	var rows []fgRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("fangraphs json: %w", err)
	}

	src := &FanGraphsSource{projections: make(map[string]*Projection, len(rows))}
	for _, row := range rows {
		name := strings.TrimSpace(row.PlayerName)
		team := strings.ToUpper(strings.TrimSpace(row.Team))
		if name == "" {
			continue
		}
		p := &Projection{
			G:       row.G,
			PA:      row.PA,
			H:       row.H,
			Singles: row.Singles,
			Doubles: row.Doubles,
			Triples: row.Triples,
			HR:      row.HR,
			RBI:     row.RBI,
			R:       row.R,
			BB:      row.BB,
			SB:      row.SB,
			CS:      row.CS,
			HBP:     row.HBP,
			SO:      row.SO,
			GIDP:    row.GIDP,
			Bats:    row.Bats,
		}
		src.projections[projKey(name, team)] = p
	}
	return src, nil
}

// GetProjection looks up a player's projection by name and MLB team.
func (s *FanGraphsSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	// Try exact name+team match first.
	key := projKey(name, mlbTeam)
	if p, ok := s.projections[key]; ok {
		return p, true
	}
	// Name-only fallback (handles mid-season trades).
	// Only used when exactly one player has this name to avoid collisions.
	norm := NormalizeName(name)
	var match *Projection
	var count int
	for k, v := range s.projections {
		if strings.HasPrefix(k, norm+"|") {
			match = v
			count++
			if count > 1 {
				return nil, false
			}
		}
	}
	if count == 1 {
		return match, true
	}
	return nil, false
}

func projKey(name, team string) string {
	return NormalizeName(name) + "|" + NormalizeTeam(team)
}

// NormalizeTeam maps team abbreviations from various sources (FanGraphs, MLB API)
// to the Fantrax convention, which is the canonical form used throughout the system.
func NormalizeTeam(team string) string {
	switch strings.ToUpper(strings.TrimSpace(team)) {
	case "SDP":
		return "SD"
	case "SFG":
		return "SF"
	case "KCR":
		return "KC"
	case "WSN":
		return "WSH"
	case "TBR":
		return "TB"
	case "AZ":
		return "ARI"
	case "CWS":
		return "CHW"
	case "OAK":
		return "ATH"
	default:
		return strings.ToUpper(strings.TrimSpace(team))
	}
}

// HitterBats returns a map of NormalizeName(name) → bat side ("R", "L", "S").
// Normalizes "B" (both) to "S" (switch).
func (s *FanGraphsSource) HitterBats() map[string]string {
	bats := make(map[string]string, len(s.projections))
	for key, proj := range s.projections {
		name := strings.SplitN(key, "|", 2)[0]
		b := strings.ToUpper(proj.Bats)
		if b == "B" {
			b = "S"
		}
		if b == "R" || b == "L" || b == "S" {
			bats[name] = b
		}
	}
	return bats
}

func NormalizeName(name string) string {
	// Strip diacritics (é→e, í→i, ñ→n) so accented FanGraphs names
	// match plain-ASCII Fantrax names.
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.TrimSpace(name)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}
