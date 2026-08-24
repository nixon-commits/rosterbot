package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeSleeperDir struct {
	leagues  []SleeperLeague
	err      error
	gotUser  string
	gotSport string
	gotSeas  string
}

func (f *fakeSleeperDir) LeaguesForUsername(_ context.Context, username, sport, season string) ([]SleeperLeague, error) {
	f.gotUser, f.gotSport, f.gotSeas = username, sport, season
	return f.leagues, f.err
}

// Sleeper labels a season by the year it STARTS, so January and February of
// the following calendar year still belong to the previous season. Asking for
// "2027" in January 2027 would return an empty list for every user.
func TestDefaultSeasonRollsOverInMarch(t *testing.T) {
	for _, tc := range []struct {
		when time.Time
		want string
	}{
		{time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "2025"},
		{time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), "2025"},
		{time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "2026"},
		{time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), "2026"},
		{time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), "2026"},
	} {
		if got := defaultSeason(tc.when); got != tc.want {
			t.Errorf("defaultSeason(%s) = %q, want %q", tc.when.Format("2006-01-02"), got, tc.want)
		}
	}
}

func TestSleeperLeaguesReturnsLeagues(t *testing.T) {
	dir := &fakeSleeperDir{leagues: []SleeperLeague{
		{LeagueID: "1", Name: "Dynasty", Season: "2026", TotalRosters: 12, TeamID: "456"},
	}}
	cfg := Config{SleeperDir: dir}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues?username=jnixon", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleSleeperLeagues(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var body struct {
		Leagues []SleeperLeague `json:"leagues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Leagues) != 1 || body.Leagues[0].Name != "Dynasty" {
		t.Errorf("leagues = %+v, want one named Dynasty", body.Leagues)
	}
	if dir.gotUser != "jnixon" {
		t.Errorf("username = %q, want jnixon", dir.gotUser)
	}
	if dir.gotSport != "nfl" {
		t.Errorf("sport = %q, want the nfl default", dir.gotSport)
	}
}

func TestSleeperLeaguesHonoursExplicitSportAndSeason(t *testing.T) {
	dir := &fakeSleeperDir{}
	cfg := Config{SleeperDir: dir}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues?username=x&sport=nba&season=2024", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleSleeperLeagues(rec, req)

	if dir.gotSport != "nba" || dir.gotSeas != "2024" {
		t.Errorf("sport/season = %q/%q, want nba/2024", dir.gotSport, dir.gotSeas)
	}
}

func TestSleeperLeaguesRequiresUsername(t *testing.T) {
	cfg := Config{SleeperDir: &fakeSleeperDir{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleSleeperLeagues(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSleeperLeaguesUnknownUsernameIs404(t *testing.T) {
	cfg := Config{SleeperDir: &fakeSleeperDir{err: ErrSleeperUserUnknown}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues?username=nobody", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleSleeperLeagues(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// A Sleeper outage must not read as "this username has no leagues".
func TestSleeperLeaguesUpstreamFailureIs502(t *testing.T) {
	cfg := Config{SleeperDir: &fakeSleeperDir{err: errors.New("boom")}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues?username=x", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleSleeperLeagues(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestSleeperLeaguesUnconfiguredIs501(t *testing.T) {
	cfg := Config{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues?username=x", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleSleeperLeagues(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

// The bearer-token operator has no UserID and so no memberships to discover
// against; this endpoint is for a signed-in tenant.
func TestSleeperLeaguesRequiresPasskeySession(t *testing.T) {
	cfg := Config{SleeperDir: &fakeSleeperDir{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues?username=x", nil)
	cfg.handleSleeperLeagues(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// sleeperLeaguesReq builds an authenticated discovery request.
func sleeperLeaguesReq(query string) *http.Request {
	req := httptest.NewRequest("GET", "/v1/sleeper/leagues?"+query, nil)
	return req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
}

// These three strings are joined into a cache key, and internal/cache resolves
// a key to <root>/<key>.json — so a separator in any of them reads and writes
// a file outside the cache root. internal/sleeper refuses them too, but a
// caller who typed nonsense deserves a 400 rather than the 502 an internal
// error would produce, and the boundary is where the input is still a user's.
func TestSleeperLeaguesRejectsUnsafeParameters(t *testing.T) {
	for name, query := range map[string]string{
		"traversal username": "username=" + url.QueryEscape("/../../../../../../tmp/pwned"),
		"slash in username":  "username=" + url.QueryEscape("a/b"),
		"dots in username":   "username=" + url.QueryEscape("a..b"),
		"space in username":  "username=" + url.QueryEscape("a b"),
		"overlong username":  "username=" + strings.Repeat("a", 65),
		"traversal sport":    "username=x&sport=" + url.QueryEscape("../pwned"),
		"uppercase sport":    "username=x&sport=NFL",
		"overlong sport":     "username=x&sport=abcdefghi",
		"traversal season":   "username=x&season=" + url.QueryEscape("../pwned"),
		"non-numeric season": "username=x&season=twenty",
		"short season":       "username=x&season=202",
	} {
		dir := &fakeSleeperDir{}
		cfg := Config{SleeperDir: dir}
		rec := httptest.NewRecorder()
		cfg.handleSleeperLeagues(rec, sleeperLeaguesReq(query))

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", name, rec.Code, rec.Body)
		}
		if dir.gotUser != "" || dir.gotSport != "" || dir.gotSeas != "" {
			t.Errorf("%s: reached the directory with %q/%q/%q; validation must run first",
				name, dir.gotUser, dir.gotSport, dir.gotSeas)
		}
	}
}

// A whitespace-only parameter is not a value the caller chose, it is an empty
// one — so it must take the default. Without the trim, `?sport=%20` skipped
// the "nfl" default and forwarded a single space to Sleeper.
func TestSleeperLeaguesTrimsBeforeDefaulting(t *testing.T) {
	dir := &fakeSleeperDir{}
	cfg := Config{SleeperDir: dir}
	rec := httptest.NewRecorder()
	cfg.handleSleeperLeagues(rec, sleeperLeaguesReq("username=x&sport=%20&season=%20%20"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if dir.gotSport != "nfl" {
		t.Errorf("sport = %q, want the nfl default", dir.gotSport)
	}
	if dir.gotSeas != defaultSeason(time.Now().UTC()) {
		t.Errorf("season = %q, want the current-season default", dir.gotSeas)
	}

	// And a padded REAL value trims to itself rather than failing validation,
	// which is the same courtesy username has always had.
	dir = &fakeSleeperDir{}
	cfg = Config{SleeperDir: dir}
	rec = httptest.NewRecorder()
	cfg.handleSleeperLeagues(rec, sleeperLeaguesReq("username=x&season=%202026%20"))
	if rec.Code != http.StatusOK || dir.gotSeas != "2026" {
		t.Errorf("padded season: status %d, season %q; want 200 and 2026", rec.Code, dir.gotSeas)
	}
}

// Underscores and digits are legal in a Sleeper username; the guard must not
// reject a real one.
func TestSleeperLeaguesAcceptsARealisticUsername(t *testing.T) {
	dir := &fakeSleeperDir{}
	cfg := Config{SleeperDir: dir}
	rec := httptest.NewRecorder()
	cfg.handleSleeperLeagues(rec, sleeperLeaguesReq("username=jon_nixon99&sport=nba&season=2024"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if dir.gotUser != "jon_nixon99" {
		t.Errorf("username = %q, want jon_nixon99", dir.gotUser)
	}
}
