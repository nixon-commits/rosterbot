# Roster Values Artifact and Route Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve `GET /v1/roster/values` — the caller's own Fantrax roster joined onto HKB dynasty value and 30-day momentum — from a per-team artifact the daily `team-values` job writes.

**Architecture:** A new leaf package `internal/rostervalues` builds one `Table` per Fantrax team from the pool the job already holds, reusing `availablepool`'s join helpers. `cmd/team-values.go` publishes each table under its team id to a new `roster/` artifact. `internal/lineupapi` gains a handler that resolves the caller's team (the `handleMe` chain plus two bearer-token fallbacks) and passes the stored bytes through `serveBlob`, exactly like `/v1/pool/available`.

**Tech Stack:** Go 1.2x, standard `testing`, the existing `layout` / `statestore` / `lineupapi` seams. No new dependencies.

**Spec:** `/Users/jnixon/RosterbotApp/docs/superpowers/specs/2026-09-02-team-values-design.md` (iOS repo). Bead: `rosterbot-ljse`.

## Global Constraints

- `internal/rostervalues` must not import `internal/fantrax` or `go-fantrax` (chromedp rides behind them); it declares its own input structs like `availablepool` does.
- `generated_at` is RFC3339 UTC with no fractional seconds, formatted to a `string` at construction (never a raw `time.Time`).
- Every place `AvailablePool` is wired gets a `RosterValues` twin: `TestTenantStores_WiresEveryTenantViewField` and `TestHandlerDocListsEveryRoute` fail if one is missed.
- Positive means better on both 30-day deltas; the roster reuses `availablepool`'s normalisation rather than re-deriving it.
- Unranked players are kept, never dropped: `counts.rostered == counts.mlb + counts.prospects`.

---

### Task 1: Export the three join helpers from `availablepool`

**Files:**
- Modify: `internal/availablepool/availablepool.go` (`normaliseChanges`, `firstRanked`, `parsePositions`)
- Modify: `internal/availablepool/availablepool_test.go` (call sites)

**Interfaces:**
- Produces: `availablepool.NormaliseChanges(hp hkb.Player) (rank, value int)`, `availablepool.FirstRanked(hist []int) int`, `availablepool.ParsePositions(raw string) []string`. Signatures unchanged apart from the initial capital.

- [ ] **Step 1: Rename the three functions and their call sites**

```bash
cd internal/availablepool
sed -i '' 's/\bnormaliseChanges\b/NormaliseChanges/g; s/\bfirstRanked\b/FirstRanked/g; s/\bparsePositions\b/ParsePositions/g' availablepool.go availablepool_test.go
```

Add one sentence to each doc comment: "Exported for `internal/rostervalues`, which joins the same two feeds for rostered players and must not drift from this definition."

- [ ] **Step 2: Run the package tests**

Run: `go test ./internal/availablepool/`
Expected: `ok` — pure rename, `TestSignConvention` still pins the sign.

- [ ] **Step 3: Commit**

```bash
git add internal/availablepool
git commit -m "refactor(availablepool): export the join helpers for the roster builder"
```

---

### Task 2: `internal/rostervalues` — types and builder

**Files:**
- Create: `internal/rostervalues/rostervalues.go`
- Test: `internal/rostervalues/rostervalues_test.go`

**Interfaces:**
- Consumes: `hkb.BuildLookup`, `hkb.Lookup.FindFor(name, hkb.Hint{MLBTeam, MinorsEligible})`, `playername.Normalize`, the three exported `availablepool` helpers.
- Produces:

