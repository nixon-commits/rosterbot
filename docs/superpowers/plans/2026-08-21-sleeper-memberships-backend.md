# Sleeper Memberships (Backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a tenant enter a Sleeper username, see the leagues that account belongs to, and store the ones they pick on their own record — read-only, with no new persisted state per league.

**Architecture:** A `Membership` value type models "one league a tenant belongs to" on any platform. The existing `User.TeamID` stays authoritative for Fantrax and is *projected* into a Membership by `AllMemberships()`, so the stored `Memberships` slice holds Sleeper entries only and no migration is needed. Two new read methods on the existing `internal/sleeper` client are exposed through a `SleeperDirectory` interface so `lineupapi` never imports the client and stays hermetically testable. Four routes, all member-reachable by virtue of *not* being listed in `adminOnlyRoutes`.

**Tech Stack:** Go 1.x, stdlib `net/http` `ServeMux` with method+pattern routing, `internal/cache` for read-through caching, stdlib `testing` + `httptest`. No new dependencies — `internal/sleeper` is documented as "a zero-dependency leaf beyond internal/cache" and must stay one.

**Spec:** `docs/superpowers/specs/2026-08-21-sleeper-memberships-design.md`

## Global Constraints

- **No new module dependencies.** `internal/sleeper` is a zero-dependency leaf beyond `internal/cache`. Keep it that way.
- **`lineupapi` must not import `internal/sleeper`.** Discovery is reached through the `SleeperDirectory` interface defined in `lineupapi`. This is what keeps `lineupapi`'s tests network-free.
- **errcheck is a hard gate.** Every returned error is checked except the print family and `(net/http.ResponseWriter).Write`. Each `_ =` must be a deliberate, comment-justified decision.
- **`adminOnlyRoutes` is an ALLOWLIST** (`internal/lineupapi/authz.go:89`). Omitting a route makes it member-reachable. None of the four routes here go in that list — and Task 4 pins that with a test, because the failure direction the list exists to make loud is a route accidentally treated as tenant-safe.
- **A nil optional dependency answers 501**, never a panic and never a silent success. Matches `cfg.Users == nil`, `cfg.Infra == nil`, and the rest.
- **Sleeper memberships are NEVER writable.** `Writable` is `false` at the one construction site and asserted by test. Nothing may set it true.
- **`POST`/`DELETE /v1/memberships` refuse `platform: "fantrax"`.** Fantrax membership is established by invite and *proven* by connect (`internal/lineupapi/connect.go:81`); accepting one over HTTP would let a caller assert the exact fact the connect task exists to prove.
- **Tests require no credentials and no network.** All HTTP is `httptest`; `internal/sleeper` overrides its `baseURL` package var.
- **Gates:** `make lint` (golangci-lint, all four modules) and `make test`. `gofmt` runs automatically via a PostToolUse hook.

## File Structure

| File | Responsibility |
|---|---|
| `internal/lineupapi/membership.go` *(new)* | `Platform`, `Membership`, `User.AllMemberships()`. Pure value logic, no I/O. |
| `internal/lineupapi/membership_test.go` *(new)* | Projection behaviour, including the every-existing-tenant case. |
| `internal/lineupapi/user.go` *(modify)* | Add the `Memberships` field to `User`. |
| `internal/sleeper/client.go` *(modify)* | `UserByName`, `LeaguesForUser` + their `fetch*` halves. |
| `internal/sleeper/client_test.go` *(modify)* | httptest coverage for both, including unknown-username. |
| `internal/lineupapi/sleeper.go` *(new)* | `SleeperLeague`, `SleeperDirectory`, `defaultSeason`, `handleSleeperLeagues`. |
| `internal/lineupapi/sleeper_test.go` *(new)* | Handler behaviour against a fake directory. |
| `internal/lineupapi/memberships.go` *(new)* | The three membership CRUD handlers. |
| `internal/lineupapi/memberships_test.go` *(new)* | CRUD behaviour, both refusals, and the not-admin-only assertion. |
| `internal/lineupapi/handler.go` *(modify)* | `SleeperDir` config field, four route registrations, doc-comment block. |
| `internal/sleeperdir/sleeperdir.go` *(new)* | Adapter implementing `lineupapi.SleeperDirectory` over `internal/sleeper`. Imports both; nothing imports it but wiring. |
| `internal/sleeperdir/sleeperdir_test.go` *(new)* | Adapter maps client types onto wire types correctly. |
| `cmd/serve.go` *(modify)* | Wire the adapter for local `serve`. |
| `lambda/main.go` *(modify)* | Wire the adapter for the deployed API. |
| `API.md` *(modify)* | Document the four routes. |

---

### Task 1: Membership type and the Fantrax projection

**Files:**
- Create: `internal/lineupapi/membership.go`
- Create: `internal/lineupapi/membership_test.go`
- Modify: `internal/lineupapi/user.go` (add one field to `User`)

**Interfaces:**
- Consumes: `User` (`internal/lineupapi/user.go:94`), its existing `TeamID` and `CreatedAt` fields.
- Produces: `Platform` (string type) with `PlatformFantrax`/`PlatformSleeper` constants; `Membership` struct with fields `Platform Platform`, `LeagueID string`, `TeamID string`, `DisplayName string`, `Writable bool`, `AddedAt time.Time`; `func (u *User) AllMemberships() []Membership`; `User.Memberships []Membership`.

- [ ] **Step 1: Write the failing test**

Create `internal/lineupapi/membership_test.go`:

```go
package lineupapi

import (
	"testing"
	"time"
)

func TestAllMembershipsProjectsFantraxTeam(t *testing.T) {
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	u := &User{TeamID: "7", CreatedAt: created}

	got := u.AllMemberships()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Platform != PlatformFantrax {
		t.Errorf("Platform = %q, want %q", got[0].Platform, PlatformFantrax)
	}
	if got[0].TeamID != "7" {
		t.Errorf("TeamID = %q, want 7", got[0].TeamID)
	}
	if !got[0].Writable {
		t.Error("Writable = false, want true — Fantrax is the platform we write to")
	}
	if !got[0].AddedAt.Equal(created) {
		t.Errorf("AddedAt = %v, want %v", got[0].AddedAt, created)
	}
}

// The every-existing-tenant case: no record in the store has a Memberships
// field, so it decodes to nil and must render as "Fantrax only".
func TestAllMembershipsWithNilSliceIsFantraxOnly(t *testing.T) {
	u := &User{TeamID: "7", Memberships: nil}
	if len(u.AllMemberships()) != 1 {
		t.Fatalf("len = %d, want 1", len(u.AllMemberships()))
	}
}

// A tenant invited but not yet connected has no proven team, so there is no
// Fantrax membership to project. Inventing one with an empty TeamID would
// claim a league binding that does not exist.
func TestAllMembershipsOmitsFantraxWhenNoTeamProven(t *testing.T) {
	u := &User{TeamID: ""}
	if len(u.AllMemberships()) != 0 {
		t.Fatalf("len = %d, want 0", len(u.AllMemberships()))
	}
}

func TestAllMembershipsAppendsSleeperAfterFantrax(t *testing.T) {
	u := &User{
		TeamID: "7",
		Memberships: []Membership{
			{Platform: PlatformSleeper, LeagueID: "123", DisplayName: "Dynasty"},
		},
	}
	got := u.AllMemberships()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Platform != PlatformFantrax {
		t.Errorf("first = %q, want fantrax first", got[0].Platform)
	}
	if got[1].LeagueID != "123" {
		t.Errorf("LeagueID = %q, want 123", got[1].LeagueID)
	}
	if got[1].Writable {
		t.Error("Writable = true for a Sleeper membership; Sleeper has no write API")
	}
}

// The projection must not alias the stored slice: a caller appending to the
// result would otherwise mutate the user record it came from.
func TestAllMembershipsDoesNotAliasStoredSlice(t *testing.T) {
	u := &User{Memberships: []Membership{{Platform: PlatformSleeper, LeagueID: "1"}}}
	got := u.AllMemberships()
	got[0].LeagueID = "mutated"
	if u.Memberships[0].LeagueID != "1" {
		t.Errorf("stored LeagueID = %q, want 1 — AllMemberships aliased the slice",
			u.Memberships[0].LeagueID)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/lineupapi/ -run TestAllMemberships -v`
Expected: FAIL — compile error, `undefined: PlatformFantrax`, `undefined: Membership`, `u.Memberships undefined`.

- [ ] **Step 3: Add the `Memberships` field to `User`**

In `internal/lineupapi/user.go`, inside `type User struct`, directly after the `TeamID` field and its comment:

```go
	// Memberships holds SLEEPER leagues only. The Fantrax membership is
	// projected from TeamID by AllMemberships rather than stored here, so the
	// proven-at-connect team and its ErrTeamTaken uniqueness claim keep exactly
	// one home. Two copies of the same fact is how they come to disagree.
	//
	// Absent on every record written before this field existed, which decodes
	// to nil and renders as "Fantrax only" — the correct answer for all of them.
	Memberships []Membership `json:"memberships,omitempty"`
```

- [ ] **Step 4: Write the implementation**

Create `internal/lineupapi/membership.go`:

```go
package lineupapi

import "time"

// Platform names a fantasy provider.
//
// A named string rather than a bool-per-platform because the set grows: the
// question a caller asks is "which provider", and every alternative encoding
// answers it by elimination.
type Platform string

const (
	PlatformFantrax Platform = "fantrax"
	PlatformSleeper Platform = "sleeper"
)

// Membership is one league a tenant belongs to, on any platform.
type Membership struct {
	Platform Platform `json:"platform"`

	// LeagueID is empty for Fantrax, and that is not an omission. The Fantrax
	// league is a property of the DEPLOYMENT (FANTRAX_LEAGUE_ID, one env var in
	// internal/config), not of the tenant, so there is no per-tenant value to
	// put here. Threading config into this package to fill it in would couple
	// the identity layer to the optimizer's configuration for a display string.
	LeagueID string `json:"league_id,omitempty"`

	// TeamID is the Fantrax team id, or the Sleeper user id that owns a roster
	// in this league. On both platforms it answers "which team here is yours".
	TeamID      string `json:"team_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`

	// Writable is whether RosterBot can act on this league. Permanently false
	// for Sleeper, whose public API has no write endpoints.
	//
	// Stored rather than derived from Platform at each call site: a capability
	// check that is a field read cannot be got wrong by a third platform the
	// way a repeated string comparison can.
	Writable bool `json:"writable"`

	AddedAt time.Time `json:"added_at"`
}

