# Sleeper `football-pickups` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A daily job that tells the operator, per Sleeper league, which unrostered players just became worth picking up — from public data only — by diffing Sleeper's depth chart day over day and reading the league's completed drops and guillotine chops.

**Architecture:** Three pure detectors in `internal/dynasty` (role change over a day-over-day player snapshot; valued drops and chops over completed transactions) feed one ranked digest per league. The snapshot is archived daily as source `sleeper-players` (history; `NoBackfill`) and ALSO kept as a `latest.json` pointer in its own small artifact, because the archive store is write-only and the diff needs to read "the most recent prior capture" — whenever that was. Dedup is one marker per event; delivery is `notify.Send` kind `waivers`. This is Plan 3 of 3; Plans 1 and 2 landed `discoverLeagues`, `LeagueProfile`, `pollWeeks`, the marker-family pattern and the per-league isolation loop this plan reuses.

**Tech Stack:** Go 1.2x, cobra, `internal/sleeper` (public REST), `internal/statsguy`, `internal/dynasty`, `internal/archive`, `internal/alertmarker`, `internal/notify`, `internal/pushover`, AWS CDK in Go (`infra/`).

**Spec:** `docs/superpowers/specs/2026-09-22-sleeper-multi-league-pickups-offers-design.md`, Section 4.

## Global Constraints