```go
package rostervalues

// PoolPlayer is the subset of a Fantrax pool row this package needs.
type PoolPlayer struct {
	ID, Name, MLBTeam string
	Positions         string // raw PosShortNames, HTML and all — ParsePositions strips it
	FantasyTeamID     string // empty for an unowned player, who is skipped
	MinorsEligible    bool
}

// Team names a Fantrax team. Only the name is consumed: the header total and
// the league rank are computed from the rows, so they cannot disagree.
type Team struct {
	ID, Name string
}

type Player struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	MLBTeam string `json:"mlb_team"`
	Pos        []string `json:"pos,omitempty"`
	FantraxPos []string `json:"fantrax_pos,omitempty"`
	Level        string `json:"level,omitempty"`
	ActiveLevels string `json:"active_levels,omitempty"`
	Prospect bool     `json:"prospect"`
	FYPD     bool     `json:"fypd"`
	Age      *float64 `json:"age,omitempty"`
	HKBValue            *int  `json:"hkb_value,omitempty"`
	HKBRank             *int  `json:"hkb_rank,omitempty"`
	RankChange30D       *int  `json:"rank_change_30d,omitempty"`
	ValueChange30D      *int  `json:"value_change_30d,omitempty"`
	RankHistory30D      []int `json:"rank_history_30d,omitempty"`
	RankHistoryStartsAt *int  `json:"rank_history_starts_at,omitempty"`
	// UnrankedReason is set, and every HKB field above absent, when HKB did
	// not value this player: ReasonNoMatch or ReasonNamesake.
	UnrankedReason string `json:"unranked_reason,omitempty"`
}

const (
	ReasonNoMatch  = "no HKB match"
	ReasonNamesake = "name shared by another Fantrax player"
)

type Counts struct {
	Rostered         int `json:"rostered"`
	Matched          int `json:"matched"`
	Unmatched        int `json:"unmatched"`
	NamesakeDeclined int `json:"namesake_declined"`
	MLB              int `json:"mlb"`
	Prospects        int `json:"prospects"`
}

type Table struct {
	GeneratedAt string   `json:"generated_at"`
	HKBAsOf     string   `json:"hkb_as_of"`
	TeamID      string   `json:"team_id"`
	TeamName    string   `json:"team_name,omitempty"`
	TeamValue   int      `json:"team_value"`
	LeagueRank  int      `json:"league_rank"`
	TeamCount   int      `json:"team_count"`
	MLB         []Player `json:"mlb"`
	Prospects   []Player `json:"prospects"`
	Counts      Counts   `json:"counts"`
}

// BuildAll returns one Table per team that has at least one rostered player,
// keyed by Fantrax team id. teams supplies names, totals and the league rank.
func BuildAll(now time.Time, hkbAsOf string, pool []PoolPlayer, hkbPlayers []hkb.Player, teams []Team) map[string]Table
```

- [ ] **Step 1: Write the failing tests**

