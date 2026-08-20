// Package sleeper is a read-only client for the public Sleeper fantasy
// football API (no API key). It is a zero-dependency leaf beyond
// internal/cache, mirroring internal/schedule's shape: URLs as vars for test
// override, a Client struct with an optional CacheDir, plain (non-context)
// method signatures.
package sleeper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// baseURL is Sleeper's public API root. A var so tests can point it at an
// httptest server.
var baseURL = "https://api.sleeper.app/v1"

// RosterTTL is the on-disk cache lifetime for league/roster/user/state data —
// it drifts during the day (waiver moves, trades), so it's cached at the same
// "today, but stable for a window" cadence as Fantrax's todayTTL.
const RosterTTL = 15 * time.Minute

// PlayersTTL is the on-disk cache lifetime for the full NFL player dump
// (/v1/players/nfl, ~14.6 MB). It changes slowly (roster moves, rookie
// additions) and must never be fetched in a loop, hence the long TTL.
const PlayersTTL = 24 * time.Hour

// Client fetches data from the Sleeper API. Leave CacheDir empty to disable
// caching (e.g. hermetic tests).
type Client struct {
	http     http.Client
	CacheDir string
}

func NewClient() *Client {
	return &Client{http: http.Client{Timeout: 10 * time.Second}}
}

// cached is the shared cache-or-fetch branch every public method below uses:
// short-circuit straight to fetch when caching is disabled (CacheDir == ""),
// otherwise read-through a FileCache at ttl keyed on key. Mirrors
// internal/fantrax/cached.go's cached[T] helper.
func cached[T any](c *Client, ttl time.Duration, key string, fetch func() (T, error)) (T, error) {
	if c.CacheDir == "" {
		return fetch()
	}
	return cache.New[T](c.CacheDir, ttl).Get(key, fetch)
}

func (c *Client) get(path string, out any) error {
	resp, err := c.http.Get(baseURL + path)
	if err != nil {
		return fmt.Errorf("sleeper GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sleeper GET %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("sleeper GET %s: read body: %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("sleeper GET %s: decode: %w", path, err)
	}
	return nil
}

// League fetches a league's static configuration.
func (c *Client) League(leagueID string) (*League, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-league", leagueID), func() (*League, error) {
		return c.fetchLeague(leagueID)
	})
}

func (c *Client) fetchLeague(leagueID string) (*League, error) {
	var lg League
	if err := c.get("/league/"+leagueID, &lg); err != nil {
		return nil, err
	}
	return &lg, nil
}

// Rosters fetches every team's roster in the league.
func (c *Client) Rosters(leagueID string) ([]Roster, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-rosters", leagueID), func() ([]Roster, error) {
		return c.fetchRosters(leagueID)
	})
}

func (c *Client) fetchRosters(leagueID string) ([]Roster, error) {
	var rosters []Roster
	if err := c.get("/league/"+leagueID+"/rosters", &rosters); err != nil {
		return nil, err
	}
	return rosters, nil
}

// Users fetches every league member.
func (c *Client) Users(leagueID string) ([]User, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-users", leagueID), func() ([]User, error) {
		return c.fetchUsers(leagueID)
	})
}

func (c *Client) fetchUsers(leagueID string) ([]User, error) {
	var users []User
	if err := c.get("/league/"+leagueID+"/users", &users); err != nil {
		return nil, err
	}
	return users, nil
}

// TradedPicks fetches every draft pick that has changed hands from its
// original owner.
func (c *Client) TradedPicks(leagueID string) ([]TradedPick, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-traded-picks", leagueID), func() ([]TradedPick, error) {
		return c.fetchTradedPicks(leagueID)
	})
}

func (c *Client) fetchTradedPicks(leagueID string) ([]TradedPick, error) {
	var picks []TradedPick
	if err := c.get("/league/"+leagueID+"/traded_picks", &picks); err != nil {
		return nil, err
	}
	return picks, nil
}

// Transactions fetches every transaction filed under the given week
// ("round" in Sleeper's API — regular-season week number).
func (c *Client) Transactions(leagueID string, week int) ([]Transaction, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-transactions", leagueID, strconv.Itoa(week)), func() ([]Transaction, error) {
		return c.fetchTransactions(leagueID, week)
	})
}

func (c *Client) fetchTransactions(leagueID string, week int) ([]Transaction, error) {
	var txns []Transaction
	if err := c.get("/league/"+leagueID+"/transactions/"+strconv.Itoa(week), &txns); err != nil {
		return nil, err
	}
	return txns, nil
}

// State fetches the current NFL week/season.
func (c *Client) State() (*NFLState, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-state"), func() (*NFLState, error) {
		return c.fetchState()
	})
}

func (c *Client) fetchState() (*NFLState, error) {
	var st NFLState
	if err := c.get("/state/nfl", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// PlayersNFL fetches the full NFL player dump (~14.6 MB, all sports). Always
// cached at PlayersTTL when CacheDir is set — this must never be called in a
// loop; callers should fetch it once per run and pass the map along.
func (c *Client) PlayersNFL() (map[string]Player, error) {
	return cached(c, PlayersTTL, cache.Key("sleeper-players-nfl"), func() (map[string]Player, error) {
		return c.fetchPlayersNFL()
	})
}

func (c *Client) fetchPlayersNFL() (map[string]Player, error) {
	var players map[string]Player
	if err := c.get("/players/nfl", &players); err != nil {
		return nil, err
	}
	return players, nil
}