- **Public data only.** No `sleeperauth` import anywhere in this plan (the depcheck test in `internal/sleeperauth` enforces it). The player dump (`sc.PlayersNFL`, 24 h cache) is fetched once per run.
- Snapshot fields: `id, name, team, position, depth_chart_position, depth_chart_order, injury_status, status`; players on an NFL club at a position some discovered league can roster.
- Detectors: **role change** = unrostered in the league, `depth_chart_order == 1` now, and not `1` on the same club in the prior snapshot (absent, unlisted, lower, or a different club); **valued drop** = a completed `waiver`/`free_agent` transaction created after the prior capture, priced in the league's `Format`, player still unrostered; **chop** = the same for `chopped`, grouped by transaction; `trade` excluded by type.
- Digest per league, only when non-empty; sections fit `pushover.MaxMessageLen` through `pushover.Builder` (whole blocks, never mid-line). An unvalued role change is listed with its depth-chart fact, never hidden. The ordering rule between a valued drop and an unvalued role change is **the operator's contribution** (`pickupRank`).
- Markers: role `(league, player, capture date)`; drop/chop `(league, transaction, player)`. Check → send → mark; `--dry-run` sends nothing, marks nothing, archives nothing, and does not move the `latest.json` pointer.
- Coverage line per league, unconditional, zero case included: prior capture date, players compared, unrostered, role/drop/chop counts, new (unmarked) count.
- First run (no prior capture): archive + save the pointer, print a baseline line, alert nothing.
- Per-league failure isolation (one league's error warned and skipped; non-zero exit at the end); the off-season stop from `discoverLeagues` handled exactly as `football-trades`/`football-offers` do.
- Schedule `FootballPickups`, `cron(15 15 * * ? *)`, `dailyGap`, on the SHARED task definition (no token); classified year-round; in `lineupapi.leagueWideJobs`; `make run-all` line; every new command has a README entry and `docs/dynasty-football.md` internals; `docs/aws-deployment.md`'s schedule count and `CLAUDE.md`'s `notify.Send` site count are updated.
- Repo lint rules: unchecked errors fail; `nilerr`; `nolintlint` require-explanation; `noctx`; gofmt/golangci-lint via hooks. Commit after every task; never push from a task.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/sleeper/types.go` (modify) | `Player.DepthChartPosition`, `Player.DepthChartOrder *int`. |
| `internal/sleeper/client_test.go` (modify) | Decode test incl. `null` order. |
| `internal/dynasty/pickups_snapshot.go` (create) | `PlayerSnapshot`, `SnapPlayer`, `RosterablePositions`, `BuildPlayerSnapshot`. Pure. |
| `internal/dynasty/pickups_snapshot_test.go` (create) | Filter/expansion tests. |
| `internal/dynasty/pickups.go` (create) | `RoleChange`, `DroppedPlayer`, `Chop`, `DetectRoleChanges`, `DetectDrops`. Pure. |
| `internal/dynasty/pickups_test.go` (create) | Detector tests. |
| `internal/dynasty/pickups_digest.go` (create) | `PickupItem`, `PickupItems`, `pickupRank` (operator), `OrderPickups`, `FormatPickupDigest`. Pure. |
| `internal/dynasty/pickups_digest_test.go` (create) | Ordering + rendering tests. |
| `cmd/season_gate.go`, `internal/lineupapi/authz.go`, `infra/infra.go`, `Makefile` (modify) | Classification, admin-only, schedule row, smoke line. |
| `internal/statestore/layout/layout.go` + tests, `internal/statestore/statestore.go`, `internal/statestore/tenant_test.go` (modify) | `FootballPickups` markers, `FootballPickupSnapshot` pointer, two constructors. |
| `cmd/football_pickups.go` (create) | Command: snapshot, pointer load/save, archive write, `pollPickups`, `alertPickups`. |
| `cmd/football_pickups_test.go` (create) | Seam tests. |
| `README.md`, `docs/dynasty-football.md`, `docs/aws-deployment.md`, `CLAUDE.md` (modify) | Docs. |

---

### Task 1: `Player` gains the depth-chart fields

**Files:**
- Modify: `internal/sleeper/types.go` (`Player` struct)
- Test: `internal/sleeper/client_test.go`

**Interfaces:**
- Produces: `Player.DepthChartPosition string` (json `depth_chart_position`), `Player.DepthChartOrder *int` (json `depth_chart_order`; nil = Sleeper sent `null`, which it does for most of the pool).

- [ ] **Step 1: Write the failing test**

Append to `internal/sleeper/client_test.go`:

```go
func TestPlayer_DecodesDepthChartAndNullOrder(t *testing.T) {
	var starter, bench Player
	if err := json.Unmarshal([]byte(`{"player_id":"1","position":"QB","team":"MIN","depth_chart_position":"QB","depth_chart_order":1}`), &starter); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"player_id":"2","position":"WR","team":"FA","depth_chart_position":null,"depth_chart_order":null}`), &bench); err != nil {
		t.Fatal(err)
	}
	if starter.DepthChartPosition != "QB" || starter.DepthChartOrder == nil || *starter.DepthChartOrder != 1 {
		t.Errorf("starter = %+v", starter)
	}
	if bench.DepthChartPosition != "" || bench.DepthChartOrder != nil {
		t.Errorf("null order must decode to nil, got %+v", bench)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sleeper/ -run TestPlayer_DecodesDepthChart -v`
Expected: FAIL — `starter.DepthChartPosition undefined`.

- [ ] **Step 3: Add the fields**

In `internal/sleeper/types.go`, add to `Player` after `InjuryStatus`:

```go
	// DepthChartPosition/DepthChartOrder are Sleeper's editorial depth chart:
	// the slot name ("QB", "RB", "LWR", "RWR", "SWR", "TE", ...) and 1 for the
	// starter. Order is a pointer because Sleeper sends null for most of the
	// pool; nil means "no recorded slot", which the pickups detector treats as
	// distinct from 2+. Measured 2026-09-21: Drew Lock and Carson Wentz both
	// read QB/1 the week they were picked up for real FAAB.
	DepthChartPosition string `json:"depth_chart_position"`
	DepthChartOrder    *int   `json:"depth_chart_order"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sleeper/ ./internal/dynasty/ ./cmd/ -run 'Player|Starter|Football'`
Expected: PASS (keyed `sleeper.Player{}` literals elsewhere are unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/sleeper/types.go internal/sleeper/client_test.go
git commit -m "feat(sleeper): Player carries depth_chart_position and a nullable depth_chart_order"
```

---

### Task 2: The player snapshot

**Files:**
- Create: `internal/dynasty/pickups_snapshot.go`
- Test: `internal/dynasty/pickups_snapshot_test.go`

**Interfaces:**
- Produces:
  - `type SnapPlayer struct { ID, Name, Team, Position, DepthChartPosition string; DepthChartOrder int; InjuryStatus, Status string }` (json snake_case; `DepthChartOrder` 0 = none).
  - `type PlayerSnapshot struct { CapturedAt time.Time; Players map[string]SnapPlayer }` (json `captured_at`, `players`).
  - `func RosterablePositions(leagues []sleeper.League) map[string]bool`.
  - `func BuildPlayerSnapshot(now time.Time, players map[string]sleeper.Player, positions map[string]bool) PlayerSnapshot`.
  - `func PlayerDisplayName(p sleeper.Player) string` — exported so the command and detectors share one spelling (check `internal/dynasty/trade_grade.go` for an existing unexported `playerDisplayName`; if present, export it by adding this exported wrapper rather than duplicating).

- [ ] **Step 1: Write the failing tests**

Create `internal/dynasty/pickups_snapshot_test.go`:

```go
package dynasty

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func intp(i int) *int { return &i }

func TestRosterablePositions_ExpandsFlexSlotsAndIgnoresBench(t *testing.T) {
	got := RosterablePositions([]sleeper.League{
		{RosterPositions: []string{"QB", "RB", "WR", "TE", "FLEX", "SUPER_FLEX", "BN", "IR", "TAXI"}},
		{RosterPositions: []string{"K", "DEF", "REC_FLEX", "IDP_FLEX"}},
	})
	for _, want := range []string{"QB", "RB", "WR", "TE", "K", "DEF", "DL", "LB", "DB"} {
		if !got[want] {
			t.Errorf("%s missing from %v", want, got)
		}
	}
	for _, no := range []string{"BN", "IR", "TAXI", "FLEX", "SUPER_FLEX", "REC_FLEX", "IDP_FLEX"} {
		if got[no] {
			t.Errorf("%s is a slot, not a position: %v", no, got)
		}
	}
}

func TestBuildPlayerSnapshot_FiltersToOnClubRosterablePlayers(t *testing.T) {
	now := time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC)
	players := map[string]sleeper.Player{
		"1": {PlayerID: "1", FirstName: "Drew", LastName: "Lock", Position: "QB", Team: "SEA", DepthChartPosition: "QB", DepthChartOrder: intp(1), Status: "Active"},
		"2": {PlayerID: "2", FirstName: "Free", LastName: "Agent", Position: "RB", Team: ""},                 // no club
		"3": {PlayerID: "3", FirstName: "Some", LastName: "Kicker", Position: "K", Team: "CHI", DepthChartOrder: intp(1)}, // not rosterable here
		"4": {PlayerID: "4", FirstName: "Deep", LastName: "Bench", Position: "WR", Team: "DET", DepthChartOrder: nil, InjuryStatus: "Out"},
	}
	snap := BuildPlayerSnapshot(now, players, map[string]bool{"QB": true, "RB": true, "WR": true, "TE": true})
	if !snap.CapturedAt.Equal(now) {
		t.Errorf("CapturedAt = %v", snap.CapturedAt)
	}
	if len(snap.Players) != 2 {
		t.Fatalf("players = %v", snap.Players)
	}
	if p := snap.Players["1"]; p.Name != "Drew Lock" || p.DepthChartOrder != 1 || p.DepthChartPosition != "QB" || p.Team != "SEA" {
		t.Errorf("lock = %+v", p)
	}
	if p := snap.Players["4"]; p.DepthChartOrder != 0 || p.InjuryStatus != "Out" {
		t.Errorf("nil order must become 0: %+v", p)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dynasty/ -run 'TestRosterablePositions|TestBuildPlayerSnapshot' -v`
Expected: FAIL — `undefined: RosterablePositions`.

- [ ] **Step 3: Write the snapshot**

Create `internal/dynasty/pickups_snapshot.go`:

```go
package dynasty

import (
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// SnapPlayer is one player as the daily pickups snapshot records them: the
// fields the role-change detector compares day over day, nothing else. Kept
// small on purpose — ~2,500 rows a day are archived forever (the archive is
// NoBackfill: Sleeper keeps no depth-chart history).
type SnapPlayer struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Team               string `json:"team"`
	Position           string `json:"position"`
	DepthChartPosition string `json:"depth_chart_position,omitempty"`
	// DepthChartOrder is 0 when Sleeper listed no slot (its JSON null); the
	// detector treats 0 as "unlisted", distinct from 2+.
	DepthChartOrder int    `json:"depth_chart_order,omitempty"`
	InjuryStatus    string `json:"injury_status,omitempty"`
	Status          string `json:"status,omitempty"`
}

// PlayerSnapshot is one capture of the filtered player dump.
type PlayerSnapshot struct {
	CapturedAt time.Time             `json:"captured_at"`
	Players    map[string]SnapPlayer `json:"players"`
}

// slotPositions expands a Sleeper roster slot into the positions it accepts.
// Plain position names map to themselves; bench, IR and taxi are not lineup
// slots and contribute nothing.
var slotPositions = map[string][]string{
	"FLEX":       {"RB", "WR", "TE"},
	"SUPER_FLEX": {"QB", "RB", "WR", "TE"},
	"REC_FLEX":   {"WR", "TE"},
	"WRRB_FLEX":  {"RB", "WR"},
	"IDP_FLEX":   {"DL", "LB", "DB"},
	"BN":         nil,
	"IR":         nil,
	"TAXI":       nil,
}

// RosterablePositions is the union of positions any of the leagues can start
// — the filter that keeps a kicker out of a league with no K slot (measured
// 2026-09-20: Sleeper's top-50 trending adds were 8 unrostered names, every
// one a K or DEF the dynasty league cannot roster).
func RosterablePositions(leagues []sleeper.League) map[string]bool {
	out := map[string]bool{}
	for _, lg := range leagues {
		for _, slot := range lg.RosterPositions {
			if exp, known := slotPositions[slot]; known {
				for _, p := range exp {
					out[p] = true
				}
				continue
			}
			out[slot] = true
		}
	}
	return out
}

// BuildPlayerSnapshot filters the dump to players on an NFL club at a
// rosterable position and flattens the nullable depth-chart order.
func BuildPlayerSnapshot(now time.Time, players map[string]sleeper.Player, positions map[string]bool) PlayerSnapshot {
	snap := PlayerSnapshot{CapturedAt: now.UTC(), Players: make(map[string]SnapPlayer, len(players)/4)}
	for id, p := range players {
		if p.Team == "" || !positions[p.Position] {
			continue
		}
		order := 0
		if p.DepthChartOrder != nil {
			order = *p.DepthChartOrder
		}
		snap.Players[id] = SnapPlayer{
			ID: id, Name: PlayerDisplayName(p), Team: p.Team, Position: p.Position,
			DepthChartPosition: p.DepthChartPosition, DepthChartOrder: order,
			InjuryStatus: p.InjuryStatus, Status: p.Status,
		}
	}
	return snap
}

// PlayerDisplayName is the one spelling of a Sleeper player's name this
// package uses (the trade grader's playerDisplayName renders the same way; if
// that helper exists, make it call this).
func PlayerDisplayName(p sleeper.Player) string {
	return strings.TrimSpace(p.FirstName + " " + p.LastName)
}
```

`internal/dynasty/aggregate.go:153` already defines `playerDisplayName(players map[string]sleeper.Player, id string) string`; leave its signature alone and make its body use `PlayerDisplayName` for the found case, so the two cannot spell a name differently.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dynasty/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dynasty/pickups_snapshot.go internal/dynasty/pickups_snapshot_test.go internal/dynasty/aggregate.go
git commit -m "feat(dynasty): daily player snapshot for pickups (rosterable positions, nullable depth order)"
```

---

### Task 3: The three detectors

**Files:**
- Create: `internal/dynasty/pickups.go`
- Test: `internal/dynasty/pickups_test.go`

**Interfaces:**
- Consumes: `PlayerSnapshot`, `SnapPlayer`, `sleeper.Transaction{Type, Status, Created, Drops map[string]int, RosterIDs}`, `statsguy.Bundle.Players[id].Value.Get(format)`.
- Produces:
  - `type RoleChange struct { PlayerID, Name, Team, Position, DepthChartPosition string; PrevOrder int; PrevTeam string; Value int; Priced bool }`
  - `type DroppedPlayer struct { TransactionID, PlayerID, Name, Position, Team, Kind string; DroppedBy int; DroppedByName string; Value int; Created int64 }`
  - `type Chop struct { TransactionID string; RosterID int; RosterName string; Players []DroppedPlayer }`
  - `func DetectRoleChanges(prev, cur PlayerSnapshot, rostered map[string]bool, bundle *statsguy.Bundle, format string) []RoleChange`
  - `func DetectDrops(txns []sleeper.Transaction, since time.Time, rostered map[string]bool, players map[string]sleeper.Player, bundle *statsguy.Bundle, format string, names map[int]string) (drops []DroppedPlayer, chops []Chop)`

- [ ] **Step 1: Write the failing tests**

Create `internal/dynasty/pickups_test.go`:

```go
package dynasty

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

func snapOf(at time.Time, ps ...SnapPlayer) PlayerSnapshot {
	s := PlayerSnapshot{CapturedAt: at, Players: map[string]SnapPlayer{}}
	for _, p := range ps {
		s.Players[p.ID] = p
	}
	return s
}

var (
	t0 = time.Date(2026, 9, 30, 15, 15, 0, 0, time.UTC)
	t1 = t0.Add(24 * time.Hour)
)

func pickupBundle() *statsguy.Bundle {
	return &statsguy.Bundle{Players: map[string]statsguy.Player{
		"lock":  {ID: "lock", Value: statsguy.FormatValues{SFDynasty: 169, NonSFRedraft: 90}},
		"maye":  {ID: "maye", Value: statsguy.FormatValues{SFDynasty: 6000, NonSFRedraft: 2496}},
		"brown": {ID: "brown", Value: statsguy.FormatValues{SFDynasty: 5000, NonSFRedraft: 3696}},
	}}
}

func TestDetectRoleChanges_ReachingOrderOneFromLowerUnlistedOrNoClub(t *testing.T) {
	prev := snapOf(t0,
		SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 2},
		SnapPlayer{ID: "rush", Name: "Cooper Rush", Team: "ATL", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1}, // already the starter
		SnapPlayer{ID: "bates", Name: "John Bates", Team: "WAS", Position: "TE", DepthChartOrder: 0},                          // unlisted
		SnapPlayer{ID: "star", Name: "Rostered Star", Team: "DET", Position: "RB", DepthChartOrder: 2},
	)
	cur := snapOf(t1,
		SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1},
		SnapPlayer{ID: "rush", Name: "Cooper Rush", Team: "ATL", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1},
		SnapPlayer{ID: "bates", Name: "John Bates", Team: "WAS", Position: "TE", DepthChartPosition: "TE", DepthChartOrder: 1},
		SnapPlayer{ID: "signed", Name: "Just Signed", Team: "CAR", Position: "RB", DepthChartPosition: "RB", DepthChartOrder: 1}, // absent from prev
		SnapPlayer{ID: "star", Name: "Rostered Star", Team: "DET", Position: "RB", DepthChartPosition: "RB", DepthChartOrder: 1},
	)
	got := DetectRoleChanges(prev, cur, map[string]bool{"star": true}, pickupBundle(), "sf_dynasty")
	ids := map[string]RoleChange{}
	for _, r := range got {
		ids[r.PlayerID] = r
	}
	if len(got) != 3 || ids["lock"].PlayerID == "" || ids["bates"].PlayerID == "" || ids["signed"].PlayerID == "" {
		t.Fatalf("got %+v", got)
	}
	if r := ids["lock"]; r.PrevOrder != 2 || r.Value != 169 || !r.Priced {
		t.Errorf("lock = %+v", r)
	}
	if r := ids["bates"]; r.PrevOrder != 0 || r.Priced {
		t.Errorf("bates should be unpriced with prev order 0: %+v", r)
	}
	if r := ids["signed"]; r.PrevTeam != "" {
		t.Errorf("signed should carry no prev team: %+v", r)
	}
	if _, bad := ids["rush"]; bad {
		t.Error("a player already at order 1 on the same club is not a change")
	}
	if _, bad := ids["star"]; bad {
		t.Error("a rostered player is never a pickup")
	}
}