```go
package rostervalues

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

func now() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }

func fixture() ([]PoolPlayer, []hkb.Player, []Team) {
	pool := []PoolPlayer{
		{ID: "1", Name: "Mason Miller", MLBTeam: "SD", Positions: "RP,P", FantasyTeamID: "mine"},
		{ID: "2", Name: "Aiva Arquette", MLBTeam: "MIA", Positions: "SS", FantasyTeamID: "mine", MinorsEligible: true},
		{ID: "3", Name: "Some Journeyman", MLBTeam: "MIA", Positions: "1B,UT", FantasyTeamID: "mine"},
		{ID: "4", Name: "Deep Prospect", MLBTeam: "TB", Positions: "OF", FantasyTeamID: "mine", MinorsEligible: true},
		{ID: "5", Name: "Luis Garcia", MLBTeam: "HOU", Positions: "SP", FantasyTeamID: "mine"},
		{ID: "6", Name: "Luis Garcia", MLBTeam: "WSH", Positions: "2B", FantasyTeamID: "theirs"},
		{ID: "7", Name: "Free Agent", MLBTeam: "NYY", Positions: "OF", FantasyStatus: ""},
		{ID: "8", Name: "Their Star", MLBTeam: "LAD", Positions: "OF", FantasyTeamID: "theirs"},
	}
	hkbPlayers := []hkb.Player{
		{Name: "Mason Miller", Team: "SD", Value: 3177, Rank: 22, Level: "MLB", Age: 26.8,
			Positions: []string{"RP"}, RankChange30Days: 4, ValueChange30Days: 120,
			RankHistory30Days: []int{26, 25, 22}},
		{Name: "Aiva Arquette", Team: "MIA", Value: 524, Rank: 323, Level: "AA", Prospect: true,
			RankHistory30Days: []int{0, 0, 340, 323}},
		{Name: "Luis Garcia", Team: "HOU", Value: 900, Rank: 150},
		{Name: "Their Star", Team: "LAD", Value: 5000, Rank: 3},
	}
	teams := []Team{
		{ID: "mine", Name: "Nixon Dynasty", TotalValue: 4601},
		{ID: "theirs", Name: "Rivals", TotalValue: 5000},
	}
	return pool, hkbPlayers, teams
}

func TestBuildAll_KeepsEveryRosteredPlayerAndSegmentsExhaustively(t *testing.T) {
	pool, hkbPlayers, teams := fixture()
	mine := BuildAll(now(), "2026-09-02", pool, hkbPlayers, teams)["mine"]

	if got := mine.Counts; got.Rostered != 5 || got.MLB+got.Prospects != got.Rostered {
		t.Fatalf("counts %+v: rostered must equal mlb + prospects", got)
	}
	if mine.Counts.Matched != 2 || mine.Counts.Unmatched != 2 || mine.Counts.NamesakeDeclined != 1 {
		t.Errorf("join counts %+v", mine.Counts)
	}
	// Ranked first, value descending; unranked last.
	if mine.MLB[0].Name != "Mason Miller" || mine.MLB[0].UnrankedReason != "" {
		t.Errorf("mlb[0] = %+v", mine.MLB[0])
	}
	last := mine.MLB[len(mine.MLB)-1]
	if last.HKBValue != nil {
		t.Errorf("unranked rows must sort last, got %+v", last)
	}
}

func TestBuildAll_UnrankedRowsCarryAReasonAndNoHKBFields(t *testing.T) {
	pool, hkbPlayers, teams := fixture()
	mine := BuildAll(now(), "2026-09-02", pool, hkbPlayers, teams)["mine"]

	byName := map[string]Player{}
	for _, p := range append(mine.MLB, mine.Prospects...) {
		byName[p.Name] = p
	}
	j := byName["Some Journeyman"]
	if j.UnrankedReason != ReasonNoMatch {
		t.Errorf("reason = %q, want %q", j.UnrankedReason, ReasonNoMatch)
	}
	g := byName["Luis Garcia"]
	if g.UnrankedReason != ReasonNamesake {
		t.Errorf("namesake reason = %q", g.UnrankedReason)
	}
	for _, p := range []Player{j, g} {
		if p.HKBValue != nil || p.HKBRank != nil || p.RankChange30D != nil || p.Age != nil || p.Pos != nil || p.Level != "" {
			t.Errorf("%s: HKB-derived fields must be absent on an unranked row: %+v", p.Name, p)
		}
		if len(p.FantraxPos) == 0 {
			t.Errorf("%s: Fantrax positions survive an unranked row", p.Name)
		}
	}
	// On the wire the absent fields are ABSENT, not zero.
	data, _ := json.Marshal(j)
	for _, key := range []string{"hkb_value", "hkb_rank", "rank_change_30d", "rank_history_30d", "age"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("unranked row leaked %q: %s", key, data)
		}
	}
}

func TestBuildAll_UnrankedSegmentFollowsMinorsEligibility(t *testing.T) {
	pool, hkbPlayers, teams := fixture()
	mine := BuildAll(now(), "2026-09-02", pool, hkbPlayers, teams)["mine"]
	for _, p := range mine.Prospects {
		if p.Name == "Deep Prospect" {
			return
		}
	}
	t.Errorf("an unranked minors-eligible player belongs in prospects: %+v", mine.Prospects)
}

func TestBuildAll_HeaderRanksTeamsByTotalValue(t *testing.T) {
	pool, hkbPlayers, teams := fixture()
	all := BuildAll(now(), "2026-09-02", pool, hkbPlayers, teams)
	mine, theirs := all["mine"], all["theirs"]
	if mine.TeamValue != 4601 || mine.LeagueRank != 2 || mine.TeamCount != 2 || mine.TeamName != "Nixon Dynasty" {
		t.Errorf("mine header = %+v", mine)
	}
	if theirs.LeagueRank != 1 {
		t.Errorf("theirs rank = %d", theirs.LeagueRank)
	}
	if mine.GeneratedAt != "2026-09-02T12:00:00Z" || mine.HKBAsOf != "2026-09-02" {
		t.Errorf("timestamps %q %q", mine.GeneratedAt, mine.HKBAsOf)
	}
}

func TestBuildAll_ZeroSentinelAndSignSurvive(t *testing.T) {
	pool, hkbPlayers, teams := fixture()
	mine := BuildAll(now(), "2026-09-02", pool, hkbPlayers, teams)["mine"]
	for _, p := range mine.Prospects {
		if p.Name == "Aiva Arquette" {
			if p.RankHistoryStartsAt == nil || *p.RankHistoryStartsAt != 2 {
				t.Errorf("starts_at = %v, want 2 (leading zeros are 'unranked', not rank 0)", p.RankHistoryStartsAt)
			}
		}
	}
	if m := mine.MLB[0]; *m.RankChange30D != 4 || *m.ValueChange30D != 120 {
		t.Errorf("deltas pass through NormaliseChanges unchanged: %+v", m)
	}
}

func TestBuildAll_SkipsUnownedAndNeverInventsATeam(t *testing.T) {
	pool, hkbPlayers, teams := fixture()
	all := BuildAll(now(), "2026-09-02", pool, hkbPlayers, teams)
	if _, ok := all[""]; ok {
		t.Errorf("free agents must not produce a table")
	}
	if len(all) != 2 {
		t.Errorf("tables = %d, want 2", len(all))
	}
}
```

