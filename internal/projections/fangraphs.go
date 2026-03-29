package projections

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"golang.org/x/text/unicode/norm"
)

var fangraphsBattingURL = "https://www.fangraphs.com/api/projections?type=fangraphsdc&stats=bat&pos=all&team=0&players=0&lg=all"

// currentAPIType tracks the FanGraphs API type parameter (e.g. "fangraphsdc", "steamerr")
// set by SetProjectionSystem. Used as part of the cache key.
var currentAPIType = "fangraphsdc"

// Supported FanGraphs projection systems.
const (
	ProjectionSteamer        = "steamer"
	ProjectionDepthCharts    = "depthcharts"
	ProjectionBatX           = "thebatx"
	ProjectionSteamerRoS     = "steamer-ros"
	ProjectionDepthChartsRoS = "depthcharts-ros"
	ProjectionBatXRoS        = "thebatx-ros"
)

// fgProjectionType maps our flag names to FanGraphs API type parameter values.
var fgProjectionType = map[string]string{
	ProjectionSteamer:        "steamer",
	ProjectionDepthCharts:    "fangraphsdc",
	ProjectionBatX:           "thebatx",
	ProjectionSteamerRoS:     "steamerr",
	ProjectionDepthChartsRoS: "rfangraphsdc",
	ProjectionBatXRoS:        "rthebatx",
}

// SetProjectionSystem updates the FanGraphs API URLs to use the given projection system.
// Valid values: "steamer", "depthcharts", "thebatx". Returns an error for unknown systems.
func SetProjectionSystem(system string) error {
	apiType, ok := fgProjectionType[system]
	if !ok {
		return fmt.Errorf("unknown projection system %q (valid: steamer, depthcharts, thebatx, steamer-ros, depthcharts-ros, thebatx-ros)", system)
	}
	currentAPIType = apiType
	base := "https://www.fangraphs.com/api/projections?type=%s&stats=%s&pos=all&team=0&players=0&lg=all"
	fangraphsBattingURL = fmt.Sprintf(base, apiType, "bat")
	fangraphsPitchingURL = fmt.Sprintf(base, apiType, "pit")
	return nil
}

// CurrentAPIType returns the active FanGraphs API type parameter for cache key construction.
func CurrentAPIType() string {
	return currentAPIType
}

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
	MLBAMID    int     `json:"xMLBAMID"`
}

// FanGraphsSource fetches Steamer projections from FanGraphs.
type FanGraphsSource struct {
	projections map[string]*Projection
	mlbamIDs    map[string]int // NormalizeName(name) → MLBAM ID
}

// fetchBattingRows fetches raw batting projection rows from the FanGraphs API.
func fetchBattingRows() ([]fgRow, error) {
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
	return rows, nil
}

// buildFanGraphsSource constructs a FanGraphsSource from raw rows.
func buildFanGraphsSource(rows []fgRow) *FanGraphsSource {
	src := &FanGraphsSource{
		projections: make(map[string]*Projection, len(rows)),
		mlbamIDs:    make(map[string]int, len(rows)),
	}
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
		}
		src.projections[projKey(name, team)] = p
		if row.MLBAMID > 0 {
			src.mlbamIDs[NormalizeName(name)] = row.MLBAMID
		}
	}
	return src
}

// NewFanGraphsSource fetches and parses the FanGraphs batting projections JSON.
func NewFanGraphsSource() (*FanGraphsSource, error) {
	rows, err := fetchBattingRows()
	if err != nil {
		return nil, err
	}
	return buildFanGraphsSource(rows), nil
}

// NewFanGraphsSourceCached is like NewFanGraphsSource but uses a file cache.
func NewFanGraphsSourceCached(cacheDir string, ttl time.Duration) (*FanGraphsSource, error) {
	c := cache.New[[]fgRow](cacheDir, ttl)
	key := cache.Key("fangraphs", "bat", currentAPIType)
	rows, err := c.Get(key, fetchBattingRows)
	if err != nil {
		return nil, err
	}
	return buildFanGraphsSource(rows), nil
}

// NewFanGraphsSourceFromCSV loads Steamer batting projections from a local CSV file
// (exported from FanGraphs leaderboard).
func NewFanGraphsSourceFromCSV(path string) (*FanGraphsSource, error) {
	required := []string{"Name", "Team", "G", "PA", "H", "1B", "2B", "3B", "HR", "RBI", "R", "BB", "SB", "CS", "HBP", "SO", "GDP", "MLBAMID"}
	f, r, col, err := openCSV(path, required)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src := &FanGraphsSource{
		projections: make(map[string]*Projection),
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

		p := &Projection{
			G:       csvFloat(record, col, "G"),
			PA:      csvFloat(record, col, "PA"),
			H:       csvFloat(record, col, "H"),
			Singles: csvFloat(record, col, "1B"),
			Doubles: csvFloat(record, col, "2B"),
			Triples: csvFloat(record, col, "3B"),
			HR:      csvFloat(record, col, "HR"),
			RBI:     csvFloat(record, col, "RBI"),
			R:       csvFloat(record, col, "R"),
			BB:      csvFloat(record, col, "BB"),
			SB:      csvFloat(record, col, "SB"),
			CS:      csvFloat(record, col, "CS"),
			HBP:     csvFloat(record, col, "HBP"),
			SO:      csvFloat(record, col, "SO"),
			GIDP:    csvFloat(record, col, "GDP"),
		}
		src.projections[projKey(name, team)] = p

		if mlbID := csvInt(record, col, "MLBAMID"); mlbID > 0 {
			src.mlbamIDs[NormalizeName(name)] = mlbID
		}
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

// MLBAMIDs returns a map of NormalizeName(name) → MLBAM player ID.
func (s *FanGraphsSource) MLBAMIDs() map[string]int {
	return s.mlbamIDs
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