func TestDetectRoleChanges_NewClubStartingJobCounts(t *testing.T) {
	prev := snapOf(t0, SnapPlayer{ID: "x", Name: "X", Team: "NYG", Position: "RB", DepthChartOrder: 1})
	cur := snapOf(t1, SnapPlayer{ID: "x", Name: "X", Team: "CLE", Position: "RB", DepthChartPosition: "RB", DepthChartOrder: 1})
	got := DetectRoleChanges(prev, cur, nil, pickupBundle(), "sf_dynasty")
	if len(got) != 1 || got[0].PrevTeam != "NYG" {
		t.Errorf("got %+v", got)
	}
}

func TestDetectRoleChanges_IsDeterministicallyOrdered(t *testing.T) {
	cur := snapOf(t1,
		SnapPlayer{ID: "b", Name: "Bob", Team: "A", Position: "WR", DepthChartOrder: 1},
		SnapPlayer{ID: "a", Name: "Al", Team: "A", Position: "WR", DepthChartOrder: 1},
		SnapPlayer{ID: "q", Name: "Quin", Team: "A", Position: "QB", DepthChartOrder: 1},
	)
	got := DetectRoleChanges(snapOf(t0), cur, nil, pickupBundle(), "sf_dynasty")
	if len(got) != 3 || got[0].Name != "Quin" || got[1].Name != "Al" || got[2].Name != "Bob" {
		t.Errorf("want position then name order, got %+v", got)
	}
}

func dropTxn(id, kind string, created time.Time, drops map[string]int) sleeper.Transaction {
	return sleeper.Transaction{TransactionID: id, Type: kind, Status: "complete", Created: created.UnixMilli(), Drops: drops, RosterIDs: []int{7}}
}

func TestDetectDrops_ValuedCompletedDropsSinceTheCapture(t *testing.T) {
	players := map[string]sleeper.Player{
		"maye":  {PlayerID: "maye", FirstName: "Drake", LastName: "Maye", Position: "QB", Team: "NE"},
		"brown": {PlayerID: "brown", FirstName: "A.J.", LastName: "Brown", Position: "WR", Team: "PHI"},
		"lock":  {PlayerID: "lock", FirstName: "Drew", LastName: "Lock", Position: "QB", Team: "SEA"},
		"nobody": {PlayerID: "nobody", FirstName: "Un", LastName: "Valued", Position: "WR", Team: "JAX"},
	}
	names := map[int]string{7: "Flint Tropics", 9: "Ghost Riders"}
	txns := []sleeper.Transaction{
		dropTxn("old", "free_agent", t0.Add(-time.Hour), map[string]int{"maye": 7}),       // before the capture
		dropTxn("fa", "free_agent", t0.Add(time.Hour), map[string]int{"lock": 7}),         // valued, kept
		dropTxn("wv", "waiver", t0.Add(2*time.Hour), map[string]int{"nobody": 7}),         // unvalued, dropped
		dropTxn("claimed", "waiver", t0.Add(3*time.Hour), map[string]int{"brown": 7}),     // rostered again since
		{TransactionID: "trade", Type: "trade", Status: "complete", Created: t0.Add(4 * time.Hour).UnixMilli(), Drops: map[string]int{"maye": 9}, RosterIDs: []int{7, 9}},
		{TransactionID: "pending", Type: "waiver", Status: "pending", Created: t0.Add(5 * time.Hour).UnixMilli(), Drops: map[string]int{"maye": 7}},
		dropTxn("chop", "chopped", t0.Add(6*time.Hour), map[string]int{"maye": 9, "brown": 9, "nobody": 9}),
	}
	drops, chops := DetectDrops(txns, t0, map[string]bool{"brown": true}, players, pickupBundle(), "non_sf_redraft", names)
	if len(drops) != 1 || drops[0].PlayerID != "lock" || drops[0].Value != 90 || drops[0].DroppedByName != "Flint Tropics" || drops[0].Kind != "free_agent" {
		t.Errorf("drops = %+v", drops)
	}
	if len(chops) != 1 || chops[0].RosterID != 9 || chops[0].RosterName != "Ghost Riders" {
		t.Fatalf("chops = %+v", chops)
	}
	// brown is rostered again (claimed) so he is not available; nobody is
	// unpriced; only maye remains, and he is priced in the redraft column.
	if len(chops[0].Players) != 1 || chops[0].Players[0].PlayerID != "maye" || chops[0].Players[0].Value != 2496 {
		t.Errorf("chop players = %+v", chops[0].Players)
	}
}

