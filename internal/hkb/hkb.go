package hkb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// fetchURL is a var so tests can override it.
var fetchURL = "https://harryknowsball.com/rankings"

// Player represents a single player from the HKB rankings dataset.
type Player struct {
	ID                 string         `json:"id"`
	OriginalIndex      int            `json:"originalIndex"`
	Rank               int            `json:"rank"`
	Name               string         `json:"name"`
	Age                float64        `json:"age"`
	Positions          []string       `json:"positions"`
	PositionRanks      map[string]int `json:"positionRanks"`
	Team               string         `json:"team"`
	Level              string         `json:"level"`
	HitterStats        *HitterStats   `json:"hitterStats"`
	PitcherStats       *PitcherStats  `json:"pitcherStats"`
	StatsYear          int            `json:"statsYear"`
	ActiveLevels       string         `json:"activeLevels"`
	Value              int            `json:"value"`
	ValueChange30Days  int            `json:"valueChange30Days"`
	RankChange30Days   int            `json:"rankChange30Days"`
	ValueChange7Days   int            `json:"valueChange7Days"`
	RankChange7Days    int            `json:"rankChange7Days"`
	AssetType          string         `json:"assetType"`
	ValueHistory30Days []int          `json:"valueHistory30Days"`
	RankHistory30Days  []int          `json:"rankHistory30Days"`
	Active             bool           `json:"active"`
	Prospect           bool           `json:"prospect"`
	FYPD               bool           `json:"fypd"`
}

// HitterStats contains batting statistics from HKB.
type HitterStats struct {
	Level            *string `json:"level"`
	GamesPlayed      int     `json:"gamesPlayed"`
	Runs             int     `json:"runs"`
	HomeRuns         int     `json:"homeRuns"`
	StrikeOuts       int     `json:"strikeOuts"`
	BaseOnBalls      int     `json:"baseOnBalls"`
	AVG              float64 `json:"avg"`
	AtBats           int     `json:"atBats"`
	OBP              float64 `json:"obp"`
	SLG              float64 `json:"slg"`
	OPS              float64 `json:"ops"`
	CaughtStealing   int     `json:"caughtStealing"`
	StolenBases      int     `json:"stolenBases"`
	PlateAppearances int     `json:"plateAppearances"`
	RBI              int     `json:"rbi"`
	TotalMetric      float64 `json:"totalMetric"`
}

// PitcherStats contains pitching statistics from HKB.
type PitcherStats struct {
	Level          *string `json:"level"`
	GamesPlayed    int     `json:"gamesPlayed"`
	InningsPitched float64 `json:"inningsPitched"`
	StrikeOuts     int     `json:"strikeOuts"`
	BaseOnBalls    int     `json:"baseOnBalls"`
	ERA            float64 `json:"era"`
	WHIP           float64 `json:"whip"`
	Wins           int     `json:"wins"`
	Losses         int     `json:"losses"`
	Saves          int     `json:"saves"`
	HomeRuns       int     `json:"homeRuns"`
	HitsAllowed    int     `json:"hitsAllowed"`
	GamesStarted   int     `json:"gamesStarted"`
	TotalMetric    float64 `json:"totalMetric"`
}

// nextDataPayload is the top-level __NEXT_DATA__ JSON structure.
type nextDataPayload struct {
	Props struct {
		PageProps struct {
			LastUpdated string   `json:"lastUpdated"`
			Players     []Player `json:"players"`
		} `json:"pageProps"`
	} `json:"props"`
}

var nextDataRe = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__"[^>]*>(.*?)</script>`)

// GetPlayers returns all HKB players, using a file cache with 8h TTL.
// This is the single entry point for all HKB data consumers.
func GetPlayers(cacheDir string) ([]Player, error) {
	c := cache.New[[]Player](cacheDir, 8*time.Hour)
	return c.Get("hkb-players", fetchPlayers)
}

func fetchPlayers() ([]Player, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(fetchURL)
	if err != nil {
		return nil, fmt.Errorf("hkb fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hkb: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hkb read body: %w", err)
	}

	matches := nextDataRe.FindSubmatch(body)
	if len(matches) < 2 {
		return nil, fmt.Errorf("hkb: __NEXT_DATA__ script tag not found in response")
	}

	var payload nextDataPayload
	if err := json.Unmarshal(matches[1], &payload); err != nil {
		return nil, fmt.Errorf("hkb json unmarshal: %w", err)
	}

	return payload.Props.PageProps.Players, nil
}
