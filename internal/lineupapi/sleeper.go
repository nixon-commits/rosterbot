package lineupapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SleeperLeague is one discovered league in this API's own wire shape.
//
// Deliberately not internal/sleeper's League type. Re-declaring five fields is
// cheaper than importing the client here: this package's tests are hermetic and
// network-free, and an import that drags in an HTTP client is how that stops
// being true by accident.
type SleeperLeague struct {
	LeagueID     string `json:"league_id"`
	Name         string `json:"name"`
	Season       string `json:"season"`
	TotalRosters int    `json:"total_rosters"`

	// TeamID is the resolved Sleeper user id — the value that identifies which
	// roster in this league is theirs. Returned here so the client can store a
	// membership from the picker without a second lookup.
	TeamID string `json:"team_id"`
}

// ErrSleeperUserUnknown is what a directory returns for a username that
// resolves to no account, so the handler can answer 404 rather than 502.
var ErrSleeperUserUnknown = errors.New("lineupapi: no such sleeper user")

// SleeperDirectory discovers a Sleeper account's leagues.
//
// An interface rather than a concrete client so this package neither imports
// internal/sleeper nor performs I/O in tests. Nil answers 501, like every other
// optional dependency on Config.
type SleeperDirectory interface {
	LeaguesForUsername(ctx context.Context, username, sport, season string) ([]SleeperLeague, error)
}

// defaultSeason is the season a date falls in.
//
// Sleeper labels a season by the year it STARTS, so January and February of the
// following calendar year still belong to the previous season. Defaulting to
// time.Now().Year() would return an empty league list for every user for two
// months a year, which reads as "you have no leagues" rather than as a bug.
func defaultSeason(now time.Time) string {
	y := now.Year()
	if now.Month() < time.March {
		y--
	}
	return strconv.Itoa(y)
}

// handleSleeperLeagues proxies a username lookup to Sleeper.
//
// Server-side rather than from the browser or the app for two reasons: no
// client needs to learn Sleeper's API shape or base URL, and the lookup lands
// behind the client's read-through cache instead of being issued once per
// keystroke by a picker.
func (cfg Config) handleSleeperLeagues(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.SleeperDir == nil {
		writeErr(w, http.StatusNotImplemented, "sleeper directory not configured")
		return
	}

	q := r.URL.Query()
	username := strings.TrimSpace(q.Get("username"))
	if username == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}
	sport := q.Get("sport")
	if sport == "" {
		sport = "nfl"
	}
	season := q.Get("season")
	if season == "" {
		season = defaultSeason(time.Now().UTC())
	}

	leagues, err := cfg.SleeperDir.LeaguesForUsername(r.Context(), username, sport, season)
	if errors.Is(err, ErrSleeperUserUnknown) {
		writeErr(w, http.StatusNotFound, "no Sleeper account with that username")
		return
	}
	if err != nil {
		// 502, never an empty 200: a Sleeper outage and an account with no
		// leagues are different answers, and only one of them is worth retrying.
		writeErr(w, http.StatusBadGateway, "could not reach Sleeper")
		return
	}
	if leagues == nil {
		leagues = []SleeperLeague{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"leagues": leagues})
}