- [ ] **Step 2: Run to verify they fail to compile**

Run: `go test ./internal/rostervalues/`
Expected: build failure, package does not exist.

- [ ] **Step 3: Implement `rostervalues.go`**

Key points beyond the types above:

```go
func BuildAll(now time.Time, hkbAsOf string, pool []PoolPlayer, hkbPlayers []hkb.Player, teams []Team) map[string]Table {
	lookup := hkb.BuildLookup(hkbPlayers)
	namesakes := map[string]int{}
	for _, pp := range pool {
		namesakes[playername.Normalize(pp.Name)]++
	}
	generated := now.UTC().Format(time.RFC3339)

	// League table: total descending, id ascending on ties so a re-run never
	// reshuffles equal teams.
	ranked := append([]Team(nil), teams...)
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].TotalValue != ranked[j].TotalValue {
			return ranked[i].TotalValue > ranked[j].TotalValue
		}
		return ranked[i].ID < ranked[j].ID
	})
	rankOf := map[string]int{}
	nameOf := map[string]string{}
	for i, tm := range ranked {
		rankOf[tm.ID] = i + 1
		nameOf[tm.ID] = tm.Name
	}

	tables := map[string]Table{}
	for _, pp := range pool {
		if pp.FantasyTeamID == "" {
			continue
		}
		t, ok := tables[pp.FantasyTeamID]
		if !ok {
			t = Table{GeneratedAt: generated, HKBAsOf: hkbAsOf, TeamID: pp.FantasyTeamID,
				TeamName: nameOf[pp.FantasyTeamID], LeagueRank: rankOf[pp.FantasyTeamID], TeamCount: len(ranked)}
		}
		t.Counts.Rostered++
		p, prospect := row(pp, lookup, namesakes, &t.Counts)
		if prospect {
			t.Prospects = append(t.Prospects, p)
		} else {
			t.MLB = append(t.MLB, p)
		}
		tables[pp.FantasyTeamID] = t
	}
	for id, t := range tables {
		sortRows(t.MLB)
		sortRows(t.Prospects)
		t.Counts.MLB, t.Counts.Prospects = len(t.MLB), len(t.Prospects)
		for _, p := range append(t.MLB, t.Prospects...) {
			if p.HKBValue != nil {
				t.TeamValue += *p.HKBValue
			}
		}
		tables[id] = t
	}
	return tables
}
```

`row` returns the unranked shape (Fantrax fields only, `Prospect: pp.MinorsEligible`, reason set, `Counts.Unmatched++` or `Counts.NamesakeDeclined++`) when `FindFor` misses or `namesakes[...] > 1`, and otherwise the full shape with pointers taken from locals and `Counts.Matched++`. `sortRows` orders ranked-first, value descending, name ascending.