func TestDetectDrops_OrdersByValueThenName(t *testing.T) {
	players := map[string]sleeper.Player{
		"maye":  {PlayerID: "maye", FirstName: "Drake", LastName: "Maye", Position: "QB", Team: "NE"},
		"brown": {PlayerID: "brown", FirstName: "A.J.", LastName: "Brown", Position: "WR", Team: "PHI"},
	}
	txns := []sleeper.Transaction{dropTxn("x", "waiver", t0.Add(time.Hour), map[string]int{"maye": 7, "brown": 7})}
	drops, _ := DetectDrops(txns, t0, nil, players, pickupBundle(), "non_sf_redraft", nil)
	if len(drops) != 2 || drops[0].PlayerID != "brown" || drops[1].PlayerID != "maye" {
		t.Errorf("want brown (3696) before maye (2496): %+v", drops)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dynasty/ -run 'TestDetectRoleChanges|TestDetectDrops' -v`
Expected: FAIL — `undefined: DetectRoleChanges`.

- [ ] **Step 3: Write the detectors**

Create `internal/dynasty/pickups.go`:

```go
package dynasty

import (
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// RoleChange is an unrostered player who became a depth-chart starter since
// the prior capture. This is the signal that caught Lock and Wentz the week
// they drew real FAAB while StatsGuy's value sat still (measured 2026-09-21):
// the value feed lags the news; the depth chart carries it.
type RoleChange struct {
	PlayerID, Name, Team, Position, DepthChartPosition string
	// PrevOrder is the prior capture's order (0 = unlisted); PrevTeam is the
	// prior club when it differs (a new starting job on a new club counts).
	PrevOrder int
	PrevTeam  string
	Value     int
	Priced    bool
}

// DroppedPlayer is a priced player a league member released.
type DroppedPlayer struct {
	TransactionID, PlayerID, Name, Position, Team string
	// Kind is the transaction type: waiver, free_agent or chopped.
	Kind          string
	DroppedBy     int
	DroppedByName string
	Value         int
	Created       int64
}

// Chop is one guillotine elimination: the roster and its priced players,
// all of whom hit waivers at once. Only the guillotine league emits the type
// (measured 2026-09-21: 14 players in one chop, four of them worth 2,000+).
type Chop struct {
	TransactionID string
	RosterID      int
	RosterName    string
	Players       []DroppedPlayer
}

// DetectRoleChanges lists cur's unrostered players at depth-chart order 1
// who were not at order 1 on the same club in prev: lower, unlisted, absent,
// or on another club. Order is (position, name) so a digest is stable.
func DetectRoleChanges(prev, cur PlayerSnapshot, rostered map[string]bool, bundle *statsguy.Bundle, format string) []RoleChange {
	var out []RoleChange
	for id, p := range cur.Players {
		if rostered[id] || p.DepthChartOrder != 1 {
			continue
		}
		before, had := prev.Players[id]
		if had && before.DepthChartOrder == 1 && before.Team == p.Team {
			continue // already the starter there
		}
		rc := RoleChange{PlayerID: id, Name: p.Name, Team: p.Team, Position: p.Position, DepthChartPosition: p.DepthChartPosition}
		if had {
			rc.PrevOrder = before.DepthChartOrder
			if before.Team != p.Team {
				rc.PrevTeam = before.Team
			}
		}
		if sv, ok := bundle.Players[id]; ok {
			rc.Value, rc.Priced = sv.Value.Get(format), true
		}
		out = append(out, rc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// DetectDrops lists the priced players released by completed waiver or
// free-agent transactions created after since and still unrostered now, and
// groups guillotine chops by transaction. A trade lists its swapped players
// under drops too and is excluded by type: those are not releases (measured
// 2026-09-21, the dynasty league's top three "drops" were all trade halves).
// An unpriced player is not reported — the spec prices drops in the league's
// column, and a drop with no value is not the signal this job carries.
func DetectDrops(txns []sleeper.Transaction, since time.Time, rostered map[string]bool, players map[string]sleeper.Player, bundle *statsguy.Bundle, format string, names map[int]string) (drops []DroppedPlayer, chops []Chop) {
	sinceMillis := since.UnixMilli()
	chopByTxn := map[string]*Chop{}
	for _, t := range txns {
		if t.Status != "complete" || t.Created <= sinceMillis {
			continue
		}
		if t.Type != "waiver" && t.Type != "free_agent" && t.Type != "chopped" {
			continue
		}
		for pid, byRoster := range t.Drops {
			if rostered[pid] {
				continue // claimed since; not available
			}
			sv, priced := bundle.Players[pid]
			if !priced {
				continue
			}
			p := players[pid]
			d := DroppedPlayer{
				TransactionID: t.TransactionID, PlayerID: pid, Name: PlayerDisplayName(p),
				Position: p.Position, Team: p.Team, Kind: t.Type,
				DroppedBy: byRoster, DroppedByName: names[byRoster],
				Value: sv.Value.Get(format), Created: t.Created,
			}
			if d.Name == "" {
				d.Name = pid
			}
			if t.Type == "chopped" {
				c, ok := chopByTxn[t.TransactionID]
				if !ok {
					c = &Chop{TransactionID: t.TransactionID, RosterID: byRoster, RosterName: names[byRoster]}
					chopByTxn[t.TransactionID] = c
				}
				c.Players = append(c.Players, d)
				continue
			}
			drops = append(drops, d)
		}
	}
	byValue := func(ds []DroppedPlayer) {
		sort.Slice(ds, func(i, j int) bool {
			if ds[i].Value != ds[j].Value {
				return ds[i].Value > ds[j].Value
			}
			return ds[i].Name < ds[j].Name
		})
	}
	byValue(drops)
	for _, c := range chopByTxn {
		byValue(c.Players)
		chops = append(chops, *c)
	}
	sort.Slice(chops, func(i, j int) bool { return chops[i].TransactionID < chops[j].TransactionID })
	return drops, chops
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dynasty/ -run 'TestDetect' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dynasty/pickups.go internal/dynasty/pickups_test.go
git commit -m "feat(dynasty): pickup detectors — role change, valued drop, guillotine chop"
```

---

### Task 4: Ranking and the digest — `pickupRank` is the operator's

**Files:**
- Create: `internal/dynasty/pickups_digest.go`
- Test: `internal/dynasty/pickups_digest_test.go`

**Interfaces:**
- Produces:
  - `type PickupItem struct { Kind string; Key string; Line string; Position string; Value int; Priced bool; ChopSize int }` — `Kind` ∈ `chop|drop|role`; `Key` is the dedup marker key (`role-<player>-<YYYY-MM-DD>`, `drop-<txn>-<player>`, `chop-<txn>-<player>`; the command prefixes the league id).
  - `func PickupItems(captureDate string, chops []Chop, drops []DroppedPlayer, roles []RoleChange) []PickupItem` — renders one item per drop and role change, and ONE item per chop **player** (so each player is marked individually) with the chop's roster on the line.
  - `func pickupRank(it PickupItem, superflex bool) (tier int, value int)` — **operator-written**; lower tier first, then higher value first.
  - `func OrderPickups(items []PickupItem, superflex bool) []PickupItem` — stable sort by `pickupRank`.
  - `func FormatPickupDigest(profile LeagueProfile, items []PickupItem) (title, body string, shown int)` — `pushover.Builder`, whole lines, STOPS at the first line that does not fit (so rank order is preserved and "…and N more" means the N lowest-ranked) and returns how many items it showed; the caller marks only those.

**⚠ Operator contribution.** `pickupRank` decides how a valued drop ranks against an unvalued role change (the spec reserves it). The executor prepares the stub and STOPS before Step 3 for the operator's choice; a suggested default is included so the task can proceed if the operator approves it as-is.

- [ ] **Step 1: Write the failing tests**

Create `internal/dynasty/pickups_digest_test.go`:

```go
package dynasty

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/pushover"
)

func digestFixture() ([]Chop, []DroppedPlayer, []RoleChange) {
	chops := []Chop{{TransactionID: "c1", RosterID: 9, RosterName: "Ghost Riders", Players: []DroppedPlayer{
		{TransactionID: "c1", PlayerID: "maye", Name: "Drake Maye", Position: "QB", Team: "NE", Kind: "chopped", Value: 2496},
		{TransactionID: "c1", PlayerID: "lemon", Name: "Makai Lemon", Position: "WR", Team: "LAR", Kind: "chopped", Value: 1173},
	}}}
	drops := []DroppedPlayer{{TransactionID: "d1", PlayerID: "lock", Name: "Drew Lock", Position: "QB", Team: "SEA", Kind: "free_agent", DroppedByName: "Flint Tropics", Value: 90}}
	roles := []RoleChange{
		{PlayerID: "bates", Name: "John Bates", Team: "WAS", Position: "TE", DepthChartPosition: "TE", PrevOrder: 0},                      // unvalued
		{PlayerID: "rush", Name: "Cooper Rush", Team: "ATL", Position: "QB", DepthChartPosition: "QB", PrevOrder: 2, Value: 91, Priced: true},
	}
	return chops, drops, roles
}

func TestPickupItems_OneItemPerPlayerWithMarkerKeys(t *testing.T) {
	chops, drops, roles := digestFixture()
	items := PickupItems("2026-10-01", chops, drops, roles)
	keys := map[string]bool{}
	for _, it := range items {
		keys[it.Key] = true
	}
	for _, want := range []string{"chop-c1-maye", "chop-c1-lemon", "drop-d1-lock", "role-bates-2026-10-01", "role-rush-2026-10-01"} {
		if !keys[want] {
			t.Errorf("missing key %s in %v", want, keys)
		}
	}
	if len(items) != 5 {
		t.Errorf("items = %d", len(items))
	}
	for _, it := range items {
		switch {
		case it.Kind == "chop" && !strings.Contains(it.Line, "Ghost Riders"):
			t.Errorf("chop line must name the eliminated roster: %q", it.Line)
		case it.Kind == "role" && it.PlayerIDIs("bates") && !strings.Contains(it.Line, "unvalued"):
			t.Errorf("unvalued role change must say so, never be hidden: %q", it.Line)
		case it.Kind == "drop" && !strings.Contains(it.Line, "Flint Tropics"):
			t.Errorf("drop line must name who dropped him: %q", it.Line)
		}
	}
}

func TestOrderPickups_DefaultRanking(t *testing.T) {
	chops, drops, roles := digestFixture()
	got := OrderPickups(PickupItems("2026-10-01", chops, drops, roles), true)
	var order []string
	for _, it := range got {
		order = append(order, it.Key)
	}
	// Chops first (value order within), then priced items by value (Rush 91
	// before Lock 90), then the unvalued role change last. Update this
	// expectation if the operator chooses a different pickupRank.
	want := "chop-c1-maye,chop-c1-lemon,role-rush-2026-10-01,drop-d1-lock,role-bates-2026-10-01"
	if strings.Join(order, ",") != want {
		t.Errorf("order = %s\nwant  = %s", strings.Join(order, ","), want)
	}
}

func TestFormatPickupDigest_TitleCountsAndBodyFitsPushover(t *testing.T) {
	chops, drops, roles := digestFixture()
	profile := LeagueProfile{Name: "Mad Lux Chopped 6.9", Superflex: false, Format: "non_sf_redraft"}
	title, body := FormatPickupDigest(profile, OrderPickups(PickupItems("2026-10-01", chops, drops, roles), false))
	if !strings.HasPrefix(title, "[Mad Lux Chopped 6.9] Pickups: 1 chop, 1 drop, 2 role changes") {
		t.Errorf("title = %q", title)
	}
	if len(body) > pushover.MaxMessageLen {
		t.Errorf("body exceeds Pushover's limit: %d", len(body))
	}
	if !strings.Contains(body, "Drake Maye") || !strings.Contains(body, "unvalued") {
		t.Errorf("body = %q", body)
	}
}

func TestFormatPickupDigest_DropsWholeLinesNotMidLine(t *testing.T) {
	var many []PickupItem
	for i := 0; i < 60; i++ {
		many = append(many, PickupItem{Kind: "drop", Key: "k", Line: strings.Repeat("x", 40), Value: 60 - i, Priced: true})
	}
	_, body := FormatPickupDigest(LeagueProfile{Name: "L"}, many)
	if len(body) > pushover.MaxMessageLen {
		t.Fatalf("len %d", len(body))
	}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line != "" && !strings.HasSuffix(line, "x") && !strings.HasPrefix(line, "…") {
			t.Errorf("line was cut mid-way: %q", line)
		}
	}
}
```

Add to `pickups_digest.go` (implementation below) a tiny test-facing helper `func (it PickupItem) PlayerIDIs(id string) bool` — or drop that clause from the test and compare `it.Key` instead; the implementer chooses, and says which.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dynasty/ -run 'TestPickupItems|TestOrderPickups|TestFormatPickupDigest' -v`
Expected: FAIL — `undefined: PickupItems`.

- [ ] **Step 3: ⚠ STOP — operator writes `pickupRank`**

Create `internal/dynasty/pickups_digest.go` with everything **except** the body of `pickupRank`, then ask the operator. Suggested default in the comment; if the operator changes it, update `TestOrderPickups_DefaultRanking`'s expectation to match.

```go
package dynasty

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/pushover"
)

// PickupItem is one line of a league's pickup digest with the facts the
// ranking and the dedup marker need.
type PickupItem struct {
	Kind     string // chop | drop | role
	Key      string // marker key without the league prefix
	PlayerID string
	Line     string
	Position string
	Value    int
	Priced   bool
	ChopSize int
}

// PlayerIDIs is a small test convenience.
func (it PickupItem) PlayerIDIs(id string) bool { return it.PlayerID == id }

// PickupItems renders detector output into digest lines, one per player so
// each is marked individually: a chop with fourteen players is fourteen
// markers under one transaction, and a digest that already sent nine of them
// carries only the other five.
func PickupItems(captureDate string, chops []Chop, drops []DroppedPlayer, roles []RoleChange) []PickupItem {
	var out []PickupItem
	for _, c := range chops {
		for _, p := range c.Players {
			out = append(out, PickupItem{
				Kind: "chop", Key: "chop-" + c.TransactionID + "-" + p.PlayerID, PlayerID: p.PlayerID,
				Line:     fmt.Sprintf("CHOP %s %s %s (%d) — %s eliminated", p.Name, p.Position, p.Team, p.Value, c.RosterName),
				Position: p.Position, Value: p.Value, Priced: true, ChopSize: len(c.Players),
			})
		}
	}
	for _, d := range drops {
		out = append(out, PickupItem{
			Kind: "drop", Key: "drop-" + d.TransactionID + "-" + d.PlayerID, PlayerID: d.PlayerID,
			Line:     fmt.Sprintf("DROP %s %s %s (%d) by %s", d.Name, d.Position, d.Team, d.Value, d.DroppedByName),
			Position: d.Position, Value: d.Value, Priced: true,
		})
	}
	for _, r := range roles {
		was := "was unlisted"
		switch {
		case r.PrevTeam != "":
			was = "was " + r.PrevTeam
		case r.PrevOrder > 0:
			was = fmt.Sprintf("was #%d", r.PrevOrder)
		}
		val := "unvalued"
		if r.Priced {
			val = fmt.Sprintf("%d", r.Value)
		}
		out = append(out, PickupItem{
			Kind: "role", Key: "role-" + r.PlayerID + "-" + captureDate, PlayerID: r.PlayerID,
			Line:     fmt.Sprintf("ROLE %s %s %s now #1 %s (%s) (%s)", r.Name, r.Position, r.Team, r.DepthChartPosition, was, val),
			Position: r.Position, Value: r.Value, Priced: r.Priced,
		})
	}
	return out
}

// pickupRank orders a league's digest: lower tier first, then higher value.
//
// OPERATOR-WRITTEN (spec Section 4: how a valued drop ranks against an
// unvalued role change is the operator's call). Suggested default:
//   tier 0  chop (the whole roster hit waivers at once; value order within)
//   tier 1  anything priced — drop or role change — by value
//   tier 2  unvalued role changes, a superflex QB first (value 1 vs 0)
// Alternatives worth considering: an unvalued QB role change in a superflex
// league outranking every drop (tier 0 with value 0), or fixed sections
// (chops, drops, roles) regardless of value.
func pickupRank(it PickupItem, superflex bool) (tier int, value int) {
	// operator writes this body (5-10 lines)
}

// OrderPickups is a stable sort by pickupRank.
func OrderPickups(items []PickupItem, superflex bool) []PickupItem {
	out := append([]PickupItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		ti, vi := pickupRank(out[i], superflex)
		tj, vj := pickupRank(out[j], superflex)
		if ti != tj {
			return ti < tj
		}
		return vi > vj
	})
	return out
}

// FormatPickupDigest renders the title and a body that fits Pushover's
// limit on whole lines; when lines are dropped the body ends with a count.
func FormatPickupDigest(profile LeagueProfile, items []PickupItem) (title, body string) {
	var chops, drops, roles int
	chopTxns := map[string]bool{}
	for _, it := range items {
		switch it.Kind {
		case "chop":
			chopTxns[strings.Split(it.Key, "-")[1]] = true
		case "drop":
			drops++
		case "role":
			roles++
		}
	}
	chops = len(chopTxns)
	title = fmt.Sprintf("[%s] Pickups: %d chop%s, %d drop%s, %d role change%s", profile.Name,
		chops, plural(chops), drops, plural(drops), roles, plural(roles))

	var b pushover.Builder
	dropped := 0
	for _, it := range items {
		if !b.Add(it.Line + "\n") {
			dropped++
		}
	}
	body = b.String()
	if dropped > 0 {
		body += fmt.Sprintf("…and %d more", dropped)
	}
	return title, pushover.Truncate(body)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
```

Operator's reference implementation of the suggested default, for when they approve it as-is:

```go
func pickupRank(it PickupItem, superflex bool) (tier int, value int) {
	switch {
	case it.Kind == "chop":
		return 0, it.Value
	case it.Priced:
		return 1, it.Value
	case superflex && it.Position == "QB":
		return 2, 1
	default:
		return 2, 0
	}
}
```

Check `pushover.Builder.Add`'s exact semantics in `internal/pushover/builder.go` (it returns false when a block does not fit; confirm whether it keeps trying later, smaller blocks — if it does, the "…and N more" count is still correct because it counts refusals).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dynasty/ -v -run 'Pickup'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dynasty/pickups_digest.go internal/dynasty/pickups_digest_test.go
git commit -m "feat(dynasty): pickup digest — per-player items, operator-ranked, Pushover-fitted"
```

---

### Task 5: Layout and statestore — the marker family and the snapshot pointer

**Files:**
- Modify: `internal/statestore/layout/layout.go`, `internal/statestore/layout/layout_test.go`, `internal/statestore/tenant_test.go`, `internal/statestore/statestore.go`

The snapshot artifact is DECLARED here but joins `All()` only in Task 6, because four guard tests couple the wiring: `TestAll_ProducersAreRealSchedules` (an `All()` member's `Producer` must name a schedule in `infra/infra.go`), `TestSeasonGate_EveryScheduledCommandIsClassified` (every scheduled command must be classified AND registered with cobra), and `TestLeagueWideJobsCoversEveryInfraSingleton`. The command is what registers `football-pickups`, so the schedule row, the classification, the admin-only entry, the `All()` membership and the smoke line all land with the command in Task 6.

**Interfaces:**
- Produces: `layout.FootballPickups` (`football/pickups/` ↔ `.football/pickups`; durable; no `MaxAge`; absent from `All()`), `layout.FootballPickupSnapshot` (`football/pickups-snapshot/` ↔ `.football/pickups-snapshot`; durable; `MaxAge: 2 * Day`; `Producer: "FootballPickups"`; joins `All()` in Task 6); `(*Selector).FootballPickupMarkers()` and `(*Selector).FootballPickupSnapshot()`, both `(lineupapi.BlobStore, error)`.

- [ ] **Step 1: Write the failing layout test**

Append to `internal/statestore/layout/layout_test.go`:

```go
func TestFootballPickups_MarkerFamilyAndSnapshotPointer(t *testing.T) {
	m := FootballPickups
	if m.S3Prefix != "football/pickups/" || m.LocalDir != ".football/pickups" || !m.Durable || m.MaxAge != 0 || m.PerTenant {
		t.Errorf("markers: %+v", m)
	}
	s := FootballPickupSnapshot
	if s.S3Prefix != "football/pickups-snapshot/" || s.LocalDir != ".football/pickups-snapshot" || !s.Durable || s.MaxAge != 2*Day || s.PerTenant || s.Partitioned || s.Producer != "FootballPickups" {
		t.Errorf("snapshot: %+v", s)
	}
	inAll := map[string]bool{}
	for _, a := range All() {
		inAll[a.Name] = true
	}
	if inAll[m.Name] {
		t.Error("markers must be absent from All(): a quiet league writes none for weeks")
	}
	for _, other := range []Artifact{FootballTrades, FootballOffers, FootballTradeLog} {
		if strings.HasPrefix(m.S3Prefix, other.S3Prefix) || strings.HasPrefix(other.S3Prefix, m.S3Prefix) ||
			strings.HasPrefix(s.S3Prefix, other.S3Prefix) || strings.HasPrefix(other.S3Prefix, s.S3Prefix) {
			t.Errorf("prefix nests with %s", other.Name)
		}
	}
}
```

Also add `FootballPickups` and `FootballPickupSnapshot` to the ops-alert prefix-collision list in `layout_test.go` and BOTH to the `all := append(layout.All(), …)` list in `internal/statestore/tenant_test.go` (neither is PerTenant; the snapshot joins `All()` itself in Task 6, at which point it may be removed from that append).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/statestore/... -run 'FootballPickups|OpsAlert|PerTenant' -v`
Expected: FAIL — `undefined: FootballPickups`.

- [ ] **Step 3: Layout + statestore**

In `internal/statestore/layout/layout.go`, after `FootballOffers`:

```go
	// FootballPickups holds one dedup marker per pickup EVENT football-pickups
	// has alerted: a role change keyed (league, player, capture date), a drop
	// or chop keyed (league, transaction, player). Same shape and reasoning as
	// FootballTrades/FootballOffers: durable, no MaxAge, absent from All().
	FootballPickups = Artifact{Name: "Football Pickup Markers", S3Prefix: "football/pickups/", LocalDir: ".football/pickups", Durable: true, Producer: "FootballPickups"}

	// FootballPickupSnapshot is the ONE object football-pickups diffs against:
	// latest.json, the most recent filtered player capture, overwritten after
	// each run's alerts go out. It exists because the Daily Archive store is
	// write-only — the history goes there as source sleeper-players — and the
	// diff needs "the most recent prior capture, whenever that was", which a
	// pointer answers without listing. Rewritten daily, so it belongs in All()
	// with a MaxAge: its age is the job's health. A sibling of the marker
	// prefix, never nested, on the same reasoning as FootballOffers.
	FootballPickupSnapshot = Artifact{Name: "Football Pickup Snapshot", S3Prefix: "football/pickups-snapshot/", LocalDir: ".football/pickups-snapshot", Durable: true, MaxAge: 2 * Day, Producer: "FootballPickups"}
```

Do NOT add it to `All()` yet — Task 6 does, once the schedule its `Producer` names exists.

In `internal/statestore/statestore.go`: `footballPickupsArtifact = of(layout.FootballPickups)`, `footballPickupSnapArtifact = of(layout.FootballPickupSnapshot)`, and

```go
// FootballPickupMarkers is one dedup marker per alerted pickup event.
func (s *Selector) FootballPickupMarkers() (lineupapi.BlobStore, error) {
	return blobStore(s, footballPickupsArtifact, "")
}

// FootballPickupSnapshot holds latest.json, the capture football-pickups
// diffs against. A BlobStore because the diff wants exactly one known key,
// read then overwritten; the archive store keeps the history.
func (s *Selector) FootballPickupSnapshot() (lineupapi.BlobStore, error) {
	return blobStore(s, footballPickupSnapArtifact, "")
}
```

- [ ] **Step 4: Verify**

Run: `go test ./internal/statestore/...`
Expected: PASS (the producer guard is untouched because the snapshot is not yet in `All()`).

- [ ] **Step 5: Commit**

```bash
git add internal/statestore/layout/layout.go internal/statestore/layout/layout_test.go internal/statestore/tenant_test.go internal/statestore/statestore.go
git commit -m "feat(layout): FootballPickups marker family and the latest.json snapshot pointer"
```

---

### Task 6: The `football-pickups` command

**Files:**
- Create: `cmd/football_pickups.go`
- Test: `cmd/football_pickups_test.go`

**Interfaces:**
- Consumes: everything above; `discoverLeagues(ctx, sc, cfg, state, out)` + `errOffSeason` handling (copy from `football_offers.go`); `pollWeeks(state)`; `dynasty.TeamNames`; `statestore.FromEnv().ArchiveWriter()` (returns `archive.Writer`; `Write(date, source, arts)`); `archive.Artifact{Filename, Bytes}`; `alertmarker.New/Sent/Record`; `notify.Send`.
- Produces:
  - `const pickupSource = "sleeper-players"`, `const pickupSnapshotKey = "latest.json"`.
  - `func loadPickupSnapshot(ctx, store lineupapi.ObjectStore) (dynasty.PlayerSnapshot, bool, error)` and `func savePickupSnapshot(store lineupapi.BlobStore, snap dynasty.PlayerSnapshot) error`.
  - `type pickupLeagueInputs struct { rostered map[string]bool; txns []sleeper.Transaction; names map[int]string }`, `type pickupLoader func(ctx, lc leagueContext) (pickupLeagueInputs, error)`, `type pickupRunner func(lc leagueContext, in pickupLeagueInputs) pickupRunResult`.
  - `func pollPickups(ctx, leagues []leagueContext, load pickupLoader, run pickupRunner) (total pickupRunResult, failed []string)`.
  - `type pickupRunResult struct { Roles, Drops, Chops, New, Sent int }`.
  - `func alertPickups(ctx, in pickupAlertInputs) pickupRunResult` where `pickupAlertInputs{markers lineupapi.BlobStore; league dynasty.LeagueProfile; items []dynasty.PickupItem; dryRun bool; send func(title, body string) error; out io.Writer}`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/football_pickups_test.go`:

```go
package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func pickupItemsFixture() []dynasty.PickupItem {
	return []dynasty.PickupItem{
		{Kind: "drop", Key: "drop-d1-lock", PlayerID: "lock", Line: "DROP Drew Lock QB SEA (90) by Flint Tropics", Value: 90, Priced: true},
		{Kind: "role", Key: "role-bates-2026-10-01", PlayerID: "bates", Line: "ROLE John Bates TE WAS now #1 TE (was unlisted) (unvalued)"},
	}
}

func pickupAlertFixture(t *testing.T) (pickupAlertInputs, *[]string) {
	t.Helper()
	sent := &[]string{}
	return pickupAlertInputs{
		markers: lineupapi.NewFileBlobStore(t.TempDir(), ""),
		league:  dynasty.LeagueProfile{LeagueID: "L1", Name: "Palm Trees", Superflex: true, Format: "sf_dynasty"},
		items:   pickupItemsFixture(),
		send:    func(title, body string) error { *sent = append(*sent, title+"\n"+body); return nil },
		out:     io.Discard,
	}, sent
}

func TestAlertPickups_SendsOneDigestAndMarksEveryItem(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	res := alertPickups(context.Background(), in)
	if res.New != 2 || res.Sent != 1 || len(*sent) != 1 {
		t.Fatalf("res=%+v sent=%d", res, len(*sent))
	}
	if !strings.HasPrefix((*sent)[0], "[Palm Trees] Pickups:") || !strings.Contains((*sent)[0], "Drew Lock") || !strings.Contains((*sent)[0], "John Bates") {
		t.Errorf("digest = %q", (*sent)[0])
	}
	for _, key := range []string{"L1-drop-d1-lock", "L1-role-bates-2026-10-01"} {
		if _, found, err := in.markers.Get(context.Background(), key); err != nil || !found {
			t.Errorf("marker %s not written (found=%v err=%v)", key, found, err)
		}
	}
}

func TestAlertPickups_SecondRunSendsNothing(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	alertPickups(context.Background(), in)
	res := alertPickups(context.Background(), in)
	if res.New != 0 || res.Sent != 0 || len(*sent) != 1 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
}

func TestAlertPickups_PartiallyMarkedDigestCarriesOnlyNewItems(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	if err := in.markers.Publish("L1-drop-d1-lock", []byte("x")); err != nil {
		t.Fatal(err)
	}
	res := alertPickups(context.Background(), in)
	if res.New != 1 || len(*sent) != 1 || strings.Contains((*sent)[0], "Drew Lock") || !strings.Contains((*sent)[0], "John Bates") {
		t.Errorf("res=%+v sent=%v", res, *sent)
	}
}

func TestAlertPickups_DryRunSendsAndMarksNothing(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.dryRun = true
	res := alertPickups(context.Background(), in)
	if res.New != 2 || res.Sent != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-d1-lock"); found {
		t.Error("dry-run wrote a marker")
	}
}

func TestAlertPickups_FailedSendMarksNothing(t *testing.T) {
	in, _ := pickupAlertFixture(t)
	in.send = func(string, string) error { return errors.New("apns down") }
	res := alertPickups(context.Background(), in)
	if res.Sent != 0 {
		t.Errorf("res=%+v", res)
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-d1-lock"); found {
		t.Error("a failed send must not mark: the next run has to retry")
	}
}

func TestAlertPickups_NilMarkersStillSends(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.markers = nil
	alertPickups(context.Background(), in)
	if len(*sent) != 1 {
		t.Errorf("sent = %d", len(*sent))
	}
}

func TestAlertPickups_RefusedTailIsNotMarked(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	var many []dynasty.PickupItem
	for i := 0; i < 40; i++ {
		many = append(many, dynasty.PickupItem{Kind: "drop", Key: "drop-t-" + strings.Repeat("p", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26)), PlayerID: "p", Line: strings.Repeat("x", 60), Value: 100 - i, Priced: true})
	}
	in.items = many
	res := alertPickups(context.Background(), in)
	if res.Sent != 1 || len(*sent) != 1 {
		t.Fatalf("res=%+v", res)
	}
	marked := 0
	for _, it := range many {
		if _, found, _ := in.markers.Get(context.Background(), "L1-"+it.Key); found {
			marked++
		}
	}
	if marked == 0 || marked == len(many) {
		t.Errorf("marked %d of %d: an overflowing digest must mark only the items it carried, so the tail alerts next run", marked, len(many))
	}
	// The first (highest-ranked) items are the marked ones.
	if _, found, _ := in.markers.Get(context.Background(), "L1-"+many[0].Key); !found {
		t.Error("the top-ranked item was shown and must be marked")
	}
}

func TestAlertPickups_EmptyItemsSendsNothing(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.items = nil
	res := alertPickups(context.Background(), in)
	if res.Sent != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
}

func TestPollPickups_OneFailingLeagueDoesNotStopTheOthers(t *testing.T) {
	leagues := []leagueContext{
		{League: sleeper.League{LeagueID: "1", Name: "Alpha"}}, {League: sleeper.League{LeagueID: "2", Name: "Bravo"}}, {League: sleeper.League{LeagueID: "3", Name: "Charlie"}},
	}
	var ran []string
	total, failed := pollPickups(context.Background(), leagues,
		func(ctx context.Context, lc leagueContext) (pickupLeagueInputs, error) {
			if lc.League.Name == "Bravo" {
				return pickupLeagueInputs{}, errors.New("boom")
			}
			return pickupLeagueInputs{}, nil
		},
		func(lc leagueContext, in pickupLeagueInputs) pickupRunResult {
			ran = append(ran, lc.League.Name)
			return pickupRunResult{New: 1, Sent: 1}
		})
	if strings.Join(ran, ",") != "Alpha,Charlie" || len(failed) != 1 || failed[0] != "Bravo" || total.Sent != 2 {
		t.Errorf("ran=%v failed=%v total=%+v", ran, failed, total)
	}
}

func TestPickupSnapshot_RoundTripsThroughTheBlobStore(t *testing.T) {
	store := lineupapi.NewFileBlobStore(t.TempDir(), "")
	if _, found, err := loadPickupSnapshot(context.Background(), store); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}
	snap := dynasty.PlayerSnapshot{CapturedAt: time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC), Players: map[string]dynasty.SnapPlayer{"lock": {ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartOrder: 1}}}
	if err := savePickupSnapshot(store, snap); err != nil {
		t.Fatal(err)
	}
	got, found, err := loadPickupSnapshot(context.Background(), store)
	if err != nil || !found || !got.CapturedAt.Equal(snap.CapturedAt) || got.Players["lock"].DepthChartOrder != 1 {
		t.Errorf("got=%+v found=%v err=%v", got, found, err)
	}
}

func TestCoverageLine_PrintsTheZeroCase(t *testing.T) {
	var out bytes.Buffer
	printPickupCoverage(&out, "Alpha", time.Date(2026, 9, 30, 15, 15, 0, 0, time.UTC), 2500, 2100, pickupRunResult{})
	if !strings.Contains(out.String(), "football-pickups: Alpha: prev=2026-09-30 players=2500 unrostered=2100 role=0 drops=0 chops=0 new=0") {
		t.Errorf("line = %q", out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run 'AlertPickups|PollPickups|PickupSnapshot|CoverageLine' -v`
Expected: FAIL — `undefined: alertPickups`.

- [ ] **Step 3: Write the command**

Create `cmd/football_pickups.go`:

```go
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
	"github.com/spf13/cobra"
)

var footballPickupsCmd = &cobra.Command{
	Use:   "football-pickups",
	Short: "Daily per-league digest of Sleeper pickup opportunities from public data",
	Long: `Snapshots Sleeper's player dump once a day (players on an NFL club at a
position some league can roster), archives it as source sleeper-players, and
diffs it against the most recent prior capture. Three detectors feed one
ranked digest per league, sent only when non-empty:

  ROLE  an unrostered player who became a depth-chart starter (#1) since the
        prior capture -- the signal that caught Lock and Wentz the week they
        drew real FAAB while StatsGuy's value sat still
  DROP  a priced player released by a completed waiver or free-agent move
        since the prior capture and still unrostered (trades excluded)
  CHOP  a guillotine elimination: the roster and its priced players

Public data only: no token. Dedup is one marker per event under
football/pickups/ (check -> send -> mark); --dry-run sends nothing, marks
nothing, archives nothing and leaves the snapshot pointer untouched. The
first run writes a baseline and alerts nothing.`,
	RunE: runFootballPickups,
}

func init() { rootCmd.AddCommand(footballPickupsCmd) }

// pickupSource is the Daily Archive source name; pickupSnapshotKey is the one
// object the diff reads and overwrites.
const (
	pickupSource      = "sleeper-players"
	pickupSnapshotKey = "latest.json"
)

func runFootballPickups(cmd *cobra.Command, args []string) error {
	cfg, sc, err := initFootball()
	if err != nil {
		return err
	}
	if err := cfg.requireSleeperUserID(); err != nil {
		return err
	}
	ctx := context.Background()

	state, err := sc.State(ctx)
	if err != nil {
		return fmt.Errorf("sleeper state: %w", err)
	}
	leagues, err := discoverLeagues(ctx, sc, cfg, state, os.Stdout)
	if err != nil {
		// An off-season stop has already printed its one line and recorded
		// the outcome; silence cobra so the exit-0 path prints nothing else,
		// exactly as checkSeasonGate does for the baseball commands.
		if errors.Is(err, errOffSeason) {
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
		}
		return err
	}
	players, err := sc.PlayersNFL(ctx)
	if err != nil {
		return fmt.Errorf("sleeper players: %w", err)
	}
	bundle, err := statsguy.LoadBundle(ctx, cacheDir, cacheTTL(statsguy.CacheTTL))
	if err != nil {
		return fmt.Errorf("statsguy bundle: %w", err)
	}
	now := time.Now().UTC()

	var lgs []sleeper.League
	for _, lc := range leagues {
		lgs = append(lgs, lc.League)
	}
	cur := dynasty.BuildPlayerSnapshot(now, players, dynasty.RosterablePositions(lgs))

	// The pointer store is a HARD requirement: without it there is nothing to
	// diff against and nothing to save, and a run that silently alerted on a
	// full-dump "diff" would page on every starter in the league.
	snapStore, err := statestore.FromEnv().FootballPickupSnapshot()
	if err != nil {
		return fmt.Errorf("init pickup snapshot store: %w", err)
	}
	prev, found, err := loadPickupSnapshot(ctx, snapStore)
	if err != nil {
		return fmt.Errorf("load prior snapshot: %w", err)
	}
	if !found {
		fmt.Printf("football-pickups: no prior capture; wrote baseline of %d players and alerted nothing\n", len(cur.Players))
		if dryRun {
			fmt.Println("football-pickups (dry-run): baseline not written")
			return nil
		}
		return persistPickupSnapshot(now, cur, snapStore)
	}

	// Soft: a marker store we cannot build disables dedup, not the digest.
	markers, err := statestore.FromEnv().FootballPickupMarkers()
	if err != nil {
		warn("football-pickups: init markers: %v (digests will repeat until resolved)", err)
		markers = nil
	}
	captureDate := now.Format("2006-01-02")

	total, failed := pollPickups(ctx, leagues,
		func(ctx context.Context, lc leagueContext) (pickupLeagueInputs, error) {
			return loadPickupLeague(ctx, sc, lc.League.LeagueID, state)
		},
		func(lc leagueContext, in pickupLeagueInputs) pickupRunResult {
			roles := dynasty.DetectRoleChanges(prev, cur, in.rostered, bundle, lc.Profile.Format)
			drops, chops := dynasty.DetectDrops(in.txns, prev.CapturedAt, in.rostered, players, bundle, lc.Profile.Format, in.names)
			items := dynasty.OrderPickups(dynasty.PickupItems(captureDate, chops, drops, roles), lc.Profile.Superflex)
			res := alertPickups(ctx, pickupAlertInputs{
				markers: markers, league: lc.Profile, items: items, dryRun: dryRun,
				send: func(title, body string) error { return sendFootballPickupAlert(ctx, title, body) },
				out:  os.Stdout,
			})
			res.Roles, res.Drops, res.Chops = len(roles), len(drops), len(chops)
			printPickupCoverage(os.Stdout, lc.League.Name, prev.CapturedAt, len(cur.Players), len(cur.Players)-countRostered(cur, in.rostered), res)
			return res
		})

	if dryRun {
		fmt.Println("football-pickups (dry-run): snapshot not archived, pointer not moved")
	} else if err := persistPickupSnapshot(now, cur, snapStore); err != nil {
		// Soft: the alerts went out and the markers are set; the next run
		// diffs against the older capture and the markers keep it quiet.
		warn("football-pickups: %v (next run diffs against the prior capture)", err)
	}
	fmt.Printf("football-pickups: %d league(s), prev=%s, %d role, %d drops, %d chops, %d new, %d digest(s) sent\n",
		len(leagues), prev.CapturedAt.Format("2006-01-02"), total.Roles, total.Drops, total.Chops, total.New, total.Sent)
	if len(failed) > 0 {
		return fmt.Errorf("football-pickups: %d of %d league(s) failed: %s", len(failed), len(leagues), strings.Join(failed, ", "))
	}
	return nil
}

// persistPickupSnapshot archives today's capture (history, NoBackfill) and
// moves the pointer (the next diff's baseline). Archive first: a pointer
// that moved without its history landing is worse than the reverse.
func persistPickupSnapshot(now time.Time, snap dynasty.PlayerSnapshot, snapStore lineupapi.BlobStore) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	w, err := statestore.FromEnv().ArchiveWriter()
	if err != nil {
		return fmt.Errorf("init archive writer: %w", err)
	}
	if err := w.Write(now, pickupSource, []archive.Artifact{{Filename: "players.json", Bytes: body}}); err != nil {
		return fmt.Errorf("archive %s: %w", pickupSource, err)
	}
	if err := snapStore.Publish(pickupSnapshotKey, body); err != nil {
		return fmt.Errorf("save snapshot pointer: %w", err)
	}
	return nil
}

func loadPickupSnapshot(ctx context.Context, store lineupapi.ObjectStore) (dynasty.PlayerSnapshot, bool, error) {
	body, found, err := store.Get(ctx, pickupSnapshotKey)
	if err != nil || !found {
		return dynasty.PlayerSnapshot{}, false, err
	}
	var snap dynasty.PlayerSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return dynasty.PlayerSnapshot{}, false, fmt.Errorf("decode %s: %w", pickupSnapshotKey, err)
	}
	return snap, true, nil
}

func savePickupSnapshot(store lineupapi.BlobStore, snap dynasty.PlayerSnapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return store.Publish(pickupSnapshotKey, body)
}

// pickupLeagueInputs is one league's public inputs.
type pickupLeagueInputs struct {
	rostered map[string]bool
	txns     []sleeper.Transaction
	names    map[int]string
}

func loadPickupLeague(ctx context.Context, sc *sleeper.Client, leagueID string, state *sleeper.NFLState) (pickupLeagueInputs, error) {
	rosters, err := sc.Rosters(ctx, leagueID)
	if err != nil {
		return pickupLeagueInputs{}, fmt.Errorf("sleeper rosters: %w", err)
	}
	users, err := sc.Users(ctx, leagueID)
	if err != nil {
		return pickupLeagueInputs{}, fmt.Errorf("sleeper users: %w", err)
	}
	in := pickupLeagueInputs{rostered: map[string]bool{}, names: dynasty.TeamNames(rosters, users)}
	for _, r := range rosters {
		for _, id := range r.Players {
			in.rostered[id] = true
		}
		for _, id := range r.Taxi {
			in.rostered[id] = true
		}
		for _, id := range r.Reserve {
			in.rostered[id] = true
		}
	}
	for _, week := range pollWeeks(state) {
		txns, err := sc.Transactions(ctx, leagueID, week)
		if err != nil {
			return pickupLeagueInputs{}, fmt.Errorf("sleeper transactions (week %d): %w", week, err)
		}
		in.txns = append(in.txns, txns...)
	}
	return in, nil
}

func countRostered(snap dynasty.PlayerSnapshot, rostered map[string]bool) int {
	n := 0
	for id := range snap.Players {
		if rostered[id] {
			n++
		}
	}
	return n
}

type pickupLoader func(ctx context.Context, lc leagueContext) (pickupLeagueInputs, error)
type pickupRunner func(lc leagueContext, in pickupLeagueInputs) pickupRunResult

// pickupRunResult is one league's counts (or the run's totals).
type pickupRunResult struct {
	Roles, Drops, Chops, New, Sent int
}

// pollPickups runs load -> run per league with the same isolation as
// pollLeagues/pollOffers: a league whose load fails is warned, named in
// failed and skipped; the others still run.
func pollPickups(ctx context.Context, leagues []leagueContext, load pickupLoader, run pickupRunner) (total pickupRunResult, failed []string) {
	for _, lc := range leagues {
		in, err := load(ctx, lc)
		if err != nil {
			warn("football-pickups: %s: %v (continuing with the other leagues)", lc.League.Name, err)
			failed = append(failed, lc.League.Name)
			continue
		}
		r := run(lc, in)
		total.Roles += r.Roles
		total.Drops += r.Drops
		total.Chops += r.Chops
		total.New += r.New
		total.Sent += r.Sent
	}
	return total, failed
}

// printPickupCoverage is the unconditional per-league line, zero case
// included: without it a quiet league and a blind diff look identical.
func printPickupCoverage(out io.Writer, league string, prevAt time.Time, players, unrostered int, r pickupRunResult) {
	fmt.Fprintf(out, "football-pickups: %s: prev=%s players=%d unrostered=%d role=%d drops=%d chops=%d new=%d\n",
		league, prevAt.Format("2006-01-02"), players, unrostered, r.Roles, r.Drops, r.Chops, r.New)
}

// pickupAlertInputs is one league's digest inputs with the side effects
// injected — the tradeRunInputs/offerRunInputs seam again.
type pickupAlertInputs struct {
	markers lineupapi.BlobStore
	league  dynasty.LeagueProfile
	items   []dynasty.PickupItem
	dryRun  bool
	send    func(title, body string) error
	out     io.Writer
}

// alertPickups sends ONE digest per league carrying only the items with no
// marker, then marks the items the digest showed: check -> send -> mark. A
// failed send marks nothing; a marker-write failure degrades to a repeat next
// run; an item the digest could not fit stays unmarked for the next run.
func alertPickups(ctx context.Context, in pickupAlertInputs) pickupRunResult {
	var res pickupRunResult
	m := alertmarker.New(in.markers, alertmarker.WithLogf(func(format string, args ...any) {
		warn("football-pickups: "+format, args...)
	}))
	var fresh []dynasty.PickupItem
	for _, it := range in.items {
		if m.Sent(ctx, in.league.LeagueID+"-"+it.Key) {
			continue
		}
		fresh = append(fresh, it)
	}
	res.New = len(fresh)
	if len(fresh) == 0 {
		return res
	}
	title, body, shown := dynasty.FormatPickupDigest(in.league, fresh)
	fmt.Fprintln(in.out, title)
	fmt.Fprintln(in.out, body)
	if in.dryRun {
		return res
	}
	if err := in.send(title, body); err != nil {
		warn("football-pickups: %s: send failed: %v (nothing marked; next run retries)", in.league.Name, err)
		return res
	}
	res.Sent = 1
	// Mark ONLY the items the digest actually carried. A line the digest
	// refused for space is still unmarked, so it leads the next run's digest
	// instead of being muted forever -- the silent-loss failure this repo's
	// check -> send -> mark rule exists to prevent.
	for _, it := range fresh[:shown] {
		m.Record(in.league.LeagueID+"-"+it.Key, []byte(it.Line))
	}
	return res
}

func sendFootballPickupAlert(ctx context.Context, title, body string) error {
	return notify.Send(ctx, notify.Event{Kind: "waivers", Title: title, Message: body})
}
```

If `lineupapi.ObjectStore` is not the read-only interface's name (Plan 1's `relogRows` takes `markers lineupapi.ObjectStore`, so it exists), use it as there.

- [ ] **Step 3b: Wiring that must land with the command**

Add `FootballPickupSnapshot` to `layout.All()` (next to `FootballValues`) and, in `internal/statestore/layout/layout_test.go`'s `TestFootballPickups_MarkerFamilyAndSnapshotPointer`, add back the assertion `if !inAll[s.Name] { t.Error("the snapshot pointer must be in All(): it is rewritten daily, so its age IS the job's health") }`; remove `FootballPickupSnapshot` from `tenant_test.go`'s `append(...)` list (it is in `All()` now). Then:

- `cmd/season_gate.go`: add `"football-pickups": {},` after `"football-offers": {},`.
- `internal/lineupapi/authz.go`: add `"football-pickups": true,` to `leagueWideJobs` beside `football-offers`.
- `infra/infra.go`, jobs table, after the `FootballOffers` row (positional, on the shared task — no `jobTaskDefs` entry):

```go
		// Daily pickup digest per league: depth-chart role changes, valued
		// drops and guillotine chops, from public data only. 15:15 UTC sits
		// after FootballValues (14:45) has warmed the StatsGuy cache. Its
		// snapshot pointer (layout.FootballPickupSnapshot) is rewritten every
		// run, so the Infra tab reads the job's health from that object's age.
		{"FootballPickups", "cron(15 15 * * ? *)", jsii.Strings("football-pickups"), dailyGap},
```

- `Makefile` `run-all`, after the `football-offers` line (one tab-indented line):

```make
	@echo "=== football-pickups --dry-run ===";         if [ -n "$$SLEEPER_LEAGUE_ID" ] && [ -n "$$SLEEPER_USER_ID" ]; then time go run . football-pickups --dry-run; else echo "SKIPPED (SLEEPER_LEAGUE_ID or SLEEPER_USER_ID unset)"; fi && echo
```

- [ ] **Step 4: Tests, lint, tidy, the guards**

Run: `go test ./cmd/ -run 'AlertPickups|PollPickups|PickupSnapshot|CoverageLine|SeasonGate' -v && go test ./internal/statestore/... ./internal/lineupapi/ -run 'FootballPickups|OpsAlert|PerTenant|Producers|LeagueWide' && go test ./internal/sleeperauth/ && (cd infra && go test ./...) && make build-modules && make check-pins && make lint && go mod tidy && git diff --stat go.mod go.sum && cat -e -t -v Makefile | grep football-pickups`
Expected: all PASS (season gate: the command is registered and classified and its schedule row exists; producer guard: `FootballPickups` is now a real schedule; the `sleeperauth` depcheck: this file does not import it); lint clean; go.mod/go.sum unchanged; the Makefile line shows a leading `^I` on one line.

- [ ] **Step 5: Live dry-run (operator's `.env` has `SLEEPER_LEAGUE_ID` and `SLEEPER_USER_ID`; no token needed)**

```bash
go run . football-pickups --dry-run 2>&1 | tail -20
```

Expected on the first run: six profile lines, then `football-pickups: no prior capture; wrote baseline ...` and `(dry-run): baseline not written`. To see a real diff locally, run it once WITHOUT `--dry-run` from a scratch cache (`STATE_BUCKET` unset writes to `.football/pickups-snapshot/` and `.archive/` in the worktree), wait a day or hand-edit `latest.json`'s `captured_at` back a week, then `--dry-run` again: per-league coverage lines and any digests print. Both local files are gitignored under `.football/`/`.archive/`; confirm with `git status` before committing.

- [ ] **Step 6: Commit**

```bash
git add cmd/football_pickups.go cmd/football_pickups_test.go cmd/season_gate.go internal/lineupapi/authz.go infra/infra.go Makefile internal/statestore/layout/layout.go internal/statestore/layout/layout_test.go internal/statestore/tenant_test.go
git commit -m "feat(football-pickups): daily per-league pickup digest; schedule, classification, admin-only, run-all gate"
```

---

### Task 7: Docs

**Files:**
- Modify: `README.md` (football `<details>` block: command example + a paragraph; env table needs no new row), `docs/dynasty-football.md` (new paragraph after `football-offers`), `docs/aws-deployment.md` (19 → 20 schedule rules, `FootballPickups` in the list), `CLAUDE.md` (`internal/notify` paragraph: the `notify.Send` count rises by one — count with `git grep -n 'notify\.Send(' -- cmd internal ':!*_test.go' | grep -v notify_test_send | wc -l` and set "Thirteen … A fourteenth" or whatever the count says).

- [ ] **Step 1: README**

In the football `<details>` block add `rosterbot football-pickups --dry-run` to the examples and append: "`football-pickups` (daily, 15:15 UTC) snapshots Sleeper's player dump once a day — players on an NFL club at a position some discovered league can roster — archives it as source `sleeper-players`, and diffs it against the most recent prior capture (kept as a `latest.json` pointer; a missed day widens the window rather than losing the diff). Per league it reports, in one ranked digest sent only when non-empty, unrostered players who just became depth-chart starters (the signal that caught real FAAB pickups while StatsGuy's value sat still), priced players released by completed waiver/free-agent moves (trades excluded), and guillotine chops with the eliminated roster's priced players. An unvalued role change is listed with its depth-chart fact rather than hidden. Public data only; one dedup marker per event; `--dry-run` sends, marks and archives nothing; the first run writes a baseline and alerts nothing. Unconditional per-league coverage line, zero case included."

- [ ] **Step 2: `docs/dynasty-football.md`**

After the `football-offers` paragraph add a `**football-pickups**` paragraph covering: the snapshot (`dynasty.BuildPlayerSnapshot`, `RosterablePositions` and why K/DEF are filtered by the leagues' own slots), the archive source `sleeper-players` (NoBackfill: Sleeper keeps no depth-chart history) AND the `latest.json` pointer in `layout.FootballPickupSnapshot` (why: the archive store is write-only; the pointer answers "most recent prior capture, whenever that was" without listing; it is in `All()` with a 2-day `MaxAge` because its age is the job's health), the three detectors with their measured rationale (Lock/Wentz at QB/1; trade halves under `drops`; the 2026-09-15 chop), `PickupItems`/`pickupRank` (operator-written; the chosen rule) /`OrderPickups`/`FormatPickupDigest` (whole lines, "…and N more"), one item and one marker per player so a partly-sent chop resumes, `alertPickups`' check → send → mark with one digest per league, `pollPickups` isolation, `persistPickupSnapshot` ordering (archive before pointer; runs AFTER the alerts so a crash keeps the old baseline and the markers keep it quiet), the coverage line, the GraphQL `get_active_players` alternative deliberately not used, and the schedule/Infra facts.

- [ ] **Step 3: `docs/aws-deployment.md` and `CLAUDE.md`**

Schedule count 19 → 20 with `FootballPickups` after `FootballOffers`; the `notify` paragraph's two count words updated to the grep's result.

- [ ] **Step 4: Full suite and lint**

Run: `go test ./... 2>&1 | tail -10 && (cd infra && go test ./...) && make lint`
Expected: all PASS; lint clean.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/dynasty-football.md docs/aws-deployment.md CLAUDE.md
git commit -m "docs: football-pickups"
```

---

## Self-Review

**Spec coverage (Section 4):**
- Daily 15:15 UTC, public only, dump once → Tasks 5, 6. ✔
- Snapshot fields and filter; archive source `sleeper-players`; NoBackfill via the archive layout; Infra chip automatic → Tasks 2, 6. ✔ (The "most recent prior partition" is read through a `latest.json` pointer rather than by listing the archive — a mechanism substitution, since the archive store is write-only; semantics identical, and documented in Task 5's comment and Task 7.)
- GraphQL player list deliberately unused → Task 7 doc. ✔
- Diff against the most recent prior capture; first run baseline → Task 6. ✔
- Three detectors, pure, in `internal/dynasty` → Task 3. ✔
- Digest per league, non-empty only, ordering with the operator's rule, unvalued role changes listed, `pushover.Builder` → Task 4. ✔
- Markers `(league, player, capture date)` / `(league, transaction, player)`; check → send → mark; dry-run neither; kind `waivers` → Tasks 5, 6. ✔ (The schedule row, classification, admin-only entry, smoke line and the snapshot's `All()` membership land in Task 6 with the command, because the season-gate test requires a classified command to be registered.)
- Coverage line unconditional → Task 6. ✔
- Deferred "best available" not built. ✔
- Cross-cutting: run-all line, schedule, classification, docs → Tasks 5, 7. ✔

**Placeholder scan:** Task 4 Step 3 is an explicit operator hand-off with a compilable reference default. Task 6 Step 5's local diff recipe is concrete. No TBDs.

**Type consistency:** `PickupItem{Kind, Key, PlayerID, Line, Position, Value, Priced, ChopSize}` (Task 4) is what `alertPickups` (Task 6) reads (`Key`, `Line`) and the tests construct. `DetectDrops(txns, since, rostered, players, bundle, format, names)` (Task 3) matches its call in Task 6's runner. `PlayerSnapshot.CapturedAt` (Task 2) is the `since` Task 6 passes. `FormatPickupDigest(profile, items)` returns `(title, body)` as `alertPickups` uses. `layout.FootballPickupSnapshot`/`FootballPickupMarkers()` (Task 5) back `snapStore`/`markers` in Task 6. `lineupapi.BlobStore.Publish(key, body)` has no ctx and `Get(ctx, key)` does — as in Plans 1 and 2.
