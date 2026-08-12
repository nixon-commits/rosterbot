package statsguy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// baseURL is StatsGuy's API root. NOT statsguyfantasy.com, which serves the
// Next.js SPA shell for /api/* — the real API lives on its own subdomain. A
// var so tests can point it at an httptest server.
var baseURL = "https://api.statsguyfantasy.com"

// CacheTTL is the single exported on-disk lifetime for the StatsGuy Bundle.
// A single constant so every caller shares one freshness policy on the same
// cache entries (see internal/statcast.CacheTTL for the precedent — CLAUDE.md
// records that re-typing a TTL as a bare literal at six sites meant the named
// constant governed only a quarter of what it appeared to).
const CacheTTL = 8 * time.Hour

const httpTimeout = 15 * time.Second

type playersResponse struct {
	Total   int         `json:"total"`
	Players []rawPlayer `json:"players"`
}

type rawPlayer struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Team     string       `json:"team"`
	Position string       `json:"position"`
	Value    FormatValues `json:"value"`
}

type picksResponse struct {
	Picks []rawPick `json:"picks"`
}

type rawPick struct {
	ID      string       `json:"id"`
	Year    int          `json:"year"`
	Round   int          `json:"round"`
	Variant string       `json:"variant"`
	Slot    int          `json:"slot"`
	Value   FormatValues `json:"value"`
}

// LoadBundle fetches both StatsGuy slices (cached at ttl) and returns the
// joined Bundle.
func LoadBundle(cacheDir string, ttl time.Duration) (*Bundle, error) {
	playersC := cache.New[playersResponse](cacheDir, ttl)
	players, err := playersC.Get(cache.Key("statsguy-players"), fetchPlayers)
	if err != nil {
		return nil, fmt.Errorf("statsguy players: %w", err)
	}

	picksC := cache.New[picksResponse](cacheDir, ttl)
	picks, err := picksC.Get(cache.Key("statsguy-picks"), fetchPicks)
	if err != nil {
		return nil, fmt.Errorf("statsguy picks: %w", err)
	}

	bundle := &Bundle{Players: make(map[string]Player, len(players.Players))}
	for _, p := range players.Players {
		bundle.Players[p.ID] = Player{
			ID:       p.ID,
			Name:     p.Name,
			Team:     p.Team,
			Position: p.Position,
			Value:    p.Value,
		}
	}
	for _, pk := range picks.Picks {
		bundle.Picks = append(bundle.Picks, Pick{
			ID:      pk.ID,
			Year:    pk.Year,
			Round:   pk.Round,
			Variant: pk.Variant,
			Slot:    pk.Slot,
			Value:   pk.Value,
		})
	}
	return bundle, nil
}

func fetchPlayers() (playersResponse, error) {
	var out playersResponse
	if err := getJSON(baseURL+"/api/v1/players", &out); err != nil {
		return playersResponse{}, err
	}
	return out, nil
}

func fetchPicks() (picksResponse, error) {
	var out picksResponse
	if err := getJSON(baseURL+"/api/v1/picks", &out); err != nil {
		return picksResponse{}, err
	}
	return out, nil
}

func getJSON(url string, out any) error {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("GET %s: read body: %w", url, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", url, err)
	}
	return nil
}