`TeamValue` is summed from the rows rather than copied from `teams` so the header and the list can never disagree; the test fixture's `TotalValue: 4601` equals `3177 + 524 + 900` minus the declined Garcia — write the fixture so both agree (`4601 = 3177 + 524 + 900`; the declined row contributes nothing, so set `teams[0].TotalValue` to `3701`). Fix the fixture and the assertion to `3701` before running.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/rostervalues/ -v`
Expected: all six PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/rostervalues
git commit -m "feat(rostervalues): build per-team roster tables with HKB value and 30d momentum (rosterbot-ljse)"
```

---

### Task 3: Layout artifact and statestore constructor

**Files:**
- Modify: `internal/statestore/layout/layout.go` (after `AvailablePool`, and `All()`)
- Modify: `internal/statestore/layout/layout_test.go:10-34` (`want` list)
- Modify: `internal/statestore/statestore.go` (`of(...)` table and a constructor after `AvailablePoolStore`)

- [ ] **Step 1: Add `"roster/"` to `TestAll_CoversEveryKnownPrefix`'s `want` list and run it**

Run: `go test ./internal/statestore/layout/ -run TestAll_CoversEveryKnownPrefix`
Expected: FAIL, `prefix "roster/" is not in All()`.

- [ ] **Step 2: Declare the artifact**

```go
	// RosterValues backs the app's My Team screen: every rostered player
	// joined onto HKB value and 30-day momentum, ONE OBJECT PER TEAM keyed by
	// Fantrax team id. Produced by TeamValues beside AvailablePool for the same
	// reason and with the same 26h tolerance. NOT PerTenant: a team's roster
	// is a property of the league; which team is yours is resolved at read
	// time by the route.
	RosterValues = Artifact{Name: "Roster Values", S3Prefix: "roster/", LocalDir: ".roster", Durable: true, MaxAge: 26 * time.Hour, Producer: "TeamValues"}
```

Add `RosterValues` to `All()` right after `AvailablePool`.

- [ ] **Step 3: Add the store constructor**

```go
	rosterValuesArtifact     = of(layout.RosterValues)
```

```go
// RosterValuesStore holds one object per Fantrax team (roster/<team_id>.json),
// rewritten daily by the TeamValues job.
func (s *Selector) RosterValuesStore() (lineupapi.BlobStore, error) {
	return blobStore(s, rosterValuesArtifact, "roster-")
}
```

- [ ] **Step 4: Run layout and statestore tests**

Run: `go test ./internal/statestore/...`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/statestore
git commit -m "feat(layout): declare the roster/ artifact and its store"
```

---

### Task 4: Route, resolver and handler tests

**Files:**
- Create: `internal/lineupapi/roster.go`
- Test: `internal/lineupapi/roster_test.go`
- Modify: `internal/lineupapi/handler.go` (`Config` fields, doc table line, `mux.HandleFunc`)
- Modify: `internal/lineupapi/tenantstores.go` (`TenantView.RosterValues`, flat fallback)
- Modify: `internal/lineupapi/wire_timestamps_test.go:79-82` (five → six passthrough routes, add `/v1/roster/values` and `rostervalues`)

**Interfaces:**
- Produces: `Config.RosterValues ObjectStore`, `Config.DefaultTenant UserID`, `Config.DefaultTeamID string`, `TenantView.RosterValues ObjectStore`, `func (cfg Config) handleRosterValues(w, r)`.

- [ ] **Step 1: Write the failing tests**

