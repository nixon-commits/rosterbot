package projections

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

var fangraphsPitchingURL = "https://www.fangraphs.com/api/projections?type=fangraphsdc&stats=pit&pos=all&team=0&players=0&lg=all"

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
	FIP float64 // Fielding Independent Pitching
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
	FIP        float64 `json:"FIP"`
	MLBAMID    int     `json:"xMLBAMID"`
}

// FanGraphsPitcherSource fetches Steamer pitching projections from FanGraphs.
type FanGraphsPitcherSource struct {
	projections map[string]*PitcherProjection
	mlbamIDs    map[string]int // NormalizeName(name) → MLBAM ID
}

// fetchPitchingRows fetches raw pitching projection rows from the FanGraphs API.
func fetchPitchingRows() ([]fgPitchRow, error) {
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
	return rows, nil
}

// buildFanGraphsPitcherSource constructs a FanGraphsPitcherSource from raw rows.
func buildFanGraphsPitcherSource(rows []fgPitchRow) *FanGraphsPitcherSource {
	src := &FanGraphsPitcherSource{
		projections: make(map[string]*PitcherProjection, len(rows)),
		mlbamIDs:    make(map[string]int, len(rows)),
	}
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
			FIP: row.FIP,
		}
		src.projections[projKey(name, team)] = p
		if row.MLBAMID > 0 {
			src.mlbamIDs[NormalizeName(name)] = row.MLBAMID
		}
	}
	return src
}

// NewFanGraphsPitcherSource fetches and parses the FanGraphs pitching projections JSON.
func NewFanGraphsPitcherSource() (*FanGraphsPitcherSource, error) {
	rows, err := fetchPitchingRows()
	if err != nil {
		return nil, err
	}
	return buildFanGraphsPitcherSource(rows), nil
}

// NewFanGraphsPitcherSourceCached is like NewFanGraphsPitcherSource but uses a file cache.
func NewFanGraphsPitcherSourceCached(cacheDir string, ttl time.Duration) (*FanGraphsPitcherSource, error) {
	c := cache.New[[]fgPitchRow](cacheDir, ttl)
	key := cache.Key("fangraphs", "pit", currentAPIType)
	rows, err := c.Get(key, fetchPitchingRows)
	if err != nil {
		return nil, err
	}
	return buildFanGraphsPitcherSource(rows), nil
}

// NewFanGraphsPitcherSourceFromCSV loads Steamer pitching projections from a local CSV file.
func NewFanGraphsPitcherSourceFromCSV(path string) (*FanGraphsPitcherSource, error) {
	required := []string{"Name", "Team", "G", "GS", "IP", "SO", "BB", "H", "ER", "HR", "W", "L", "QS", "SV", "HLD", "HBP", "FIP", "MLBAMID"}
	f, r, col, err := openCSV(path, required)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src := &FanGraphsPitcherSource{
		projections: make(map[string]*PitcherProjection),
		mlbamIDs:    make(map[string]int),
	}

	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv read: %w", err)
		}

		name := strings.TrimSpace(record[col["Name"]])
		team := strings.ToUpper(strings.TrimSpace(record[col["Team"]]))
		if name == "" {
			continue
		}

		p := &PitcherProjection{
			G:   csvFloat(record, col, "G"),
			GS:  csvFloat(record, col, "GS"),
			IP:  csvFloat(record, col, "IP"),
			K:   csvFloat(record, col, "SO"),
			BBA: csvFloat(record, col, "BB"),
			HA:  csvFloat(record, col, "H"),
			ER:  csvFloat(record, col, "ER"),
			HRA: csvFloat(record, col, "HR"),
			W:   csvFloat(record, col, "W"),
			L:   csvFloat(record, col, "L"),
			QS:  csvFloat(record, col, "QS"),
			SV:  csvFloat(record, col, "SV"),
			HLD: csvFloat(record, col, "HLD"),
			BS:  csvFloat(record, col, "BS"),
			HBP: csvFloat(record, col, "HBP"),
			WP:  csvFloat(record, col, "WP"),
			BK:  csvFloat(record, col, "BK"),
			CG:  csvFloat(record, col, "CG"),
			SHO: csvFloat(record, col, "SHO"),
			PKO: csvFloat(record, col, "PKO"),
			FIP: csvFloat(record, col, "FIP"),
		}
		src.projections[projKey(name, team)] = p

		if mlbID := csvInt(record, col, "MLBAMID"); mlbID > 0 {
			src.mlbamIDs[NormalizeName(name)] = mlbID
		}
	}

	return src, nil
}

// Len returns the number of players in this source.
func (s *FanGraphsPitcherSource) Len() int { return len(s.projections) }

// PitcherInfo returns pitcher FIP and IP-weighted league average FIP.
func (s *FanGraphsPitcherSource) PitcherInfo() (fip map[string]float64, leagueAvgFIP float64) {
	fip = make(map[string]float64, len(s.projections))
	var totalFIPxIP, totalIP float64
	for key, proj := range s.projections {
		name := strings.SplitN(key, "|", 2)[0]
		if proj.FIP > 0 {
			fip[name] = proj.FIP
		}
		if proj.IP > 0 && proj.FIP > 0 {
			totalFIPxIP += proj.FIP * proj.IP
			totalIP += proj.IP
		}
	}
	if totalIP > 0 {
		leagueAvgFIP = totalFIPxIP / totalIP
	}
	return
}

// MLBAMIDs returns a map of NormalizeName(name) → MLBAM player ID.
func (s *FanGraphsPitcherSource) MLBAMIDs() map[string]int {
	return s.mlbamIDs
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

// LoadPitcherProjections tries to load pitcher projections with RoS-first priority.
// For base systems (e.g. "depthcharts"): RoS API → Preseason API → CSV.
// For explicit RoS systems (e.g. "depthcharts-ros"): RoS API → CSV.
func LoadPitcherProjections(system, cacheDir string, ttl time.Duration) (*FanGraphsPitcherSource, LoadResult, error) {
	result := LoadResult{System: system}

	// Build the list of systems to try via API.
	systems := []string{}
	if ros, ok := rosVariant[system]; ok {
		systems = append(systems, ros, system)
	} else {
		systems = append(systems, system)
	}

	// Try each API system in order.
	for i, sys := range systems {
		if err := SetProjectionSystem(sys); err != nil {
			continue
		}
		src, err := NewFanGraphsPitcherSourceCached(cacheDir, ttl)
		if err != nil {
			if i < len(systems)-1 {
				continue
			}
			break
		}
		if src.Len() == 0 {
			if i < len(systems)-1 {
				result.FellBack = true
				continue
			}
			break
		}
		result.System = sys
		return src, result, nil
	}

	// Restore the original system for display/cache key consistency.
	SetProjectionSystem(system)

	// CSV fallback.
	src, err := NewFanGraphsPitcherSourceFromCSV("fangraphs-leaderboard-projections_pitchers.csv")
	if err != nil {
		return nil, result, fmt.Errorf("all pitching projection sources unavailable: %w", err)
	}
	if src.Len() == 0 {
		return nil, result, fmt.Errorf("CSV pitching projections file is empty")
	}
	result.FromCSV = true
	return src, result, nil
}
