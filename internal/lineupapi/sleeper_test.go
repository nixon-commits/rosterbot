package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
