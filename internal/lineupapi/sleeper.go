package lineupapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SleeperLeague is one discovered league in this API's own wire shape.
//
// Deliberately not internal/sleeper's League type. Re-declaring seven fields is
// cheaper than importing the client here: this package's tests are hermetic and
// network-free, and an import that drags in an HTTP client is how that stops
// being true by accident.
type SleeperLeague struct {
	LeagueID     string `json:"league_id"`
	Name         string `json:"name"`
	Season       string `json:"season"`
	TotalRosters int    `json:"total_rosters"`

	// Status is Sleeper's own lifecycle value: pre_draft, drafting, in_season,
	// complete. It is here because it is DOCUMENTED, which settings.type — the
	// field a "Redraft"/"Dynasty" label would have to come from — is not: the
	// Sleeper docs render settings as an empty object, and this account's own
	// leagues carry a type value the conventional 0/1/2 mapping does not cover.
	// A format word derived from it would render confidently for every league
	// while being unverifiable for some, so none is served.
	Status string `json:"status,omitempty"`

	// Avatar is the league's avatar id, resolved by a client against
	// https://sleepercdn.com/avatars/thumbs/<avatar>. Empty is ordinary — a
	// league need not have one — so clients must carry a fallback rather than
	// render a broken image.
	Avatar string `json:"avatar,omitempty"`

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

// The three parameters this endpoint forwards, each pinned to the shape it
// really has. Compiled once here rather than per request.
//
// This is not cosmetic validation. All three are joined into a cache key by
// internal/sleeper, and internal/cache resolves a key to <root>/<key>.json —
// so a value carrying a separator addresses a file OUTSIDE the cache root. The
// Lambda roots that cache at /tmp/sleeper-cache, and these values arrive from
// a tenant's query string, which is the whole exposure. internal/sleeper
// refuses such arguments itself; validating here as well is what turns a typo
// into a 400 the caller can act on rather than the 502 an internal error gets,
// and keeps the guarantee from depending on one layer alone.
var (
	// Sleeper's own username charset.
	sleeperUsernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	// A sport code: "nfl", "nba", "lcs".
	sleeperSportRe = regexp.MustCompile(`^[a-z]{2,8}$`)
	// A season is the four-digit year the season STARTS in.
	sleeperSeasonRe = regexp.MustCompile(`^[0-9]{4}$`)
)

// handleSleeperLeagues proxies a username lookup to Sleeper.
//
// Server-side rather than from the browser or the app for two reasons: no
// client needs to learn Sleeper's API shape or base URL, and every lookup goes
// through one place that can be cached or rate-limited rather than N app
// installs hitting Sleeper directly. The caching is weaker than it sounds —
// serve disables it (New("")) and Lambda's lives in per-execution-environment
// /tmp — so a picker still reaches Sleeper across cold containers.
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
	// Trimmed BEFORE the empty check, exactly as username is: a whitespace-only
	// parameter is an absent value, not a chosen one. Without this, `?sport=%20`
	// skipped the "nfl" default and forwarded a single space to Sleeper.
	sport := strings.TrimSpace(q.Get("sport"))
	if sport == "" {
		sport = "nfl"
	}
	season := strings.TrimSpace(q.Get("season"))
	if season == "" {
		season = defaultSeason(time.Now().UTC())
	}
	if !sleeperUsernameRe.MatchString(username) {
		writeErr(w, http.StatusBadRequest, "username is not a valid Sleeper username")
		return
	}
	if !sleeperSportRe.MatchString(sport) {
		writeErr(w, http.StatusBadRequest, "sport is not a valid sport code")
		return
	}
	if !sleeperSeasonRe.MatchString(season) {
		writeErr(w, http.StatusBadRequest, "season must be a four-digit year")
		return
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
