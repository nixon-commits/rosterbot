// Package sleeper is a read-only client for the public Sleeper fantasy
// football API (no API key). It is a zero-dependency leaf beyond
// internal/cache, mirroring internal/schedule's shape: URLs as vars for test
// override, a Client struct with an optional CacheDir.
package sleeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("sleeper GET %s: build request: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sleeper GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
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

// ErrInvalidArgument rejects an argument that cannot safely become part of a
// cache key.
//
// A key is a FILE PATH: internal/cache resolves it as <root>/<key>.json. So an
// argument carrying a separator or a parent reference escapes the cache root —
// with the Lambda's /tmp/sleeper-cache root, a username of
// "/../../../../../../tmp/pwned" resolves to /tmp/pwned.json. That matters here
// and not in the other methods because UserByName and LeaguesForUser take
// TENANT-SUPPLIED strings from a query string, and the cache READ happens
// unconditionally, before any fetch, so simply calling the method is enough.
//
// Real Sleeper usernames and ids never contain these characters, so refusing
// them costs nothing a caller wanted.
var ErrInvalidArgument = errors.New("sleeper: argument contains a path separator")

// checkKeyParts refuses any argument that would escape the cache root. Shared
// by both call sites rather than repeated inline, so a third one added later
// has one obvious thing to call.
func checkKeyParts(parts ...string) error {
	for _, p := range parts {
		if strings.ContainsAny(p, `/\`) || strings.Contains(p, "..") {
			return fmt.Errorf("%w: %q", ErrInvalidArgument, p)
		}
	}
	return nil
}

// League fetches a league's static configuration.
func (c *Client) League(ctx context.Context, leagueID string) (*League, error) {
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-league", leagueID), RosterTTL, func() (*League, error) {
		return c.fetchLeague(ctx, leagueID)
	})
}

func (c *Client) fetchLeague(ctx context.Context, leagueID string) (*League, error) {
	var lg League
	if err := c.get(ctx, "/league/"+leagueID, &lg); err != nil {
		return nil, err
	}
	return &lg, nil
}

// Rosters fetches every team's roster in the league.
func (c *Client) Rosters(ctx context.Context, leagueID string) ([]Roster, error) {
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-rosters", leagueID), RosterTTL, func() ([]Roster, error) {
		return c.fetchRosters(ctx, leagueID)
	})
}

func (c *Client) fetchRosters(ctx context.Context, leagueID string) ([]Roster, error) {
	var rosters []Roster
	if err := c.get(ctx, "/league/"+leagueID+"/rosters", &rosters); err != nil {
		return nil, err
	}
	return rosters, nil
}

// Users fetches every league member.
func (c *Client) Users(ctx context.Context, leagueID string) ([]User, error) {
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-users", leagueID), RosterTTL, func() ([]User, error) {
		return c.fetchUsers(ctx, leagueID)
	})
}

func (c *Client) fetchUsers(ctx context.Context, leagueID string) ([]User, error) {
	var users []User
	if err := c.get(ctx, "/league/"+leagueID+"/users", &users); err != nil {
		return nil, err
	}
	return users, nil
}

// TradedPicks fetches every draft pick that has changed hands from its
// original owner.
func (c *Client) TradedPicks(ctx context.Context, leagueID string) ([]TradedPick, error) {
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-traded-picks", leagueID), RosterTTL, func() ([]TradedPick, error) {
		return c.fetchTradedPicks(ctx, leagueID)
	})
}

func (c *Client) fetchTradedPicks(ctx context.Context, leagueID string) ([]TradedPick, error) {
	var picks []TradedPick
	if err := c.get(ctx, "/league/"+leagueID+"/traded_picks", &picks); err != nil {
		return nil, err
	}
	return picks, nil
}

// Transactions fetches every transaction filed under the given week
// ("round" in Sleeper's API — regular-season week number).
func (c *Client) Transactions(ctx context.Context, leagueID string, week int) ([]Transaction, error) {
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-transactions", leagueID, strconv.Itoa(week)), RosterTTL, func() ([]Transaction, error) {
		return c.fetchTransactions(ctx, leagueID, week)
	})
}

func (c *Client) fetchTransactions(ctx context.Context, leagueID string, week int) ([]Transaction, error) {
	var txns []Transaction
	if err := c.get(ctx, "/league/"+leagueID+"/transactions/"+strconv.Itoa(week), &txns); err != nil {
		return nil, err
	}
	return txns, nil
}

// State fetches the current NFL week/season.
func (c *Client) State(ctx context.Context) (*NFLState, error) {
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-state"), RosterTTL, func() (*NFLState, error) {
		return c.fetchState(ctx)
	})
}

func (c *Client) fetchState(ctx context.Context) (*NFLState, error) {
	var st NFLState
	if err := c.get(ctx, "/state/nfl", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// PlayersNFL fetches the full NFL player dump (~14.6 MB, all sports). Always
// cached at PlayersTTL when CacheDir is set — this must never be called in a
// loop; callers should fetch it once per run and pass the map along.
func (c *Client) PlayersNFL(ctx context.Context) (map[string]Player, error) {
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-players-nfl"), PlayersTTL, func() (map[string]Player, error) {
		return c.fetchPlayersNFL(ctx)
	})
}

func (c *Client) fetchPlayersNFL(ctx context.Context) (map[string]Player, error) {
	var players map[string]Player
	if err := c.get(ctx, "/players/nfl", &players); err != nil {
		return nil, err
	}
	return players, nil
}

// ErrUserNotFound is returned when a username resolves to no Sleeper account.
//
// It exists because Sleeper spells that outcome as a 200 with a `null` body
// rather than a 404, which get() cannot distinguish from success. Returning a
// zero User instead would send the caller on to request leagues for the empty
// string and get an empty list — "this username has no leagues" rather than
// "there is no such username", which are different answers to show a user.
var ErrUserNotFound = errors.New("sleeper: no such user")

// UserByName resolves a Sleeper username to its account.
func (c *Client) UserByName(ctx context.Context, username string) (*User, error) {
	if err := checkKeyParts(username); err != nil {
		return nil, err
	}
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-user", username), RosterTTL, func() (*User, error) {
		return c.fetchUserByName(ctx, username)
	})
}

func (c *Client) fetchUserByName(ctx context.Context, username string) (*User, error) {
	var u User
	if err := c.get(ctx, "/user/"+url.PathEscape(username), &u); err != nil {
		return nil, err
	}
	if u.UserID == "" {
		return nil, ErrUserNotFound
	}
	return &u, nil
}

// LeaguesForUser lists every league an account belongs to for one sport and
// season. Sleeper scopes leagues by both, so a user id alone does not identify
// a league set.
func (c *Client) LeaguesForUser(ctx context.Context, userID, sport, season string) ([]League, error) {
	if err := checkKeyParts(userID, sport, season); err != nil {
		return nil, err
	}
	return cache.GetOrFetch(c.CacheDir, cache.Key("sleeper-user-leagues", userID, sport, season), RosterTTL,
		func() ([]League, error) {
			return c.fetchLeaguesForUser(ctx, userID, sport, season)
		})
}

func (c *Client) fetchLeaguesForUser(ctx context.Context, userID, sport, season string) ([]League, error) {
	path := "/user/" + url.PathEscape(userID) +
		"/leagues/" + url.PathEscape(sport) +
		"/" + url.PathEscape(season)
	var ls []League
	if err := c.get(ctx, path, &ls); err != nil {
		return nil, err
	}
	return ls, nil
}