```go
package lineupapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type memBlobs map[string][]byte

func (m memBlobs) Get(_ context.Context, key string) ([]byte, bool, error) {
	b, ok := m[key]
	return b, ok, nil
}

func rosterReq(t *testing.T, cfg Config, caller Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/roster/values", nil)
	req = req.WithContext(withCaller(req.Context(), caller))
	rec := httptest.NewRecorder()
	cfg.handleRosterValues(rec, req)
	return rec
}

func rosterUsers(t *testing.T, users ...*User) *FileUserStore {
	t.Helper()
	store := NewFileUserStore(t.TempDir())
	for _, u := range users {
		if err := store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	return store
}

func TestRosterValues_ServesTheCallersOwnTeam(t *testing.T) {
	cfg := Config{
		RosterValues: memBlobs{"team-7": []byte(`{"team_id":"team-7"}`), "team-9": []byte(`{"team_id":"team-9"}`)},
		Users:        rosterUsers(t, &User{ID: "alice", TeamID: "team-7", Status: UserActive}),
	}
	rec := rosterReq(t, cfg, Caller{UserID: "alice", Role: RoleMember})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"team-7"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRosterValues_FallsBackToTheConnectionsTeam(t *testing.T) {
	cfg := Config{
		RosterValues: memBlobs{"team-7": []byte(`{}`)},
		Users:        rosterUsers(t, &User{ID: "alice", Status: UserActive}),
		Connections:  &memConnections{conn: &FantraxConnection{UserID: "alice", TeamID: "team-7"}},
	}
	if rec := rosterReq(t, cfg, Caller{UserID: "alice"}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRosterValues_BearerResolvesThroughTheDefaultTenant(t *testing.T) {
	cfg := Config{
		RosterValues:  memBlobs{"team-7": []byte(`{}`)},
		Users:         rosterUsers(t, &User{ID: "operator", TeamID: "team-7", Status: UserActive}),
		DefaultTenant: "operator",
	}
	if rec := rosterReq(t, cfg, Caller{Role: RoleAdmin, ViaToken: true}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRosterValues_BearerFallsBackToTheDeploymentTeam(t *testing.T) {
	cfg := Config{RosterValues: memBlobs{"team-7": []byte(`{}`)}, DefaultTeamID: "team-7"}
	if rec := rosterReq(t, cfg, Caller{Role: RoleAdmin, ViaToken: true}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRosterValues_NoTeamIsAnOrdinary404(t *testing.T) {
	cfg := Config{
		RosterValues: memBlobs{"team-7": []byte(`{}`)},
		Users:        rosterUsers(t, &User{ID: "newbie", Status: UserActive}),
	}
	rec := rosterReq(t, cfg, Caller{UserID: "newbie"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no Fantrax team") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRosterValues_StoreNilIs501AndMissingBlobIs404(t *testing.T) {
	if rec := rosterReq(t, Config{DefaultTeamID: "team-7"}, Caller{ViaToken: true}); rec.Code != http.StatusNotImplemented {
		t.Errorf("nil store: %d", rec.Code)
	}
	cfg := Config{RosterValues: memBlobs{}, DefaultTeamID: "team-7"}
	if rec := rosterReq(t, cfg, Caller{ViaToken: true}); rec.Code != http.StatusNotFound {
		t.Errorf("missing blob: %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/lineupapi/ -run TestRosterValues`
Expected: build failure, `handleRosterValues` undefined.

- [ ] **Step 3: Implement**

`handler.go`: add to `Config` after `AvailablePool`:

```go
	RosterValues  ObjectStore

	// DefaultTenant stands in for the bearer-token caller, which has no UserID
	// by construction, wherever a route needs a PERSON rather than a store:
	// the Lambda sets it from ROSTERBOT_USER_ID, matching tenantStores.For.
	// DefaultTeamID is the last resort behind it (FANTRAX_TEAM_ID, set by
	// `serve`), so a local run answers the bearer with the deployment's roster.
	DefaultTenant UserID
	DefaultTeamID string
```

Route table line (between `/v1/pool/available` and `/v1/reports/{name}`):

```
//	GET    /v1/roster/values                     -> the caller's own roster with HKB value + 30d momentum
```

and `mux.HandleFunc("GET /v1/roster/values", cfg.handleRosterValues)` beside the pool registration.

`tenantstores.go`: `RosterValues ObjectStore` on `TenantView` after `AvailablePool`; `RosterValues: cfg.RosterValues,` in the flat fallback literal.

`roster.go`:

