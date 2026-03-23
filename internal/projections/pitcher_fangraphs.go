package projections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var fangraphsPitchingURL = "https://www.fangraphs.com/api/projections?type=steamer&stats=pit&pos=all&team=0&players=0&lg=all"

// PitcherProjection holds projected season counting stats for a pitcher.
// All values are season totals; per-game rates are derived by dividing by G.
type PitcherProjection struct {
	G   float64 // Games
	GS  float64 // Games started
	IP  float64 // Innings pitched
	K   float64 // Strikeouts
	BBA float64 // Walks allowed
	HA  float64 // Hits allowed
	ER  float64 // Earned runs
	HRA float64 // Home runs allowed
	W   float64 // Wins
	L   float64 // Losses
	QS  float64 // Quality starts
	SV  float64 // Saves
	HLD float64 // Holds
	BS  float64 // Blown saves
	HBP float64 // Hit batsmen (pitcher)
	WP  float64 // Wild pitches
	BK  float64 // Balks
	CG  float64 // Complete games
	SHO float64 // Shutouts
	PKO float64 // Pickoffs
}

// PitcherSource can look up a pitcher projection for a player.
type PitcherSource interface {
	GetPitcherProjection(name, mlbTeam string) (*PitcherProjection, bool)
}

type fgPitchRow struct {
	PlayerName string  `json:"PlayerName"`
	Team       string  `json:"Team"`
	G          float64 `json:"G"`
	GS         float64 `json:"GS"`
	IP         float64 `json:"IP"`
	K          float64 `json:"SO"` // FanGraphs uses "SO" for pitcher strikeouts
	BBA        float64 `json:"BB"`
	HA         float64 `json:"H"`
	ER         float64 `json:"ER"`
	HRA        float64 `json:"HR"`
	W          float64 `json:"W"`
	L          float64 `json:"L"`
	QS         float64 `json:"QS"`
	SV         float64 `json:"SV"`
	HLD        float64 `json:"HLD"`
	BS         float64 `json:"BS"`
	HBP        float64 `json:"HBP"`
	WP         float64 `json:"WP"`
	BK         float64 `json:"BK"`
	CG         float64 `json:"CG"`
	SHO        float64 `json:"SHO"`
	PKO        float64 `json:"PKO"`
}

// FanGraphsPitcherSource fetches Steamer pitching projections from FanGraphs.
type FanGraphsPitcherSource struct {
	projections map[string]*PitcherProjection
}

// NewFanGraphsPitcherSource fetches and parses the FanGraphs pitching projections JSON.
func NewFanGraphsPitcherSource() (*FanGraphsPitcherSource, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(fangraphsPitchingURL)
	if err != nil {
		return nil, fmt.Errorf("fangraphs pitching fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fangraphs pitching: status %d", resp.StatusCode)
	}

	var rows []fgPitchRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("fangraphs pitching json: %w", err)
	}

	src := &FanGraphsPitcherSource{projections: make(map[string]*PitcherProjection, len(rows))}
	for _, row := range rows {
		name := strings.TrimSpace(row.PlayerName)
		team := strings.ToUpper(strings.TrimSpace(row.Team))
		if name == "" {
			continue
		}
		p := &PitcherProjection{
			G: row.G, GS: row.GS, IP: row.IP, K: row.K,
			BBA: row.BBA, HA: row.HA, ER: row.ER, HRA: row.HRA,
			W: row.W, L: row.L, QS: row.QS,
			SV: row.SV, HLD: row.HLD, BS: row.BS,
			HBP: row.HBP, WP: row.WP, BK: row.BK,
			CG: row.CG, SHO: row.SHO, PKO: row.PKO,
		}
		src.projections[projKey(name, team)] = p
	}
	return src, nil
}

// GetPitcherProjection looks up a pitcher's projection by name and MLB team.
func (s *FanGraphsPitcherSource) GetPitcherProjection(name, mlbTeam string) (*PitcherProjection, bool) {
	key := projKey(name, mlbTeam)
	if p, ok := s.projections[key]; ok {
		return p, true
	}
	// Name-only fallback (handles mid-season trades).
	norm := NormalizeName(name)
	var match *PitcherProjection
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