// AllMemberships returns the tenant's leagues across platforms, Fantrax first.
//
// It PROJECTS TeamID rather than reading a stored Fantrax membership. That is
// the whole trick: callers get one uniform list, while the field the connect
// flow proves against Fantrax's own MyTeamIDs — and that the store enforces
// uniqueness on — stays singular and untouched.
//
// A tenant with no proven team yields no Fantrax entry. An empty TeamID is
// "not connected yet", and a membership claiming a league with no team in it
// would be a fact nobody established.
func (u *User) AllMemberships() []Membership {
	out := make([]Membership, 0, len(u.Memberships)+1)
	if u.TeamID != "" {
		out = append(out, Membership{
			Platform: PlatformFantrax,
			TeamID:   u.TeamID,
			Writable: true,
			AddedAt:  u.CreatedAt,
		})
	}
	// append onto a fresh slice, never returning u.Memberships itself — a
	// caller appending to the result would otherwise write into the record.
	out = append(out, u.Memberships...)
	return out
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/lineupapi/ -run TestAllMemberships -v`
Expected: PASS, 5 tests.

- [ ] **Step 6: Run the full package suite and lint**

Run: `go test ./internal/lineupapi/ && make lint`
Expected: PASS, no lint findings. Nothing else in the package should change behaviour — this task adds a field and a method and alters no existing path.

- [ ] **Step 7: Commit**

```bash
git add internal/lineupapi/membership.go internal/lineupapi/membership_test.go internal/lineupapi/user.go
git commit -m "feat(identity): add Membership and project the Fantrax team into it

User.TeamID stays authoritative: it is proven against Fantrax's own MyTeamIDs
at connect time and carries the store's one-claimant-per-team constraint, so
AllMemberships projects it rather than migrating it. The stored Memberships
slice holds Sleeper entries only, which is why existing records need no
migration — an absent field decodes to nil and renders as Fantrax only.

No behaviour change: nothing reads AllMemberships yet."
```

---

### Task 2: Sleeper username and league-list lookups

**Files:**
- Modify: `internal/sleeper/client.go`
- Modify: `internal/sleeper/client_test.go` (append)

**Interfaces:**
- Consumes: the package's existing `cached[T]` helper, `(*Client).get`, `RosterTTL`, `cache.Key`, and the existing `User` and `League` types in `internal/sleeper/types.go`.
- Produces: `func (c *Client) UserByName(username string) (*User, error)`; `func (c *Client) LeaguesForUser(userID, sport, season string) ([]League, error)`; `var ErrUserNotFound error`.

- [ ] **Step 1: Write the failing test**

Append to `internal/sleeper/client_test.go`:

```go
func TestUserByNameParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/jnixon" {
			t.Errorf("path = %q, want /user/jnixon", r.URL.Path)
		}
		w.Write([]byte(`{"user_id":"456","username":"jnixon","display_name":"Jon"}`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	u, err := NewClient().UserByName("jnixon")
	if err != nil {
		t.Fatalf("UserByName: %v", err)
	}
	if u.UserID != "456" {
		t.Errorf("UserID = %q, want 456", u.UserID)
	}
	if u.DisplayName != "Jon" {
		t.Errorf("DisplayName = %q, want Jon", u.DisplayName)
	}
}

// Sleeper answers an unknown username with a 200 and a literal `null` body
// rather than a 404, so the status check in get() lets it through and the
// decode succeeds into a zero User. An empty UserID is therefore the only
// reliable signal, and it must not be reported as success — a caller would
// go on to request leagues for the empty string.
func TestUserByNameUnknownUsernameIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`null`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	if _, err := NewClient().UserByName("nobody"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

// The 404 spelling of the same outcome, in case Sleeper changes its mind.
func TestUserByNameNotFoundStatusIsAlsoNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	if _, err := NewClient().UserByName("nobody"); err == nil {
		t.Error("err = nil, want an error for a 404")
	}
}

func TestLeaguesForUserParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/456/leagues/nfl/2026" {
			t.Errorf("path = %q, want /user/456/leagues/nfl/2026", r.URL.Path)
		}
		w.Write([]byte(`[
			{"league_id":"1","name":"Dynasty","season":"2026","total_rosters":12},
			{"league_id":"2","name":"Redraft","season":"2026","total_rosters":10}
		]`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	ls, err := NewClient().LeaguesForUser("456", "nfl", "2026")
	if err != nil {
		t.Fatalf("LeaguesForUser: %v", err)
	}
	if len(ls) != 2 {
		t.Fatalf("len = %d, want 2", len(ls))
	}
	if ls[0].Name != "Dynasty" {
		t.Errorf("Name = %q, want Dynasty", ls[0].Name)
	}
	if ls[1].TotalRosters != 10 {
		t.Errorf("TotalRosters = %d, want 10", ls[1].TotalRosters)
	}
}

// A username with a slash or space must not escape the path and address a
// different endpoint.
func TestUserByNameEscapesUsername(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`{"user_id":"1"}`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	if _, err := NewClient().UserByName("a/b"); err != nil {
		t.Fatalf("UserByName: %v", err)
	}
	if gotPath != "/user/a%2Fb" {
		t.Errorf("path = %q, want /user/a%%2Fb", gotPath)
	}
}
```

Add `"errors"` to that file's import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sleeper/ -run 'TestUserByName|TestLeaguesForUser' -v`
Expected: FAIL — compile error, `c.UserByName undefined`, `undefined: ErrUserNotFound`.

- [ ] **Step 3: Write the implementation**

Add `"errors"` and `"net/url"` to the import block in `internal/sleeper/client.go`, then append:

```go
// ErrUserNotFound is returned when a username resolves to no Sleeper account.
//
// It exists because Sleeper spells that outcome as a 200 with a `null` body
// rather than a 404, which get() cannot distinguish from success. Returning a
// zero User instead would send the caller on to request leagues for the empty
// string and get an empty list — "this username has no leagues" rather than
// "there is no such username", which are different answers to show a user.
var ErrUserNotFound = errors.New("sleeper: no such user")

// UserByName resolves a Sleeper username to its account.
func (c *Client) UserByName(username string) (*User, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-user", username), func() (*User, error) {
		return c.fetchUserByName(username)
	})
}

func (c *Client) fetchUserByName(username string) (*User, error) {
	var u User
	if err := c.get("/user/"+url.PathEscape(username), &u); err != nil {
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
func (c *Client) LeaguesForUser(userID, sport, season string) ([]League, error) {
	return cached(c, RosterTTL, cache.Key("sleeper-user-leagues", userID, sport, season),
		func() ([]League, error) {
			return c.fetchLeaguesForUser(userID, sport, season)
		})
}

func (c *Client) fetchLeaguesForUser(userID, sport, season string) ([]League, error) {
	path := "/user/" + url.PathEscape(userID) +
		"/leagues/" + url.PathEscape(sport) +
		"/" + url.PathEscape(season)
	var ls []League
	if err := c.get(path, &ls); err != nil {
		return nil, err
	}
	return ls, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/sleeper/ -v`
Expected: PASS, including the pre-existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/sleeper/client.go internal/sleeper/client_test.go
git commit -m "feat(sleeper): add UserByName and LeaguesForUser

The two lookups a username-driven league picker needs, in the package's
existing cached[T] shape. Both path segments are url.PathEscape'd.

ErrUserNotFound exists because Sleeper answers an unknown username with a 200
and a null body, not a 404 — an empty UserID is the only reliable signal, and
letting it through as success turns 'no such username' into 'this user has no
leagues', which is a different thing to show someone."
```

---

### Task 3: Discovery route — `GET /v1/sleeper/leagues`

**Files:**
- Create: `internal/lineupapi/sleeper.go`
- Create: `internal/lineupapi/sleeper_test.go`
- Modify: `internal/lineupapi/handler.go` (Config field + one route + doc comment)
- Modify: `API.md`

**Interfaces:**
- Consumes: `CallerFrom(ctx) Caller` (`authz.go:159`), `writeJSON(w, code, v)` and `writeErr(w, code, msg)` (`handler.go:405`, `:418`).
- Produces: `SleeperLeague` struct with fields `LeagueID string`, `Name string`, `Season string`, `TotalRosters int`, `TeamID string`; `SleeperDirectory` interface with method `LeaguesForUsername(ctx context.Context, username, sport, season string) ([]SleeperLeague, error)`; `var ErrSleeperUserUnknown error`; `func defaultSeason(now time.Time) string`; `Config.SleeperDir SleeperDirectory`.

- [ ] **Step 1: Write the failing test**

Create `internal/lineupapi/sleeper_test.go`:

```go
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
```

**Note:** `withCaller(ctx, Caller)` already exists at `internal/lineupapi/authz.go:148` and is the counterpart to `CallerFrom`. Nothing needs adding — just use it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/lineupapi/ -run 'TestSleeperLeagues|TestDefaultSeason' -v`
Expected: FAIL — compile error, `undefined: SleeperLeague`, `cfg.SleeperDir undefined`, `undefined: defaultSeason`.

- [ ] **Step 3: Write the implementation**

Create `internal/lineupapi/sleeper.go`:

```go
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
```

- [ ] **Step 4: Add the Config field and register the route**

In `internal/lineupapi/handler.go`, add to `type Config struct`, after the `PushDevices` block:

```go
	// SleeperDir backs GET /v1/sleeper/leagues. Nil answers 501, matching the
	// other optional dependencies; production and `serve` both wire the
	// internal/sleeperdir adapter.
	SleeperDir SleeperDirectory
```

Register the route beside the other member routes (near `mux.HandleFunc("GET /v1/me", ...)` at `handler.go:128`):

```go
	mux.HandleFunc("GET /v1/sleeper/leagues", cfg.handleSleeperLeagues)
```

And add to the route doc-comment block that starts around `handler.go:101`:

```go
//	GET  /v1/sleeper/leagues -> discover a Sleeper account's leagues by username
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/lineupapi/ -run 'TestSleeperLeagues|TestDefaultSeason' -v`
Expected: PASS, 8 tests.

- [ ] **Step 6: Document the route in `API.md`**

Add a section matching the file's existing per-route format:

````markdown
### GET /v1/sleeper/leagues

Discover which Sleeper leagues a username belongs to. Requires a passkey
session. Read-only — this endpoint stores nothing; use `POST /v1/memberships`
to keep one of the results.

Query parameters:

| Name | Required | Default | Notes |
|---|---|---|---|
| `username` | yes | — | Sleeper username, not a user id |
| `sport` | no | `nfl` | Sleeper scopes leagues by sport |
| `season` | no | current season | The year the season STARTS; Jan–Feb resolve to the previous year |

```json
{
  "leagues": [
    {"league_id": "1", "name": "Dynasty", "season": "2026", "total_rosters": 12, "team_id": "456"}
  ]
}
```

`404` if no Sleeper account has that username. `502` if Sleeper is unreachable —
distinct from a `200` with an empty list, which means the account exists and has
no leagues for that sport and season. `501` if the deployment has no Sleeper
directory configured.
````

- [ ] **Step 7: Run the full suite, lint, and commit**

Run: `make test && make lint`

```bash
git add internal/lineupapi/sleeper.go internal/lineupapi/sleeper_test.go internal/lineupapi/handler.go API.md
git commit -m "feat(api): add GET /v1/sleeper/leagues username discovery

Proxied server-side so no client learns Sleeper's API shape and the lookup
lands behind the existing read-through cache rather than being issued per
keystroke by a picker.

SleeperDirectory is an interface so lineupapi neither imports internal/sleeper
nor does I/O in its tests. Nil answers 501 like every other optional dependency.

defaultSeason rolls over in March: Sleeper labels a season by the year it
starts, so defaulting to the calendar year would return an empty list for every
user through January and February — indistinguishable from having no leagues.

Not added to adminOnlyRoutes; that list is an allowlist, so omission is what
makes this member-reachable."
```

---

### Task 4: Membership CRUD — `GET`/`POST`/`DELETE /v1/memberships`

**Files:**
- Create: `internal/lineupapi/memberships.go`
- Create: `internal/lineupapi/memberships_test.go`
- Modify: `internal/lineupapi/handler.go` (three routes + doc comment)
- Modify: `API.md`

**Interfaces:**
- Consumes: `Membership`, `Platform`, `PlatformSleeper`, `PlatformFantrax`, `(*User).AllMemberships()` from Task 1; `cfg.mutateUser(ctx, id, fn)` and `errNoSuchUser` (`tenants_admin.go:26`, `:21`); `UserStore.GetUser`.
- Produces: `handleListMemberships`, `handleAddMembership`, `handleDeleteMembership`.

- [ ] **Step 1: Write the failing test**

Create `internal/lineupapi/memberships_test.go`.

The package's convention for a real `UserStore` in tests is `NewFileUserStore(t.TempDir())` (see `me_test.go:14`) — a genuine store over a temp directory, not a hand-written fake. It gives real `Version` handling, which `mutateUser` needs since it retries on `ErrUserConflict`. Start the file with this fixture:

```go
// membershipFixture builds a real store over a temp dir, per me_test.go's
// convention. Emails are defaulted per user because CreateUser claims the
// address, so two users sharing an empty one collide with ErrEmailTaken.
func membershipFixture(t *testing.T, users ...*User) *FileUserStore {
	t.Helper()
	store := NewFileUserStore(t.TempDir())
	for _, u := range users {
		if u.Email == "" {
			u.Email = string(u.ID) + "@example.test"
		}
		if err := store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("CreateUser %s: %v", u.ID, err)
		}
	}
	return store
}
```

```go
package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListMembershipsIncludesProjectedFantrax(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", TeamID: "7"})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/memberships", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleListMemberships(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var body struct {
		Memberships []Membership `json:"memberships"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Memberships) != 1 || body.Memberships[0].Platform != PlatformFantrax {
		t.Errorf("memberships = %+v, want one fantrax entry", body.Memberships)
	}
}