```go
package lineupapi

import (
	"context"
	"net/http"
)

// handleRosterValues serves the caller's own roster: every rostered player
// joined onto HKB value and 30-day momentum, one artifact per Fantrax team.
//
// Passthrough via serveBlob like /v1/pool/available; the key is the team id,
// so "whose roster" is decided here and the bytes are never parsed.
func (cfg Config) handleRosterValues(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	if view.RosterValues == nil {
		writeErr(w, http.StatusNotImplemented, "roster values store not configured")
		return
	}
	teamID, err := cfg.teamIDFor(r.Context(), CallerFrom(r.Context()))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if teamID == "" {
		// Ordinary for a member who has not connected Fantrax yet: there is
		// no team to have a roster. 404 so the client shows its empty state.
		writeErr(w, http.StatusNotFound, "no Fantrax team bound to this account")
		return
	}
	serveBlob(w, r, view.RosterValues, teamID, "roster values")
}

// teamIDFor is handleMe's resolution — the user record, then the connection —
// with the bearer caller mapped onto DefaultTenant first and DefaultTeamID as
// the final fallback. An empty result means "no team", not an error.
func (cfg Config) teamIDFor(ctx context.Context, caller Caller) (string, error) {
	uid := caller.UserID
	if uid == "" {
		uid = cfg.DefaultTenant
	}
	if uid != "" {
		if cfg.Users != nil {
			u, ok, err := cfg.Users.GetUser(ctx, uid)
			if err != nil {
				return "", err
			}
			if ok && u.TeamID != "" {
				return u.TeamID, nil
			}
		}
		if cfg.Connections != nil {
			if conn, ok, err := cfg.Connections.GetConnection(ctx, uid); err == nil && ok && conn.TeamID != "" {
				return conn.TeamID, nil
			}
		}
	}
	return cfg.DefaultTeamID, nil
}
```

Note the nil-store check runs before resolution so the 501 test passes without a user directory.

- [ ] **Step 4: Update the passthrough comment in `wire_timestamps_test.go`** ("five" → "six", add `/v1/roster/values` and `rostervalues` to the lists) and run the whole package

Run: `go test ./internal/lineupapi/`
Expected: `ok`, including `TestHandlerDocListsEveryRoute`.

- [ ] **Step 5: Commit**

```bash
git add internal/lineupapi
git commit -m "feat(api): GET /v1/roster/values resolves the caller's team and serves its roster artifact (rosterbot-ljse)"
```

---

### Task 5: Wire the Lambda, `serve`, and infra

**Files:**
- Modify: `lambda/tenantstores.go:101-103` (add `RosterValues` after `AvailablePool`)
- Modify: `lambda/main.go:129-131` and `:242` (flat store; `DefaultTenant`)
- Modify: `cmd/serve.go:91` (file store, `DefaultTeamID`)
- Modify: `infra/infra.go:513` (`GrantRead(apiFn, "roster/*")`)

- [ ] **Step 1: Run the reflect guard to see it fail**

Run: `go test ./lambda/ -run TestTenantStores_WiresEveryTenantViewField`
Expected: FAIL, `lineupapi.TenantView.RosterValues is nil after tenantStores.build`.

- [ ] **Step 2: Wire all four sites**

`lambda/tenantstores.go`:
```go
	if v.RosterValues, err = store(layout.RosterValues.PrefixFor(tenant)); err != nil {
		return v, fmt.Errorf("tenant %s: roster values store: %w", uid, err)
	}
```

`lambda/main.go`, after the `AvailablePool` block:
```go
	if cfg.RosterValues, err = store(layout.RosterValues.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 roster values store: %w", err)
	}
```
and beside `apiCfg.Tenants = ...`:
```go
	apiCfg.DefaultTenant = lineupapi.UserID(os.Getenv("ROSTERBOT_USER_ID"))
```

`cmd/serve.go`, after the `AvailablePool` line:
```go
		// The My Team screen's payload, one file per Fantrax team, written by
		// the same job. FANTRAX_TEAM_ID lets the bearer caller — the local
		// curl and Simulator workflow — read the deployment's own roster.
		RosterValues:  lineupapi.NewFileBlobStore(layout.RosterValues.LocalDir, "roster-"),
		DefaultTeamID: os.Getenv("FANTRAX_TEAM_ID"),
```

`infra/infra.go`, after the `pool/*` grant:
```go
	stateBucket.GrantRead(apiFn, jsii.String("roster/*"))
```

- [ ] **Step 3: Run lambda, cmd and infra tests**

Run: `go test ./lambda/ ./cmd/ && (cd infra && go build ./... && go test ./...)`
Expected: `ok` everywhere (infra is its own module).

