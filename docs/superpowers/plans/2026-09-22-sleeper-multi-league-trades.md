# Sleeper Multi-League Foundation + `football-trades` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Discover every Sleeper league the operator belongs to, derive a per-league format profile, and extend `football-trades` from one league to all of them.

**Architecture:** A pure `dynasty.LeagueProfile` derivation (superflex from roster slots, kind from Sleeper's undocumented `settings.type`, an env override that wins) feeds a `cmd/football_leagues.go` discovery step shared by the football jobs. `football-trades` loops the discovered leagues, running its existing per-league body unchanged; the log row and dashboard entry carry the league. This is Plan 1 of 3 from the spec; Plans 2 (`football-offers`) and 3 (`football-pickups`) build on the discovery step defined here.

**Tech Stack:** Go 1.2x (root module), cobra, `internal/sleeper` (public REST client), `internal/statsguy`, `internal/dynasty`, `internal/alertmarker`, `internal/notify`, vanilla-JS dashboard (`web/dashboard/football.js`), AWS CDK in Go (`infra/`).

**Spec:** `docs/superpowers/specs/2026-09-22-sleeper-multi-league-pickups-offers-design.md` (Section 2 and the `Transaction` type change from Section 1).

## Global Constraints

- Every unchecked error is a lint failure (`errcheck` excludes only the print family); each `_ =` is a deliberate decision.
- `nilerr` + `nolintlint` with `require-explanation`: a soft-fail must print or wrap its error; a bare `//nolint` fails the build.
- `noctx`: use `http.NewRequestWithContext`, never `http.Get`/`http.NewRequest`.
- `gofmt` runs on every edit via hook; `make lint` and `go mod tidy` after code changes.
- Coverage lines print **unconditionally**, including the zero case (repo rule: a silent zero and a blind check are indistinguishable).
- Alert order is **check → send → mark**; `--dry-run` skips both send and mark.
- New env vars, flags and commands go in `README.md`; internal detail in `docs/dynasty-football.md`. A new `internal/` package with no doc mention fails `TestEveryInternalPackageHasDocCoverage`.
- The `secret()` helper in `infra/infra.go` references an SSM SecureString parameter `/rosterbot/<NAME>` that **must exist in us-west-1 before the deploy** — a missing parameter fails every task launch (`ResourceInitializationError`).
- Commit after every task; never push from a task step (the session end pushes).
- The operator's Sleeper user id is `738883211463155712` (display name `NIX0N`).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/sleeper/types.go` (modify) | `Transaction` gains `Creator`, `ConsenterIDs`. |
| `internal/sleeper/client_test.go` (modify) | Decode test for the two new fields. |
| `internal/dynasty/profile.go` (create) | `LeagueKind`, `LeagueProfile`, `DeriveProfile`, `formatFor` (operator-written), `ParseFormatOverrides`. Pure. |
| `internal/dynasty/profile_test.go` (create) | Table test over the operator's six live league shapes; override and error cases. |
| `internal/dynasty/trade_log.go` (modify) | `TradeLogRow` gains `LeagueID`, `LeagueName`; `InLeague` setter. |
| `internal/dynasty/trade_log_model.go` (modify) | `TradeLogEntry` gains the two fields; `entryFor` maps them. |
| `internal/dynasty/trade_log_test.go` (modify) | Tests for `InLeague` and `entryFor`. |
| `cmd/football_init.go` (modify) | `FootballConfig` gains `SleeperUserID`, `FormatOverrides`; `requireSleeperUserID`. |
| `cmd/football_init_test.go` (modify) | Env parsing tests. |
| `cmd/football_leagues.go` (create) | `leagueContext`, `leagueContexts` (pure), `discoverLeagues` (fetch + print). |
| `cmd/football_leagues_test.go` (create) | Filter/sort/override tests. |
| `cmd/football_trades.go` (modify) | Loop over leagues; per-league failure isolation; league-prefixed title; league on log rows; relog per league. |
| `cmd/football_trades_log_test.go` (modify) | Fixture carries a profile; title and log-row assertions. |
| `cmd/football_trades_test.go` (modify) | `formatTradeAlert` signature. |
| `web/dashboard/football.js` (modify) | League chip on each trade card. |
| `infra/infra.go` (modify) | `SLEEPER_USER_ID` injected from SSM. |
| `Makefile` (modify) | `run-all` gate for `football-trades`. |
| `README.md`, `docs/dynasty-football.md` (modify) | Env vars, behaviour. |

---

### Task 1: `Transaction` gains `creator` and `consenter_ids`

**Files:**
- Modify: `internal/sleeper/types.go:68-80`
- Test: `internal/sleeper/client_test.go`

**Interfaces:**
- Produces: `sleeper.Transaction.Creator string` (Sleeper user id of the proposer), `sleeper.Transaction.ConsenterIDs []int` (roster ids that have accepted). Plan 2's offer filter reads both.

- [ ] **Step 1: Write the failing test**

Append to `internal/sleeper/client_test.go`:

```go
func TestTransaction_DecodesCreatorAndConsenters(t *testing.T) {
	const body = `{"transaction_id":"t1","type":"trade","status":"pending",
	  "roster_ids":[3,7],"creator":"738883211463155712","consenter_ids":[3],
	  "adds":{"4984":3},"drops":{"9509":7},"created":1758400000000}`
	var txn Transaction
	if err := json.Unmarshal([]byte(body), &txn); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if txn.Creator != "738883211463155712" {
		t.Errorf("Creator = %q, want the proposer's user id", txn.Creator)
	}
	if len(txn.ConsenterIDs) != 1 || txn.ConsenterIDs[0] != 3 {
		t.Errorf("ConsenterIDs = %v, want [3]", txn.ConsenterIDs)
	}
}
```

Add `"encoding/json"` to the test file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sleeper/ -run TestTransaction_DecodesCreatorAndConsenters -v`
Expected: FAIL — `txn.Creator undefined` (compile error).

- [ ] **Step 3: Add the fields**

In `internal/sleeper/types.go`, replace the `Transaction` struct with:

```go
// Transaction is one league transaction (trade, waiver claim, free-agent
// add/drop, or — in a guillotine league — a chop) for a given week ("round"
// in Sleeper's API). The same shape is returned by the public REST feed and,
// with the two trailing fields populated, by the authenticated GraphQL
// league_transactions_by_status query that Plan 2 reads pending offers from.
type Transaction struct {
	TransactionID string                 `json:"transaction_id"`
	Type          string                 `json:"type"`   // trade, free_agent, waiver, chopped
	Status        string                 `json:"status"` // complete, pending, failed
	RosterIDs     []int                  `json:"roster_ids"`
	Adds          map[string]int         `json:"adds"`
	Drops         map[string]int         `json:"drops"`
	DraftPicks    []TransactionDraftPick `json:"draft_picks"`
	WaiverBudget  []WaiverBudgetTransfer `json:"waiver_budget"`
	Created       int64                  `json:"created"` // epoch millis

	// Creator is the Sleeper USER id (not roster id) that proposed the
	// transaction. For a trade it is who sent the offer, which is what
	// separates "an offer made to me" from "an offer I made".
	Creator string `json:"creator"`
	// ConsenterIDs are the ROSTER ids that have accepted so far. On a
	// completed trade it holds every party; on a pending one it shows who is
	// still being waited on.
	ConsenterIDs []int `json:"consenter_ids"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sleeper/ -v`
Expected: PASS (all tests in the package).

- [ ] **Step 5: Commit**

```bash
git add internal/sleeper/types.go internal/sleeper/client_test.go
git commit -m "feat(sleeper): Transaction carries creator and consenter_ids

Both fields are in the REST payload today and are what Plan 2's pending-offer
filter needs to tell an offer made to the operator from one they sent."
```

---

### Task 2: `dynasty.LeagueProfile` — the per-league format derivation

**Files:**
- Create: `internal/dynasty/profile.go`
- Test: `internal/dynasty/profile_test.go`

**Interfaces:**
- Consumes: `sleeper.League{LeagueID, Name, RosterPositions []string, Settings map[string]int}` (exists).
- Produces:
  - `type LeagueKind string` with constants `KindRedraft`, `KindKeeper`, `KindDynasty`, `KindGuillotine`, `KindUnknown`.
  - `type LeagueProfile struct { LeagueID, Name string; Superflex bool; Kind LeagueKind; Format string; FormatSource string }` — `Format` is one of `sf_dynasty|non_sf_dynasty|sf_redraft|non_sf_redraft`; `FormatSource` is `"derived"` or `"override"`.
  - `func DeriveProfile(lg sleeper.League, overrides map[string]string) LeagueProfile`
  - `func ParseFormatOverrides(s string) (map[string]string, error)` — parses `"<league_id>=<format>,..."`; empty string → empty map, nil error.
  - `func KnownFormat(f string) bool`.

**⚠ Operator contribution.** `formatFor(kind LeagueKind, superflex bool) string` is the mapping from league kind to StatsGuy column. The spec reserves it for the operator: the executor prepares the stub and the test table in Step 1, **stops before Step 3 and asks the operator to write or approve the mapping**. A suggested default is included so the task is executable either way; the keeper row is the one the operator is most likely to change.

- [ ] **Step 1: Write the failing test**

Create `internal/dynasty/profile_test.go`:

```go
package dynasty

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// The six leagues the operator was measured in on 2026-09-21. settings.type is
// undocumented; these are the values Sleeper returned for each.
func liveLeagueFixtures() []sleeper.League {
	sf := []string{"QB", "RB", "RB", "WR", "WR", "WR", "TE", "FLEX", "FLEX", "SUPER_FLEX", "BN"}
	std := []string{"QB", "RB", "RB", "WR", "WR", "TE", "FLEX", "K", "DEF", "BN"}
	return []sleeper.League{
		{LeagueID: "1312135439800356864", Name: "Palm Trees & Promethazine", Status: "in_season", RosterPositions: sf, Settings: map[string]int{"type": 2}},
		{LeagueID: "1312113686978007040", Name: "Mad Lux Euphoria Utopia League", Status: "in_season", RosterPositions: sf, Settings: map[string]int{"type": 2}},
		{LeagueID: "1312135540388171776", Name: "Camp Roberts FFL", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 1}},
		{LeagueID: "1388008384137007104", Name: "Mad Luxurious 2.0", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 0}},
		{LeagueID: "1312176751115259904", Name: "No Punt Intended", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 0}},
		{LeagueID: "1389013496645058560", Name: "Mad Lux Chopped 6.9", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 3}},
	}
}

func TestDeriveProfile_LiveLeagues(t *testing.T) {
	want := map[string]struct {
		kind      LeagueKind
		superflex bool
		format    string
	}{
		"1312135439800356864": {KindDynasty, true, "sf_dynasty"},
		"1312113686978007040": {KindDynasty, true, "sf_dynasty"},
		"1312135540388171776": {KindKeeper, false, "non_sf_dynasty"}, // operator's call — see formatFor
		"1388008384137007104": {KindRedraft, false, "non_sf_redraft"},
		"1312176751115259904": {KindRedraft, false, "non_sf_redraft"},
		"1389013496645058560": {KindGuillotine, false, "non_sf_redraft"},
	}
	for _, lg := range liveLeagueFixtures() {
		p := DeriveProfile(lg, nil)
		w := want[lg.LeagueID]
		if p.Kind != w.kind || p.Superflex != w.superflex || p.Format != w.format {
			t.Errorf("%s: got kind=%s sf=%v format=%s, want kind=%s sf=%v format=%s",
				lg.Name, p.Kind, p.Superflex, p.Format, w.kind, w.superflex, w.format)
		}
		if p.FormatSource != "derived" {
			t.Errorf("%s: FormatSource = %q, want derived", lg.Name, p.FormatSource)
		}
		if p.LeagueID != lg.LeagueID || p.Name != lg.Name {
			t.Errorf("%s: identity not carried", lg.Name)
		}
	}
}

func TestDeriveProfile_OverrideWinsAndIsLabelled(t *testing.T) {
	lg := liveLeagueFixtures()[2] // keeper
	p := DeriveProfile(lg, map[string]string{lg.LeagueID: "non_sf_redraft"})
	if p.Format != "non_sf_redraft" || p.FormatSource != "override" {
		t.Errorf("got %s/%s, want non_sf_redraft/override", p.Format, p.FormatSource)
	}
	if p.Kind != KindKeeper {
		t.Errorf("override must not rewrite Kind; got %s", p.Kind)
	}
}

func TestDeriveProfile_UnknownTypeIsNamedNotGuessed(t *testing.T) {
	lg := sleeper.League{LeagueID: "x", Name: "X", RosterPositions: []string{"QB", "SUPER_FLEX"}, Settings: map[string]int{"type": 9}}
	if p := DeriveProfile(lg, nil); p.Kind != KindUnknown {
		t.Errorf("type 9 → Kind = %s, want unknown", p.Kind)
	}
	// A league with no settings at all is also unknown, not redraft-by-zero-value.
	lg.Settings = nil
	if p := DeriveProfile(lg, nil); p.Kind != KindUnknown {
		t.Errorf("no settings → Kind = %s, want unknown", p.Kind)
	}
}

func TestParseFormatOverrides(t *testing.T) {
	got, err := ParseFormatOverrides(" 1=sf_dynasty, 2=non_sf_redraft ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "sf_dynasty" || got["2"] != "non_sf_redraft" || len(got) != 2 {
		t.Errorf("got %v", got)
	}
	if got, err := ParseFormatOverrides(""); err != nil || len(got) != 0 {
		t.Errorf("empty: got %v, %v", got, err)
	}
	for _, bad := range []string{"1", "1=", "=sf_dynasty", "1=bogus", "1=sf_dynasty,1=sf_redraft"} {
		if _, err := ParseFormatOverrides(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dynasty/ -run 'TestDeriveProfile|TestParseFormatOverrides' -v`
Expected: FAIL — `undefined: DeriveProfile` (compile error).

- [ ] **Step 3: ⚠ STOP — operator writes `formatFor`**

Create `internal/dynasty/profile.go` with everything **except** the body of `formatFor`, then ask the operator to fill it. The suggested default is in the comment; if the operator changes the keeper mapping, update the keeper row in `TestDeriveProfile_LiveLeagues` to match.

```go
package dynasty

import (
	"fmt"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// LeagueKind is a league's format class as derived from Sleeper's UNDOCUMENTED
// settings.type. The mapping is what the operator's six leagues returned on
// 2026-09-21 (bd memory sleeper-multi-league-format-signals-2026-09-21); it is
// a default, not a fact, which is why DeriveProfile takes overrides.
type LeagueKind string

const (
	KindRedraft    LeagueKind = "redraft"
	KindKeeper     LeagueKind = "keeper"
	KindDynasty    LeagueKind = "dynasty"
	KindGuillotine LeagueKind = "guillotine"
	KindUnknown    LeagueKind = "unknown"
)

// LeagueProfile is everything the multi-league jobs derive from one league
// once: which StatsGuy column grades its trades and ranks its pickups, and
// the two facts that column was chosen from.
type LeagueProfile struct {
	LeagueID  string
	Name      string
	Superflex bool
	Kind      LeagueKind
	// Format is the StatsGuy column (sf_dynasty | non_sf_dynasty | sf_redraft
	// | non_sf_redraft). FormatSource says how it was chosen: "derived" from
	// Kind+Superflex, or "override" from SLEEPER_FORMAT_OVERRIDES. Every run
	// prints both, because settings.type is undocumented and a wrong column
	// renders as a confident number rather than a visible miss.
	Format       string
	FormatSource string
}

var knownFormats = map[string]bool{
	"sf_dynasty": true, "non_sf_dynasty": true, "sf_redraft": true, "non_sf_redraft": true,
}

// KnownFormat reports whether f is one of StatsGuy's four value columns.
func KnownFormat(f string) bool { return knownFormats[f] }

// DeriveProfile derives a league's profile. Superflex comes from
// roster_positions, which is documented and reliable. Kind comes from
// settings.type. An override for this league id replaces the derived Format
// and is labelled as such; it never rewrites Kind, which is reported as
// observed.
func DeriveProfile(lg sleeper.League, overrides map[string]string) LeagueProfile {
	p := LeagueProfile{LeagueID: lg.LeagueID, Name: lg.Name, Kind: KindUnknown}
	for _, pos := range lg.RosterPositions {
		if pos == "SUPER_FLEX" {
			p.Superflex = true
			break
		}
	}
	if t, ok := lg.Settings["type"]; ok {
		switch t {
		case 0:
			p.Kind = KindRedraft
		case 1:
			p.Kind = KindKeeper
		case 2:
			p.Kind = KindDynasty
		case 3:
			p.Kind = KindGuillotine
		}
	}
	p.Format = formatFor(p.Kind, p.Superflex)
	p.FormatSource = "derived"
	if f, ok := overrides[lg.LeagueID]; ok {
		p.Format = f
		p.FormatSource = "override"
	}
	return p
}

// formatFor maps a league's kind and superflex-ness to the StatsGuy column
// that should price it.
//
// OPERATOR-WRITTEN (spec Section 2). Suggested default:
//   dynasty, keeper, unknown → the dynasty column (multi-year value; unknown
//                              falls back to the deployment's original
//                              DYNASTY_FORMAT default)
//   redraft, guillotine      → the redraft column (single-season value)
//   superflex picks sf_* over non_sf_*.
// The keeper case is the judgement call: a league that keeps two or three
// players is closer to redraft than to dynasty.
func formatFor(kind LeagueKind, superflex bool) string {
	// operator writes this body (5-8 lines)
}

// ParseFormatOverrides parses SLEEPER_FORMAT_OVERRIDES: a comma-separated
// list of <league_id>=<format>. Whitespace around entries is ignored. An
// empty string is an empty map. Any malformed entry, unknown format, or
// repeated league id is an error rather than a silent skip — an override that
// did not apply would reproduce the exact silent-wrong-column failure the
// override exists to prevent.
func ParseFormatOverrides(s string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		id, format, ok := strings.Cut(entry, "=")
		id, format = strings.TrimSpace(id), strings.TrimSpace(format)
		if !ok || id == "" || format == "" {
			return nil, fmt.Errorf("SLEEPER_FORMAT_OVERRIDES: entry %q is not <league_id>=<format>", entry)
		}
		if !KnownFormat(format) {
			return nil, fmt.Errorf("SLEEPER_FORMAT_OVERRIDES: %q: unknown format %q (want sf_dynasty|non_sf_dynasty|sf_redraft|non_sf_redraft)", entry, format)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("SLEEPER_FORMAT_OVERRIDES: league %s listed twice", id)
		}
		out[id] = format
	}
	return out, nil
}
```

Operator's reference implementation of the suggested default, for when they approve it as-is:

```go
func formatFor(kind LeagueKind, superflex bool) string {
	dynastyLike := kind == KindDynasty || kind == KindKeeper || kind == KindUnknown
	switch {
	case dynastyLike && superflex:
		return "sf_dynasty"
	case dynastyLike:
		return "non_sf_dynasty"
	case superflex:
		return "sf_redraft"
	default:
		return "non_sf_redraft"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dynasty/ -run 'TestDeriveProfile|TestParseFormatOverrides' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dynasty/profile.go internal/dynasty/profile_test.go
git commit -m "feat(dynasty): LeagueProfile derives each league's StatsGuy column

Superflex from roster_positions (reliable); kind from Sleeper's undocumented
settings.type using the mapping the operator's six leagues returned; an env
override wins and is labelled so every run can print how the column was chosen."
```

---

### Task 3: Trade log rows and dashboard entries carry the league

**Files:**
- Modify: `internal/dynasty/trade_log.go:77-101` (struct), append `InLeague`
- Modify: `internal/dynasty/trade_log_model.go:37-51`, `:123-165`
- Test: `internal/dynasty/trade_log_test.go`

**Interfaces:**
- Produces: `TradeLogRow.LeagueID string json:"league_id,omitempty"`, `TradeLogRow.LeagueName string json:"league_name,omitempty"`, `func (r TradeLogRow) InLeague(id, name string) TradeLogRow`; `TradeLogEntry.LeagueID json:"leagueId,omitempty"`, `TradeLogEntry.LeagueName json:"leagueName,omitempty"`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dynasty/trade_log_test.go`:

```go
func TestTradeLogRow_InLeagueStampsIdentityOnly(t *testing.T) {
	row := TradeLogRow{TransactionID: "t1", AlertFormat: "sf_dynasty"}
	got := row.InLeague("1312135439800356864", "Palm Trees & Promethazine")
	if got.LeagueID != "1312135439800356864" || got.LeagueName != "Palm Trees & Promethazine" {
		t.Errorf("league not stamped: %+v", got)
	}
	if got.TransactionID != "t1" || got.AlertFormat != "sf_dynasty" {
		t.Errorf("other fields disturbed: %+v", got)
	}
	if row.LeagueID != "" {
		t.Errorf("InLeague must return a copy, not mutate the receiver")
	}
}

func TestBuildTradeLogModel_CarriesLeagueAndLeavesLegacyBlank(t *testing.T) {
	when := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	rows := []TradeLogRow{
		{Dt: "2026-09-22", TransactionID: "new", GradedAt: when, LeagueID: "L1", LeagueName: "Chopped"},
		{Dt: "2026-08-18", TransactionID: "old", GradedAt: when}, // written before multi-league
	}
	m := BuildTradeLogModel(rows, when)
	byID := map[string]TradeLogEntry{}
	for _, e := range m.Trades {
		byID[e.TransactionID] = e
	}
	if e := byID["new"]; e.LeagueID != "L1" || e.LeagueName != "Chopped" {
		t.Errorf("new row: league not carried: %+v", e)
	}
	if e := byID["old"]; e.LeagueID != "" || e.LeagueName != "" {
		t.Errorf("legacy row must stay blank, not be backfilled: %+v", e)
	}
}
```

If `trade_log_test.go` lacks a `time` import, add it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dynasty/ -run 'TestTradeLogRow_InLeague|TestBuildTradeLogModel_CarriesLeague' -v`
Expected: FAIL — `row.InLeague undefined`.

- [ ] **Step 3: Add the fields and the setter**

In `internal/dynasty/trade_log.go`, add to `TradeLogRow` directly after `TransactionID`:

```go
	// LeagueID / LeagueName identify which of the operator's leagues the trade
	// happened in. Both omitempty: rows written before football-trades went
	// multi-league carry neither, and they are left blank rather than
	// backfilled — every such row happens to be from one league, but stamping
	// that in would write an assumption as a fact.
	LeagueID   string `json:"league_id,omitempty"`
	LeagueName string `json:"league_name,omitempty"`
```

Append after `BuildTradeLogRow`:

```go
// InLeague returns a copy of the row stamped with the league it was graded
// in. A setter rather than two more BuildTradeLogRow parameters: the builder
// already takes six, and the league is known to the caller's loop, not to the
// grader.
func (r TradeLogRow) InLeague(id, name string) TradeLogRow {
	r.LeagueID = id
	r.LeagueName = name
	return r
}
```

In `internal/dynasty/trade_log_model.go`, add to `TradeLogEntry` after `TransactionID`:

```go
	// LeagueID / LeagueName as on TradeLogRow; blank on rows that predate
	// multi-league polling, which the view renders as a dash, not a guess.
	LeagueID   string `json:"leagueId,omitempty"`
	LeagueName string `json:"leagueName,omitempty"`
```

and in `entryFor`'s return literal add `LeagueID: r.LeagueID, LeagueName: r.LeagueName,` after `TransactionID:`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dynasty/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dynasty/trade_log.go internal/dynasty/trade_log_model.go internal/dynasty/trade_log_test.go
git commit -m "feat(dynasty): trade log rows and entries carry their league

omitempty on both, and legacy rows stay blank rather than backfilled."
```

---

### Task 4: `FootballConfig` reads `SLEEPER_USER_ID` and `SLEEPER_FORMAT_OVERRIDES`

**Files:**
- Modify: `cmd/football_init.go:13-43`
- Test: `cmd/football_init_test.go`

**Interfaces:**
- Produces: `FootballConfig.SleeperUserID string`, `FootballConfig.FormatOverrides map[string]string`, `func (c *FootballConfig) requireSleeperUserID() error`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/football_init_test.go` (follow the file's existing `t.Setenv` pattern; `SLEEPER_LEAGUE_ID` must be set for `loadFootballConfig` to succeed):

```go
func TestLoadFootballConfig_SleeperUserIDIsOptionalAtLoad(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "L")
	t.Setenv("SLEEPER_USER_ID", "")
	cfg, err := loadFootballConfig()
	if err != nil {
		t.Fatalf("football-values must still load without SLEEPER_USER_ID: %v", err)
	}
	if err := cfg.requireSleeperUserID(); err == nil {
		t.Errorf("requireSleeperUserID must fail when unset")
	}
	t.Setenv("SLEEPER_USER_ID", "738883211463155712")
	cfg, err = loadFootballConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SleeperUserID != "738883211463155712" || cfg.requireSleeperUserID() != nil {
		t.Errorf("user id not read: %+v", cfg)
	}
}

func TestLoadFootballConfig_FormatOverridesParsedAndRejected(t *testing.T) {
	t.Setenv("SLEEPER_LEAGUE_ID", "L")
	t.Setenv("SLEEPER_FORMAT_OVERRIDES", "1=sf_redraft")
	cfg, err := loadFootballConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FormatOverrides["1"] != "sf_redraft" {
		t.Errorf("override not parsed: %v", cfg.FormatOverrides)
	}
	t.Setenv("SLEEPER_FORMAT_OVERRIDES", "1=nope")
	if _, err := loadFootballConfig(); err == nil {
		t.Errorf("a malformed override must fail config load, not be skipped")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run 'TestLoadFootballConfig_SleeperUserID|TestLoadFootballConfig_FormatOverrides' -v`
Expected: FAIL — `cfg.requireSleeperUserID undefined`.

- [ ] **Step 3: Extend the config**

In `cmd/football_init.go`, replace the struct and loader:

```go
// FootballConfig holds dynasty-football configuration, read directly from
// env. Deliberately independent of internal/config.Config: config.Load fails
// outright without the four FANTRAX_* vars, and football commands must never
// need Fantrax credentials.
type FootballConfig struct {
	// SleeperLeagueID is the single league football-values reads. The
	// multi-league jobs (football-trades, and Plans 2/3's offers and pickups)
	// discover leagues from SleeperUserID instead and never read it.
	SleeperLeagueID string
	// SleeperUserID is the operator's Sleeper account id — stable where a
	// username is not. Optional at load so football-values keeps working
	// without it; the jobs that need it call requireSleeperUserID.
	SleeperUserID string
	// FormatOverrides is SLEEPER_FORMAT_OVERRIDES parsed: league id → StatsGuy
	// column, applied by dynasty.DeriveProfile over its derivation.
	FormatOverrides          map[string]string
	DynastyFormat            string
	FootballPushoverUserKey  string
	FootballPushoverGroupKey string
}

func loadFootballConfig() (*FootballConfig, error) {
	leagueID := os.Getenv("SLEEPER_LEAGUE_ID")
	if leagueID == "" {
		return nil, fmt.Errorf("missing required env var: SLEEPER_LEAGUE_ID")
	}
	overrides, err := dynasty.ParseFormatOverrides(os.Getenv("SLEEPER_FORMAT_OVERRIDES"))
	if err != nil {
		return nil, err
	}
	format := os.Getenv("DYNASTY_FORMAT")
	if format == "" {
		format = "sf_dynasty"
	}
	userKey := os.Getenv("FOOTBALL_PUSHOVER_USER_KEY")
	if userKey == "" {
		userKey = os.Getenv("PUSHOVER_USER_KEY")
	}
	groupKey := os.Getenv("FOOTBALL_PUSHOVER_GROUP_KEY")
	if groupKey == "" {
		groupKey = os.Getenv("PUSHOVER_GROUP_KEY")
	}
	return &FootballConfig{
		SleeperLeagueID:          leagueID,
		SleeperUserID:            os.Getenv("SLEEPER_USER_ID"),
		FormatOverrides:          overrides,
		DynastyFormat:            format,
		FootballPushoverUserKey:  userKey,
		FootballPushoverGroupKey: groupKey,
	}, nil
}

// requireSleeperUserID is the check every multi-league job runs first. Kept
// off loadFootballConfig so a deployment that has not yet set the parameter
// breaks the new jobs loudly and leaves football-values alone.
func (c *FootballConfig) requireSleeperUserID() error {
	if c.SleeperUserID == "" {
		return fmt.Errorf("missing required env var: SLEEPER_USER_ID (the operator's Sleeper account id; this job discovers leagues from it)")
	}
	return nil
}
```

Add `"github.com/nixon-commits/rosterbot/internal/dynasty"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run 'TestLoadFootballConfig|TestInitFootball' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/football_init.go cmd/football_init_test.go
git commit -m "feat(cmd): FootballConfig reads SLEEPER_USER_ID and SLEEPER_FORMAT_OVERRIDES

Optional at load so football-values is untouched; multi-league jobs require it."
```

---

### Task 5: League discovery — `cmd/football_leagues.go`

**Files:**
- Create: `cmd/football_leagues.go`
- Test: `cmd/football_leagues_test.go`

**Interfaces:**
- Consumes: `sleeper.Client.LeaguesForUser(ctx, userID, sport, season string) ([]sleeper.League, error)` (exists), `dynasty.DeriveProfile`.
- Produces:
  - `type leagueContext struct { League sleeper.League; Profile dynasty.LeagueProfile }`
  - `func leagueContexts(leagues []sleeper.League, overrides map[string]string) []leagueContext` — pure; drops `Status == "complete"`, sorts by name then id.
  - `func discoverLeagues(ctx context.Context, sc *sleeper.Client, cfg *FootballConfig, season string, out io.Writer) ([]leagueContext, error)` — fetches, filters, prints one line per league, errors if zero leagues remain.

- [ ] **Step 1: Write the failing tests**

Create `cmd/football_leagues_test.go`:

```go
package cmd

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func TestLeagueContexts_SkipsCompleteAndSortsByName(t *testing.T) {
	in := []sleeper.League{
		{LeagueID: "3", Name: "Zeta", Status: "in_season", Settings: map[string]int{"type": 0}},
		{LeagueID: "1", Name: "Done", Status: "complete", Settings: map[string]int{"type": 2}},
		{LeagueID: "2", Name: "Alpha", Status: "pre_draft", Settings: map[string]int{"type": 2}, RosterPositions: []string{"SUPER_FLEX"}},
	}
	got := leagueContexts(in, nil)
	if len(got) != 2 || got[0].League.Name != "Alpha" || got[1].League.Name != "Zeta" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Profile.Kind != dynasty.KindDynasty || !got[0].Profile.Superflex {
		t.Errorf("profile not derived: %+v", got[0].Profile)
	}
}

func TestLeagueContexts_AppliesOverrides(t *testing.T) {
	in := []sleeper.League{{LeagueID: "9", Name: "K", Status: "in_season", Settings: map[string]int{"type": 1}}}
	got := leagueContexts(in, map[string]string{"9": "non_sf_redraft"})
	if got[0].Profile.Format != "non_sf_redraft" || got[0].Profile.FormatSource != "override" {
		t.Errorf("override not applied: %+v", got[0].Profile)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run TestLeagueContexts -v`
Expected: FAIL — `undefined: leagueContexts`.

- [ ] **Step 3: Implement discovery**

Create `cmd/football_leagues.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// leagueContext is one discovered league plus everything the multi-league
// football jobs derive from it once per run.
type leagueContext struct {
	League  sleeper.League
	Profile dynasty.LeagueProfile
}

// leagueContexts filters and orders the leagues a discovery returned and
// derives each profile. Pure, so the filter and the sort are testable without
// a Sleeper client.
//
// A "complete" league is skipped: Sleeper rolls a dynasty league into a NEW
// league id for the next season, so offseason activity lives there, and the
// finished one only accumulates nothing. Order is by name then id so the run
// output and the alert order are stable across runs.
func leagueContexts(leagues []sleeper.League, overrides map[string]string) []leagueContext {
	out := make([]leagueContext, 0, len(leagues))
	for _, lg := range leagues {
		if lg.Status == "complete" {
			continue
		}
		out = append(out, leagueContext{League: lg, Profile: dynasty.DeriveProfile(lg, overrides)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].League.Name != out[j].League.Name {
			return out[i].League.Name < out[j].League.Name
		}
		return out[i].League.LeagueID < out[j].League.LeagueID
	})
	return out
}

// discoverLeagues lists the operator's NFL leagues for season and prints one
// line per league naming the value column it resolved to and how. The line
// is unconditional: settings.type is undocumented, and a wrong column renders
// as a confident number, so the resolution must be visible on every run.
//
// Zero leagues is an error, not an empty success — a user id with no leagues
// this season is a configuration fault (wrong id, wrong season) and a job
// that exits 0 over it would read as a quiet week forever.
func discoverLeagues(ctx context.Context, sc *sleeper.Client, cfg *FootballConfig, season string, out io.Writer) ([]leagueContext, error) {
	if err := cfg.requireSleeperUserID(); err != nil {
		return nil, err
	}
	leagues, err := sc.LeaguesForUser(ctx, cfg.SleeperUserID, "nfl", season)
	if err != nil {
		return nil, fmt.Errorf("sleeper leagues for user %s (%s): %w", cfg.SleeperUserID, season, err)
	}
	lcs := leagueContexts(leagues, cfg.FormatOverrides)
	if len(lcs) == 0 {
		return nil, fmt.Errorf("sleeper user %s has no non-complete NFL leagues for %s (%d returned)", cfg.SleeperUserID, season, len(leagues))
	}
	for _, lc := range lcs {
		p := lc.Profile
		fmt.Fprintf(out, "football: league %q (%s) kind=%s superflex=%v format=%s (%s)\n",
			p.Name, p.LeagueID, p.Kind, p.Superflex, p.Format, p.FormatSource)
	}
	return lcs, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestLeagueContexts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/football_leagues.go cmd/football_leagues_test.go
git commit -m "feat(cmd): discover the operator's Sleeper leagues and print each profile

Pure filter/sort/derive plus a fetching wrapper; zero leagues is an error."
```

---

### Task 6: `football-trades` loops every discovered league

**Files:**
- Modify: `cmd/football_trades.go` (whole `runFootballTrades`, `tradeRunInputs`, `gradeAndAlertTrades`, `relogFootballTrades`, `formatTradeAlert`)
- Modify: `cmd/football_trades_log_test.go:142-176` (fixture) and add tests
- Modify: `cmd/football_trades_test.go` (`formatTradeAlert` call sites)

**Interfaces:**
- Consumes: `discoverLeagues`, `leagueContext`, `dynasty.LeagueProfile`, `TradeLogRow.InLeague`.
- Produces: `tradeRunInputs.league dynasty.LeagueProfile` (replaces `format string`); `formatTradeAlert(leagueName string, txn, sides, v)`; `loadLeagueTrades(ctx, sc, leagueID string, state *sleeper.NFLState) (trades []sleeper.Transaction, names map[int]string, err error)`; `relogRows(ctx, lc leagueContext, markers lineupapi.ObjectStore, now, trades, players, bundle, names) (rows []dynasty.TradeLogRow, skipped int)`.

- [ ] **Step 1: Update the fixture and write the failing tests**

In `cmd/football_trades_log_test.go`, in `tradeAlertFixture`, replace the line `format: "sf_dynasty",` with:

```go
		league: dynasty.LeagueProfile{LeagueID: "L1", Name: "Palm Trees & Promethazine", Format: "sf_dynasty"},
```

(add the `dynasty` import if the file lacks it). Then append:

```go
func TestGradeAndAlertTrades_TitleCarriesTheLeagueAndRowsCarryItsIdentity(t *testing.T) {
	in, sent := tradeAlertFixture(t)
	res := gradeAndAlertTrades(context.Background(), in)
	if len(*sent) != 1 || !strings.HasPrefix((*sent)[0], "[Palm Trees & Promethazine] Trade:") {
		t.Fatalf("title = %v, want the league name as a prefix", *sent)
	}
	if len(res.LogRows) != 1 {
		t.Fatalf("rows = %d", len(res.LogRows))
	}
	if r := res.LogRows[0]; r.LeagueID != "L1" || r.LeagueName != "Palm Trees & Promethazine" || r.AlertFormat != "sf_dynasty" {
		t.Errorf("row not stamped with its league/format: %+v", r)
	}
}
```

Ensure `context` and `strings` are imported in that test file.

In `cmd/football_trades_test.go`, every `formatTradeAlert(` call gains a leading `"Palm Trees"` argument, and the assertions that check `title` for a prefix of `"Trade:"` change to `"[Palm Trees] Trade:"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run 'TestGradeAndAlertTrades|TestFormatTradeAlert|TestTradeLogWriteFailure' -v`
Expected: FAIL — `unknown field league in struct literal` (compile error).

- [ ] **Step 3: Rewrite the command around the league loop**

Replace `runFootballTrades` through `gradeAndAlertTrades`, and `relogFootballTrades`, in `cmd/football_trades.go` with the following. `writeFootballTradeLog`, `appendFootballTradeLog`, `pollWeeks`, `tradeDateLabel` and `sendFootballTradeAlert` are unchanged.

```go
func runFootballTrades(cmd *cobra.Command, args []string) error {
	cfg, sc, err := initFootball()
	if err != nil {
		return err
	}
	ctx := context.Background()

	state, err := sc.State(ctx)
	if err != nil {
		return fmt.Errorf("sleeper state: %w", err)
	}
	leagues, err := discoverLeagues(ctx, sc, cfg, state.Season, os.Stdout)
	if err != nil {
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

	// Soft: a marker store we cannot build disables dedup, not the alert --
	// same policy as optimize.go's IL-start/GS-floor markers. EXCEPTION:
	// --relog reads the marker store AS ITS DATA SOURCE (the marker body is
	// the only surviving grade-time fact for a pre-log trade), so a relog
	// with no store to read from has nothing to backfill and must hard-fail
	// rather than silently rebuild zero rows.
	markers, err := statestore.FromEnv().FootballTradeMarkers()
	if err != nil {
		if footballTradesRelog {
			return fmt.Errorf("init football-trade markers: %w (--relog reads the marker store as its data source and cannot proceed without one)", err)
		}
		warn("football-trades: init markers: %v (alert will repeat until resolved)", err)
		markers = nil
	}
	now := time.Now().UTC()

	// Per-league isolation, cmd/archive.go's per-source pattern: one league's
	// fetch error is printed and the loop continues, so a Sleeper hiccup on
	// one league never silences the other five; the run still exits non-zero
	// at the end so ops alerting sees it.
	var (
		failed   []string
		logRows  []dynasty.TradeLogRow
		found    int
		totals   tradeRunResult
		relogged int
	)
	for _, lc := range leagues {
		trades, names, err := loadLeagueTrades(ctx, sc, lc.League.LeagueID, state)
		if err != nil {
			warn("football-trades: %s: %v (continuing with the other leagues)", lc.League.Name, err)
			failed = append(failed, lc.League.Name)
			continue
		}
		found += len(trades)

		if footballTradesRelog {
			rows, skipped := relogRows(ctx, lc, markers, now, trades, players, bundle, names)
			if skipped > 0 {
				fmt.Printf("football-trades --relog: %s: skipped %d trade(s) with no dedup marker -- never alerted, so the next poll will capture them properly\n", lc.League.Name, skipped)
			}
			logRows = append(logRows, rows...)
			relogged += len(rows)
			continue
		}

		res := gradeAndAlertTrades(ctx, tradeRunInputs{
			markers: markers,
			now:     now,
			trades:  trades,
			players: players,
			bundle:  bundle,
			names:   names,
			league:  lc.Profile,
			dryRun:  dryRun,
			send:    func(title, body string) error { return sendFootballTradeAlert(ctx, title, body) },
			out:     os.Stdout,
		})
		logRows = append(logRows, res.LogRows...)
		totals.Graded += res.Graded
		totals.Alerted += res.Alerted
		totals.Skipped += res.Skipped
	}

	if footballTradesRelog {
		if err := finishRelog(now, logRows, relogged); err != nil {
			return err
		}
	} else {
		// Soft-fail, and deliberately WITHOUT the `continue` the send path
		// inside the loop uses: this runs AFTER every send, on the rows the
		// loop returned, precisely so a log failure cannot reach back and
		// suppress an alert that already went out. Same precedent as
		// cmd/grade.go's lineup gaps never taking down the grades.
		if len(logRows) > 0 {
			if added, err := writeFootballTradeLog(now, logRows); err != nil {
				warn("football-trades: trade log not written: %v (the alerts still went out, and the markers are set, so the next poll will NOT retry these -- recover with `football-trades --relog`)", err)
			} else {
				fmt.Printf("football-trades: logged %d graded trade(s) to dt=%s\n", added, now.Format("2006-01-02"))
			}
		}
		fmt.Printf("football-trades: %d league(s), %d trades found, %d already alerted, %d graded, %d sent\n",
			len(leagues), found, totals.Skipped, totals.Graded, totals.Alerted)
	}

	if len(failed) > 0 {
		return fmt.Errorf("football-trades: %d of %d league(s) failed: %s", len(failed), len(leagues), strings.Join(failed, ", "))
	}
	return nil
}

// loadLeagueTrades fetches one league's rosters, users and completed trades
// for every polled week, plus the roster-id → team-name map the grader
// labels sides with.
func loadLeagueTrades(ctx context.Context, sc *sleeper.Client, leagueID string, state *sleeper.NFLState) ([]sleeper.Transaction, map[int]string, error) {
	rosters, err := sc.Rosters(ctx, leagueID)
	if err != nil {
		return nil, nil, fmt.Errorf("sleeper rosters: %w", err)
	}
	users, err := sc.Users(ctx, leagueID)
	if err != nil {
		return nil, nil, fmt.Errorf("sleeper users: %w", err)
	}
	var trades []sleeper.Transaction
	for _, week := range pollWeeks(state) {
		txns, err := sc.Transactions(ctx, leagueID, week)
		if err != nil {
			return nil, nil, fmt.Errorf("sleeper transactions (week %d): %w", week, err)
		}
		for _, t := range txns {
			if t.Type == "trade" && t.Status == "complete" {
				trades = append(trades, t)
			}
		}
	}
	return trades, dynasty.TeamNames(rosters, users), nil
}

// tradeRunInputs is everything gradeAndAlertTrades needs for ONE league, with
// the two side effects it cannot own -- sending, and printing -- injected.
//
// The seam exists because runFootballTrades calls initFootball() and
// statestore.FromEnv() internally and so cannot be driven from a test at all.
// Extracting the decision loop is what makes "the alert still goes out when the
// log write fails" an assertion rather than a claim.
type tradeRunInputs struct {
	markers lineupapi.BlobStore
	now     time.Time
	trades  []sleeper.Transaction
	players map[string]sleeper.Player
	bundle  *statsguy.Bundle
	names   map[int]string
	// league supplies the alert's headline format and the identity stamped
	// onto every log row. It replaced a bare format string when the job went
	// multi-league: the column is now a property of the league, not the run.
	league dynasty.LeagueProfile
	dryRun bool
	send   func(title, body string) error
	out    io.Writer
}

// tradeRunResult is what one league's poll decided: the rows to log, and the counts.
type tradeRunResult struct {
	LogRows                  []dynasty.TradeLogRow
	Graded, Alerted, Skipped int
}

// gradeAndAlertTrades runs the check -> send -> mark loop for one league and
// returns the rows worth logging. It never writes the log itself: the caller
// does that after every league's sends have been attempted, which is what
// keeps a log failure off the alerting path.
func gradeAndAlertTrades(ctx context.Context, in tradeRunInputs) tradeRunResult {
	var res tradeRunResult

	// One Marker over the whole poll. in.markers may be nil (the soft-degrade
	// path in runFootballTrades) -- alertmarker.New(nil, ...) is a working
	// configuration: no dedup, alert every run, never silence.
	m := alertmarker.New(in.markers, alertmarker.WithLogf(func(format string, args ...any) {
		warn("football-trades: "+format, args...)
	}))

	for _, txn := range in.trades {
		// check: already alerted for this transaction_id? A read failure
		// reads as unsent and is logged by the Marker itself. Transaction ids
		// are global Sleeper snowflakes, so one marker namespace serves every
		// league without collision.
		if m.Sent(ctx, txn.TransactionID) {
			res.Skipped++
			continue
		}

		sides := dynasty.BuildTradeSides(txn, in.players, in.bundle, in.names, in.league.Format)
		verdict := dynasty.GradeTrade(sides)
		res.Graded++

		title, body := formatTradeAlert(in.league.Name, txn, sides, verdict)
		fmt.Fprintln(in.out, title)
		fmt.Fprintln(in.out, body)

		if in.dryRun {
			continue // do not send, mark or log in a dry-run
		}

		// check -> send -> mark, never claim-then-send (rosterbot-chs). A
		// failed send returns its error with nothing marked, so the next
		// poll retries; a marker-write failure after a successful send
		// degrades to a duplicate alert next poll, never to silence.
		if err := m.Send(txn.TransactionID, []byte(dynasty.TradeVerdictSummary(verdict)), func() error {
			return in.send(title, body)
		}); err != nil {
			warn("football-trades: send failed for %s: %v", txn.TransactionID, err)
			continue // do not mark: a failed send must retry, never go silent
		}
		res.Alerted++

		// Logged only once the alert has actually gone out, so the log means
		// exactly "what was reported" -- a send that failed will retry next
		// poll and be logged then, at that run's prices.
		res.LogRows = append(res.LogRows,
			dynasty.BuildTradeLogRow(in.now, txn, in.players, in.bundle, in.names, in.league.Format).
				InLeague(in.league.LeagueID, in.league.Name))
	}
	return res
}

// relogRows is the per-league half of the one-shot --relog backfill.
//
// Every trade the league had made was already alerted and marked before the
// durable log existed, and the main loop skips a marked trade BEFORE it
// grades -- so a log added to that loop alone would ship permanently empty in
// an offseason league. This rebuilds those rows.
//
// What it can and cannot recover is the whole point. Sleeper retains completed
// trades forever, so the IDENTITY (teams, players, picks, FAAB, date) is exact.
// StatsGuy has no history, so the VALUES are today's, which is a different
// number from what the alert reported -- hence Regraded, which the dashboard
// renders as an explicit badge. The one surviving grade-time fact is the dedup
// marker's body, carried across as OriginalSummary.
//
// It sends nothing and writes no markers: this is a log repair, not an alert
// replay. That is enforced by the signature rather than by care -- markers
// arrives as the read-only lineupapi.ObjectStore, so Publish is not reachable
// from here at all, and no send function is passed in.
func relogRows(ctx context.Context, lc leagueContext, markers lineupapi.ObjectStore, now time.Time, trades []sleeper.Transaction, players map[string]sleeper.Player, bundle *statsguy.Bundle, names map[int]string) (rows []dynasty.TradeLogRow, skipped int) {
	for _, txn := range trades {
		// ONLY ALREADY-ALERTED TRADES. The marker is the evidence that this
		// trade predates the log; without one, the next ordinary poll will
		// alert it and capture a genuine grade-time row. A marker READ
		// FAILURE skips as well: absent evidence is not evidence of absence,
		// and skipping a trade that was alerted leaves one row missing from a
		// backfill that can simply be re-run, while relogging one that was not
		// permanently substitutes today's prices for a capture that had not
		// happened yet.
		body, found, err := markers.Get(ctx, txn.TransactionID)
		if err != nil {
			warn("football-trades --relog: marker read %s: %v (skipping -- cannot confirm it was alerted)", txn.TransactionID, err)
			skipped++
			continue
		}
		if !found {
			skipped++
			continue
		}
		row := dynasty.BuildTradeLogRow(now, txn, players, bundle, names, lc.Profile.Format).
			InLeague(lc.Profile.LeagueID, lc.Profile.Name)
		row.Regraded = true
		row.OriginalSummary = strings.TrimSpace(string(body))
		rows = append(rows, row)
		fmt.Printf("relog [%s] %s (%s): %s\n", lc.Profile.Name, txn.TransactionID, tradeDateLabel(row), row.Verdicts[lc.Profile.Format].Summary)
	}
	return rows, skipped
}

// finishRelog writes the rebuilt rows once, across every league, honouring
// --dry-run.
func finishRelog(now time.Time, rows []dynasty.TradeLogRow, rebuilt int) error {
	if dryRun {
		fmt.Printf("football-trades --relog (dry-run): %d trade(s) rebuilt; not written\n", rebuilt)
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("football-trades --relog: no already-alerted trades to rebuild; nothing written")
		return nil
	}
	added, err := writeFootballTradeLog(now, rows)
	if err != nil {
		return fmt.Errorf("relog trade log: %w", err)
	}
	fmt.Printf("football-trades --relog: %d trade(s) rebuilt, %d written to dt=%s (%d already captured and left untouched)\n",
		rebuilt, added, now.Format("2006-01-02"), rebuilt-added)
	return nil
}
```

Replace `formatTradeAlert`:

```go
// formatTradeAlert renders one graded trade. The league name leads the title
// because the operator's six leagues now share one alert channel, and a
// verdict with no league is a verdict about nothing in particular.
func formatTradeAlert(leagueName string, txn sleeper.Transaction, sides []dynasty.TradeSide, v dynasty.TradeVerdict) (title, body string) {
	var parts []string
	for _, s := range sides {
		var assets []string
		for _, a := range s.Assets {
			assets = append(assets, a.Name)
		}
		parts = append(parts, fmt.Sprintf("%s gets %s", s.TeamName, strings.Join(assets, ", ")))
	}
	body = strings.Join(parts, " | ")

	switch {
	case v.Status == dynasty.TradeFavors:
		title = fmt.Sprintf("Trade: favors %s (+%.0f%%)", v.FavoredTeamName, v.Pct)
	case v.Status == dynasty.TradeDeadEven:
		// Everything priced and both sides carrying real, equal value. The
		// "nothing to compare" wording below would be false here, and would
		// send the operator hunting for missing data that is not missing.
		title = "Trade: dead even, so no verdict"
	case v.UnpricedAssets > 0:
		title = "Trade: too many unpriced assets to grade"
	default:
		// Incomplete with nothing unpriced: everything priced and every side
		// totals zero, or nothing this system models changed hands. Blaming an
		// unpriced asset here would send the operator looking for one that does
		// not exist.
		title = "Trade: nothing to compare, so no verdict"
	}
	if leagueName != "" {
		title = "[" + leagueName + "] " + title
	}
	// Rune-safe fit to Pushover's limit — the old inline slice could cut a
	// multibyte team or player name mid-rune.
	body = pushover.Truncate(body)
	return title, body
}
```

Update the cobra `Long` text's last sentence of paragraph two from "Needs no Fantrax credentials -- only SLEEPER_LEAGUE_ID." to "Needs no Fantrax credentials -- only SLEEPER_LEAGUE_ID (for initFootball) and SLEEPER_USER_ID, from which every non-complete NFL league is discovered and polled."

- [ ] **Step 4: Run the package tests, lint, tidy**

Run: `go test ./cmd/ -run 'Football|Trade|PollWeeks|LeagueContexts' -v && make lint && go mod tidy && git diff --stat go.mod go.sum`
Expected: PASS; lint clean; `go.mod`/`go.sum` unchanged.

- [ ] **Step 5: Idempotency check against the live leagues (operator's `.env` has `SLEEPER_LEAGUE_ID` and `SLEEPER_USER_ID`)**

Run: `go run . football-trades --dry-run 2>&1 | tail -20`
Expected: six `football: league ...` lines, each naming its column; a summary line `6 league(s), N trades found, ...`; exit 0. Every completed trade in the five non-original leagues prints as `graded` (they have no markers yet) — **this is a dry run, so nothing is sent or marked.**

**Before the first real run**, tell the operator how many historical trades the five newly-discovered leagues hold (count the dry-run's title lines):

```bash
go run . football-trades --dry-run 2>&1 | grep -c '^\[[^]]*\] Trade'
```

None of them has a marker, so the first real run alerts every one. Two acceptable outcomes, the operator's choice: accept the one-time burst (each is a real, correctly graded, completed trade, and it never repeats), or file a follow-up bd issue for a `--since <date>` flag and hold the first real run until it lands. Do **not** invent a marker-seeding flag in this task. Record the choice in Task 9's commit message.

- [ ] **Step 6: Commit**

```bash
git add cmd/football_trades.go cmd/football_trades_log_test.go cmd/football_trades_test.go
git commit -m "feat(football-trades): poll every discovered league, not one

Per-league failure isolation (one league's error never silences the rest; the
run still exits non-zero), league-prefixed alert titles, the league stamped on
every log row, and --relog rebuilt per league and written once."
```

---

### Task 7: Dashboard trade cards show the league

**Files:**
- Modify: `web/dashboard/football.js:396-410` (`tradeCard` head)

**Interfaces:**
- Consumes: `TradeLogEntry.leagueName` (Task 3).

- [ ] **Step 1: Add the league chip**

In `tradeCard`, directly after `head.appendChild(el("h2", null, trade.tradeDate || "Date unknown"));` insert:

```js
  // League chip. Blank on rows written before football-trades went
  // multi-league: those render a dash with the reason in the tooltip rather
  // than a guessed name — every such row happens to be from one league, but
  // the log does not say so and the view must not either.
  const league = el("span", "badge badge-info", trade.leagueName || "—");
  if (!trade.leagueName) league.title = "League not recorded — this row predates multi-league polling";
  head.appendChild(league);
```

- [ ] **Step 2: Verify in the browser against a fixture**

The Football tab reads `report/football-trades.json`. Verify without credentials by the bearer-token `_verify.html` route documented in bd memory `rosterbot-local-dashboard-live-verify` (or the stubbed `api.js` variant), pointing `reportFootballTrades()` at a local JSON file containing one entry with `leagueName: "Chopped"` and one without. Confirm: the first card shows a `Chopped` chip, the second a `—` chip with the tooltip, no console errors. Then `resize_window` to mobile and confirm the head row wraps rather than overflowing.

- [ ] **Step 3: Commit**

```bash
git add web/dashboard/football.js
git commit -m "feat(dashboard): football trade cards name their league

Legacy rows render a dash with the reason in the tooltip, never a guess."
```

---

### Task 8: Infra — inject `SLEEPER_USER_ID`; Makefile gate

**Files:**
- Modify: `infra/infra.go:377` (beside `SLEEPER_LEAGUE_ID`)
- Modify: `Makefile:172`

**Interfaces:**
- Consumes: `secret(name)` helper (`infra/infra.go:279`).

- [ ] **Step 1: ⚠ Operator pre-step, BEFORE this lands on main**

Create the SSM parameter in `us-west-1` (the plan never reads it back; it is not sensitive, but the `secret()` helper is the established injection path and keeps it beside `SLEEPER_LEAGUE_ID`):

```bash
aws ssm put-parameter --region us-west-1 --name /rosterbot/SLEEPER_USER_ID --type SecureString --value 738883211463155712
```

A missing parameter fails **every** task launch at provisioning (`ResourceInitializationError`), taking down all scheduled jobs — the same merge-order rule the `APNS_AUTH_KEY` comment records.

- [ ] **Step 2: Add the injection**

In `infra/infra.go`, directly after the `"SLEEPER_LEAGUE_ID": secret("SLEEPER_LEAGUE_ID"),` line:

```go
			// SLEEPER_USER_ID is the operator's Sleeper account id. The
			// multi-league football jobs (football-trades, and the offers and
			// pickups jobs that follow it) discover every league from it;
			// they fail fast without it (requireSleeperUserID). Same
			// merge-order rule as APNS_AUTH_KEY below: the SSM parameter must
			// exist before this deploys.
			"SLEEPER_USER_ID": secret("SLEEPER_USER_ID"),
```

- [ ] **Step 3: Build the nested modules and diff the stack**

Run: `make build-modules && make check-pins`
Expected: clean.

Run (from `infra/`): `cdk diff -c enableBuild=true --method=template 2>&1 | grep -A2 SLEEPER_USER_ID`
Expected: one added `Secrets` entry on the task definition; no other resource changes. (`-c enableBuild=true` is required or the diff falsely shows the CodeBuild project destroyed; `--method=template` because change-set mode hides Modify entries — both from bd memory.)

- [ ] **Step 4: Gate the smoke line**

In `Makefile`, replace the `football-trades --dry-run` recipe line with:

```make
	@echo "=== football-trades --dry-run ===";           if [ -n "$$SLEEPER_LEAGUE_ID" ] && [ -n "$$SLEEPER_USER_ID" ]; then time go run . football-trades --dry-run; else echo "SKIPPED (SLEEPER_LEAGUE_ID or SLEEPER_USER_ID unset)"; fi && echo
```

- [ ] **Step 5: Commit**

```bash
git add infra/infra.go Makefile
git commit -m "infra: inject SLEEPER_USER_ID into the task definition; gate run-all on it"
```

---

### Task 9: Docs — README and `docs/dynasty-football.md`

**Files:**
- Modify: `README.md` (env table near line 576-598; football section near line 373-387)
- Modify: `docs/dynasty-football.md` (after the `initShared`/`initFootball` paragraph; the `football-trades` paragraph)

- [ ] **Step 1: README env table**

After the `DYNASTY_FORMAT` row add:

```markdown
| `SLEEPER_USER_ID` | — | The operator's Sleeper account id. `football-trades` (and the offers/pickups jobs) discover every non-complete NFL league from it and fail fast without it; `football-values` does not read it. |
| `SLEEPER_FORMAT_OVERRIDES` | — | `<league_id>=<format>,...` — pins the StatsGuy column for a league whose format the derivation from Sleeper's undocumented `settings.type` gets wrong. A malformed entry fails config load rather than being skipped. Every run prints the column each league resolved to and whether it was derived or overridden. |
```

Change the `DYNASTY_FORMAT` row's description to: "Which StatsGuy format `football-values`' printed summary reads. The multi-league jobs ignore it — each league's column comes from its own profile (superflex from roster slots, kind from `settings.type`, or `SLEEPER_FORMAT_OVERRIDES`)."

Change line 576's sentence to: "Required for the dynasty football (Sleeper) commands — `football-values`, `football-trades` — and *only* those; no `FANTRAX_*` var is needed to run them: `SLEEPER_LEAGUE_ID`, plus `SLEEPER_USER_ID` for `football-trades`."

In the football `<details>` block, after the `football-trades --relog --dry-run` example line, add a sentence to the paragraph: "`football-trades` polls **every** NFL league the operator's `SLEEPER_USER_ID` belongs to (skipping `complete` ones), grading each in the StatsGuy column its own profile selects and prefixing every alert title with the league name; one league's fetch error is printed and skipped while the others still alert, and the run then exits non-zero. Each logged trade records its league; rows written before multi-league polling stay blank and render as a dash."

- [ ] **Step 2: `docs/dynasty-football.md`**

After the `initShared`/`initFootball` paragraph, add:

```markdown
**Multi-league discovery** (`cmd/football_leagues.go`, `internal/dynasty/profile.go`) — the football jobs other than `football-values` are keyed on `SLEEPER_USER_ID`, the operator's account id, not on a league id: `discoverLeagues` calls `LeaguesForUser(nfl, <state.season>)`, drops `complete` leagues (Sleeper rolls a dynasty league into a new id each season, so the finished one accumulates nothing), sorts by name for stable output, and derives a **`dynasty.LeagueProfile`** per league. `Superflex` comes from `roster_positions` (documented, reliable). `Kind` comes from Sleeper's **undocumented** `settings.type` using the mapping the operator's six leagues returned on 2026-09-21 — `0 redraft, 1 keeper, 2 dynasty, 3 guillotine`, anything else `unknown` — and is therefore a default, not a fact: `SLEEPER_FORMAT_OVERRIDES` (`<league_id>=<format>,...`) replaces the derived `Format` and is labelled `override`, and **every run prints one line per league naming the column and its source**, because a wrong column renders as a confident number rather than a visible miss. `formatFor(kind, superflex)` is the kind → column mapping (dynasty/keeper/unknown → the dynasty column, redraft/guillotine → the redraft column, superflex choosing `sf_*`); the keeper case is a judgement call recorded there. `requireSleeperUserID` is checked by the multi-league jobs only, so a deployment without the parameter breaks them loudly and leaves `football-values` alone.
```

In the `football-trades` paragraph, replace "Needs no Fantrax credentials -- only `SLEEPER_LEAGUE_ID`" wording (if present) and append: "**Since the multi-league change**, the poll loops every discovered league with per-league failure isolation (one league's error is printed and skipped, the others still alert, and the run exits non-zero at the end so `opsalert` sees it), the alert title carries the league name, `tradeRunInputs.league` (a `LeagueProfile`) replaces the old bare format string so the column is a property of the league rather than the run, and every `TradeLogRow` is stamped with `LeagueID`/`LeagueName` via `InLeague` — `omitempty`, with pre-multi-league rows left blank rather than backfilled. Markers still key on `transaction_id`, a global snowflake, so one namespace serves all leagues. `--relog` rebuilds per league (`relogRows`) and writes once (`finishRelog`)."

- [ ] **Step 3: Doc-coverage and full test run**

Run: `go test ./... 2>&1 | tail -15 && make lint`
Expected: all PASS (including `TestEveryInternalPackageHasDocCoverage` and `TestSeasonGate_EveryScheduledCommandIsClassified`); lint clean.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/dynasty-football.md
git commit -m "docs: multi-league football-trades, SLEEPER_USER_ID, SLEEPER_FORMAT_OVERRIDES"
```

---

## Self-Review

**Spec coverage (Section 2 + the Section 1 type change):**
- `SLEEPER_USER_ID` discovery, skip `complete`, season from state → Task 5/6. ✔
- `LeagueProfile` with `Superflex`, `Kind`, `Format`, override env, printed column per run, `formatFor` operator-written → Task 2/5. ✔
- Trades loop unchanged per league, marker key unchanged, title prefix, per-league `Format` replacing `DYNASTY_FORMAT`, `AlertFormat` recorded → Task 6. ✔
- Failure isolation with non-zero exit → Task 6. ✔
- `TradeLogRow` `LeagueID`/`LeagueName` omitempty, legacy blank, dashboard column → Task 3/7. ✔
- Player dump and bundle loaded once per run → Task 6. ✔
- `Transaction.Creator`/`ConsenterIDs` → Task 1. ✔
- Infra env, run-all gate, README, `docs/dynasty-football.md` → Task 8/9. ✔
- Not in this plan by design: `SLEEPER_FORMAT_OVERRIDES` is not wired into the task definition (no league needs it today; it is a plain env var an operator can add to the task if one does).

**Placeholder scan:** Task 2 Step 3 is an explicit operator hand-off with a compilable reference implementation supplied, not a TBD. Task 6 Step 5's historical-burst decision is a concrete either/or for the operator, with the tempting third option (a marker-seeding flag) explicitly ruled out so an executor does not invent it.

**Type consistency:** `tradeRunInputs.league dynasty.LeagueProfile` (Task 6) matches `dynasty.LeagueProfile` (Task 2). `formatTradeAlert(leagueName string, txn, sides, v)` is used with that order in Task 6's code and tests. `TradeLogRow.InLeague(id, name)` (Task 3) is called as `.InLeague(in.league.LeagueID, in.league.Name)` (Task 6). `leagueContext.Profile` (Task 5) is read as `lc.Profile` (Task 6). `FootballConfig.FormatOverrides` (Task 4) is passed as `cfg.FormatOverrides` (Task 5).