func TestAddMembershipStoresSleeperLeague(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", TeamID: "7"})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships",
		strings.NewReader(`{"platform":"sleeper","league_id":"123","team_id":"456","display_name":"Dynasty"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	u, _, err := store.GetUser(req.Context(), "u1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if len(u.Memberships) != 1 {
		t.Fatalf("stored = %d memberships, want 1", len(u.Memberships))
	}
	if u.Memberships[0].LeagueID != "123" {
		t.Errorf("LeagueID = %q, want 123", u.Memberships[0].LeagueID)
	}
	if u.Memberships[0].Writable {
		t.Error("Writable = true; Sleeper has no write API and this must never be set")
	}
	if u.Memberships[0].AddedAt.IsZero() {
		t.Error("AddedAt is zero, want a stamp")
	}
}

// The whole reason this route refuses fantrax: TeamID is proven against
// Fantrax's own MyTeamIDs by the connect task, and a caller naming their own
// team would be asserting exactly the thing that proof exists to establish.
func TestAddMembershipRefusesFantrax(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1"})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships",
		strings.NewReader(`{"platform":"fantrax","league_id":"L","team_id":"9"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	u, _, _ := store.GetUser(req.Context(), "u1")
	if len(u.Memberships) != 0 {
		t.Error("a fantrax membership was stored")
	}
}

func TestAddMembershipRequiresLeagueID(t *testing.T) {
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1"})}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships", strings.NewReader(`{"platform":"sleeper"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAddMembershipDuplicateIsConflict(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", Memberships: []Membership{
		{Platform: PlatformSleeper, LeagueID: "123"},
	}})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships",
		strings.NewReader(`{"platform":"sleeper","league_id":"123"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

// Two tenants in the same Sleeper league must both be able to add it. The
// league data is world-readable, so exclusivity would protect nothing and
// break the common case.
func TestAddMembershipAllowsTheSameLeagueForTwoUsers(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1"}, &User{ID: "u2"})
	cfg := Config{Users: store}

	for _, uid := range []UserID{"u1", "u2"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/memberships",
			strings.NewReader(`{"platform":"sleeper","league_id":"123"}`))
		req = req.WithContext(withCaller(req.Context(), Caller{UserID: uid}))
		cfg.handleAddMembership(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", uid, rec.Code)
		}
	}
}

func TestDeleteMembershipRemovesIt(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", Memberships: []Membership{
		{Platform: PlatformSleeper, LeagueID: "123"},
		{Platform: PlatformSleeper, LeagueID: "999"},
	}})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/v1/memberships/sleeper/123", nil)
	req.SetPathValue("platform", "sleeper")
	req.SetPathValue("leagueID", "123")
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleDeleteMembership(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	u, _, _ := store.GetUser(req.Context(), "u1")
	if len(u.Memberships) != 1 || u.Memberships[0].LeagueID != "999" {
		t.Errorf("remaining = %+v, want only 999", u.Memberships)
	}
}

// Same rule as UserStore.DeleteCredential: the caller's intent is satisfied
// either way, and erroring invites a check-then-act race.
func TestDeleteMembershipAbsentIsNotAnError(t *testing.T) {
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1"})}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/v1/memberships/sleeper/nope", nil)
	req.SetPathValue("platform", "sleeper")
	req.SetPathValue("leagueID", "nope")
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleDeleteMembership(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestDeleteMembershipRefusesFantrax(t *testing.T) {
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1", TeamID: "7"})}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/v1/memberships/fantrax/L", nil)
	req.SetPathValue("platform", "fantrax")
	req.SetPathValue("leagueID", "L")
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleDeleteMembership(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// adminOnlyRoutes is an allowlist, so a route's absence is what makes it
// tenant-reachable. That is the quiet direction, so pin it.
func TestMembershipRoutesAreNotAdminOnly(t *testing.T) {
	for _, p := range []string{
		"/v1/memberships",
		"/v1/memberships/sleeper/123",
		"/v1/sleeper/leagues",
	} {
		if isAdminOnlyPath(p) {
			t.Errorf("isAdminOnlyPath(%q) = true; members must reach their own leagues", p)
		}
	}
}
```


- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/lineupapi/ -run 'TestListMemberships|TestAddMembership|TestDeleteMembership|TestMembershipRoutes' -v`
Expected: FAIL — compile error, `cfg.handleListMemberships undefined`.

- [ ] **Step 3: Write the implementation**

Create `internal/lineupapi/memberships.go`:

```go
package lineupapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// handleListMemberships returns the caller's own leagues across platforms.
func (cfg Config) handleListMemberships(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	u, ok, err := cfg.Users.GetUser(r.Context(), caller.UserID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": u.AllMemberships()})
}

// handleAddMembership records one Sleeper league on the caller's own record.
//
// Sleeper ONLY, and the refusal below is the substance of this handler. A
// Fantrax membership is established by the invite and proven against Fantrax's
// own MyTeamIDs by the connect task; accepting one here would let a caller
// assert the exact fact that proof exists to establish.
//
// There is deliberately no uniqueness check ACROSS users. Sleeper league data
// is world-readable, so claiming one grants access to nothing private, and two
// tenants in the same league must both be able to add it — the opposite of the
// ErrTeamTaken rule that governs Fantrax.
func (cfg Config) handleAddMembership(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}

	var body struct {
		Platform    Platform `json:"platform"`
		LeagueID    string   `json:"league_id"`
		TeamID      string   `json:"team_id"`
		DisplayName string   `json:"display_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if body.Platform != PlatformSleeper {
		writeErr(w, http.StatusBadRequest, "only sleeper memberships can be added here")
		return
	}
	if body.LeagueID == "" {
		writeErr(w, http.StatusBadRequest, "league_id is required")
		return
	}

	errDuplicate := errors.New("duplicate membership")
	u, err := cfg.mutateUser(r.Context(), caller.UserID, func(u *User) error {
		for _, m := range u.Memberships {
			if m.Platform == body.Platform && m.LeagueID == body.LeagueID {
				return errDuplicate
			}
		}
		u.Memberships = append(u.Memberships, Membership{
			Platform:    PlatformSleeper,
			LeagueID:    body.LeagueID,
			TeamID:      body.TeamID,
			DisplayName: body.DisplayName,
			// Never true. Sleeper's public API has no write endpoints, and this
			// field is what the clients trust when deciding to show an action.
			Writable: false,
			AddedAt:  time.Now().UTC(),
		})
		return nil
	})
	switch {
	case errors.Is(err, errDuplicate):
		writeErr(w, http.StatusConflict, "that league is already on your account")
		return
	case errors.Is(err, errNoSuchUser):
		writeErr(w, http.StatusNotFound, "no such user")
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "could not update memberships")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": u.AllMemberships()})
}

// handleDeleteMembership removes one Sleeper league from the caller's record.
//
// Removing an absent membership is not an error: the caller's intent is
// satisfied either way, and the alternative invites a check-then-act race —
// the same rule UserStore.DeleteCredential documents.
//
// Fantrax is refused. Removing that membership means deleting the tenant's
// proven team binding, which is an admin operation (DELETE /v1/tenants/{id}),
// not a self-service one.
func (cfg Config) handleDeleteMembership(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}

	platform := Platform(r.PathValue("platform"))
	leagueID := r.PathValue("leagueID")
	if platform != PlatformSleeper {
		writeErr(w, http.StatusBadRequest, "only sleeper memberships can be removed here")
		return
	}

	u, err := cfg.mutateUser(r.Context(), caller.UserID, func(u *User) error {
		kept := u.Memberships[:0]
		for _, m := range u.Memberships {
			if m.Platform == platform && m.LeagueID == leagueID {
				continue
			}
			kept = append(kept, m)
		}
		u.Memberships = kept
		return nil
	})
	switch {
	case errors.Is(err, errNoSuchUser):
		writeErr(w, http.StatusNotFound, "no such user")
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "could not update memberships")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": u.AllMemberships()})
}
```

- [ ] **Step 4: Register the three routes**

In `internal/lineupapi/handler.go`, beside the route added in Task 3:

```go
	mux.HandleFunc("GET /v1/memberships", cfg.handleListMemberships)
	mux.HandleFunc("POST /v1/memberships", cfg.handleAddMembership)
	mux.HandleFunc("DELETE /v1/memberships/{platform}/{leagueID}", cfg.handleDeleteMembership)
```

And in the doc-comment block:

```go
//	GET/POST /v1/memberships          -> the caller's own leagues; add a Sleeper one
//	DELETE   /v1/memberships/{p}/{id} -> remove a Sleeper league
```

**Do NOT add any of these to `adminOnlyRoutes`.**

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/lineupapi/ -run 'TestListMemberships|TestAddMembership|TestDeleteMembership|TestMembershipRoutes' -v`
Expected: PASS, 10 tests.

- [ ] **Step 6: Document the routes in `API.md`**

Add sections matching the file's existing format: `GET /v1/memberships` returning `{"memberships": [...]}`; `POST /v1/memberships` taking `{"platform":"sleeper","league_id":"…","team_id":"…","display_name":"…"}` and returning the updated list, `400` for a non-sleeper platform or a missing `league_id`, `409` for a duplicate; `DELETE /v1/memberships/{platform}/{leagueID}` returning the updated list, `400` for a non-sleeper platform, `200` for an absent membership. State explicitly that `writable` is always `false` for Sleeper and that the Fantrax entry is projected from the tenant's proven team rather than stored.

- [ ] **Step 7: Run the full suite, lint, and commit**

Run: `make test && make lint`

```bash
git add internal/lineupapi/memberships.go internal/lineupapi/memberships_test.go internal/lineupapi/handler.go API.md
git commit -m "feat(api): add membership CRUD for Sleeper leagues

Sleeper only, both ways. A Fantrax membership is established by invite and
proven against Fantrax's own MyTeamIDs by the connect task, so accepting one
over HTTP would let a caller assert the thing that proof exists to establish;
removing one means deleting a proven team binding, which is an admin operation.

No cross-user uniqueness, deliberately, and the inverse of the ErrTeamTaken
rule: Sleeper league data is world-readable, so exclusivity would protect
nothing while breaking two tenants in the same league.

Writable is false at the single construction site and asserted by test.
Deleting an absent membership succeeds, per the DeleteCredential rule.

TestMembershipRoutesAreNotAdminOnly pins the allowlist behaviour, which is the
quiet failure direction."
```

---

### Task 5: Wire the adapter into `serve` and the Lambda

**Files:**
- Create: `internal/sleeperdir/sleeperdir.go`
- Create: `internal/sleeperdir/sleeperdir_test.go`
- Modify: `cmd/serve.go`
- Modify: `lambda/main.go`

**Interfaces:**
- Consumes: `lineupapi.SleeperDirectory`, `lineupapi.SleeperLeague`, `lineupapi.ErrSleeperUserUnknown` (Task 3); `sleeper.NewClient`, `(*sleeper.Client).UserByName`, `(*sleeper.Client).LeaguesForUser`, `sleeper.ErrUserNotFound` (Task 2).
- Produces: `func New(cacheDir string) *Directory`; `func (d *Directory) LeaguesForUsername(ctx, username, sport, season string) ([]lineupapi.SleeperLeague, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/sleeperdir/sleeperdir_test.go`:

```go
package sleeperdir

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

type stubClient struct {
	user    *sleeper.User
	userErr error
	leagues []sleeper.League
}

func (s stubClient) UserByName(string) (*sleeper.User, error) { return s.user, s.userErr }
func (s stubClient) LeaguesForUser(_, _, _ string) ([]sleeper.League, error) {
	return s.leagues, nil
}

func TestLeaguesForUsernameStampsResolvedUserIDAsTeamID(t *testing.T) {
	d := &Directory{client: stubClient{
		user:    &sleeper.User{UserID: "456"},
		leagues: []sleeper.League{{LeagueID: "1", Name: "Dynasty", Season: "2026", TotalRosters: 12}},
	}}

	got, err := d.LeaguesForUsername(context.Background(), "jnixon", "nfl", "2026")
	if err != nil {
		t.Fatalf("LeaguesForUsername: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].TeamID != "456" {
		t.Errorf("TeamID = %q, want the resolved user id 456", got[0].TeamID)
	}
	if got[0].Name != "Dynasty" {
		t.Errorf("Name = %q, want Dynasty", got[0].Name)
	}
}

// The client's not-found must arrive as the API layer's not-found, or the
// handler answers 502 for a typo'd username.
func TestLeaguesForUsernameTranslatesNotFound(t *testing.T) {
	d := &Directory{client: stubClient{userErr: sleeper.ErrUserNotFound}}
	_, err := d.LeaguesForUsername(context.Background(), "nobody", "nfl", "2026")
	if !errors.Is(err, lineupapi.ErrSleeperUserUnknown) {
		t.Errorf("err = %v, want lineupapi.ErrSleeperUserUnknown", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sleeperdir/ -v`
Expected: FAIL — no such package / `undefined: Directory`.

- [ ] **Step 3: Write the implementation**

Create `internal/sleeperdir/sleeperdir.go`:

```go
// Package sleeperdir adapts internal/sleeper to lineupapi.SleeperDirectory.
//
// It exists so lineupapi never imports the HTTP client: the API package stays
// hermetic and its tests network-free, while this package — which nothing but
// wiring imports — owns the type translation and the error mapping.
package sleeperdir

import (
	"context"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// client is the slice of *sleeper.Client this adapter uses, named as an
// interface so the test can stub it without an httptest server.
type client interface {
	UserByName(username string) (*sleeper.User, error)
	LeaguesForUser(userID, sport, season string) ([]sleeper.League, error)
}

// Directory resolves a Sleeper username to the leagues that account is in.
type Directory struct{ client client }

// New returns a Directory over a fresh Sleeper client. cacheDir may be empty
// to disable caching, matching sleeper.Client's own rule.
func New(cacheDir string) *Directory {
	c := sleeper.NewClient()
	c.CacheDir = cacheDir
	return &Directory{client: c}
}

// LeaguesForUsername resolves the username, then lists that account's leagues.
//
// Two calls rather than one because Sleeper's league listing is keyed by user
// id, not username. The resolved id is stamped onto every row as TeamID: on
// Sleeper the account id IS the roster owner id, so this is the value that
// answers "which team in this league is yours" and it saves the client a
// second round trip when it stores the membership.
func (d *Directory) LeaguesForUsername(_ context.Context, username, sport, season string) ([]lineupapi.SleeperLeague, error) {
	u, err := d.client.UserByName(username)
	if err != nil {
		if errors.Is(err, sleeper.ErrUserNotFound) {
			return nil, lineupapi.ErrSleeperUserUnknown
		}
		return nil, err
	}
	ls, err := d.client.LeaguesForUser(u.UserID, sport, season)
	if err != nil {
		return nil, err
	}
	out := make([]lineupapi.SleeperLeague, 0, len(ls))
	for _, l := range ls {
		out = append(out, lineupapi.SleeperLeague{
			LeagueID:     l.LeagueID,
			Name:         l.Name,
			Season:       l.Season,
			TotalRosters: l.TotalRosters,
			TeamID:       u.UserID,
		})
	}
	return out, nil
}
```

Add `"errors"` to the import block.

The `context.Context` parameter is accepted and ignored because `internal/sleeper` uses "plain (non-context) method signatures" by design, as its package doc states. Keeping it on the interface means the API layer's call site is context-aware for whenever that changes.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/sleeperdir/ -v`
Expected: PASS, 2 tests.

- [ ] **Step 5: Wire it into `serve` and the Lambda**

In `cmd/serve.go`, where the other `lineupapi.Config` fields are populated, add:

```go
		SleeperDir: sleeperdir.New(cacheDir),
```

using whatever local cache directory that file already uses for other clients — find it with `grep -n "CacheDir" cmd/serve.go`. If none exists, pass `""` to disable caching locally.

Do the same in `lambda/main.go`. The Lambda's writable filesystem is `/tmp`, so pass `"/tmp/sleeper-cache"` there, matching how other clients are given a cache path in that file — check with `grep -n "CacheDir\|/tmp" lambda/main.go` and follow the existing convention.

- [ ] **Step 6: Verify every module still builds**

Run: `make build-modules && make check-pins && make test && make lint`
Expected: all pass. `build-modules` is what catches a stale `lambda/go.mod` after touching that module — a failure there is fixed with `cd lambda && go mod tidy`.

- [ ] **Step 7: Commit**

```bash
git add internal/sleeperdir/ cmd/serve.go lambda/main.go
git commit -m "feat(api): wire the Sleeper directory into serve and the Lambda

sleeperdir owns the translation so lineupapi never imports the HTTP client and
keeps its network-free test story. It resolves username -> user id, then lists
that id's leagues, stamping the resolved id onto every row as TeamID — on
Sleeper the account id IS the roster owner id, so that one value answers 'which
team here is yours' and saves the client a second round trip.

sleeper.ErrUserNotFound is translated to lineupapi.ErrSleeperUserUnknown so a
typo'd username answers 404 rather than 502."
```

---

## Self-Review

**Spec coverage.** Walked each spec section: *Schema* → Task 1. *API* (both client methods) → Task 2; (four routes) → Tasks 3 and 4. *Trust* (no verification, no uniqueness) → Task 4, `TestAddMembershipAllowsTheSameLeagueForTwoUsers`. *Testing* (httptest seam, `Writable` assertion, `AllMemberships` nil case) → Tasks 1, 2, 4. *Out of scope* — no task touches `layout.go`, `FANTRAX_LEAGUE_ID`, or any scheduled job; confirmed by the file list.

Two spec items are **deliberately not in this plan**: the iOS surface (§Surfaces) and dashboard parity, which belong to the separate iOS plan, and the three-way `jobs.go` ↔ `API.md` ↔ `Models/*.swift` drift check, which cannot run until the Swift models exist. Both are recorded in *Follow-on work* below.

**Placeholder scan.** No "TBD", no "add error handling", no "similar to Task N". Three steps name a `grep` to confirm an existing helper's spelling before use (`withCaller`, the in-memory `UserStore` fake, the cache-dir convention) — these are verification steps with an exact command and a stated fallback, not deferred decisions.

**Type consistency.** Checked each name across tasks: `Platform`/`PlatformSleeper`/`PlatformFantrax`, `Membership` and its six fields, `AllMemberships`, `SleeperLeague` and its five fields, `SleeperDirectory.LeaguesForUsername`, `ErrSleeperUserUnknown` (lineupapi) vs `ErrUserNotFound` (sleeper) — two distinct errors across two packages, translated in Task 5 and asserted by `TestLeaguesForUsernameTranslatesNotFound`. `Config.SleeperDir` is spelled identically in Tasks 3 and 5. `leagueID` is the path-value key in both the route pattern and the handler.

## Follow-on work

- **iOS plan** — `Membership` model, `LeaguesView`, the username picker, and the read-only detail view. Blocked on this plan being deployed, since the app cannot be verified against routes that do not exist.
- **Contract drift check** — re-read `jobs.go`, `API.md` and `Models/*.swift` together once the Swift models land.
- **Dashboard parity** — the same list and picker in `web/dashboard/settings.js`. Optional; the spec marks it as not required for the first cut.