- [ ] **Step 4: Commit**

```bash
git add lambda cmd/serve.go infra/infra.go
git commit -m "feat(wiring): roster values store in the Lambda, serve, and the API read grant"
```

---

### Task 6: Produce the artifact in `team-values`

**Files:**
- Modify: `cmd/team-values.go` (build before the dry-run gate, publish after the pool, soft-fail)

- [ ] **Step 1: Add the builder and publisher**

```go
// buildRosterValues maps the pool and the league table across to the leaf
// package, which deliberately does not import internal/fantrax. The FULL pool
// is passed for the same reason buildAvailablePool passes it: the namesake
// guard can only see a contested name if both rows are present.
func buildRosterValues(date time.Time, pool []models.PoolPlayer, hkbPlayers []hkb.Player, rows []teamvalue.Row, teamNames map[string]string) map[string]rostervalues.Table {
	players := make([]rostervalues.PoolPlayer, 0, len(pool))
	for _, pp := range pool {
		players = append(players, rostervalues.PoolPlayer{
			ID: pp.PlayerID, Name: pp.Name, MLBTeam: pp.MLBTeamShortName,
			Positions: pp.PosShortNames, FantasyTeamID: pp.FantasyTeamID,
			MinorsEligible: pp.MinorsEligible,
		})
	}
	teams := make([]rostervalues.Team, 0, len(rows))
	for _, r := range rows {
		name := teamNames[r.TeamID]
		if name == "" {
			name = r.TeamName
		}
		teams = append(teams, rostervalues.Team{ID: r.TeamID, Name: name, TotalValue: r.TotalValue()})
	}
	return rostervalues.BuildAll(time.Now().UTC(), date.Format("2006-01-02"), players, hkbPlayers, teams)
}

// publishRosterValues stores one object per team under its Fantrax team id.
func publishRosterValues(tables map[string]rostervalues.Table) error {
	store, err := statestore.FromEnv().RosterValuesStore()
	if err != nil {
		return fmt.Errorf("open roster values store: %w", err)
	}
	for id, t := range tables {
		data, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("marshal roster %s: %w", id, err)
		}
		if err := store.Publish(id, data); err != nil {
			return fmt.Errorf("publish roster %s: %w", id, err)
		}
	}
	return nil
}
```

In `runTeamValues`, right after the `available pool:` summary line and before the dry-run gate:

```go
	rosters := buildRosterValues(date, pool, hkbPlayers, rows, teamNames)
	fmt.Printf("roster values: %d teams\n", len(rosters))
```

and after `publishAvailablePool`:

```go
	// The My Team screen's payload. Same producer, same soft-fail rationale.
	if err := publishRosterValues(rosters); err != nil {
		warn("team-values: roster values not written: %v", err)
	}
```

- [ ] **Step 2: Build and run the dry-run path**

Run: `go build ./... && go vet ./cmd/`
Expected: clean. (A live `team-values --dry-run` needs Fantrax credentials and is not run here.)

- [ ] **Step 3: Commit**

```bash
git add cmd/team-values.go
git commit -m "feat(team-values): publish per-team roster values beside the available pool (rosterbot-ljse)"
```

---

### Task 7: Document the route

**Files:**
- Modify: `docs/ios-api-contract.md` (route list at line ~39, "Five endpoints" → six at line ~38, new section after `GET /v1/pool/available`)

- [ ] **Step 1: Add the section**

Copy the payload example and the rules from the spec's "Backend contract" section verbatim: one object per team, team resolution chain, unranked rows and their two reasons, `rostered == mlb + prospects`, header fields, and the pool's freshness/error rules by reference.

- [ ] **Step 2: Commit**

```bash
git add docs/ios-api-contract.md
git commit -m "docs(api): document GET /v1/roster/values"
```

---

### Task 8: Full verification

- [ ] **Step 1:** `go build ./... && go vet ./... && go test ./...` — all `ok`.
- [ ] **Step 2:** `git log --oneline origin/main..HEAD` shows the seven commits above.
- [ ] **Step 3:** Push once and open the PR (the repo's CodeBuild runs `cdk deploy --all` on merge to main, so merging is the deploy).
