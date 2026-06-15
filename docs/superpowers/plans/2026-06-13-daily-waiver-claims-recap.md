# Daily Waiver-Claims Recap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `claims` command that produces a daily, league-wide recap of processed waiver/FA claims with HKB value gained, Statcast signal tie-in, a value leaderboard, a notable-drops watch, and a persisted audit ledger — no-op when there are no new claims.

**Architecture:** A new `internal/claims` package (mirroring `internal/transactions`) orchestrates: fetch CLAIM/DROP transactions via a new `fantrax.Client.GetRecentTransactions` wrapper → pair CLAIM+DROP rows by `Transaction.ID` into moves → value each side via HKB → enrich added players with `waivers` Statcast signals + a FanGraphs projected-FPG → emit stdout/GHA/Pushover and write `.waivers/claims/<date>.json`. Cursor-based daily run via a new `claims.yml` workflow.

**Tech Stack:** Go, Cobra, `go-fantrax` auth client (CLAIM_DROP transaction view), existing `internal/{fantrax,hkb,waivers,projections,playername,notify}` packages.

---

## File Structure

- Create `internal/fantrax/transactions.go` — `GetRecentTransactions(since)` wrapper + `allTransactions()` cached helper.
- Modify `internal/fantrax/cachekeys.go` — add `keyAllTransactions`.
- Create `internal/claims/types.go` — `Move`, `SidePlayer`, `Ledger`/`LedgerEntry`, `ClaimsClient` interface, `Options`.
- Create `internal/claims/cursor.go` — `loadCursor`/`saveCursor` (`.cache/last-claims.json`).
- Create `internal/claims/group.go` — `BuildMoves(txs, hkbLookup)` pairing + valuation.
- Create `internal/claims/hkb.go` — `buildHKBLookup`, `lookupHKB` (normalized-name join).
- Create `internal/claims/enrich.go` — `EnrichSignals` (Savant) + `projectionLookup` (FanGraphs FPG).
- Create `internal/claims/report.go` — stdout report, leaderboard, drops watch, bid efficiency, GHA summary, Pushover digest.
- Create `internal/claims/ledger.go` — `BuildLedger`, `WriteLedger`.
- Create `internal/claims/run.go` — `Run(ft, today, opts)` orchestration + no-op.
- Create `cmd/claims.go` — command wiring.
- Create `.github/workflows/claims.yml`.
- Modify `Makefile` (`run-all`), `README.md`, `CLAUDE.md`.

Test files alongside each package file (`*_test.go`).

---

## Task 1: Fantrax wrapper — `GetRecentTransactions`

**Files:**
- Modify: `internal/fantrax/cachekeys.go`
- Create: `internal/fantrax/transactions.go`
- Test: `internal/fantrax/transactions_test.go`

- [ ] **Step 1: Add the cache-key constant**

In `internal/fantrax/cachekeys.go`, add inside the `const (...)` block, after `keyAllTrades`:

```go
	keyAllTransactions    = "fantrax-all-transactions"
```

- [ ] **Step 2: Write the failing test**

Create `internal/fantrax/transactions_test.go`:

```go
package fantrax

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"
)

func TestFilterTransactionsSince(t *testing.T) {
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	all := []models.Transaction{
		{ID: "a", Type: "CLAIM", ProcessedDate: base.AddDate(0, 0, -2)},
		{ID: "b", Type: "CLAIM", ProcessedDate: base.AddDate(0, 0, 1)},
		{ID: "c", Type: "DROP", ProcessedDate: base.AddDate(0, 0, 2)},
	}
	got := filterTransactionsSince(all, base)
	if len(got) != 2 {
		t.Fatalf("want 2 transactions after cutoff, got %d", len(got))
	}
	for _, tx := range got {
		if !tx.ProcessedDate.After(base) {
			t.Errorf("transaction %s not after cutoff", tx.ID)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/fantrax/ -run TestFilterTransactionsSince`
Expected: FAIL — `undefined: filterTransactionsSince`.

- [ ] **Step 4: Write the implementation**

Create `internal/fantrax/transactions.go`:

```go
package fantrax

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/models"
)

// GetRecentTransactions returns CLAIM/DROP transactions processed after `since`.
// It wraps the auth client's CLAIM_DROP transaction view (distinct from
// GetAllTrades, which is the TRADE view).
func (c *Client) GetRecentTransactions(since time.Time) ([]models.Transaction, error) {
	all, err := c.allTransactions()
	if err != nil {
		return nil, fmt.Errorf("fetch transactions: %w", err)
	}
	return filterTransactionsSince(all, since), nil
}

func filterTransactionsSince(all []models.Transaction, since time.Time) []models.Transaction {
	var recent []models.Transaction
	for _, tx := range all {
		if tx.ProcessedDate.After(since) {
			recent = append(recent, tx)
		}
	}
	return recent
}

func (c *Client) allTransactions() ([]models.Transaction, error) {
	if c.cacheDir == "" {
		return c.auth.GetAllTransactions()
	}
	fc := cache.New[[]models.Transaction](c.cacheDir, c.todayTTL)
	key := cache.Key(keyAllTransactions, c.leagueID)
	return fc.Get(key, func() ([]models.Transaction, error) {
		return c.auth.GetAllTransactions()
	})
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/fantrax/ -run TestFilterTransactionsSince`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/fantrax/cachekeys.go internal/fantrax/transactions.go internal/fantrax/transactions_test.go
git commit -m "feat(fantrax): wrap CLAIM_DROP transaction view via GetRecentTransactions"
```

---

## Task 2: Claims types + client interface

**Files:**
- Create: `internal/claims/types.go`

- [ ] **Step 1: Write the types**

Create `internal/claims/types.go`:

```go
package claims

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/waivers"
	"github.com/pmurley/go-fantrax/models"
)

// ClaimsClient is the subset of *fantrax.Client the claims report needs.
type ClaimsClient interface {
	GetRecentTransactions(since time.Time) ([]models.Transaction, error)
}

// SidePlayer is one player on one side of a move (added or dropped) with HKB value.
type SidePlayer struct {
	Name     string
	Position string
	Value    int  // HKB value (0 if unranked)
	Ranked   bool // found in HKB
	Rank     int  // HKB overall rank
	Trend30D int  // HKB 30-day value change
	Level    string
	Prospect bool

	// Stats — at most one populated.
	IsPitcher bool
	HasStats  bool
	OPS       float64
	ERA       float64
	WHIP      float64

	// Enrichment (added players only).
	MLBAMID      int
	Signal       waivers.Signal
	ProjectedFPG float64 // 0 = unavailable
}

// Move is one waiver/FA transaction set: a team adds a player, usually dropping one.
type Move struct {
	TxID          string
	TeamName      string
	TeamID        string
	ClaimType     string // "FA" or "WW"
	ProcessedDate time.Time
	BidAmount     string // raw, may be empty
	Priority      string // raw, may be empty
	Added         []SidePlayer
	Dropped       []SidePlayer
}

// NetValue is added HKB value minus dropped HKB value.
func (m Move) NetValue() int {
	var net int
	for _, p := range m.Added {
		net += p.Value
	}
	for _, p := range m.Dropped {
		net -= p.Value
	}
	return net
}

// Options configures a claims run.
type Options struct {
	CacheDir         string // defaults to ".cache"
	DryRun           bool
	NoSignals        bool
	Since            time.Time // zero = use cursor
	DropsMin         int       // notable-drops HKB threshold
	PushoverUserKey  string
	PushoverAPIToken string
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/claims/`
Expected: builds (package has no other files yet, so this just type-checks `types.go`).

- [ ] **Step 3: Commit**

```bash
git add internal/claims/types.go
git commit -m "feat(claims): add Move/SidePlayer/Ledger types and ClaimsClient interface"
```

---

## Task 3: HKB lookup join

**Files:**
- Create: `internal/claims/hkb.go`
- Test: `internal/claims/hkb_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claims/hkb_test.go`:

```go
package claims

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

func TestLookupHKB_MatchesNormalizedName(t *testing.T) {
	players := []hkb.Player{
		{Name: "Bobby Witt Jr.", Value: 9000, Rank: 1, ValueChange30Days: 50, Level: "MLB",
			HitterStats: &hkb.HitterStats{OPS: 0.910}},
	}
	lookup := buildHKBLookup(players)

	sp := lookupHKB("Bobby Witt Jr", "SS", lookup)
	if !sp.Ranked {
		t.Fatal("expected ranked match")
	}
	if sp.Value != 9000 || sp.Rank != 1 || sp.Trend30D != 50 {
		t.Errorf("unexpected HKB fields: %+v", sp)
	}
	if sp.IsPitcher || !sp.HasStats || sp.OPS != 0.910 {
		t.Errorf("expected hitter stats, got %+v", sp)
	}

	miss := lookupHKB("Nobody Here", "OF", lookup)
	if miss.Ranked {
		t.Error("expected unranked for unknown player")
	}
	if miss.Name != "Nobody Here" || miss.Position != "OF" {
		t.Errorf("unranked player should keep name/pos: %+v", miss)
	}
}
```

> Before writing, confirm the `hkb.Player` field names (`Value`, `Rank`, `ValueChange30Days`, `Level`, `Prospect`, `HitterStats.OPS`, `PitcherStats.ERA/WHIP`) by reading `internal/hkb/` — they match the usage in `internal/transactions/transactions.go:390-416`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claims/ -run TestLookupHKB`
Expected: FAIL — `undefined: buildHKBLookup`.

- [ ] **Step 3: Write the implementation**

Create `internal/claims/hkb.go`:

```go
package claims

import (
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

func buildHKBLookup(players []hkb.Player) map[string]hkb.Player {
	m := make(map[string]hkb.Player, len(players))
	for _, p := range players {
		m[playername.Normalize(p.Name)] = p
	}
	return m
}

// lookupHKB builds a SidePlayer for `name`, enriching with HKB data when found.
func lookupHKB(name, position string, lookup map[string]hkb.Player) SidePlayer {
	sp := SidePlayer{Name: name, Position: position}
	p, ok := lookup[playername.Normalize(name)]
	if !ok {
		return sp
	}
	sp.Ranked = true
	sp.Value = p.Value
	sp.Rank = p.Rank
	sp.Trend30D = p.ValueChange30Days
	sp.Level = p.Level
	sp.Prospect = p.Prospect
	if p.PitcherStats != nil {
		sp.IsPitcher = true
		sp.HasStats = true
		sp.ERA = p.PitcherStats.ERA
		sp.WHIP = p.PitcherStats.WHIP
	} else if p.HitterStats != nil {
		sp.HasStats = true
		sp.OPS = p.HitterStats.OPS
	}
	return sp
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claims/ -run TestLookupHKB`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claims/hkb.go internal/claims/hkb_test.go
git commit -m "feat(claims): HKB normalized-name lookup producing SidePlayer"
```

---

## Task 4: Pair CLAIM/DROP rows into moves

**Files:**
- Create: `internal/claims/group.go`
- Test: `internal/claims/group_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claims/group_test.go`:

```go
package claims

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/pmurley/go-fantrax/models"
)

func TestBuildMoves_PairsClaimAndDropByTxID(t *testing.T) {
	d := time.Date(2026, 6, 12, 18, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{ID: "set1", Type: "CLAIM", ClaimType: "WW", TeamName: "Aces", TeamID: "t1",
			PlayerName: "Added Guy", PlayerPosition: "OF", BidAmount: "12", Priority: "3", ProcessedDate: d},
		{ID: "set1", Type: "DROP", TeamName: "Aces", TeamID: "t1",
			PlayerName: "Dropped Guy", PlayerPosition: "SP", ProcessedDate: d},
		{ID: "set2", Type: "CLAIM", ClaimType: "FA", TeamName: "Bandits", TeamID: "t2",
			PlayerName: "Solo Add", PlayerPosition: "1B", ProcessedDate: d},
	}
	lookup := buildHKBLookup([]hkb.Player{
		{Name: "Added Guy", Value: 3000},
		{Name: "Dropped Guy", Value: 1000},
	})

	moves := BuildMoves(txs, lookup)
	if len(moves) != 2 {
		t.Fatalf("want 2 moves, got %d", len(moves))
	}

	// Moves are sorted by NetValue desc; set1 = 3000-1000 = 2000 leads.
	m := moves[0]
	if m.TeamName != "Aces" || m.ClaimType != "WW" || m.BidAmount != "12" {
		t.Errorf("unexpected move metadata: %+v", m)
	}
	if len(m.Added) != 1 || len(m.Dropped) != 1 {
		t.Fatalf("want 1 add + 1 drop, got %d/%d", len(m.Added), len(m.Dropped))
	}
	if m.NetValue() != 2000 {
		t.Errorf("want net 2000, got %d", m.NetValue())
	}

	// set2 is a bare add (no drop).
	if len(moves[1].Dropped) != 0 || moves[1].NetValue() != 0 {
		t.Errorf("bare add should have no drops and net 0: %+v", moves[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claims/ -run TestBuildMoves`
Expected: FAIL — `undefined: BuildMoves`.

- [ ] **Step 3: Write the implementation**

Create `internal/claims/group.go`:

```go
package claims

import (
	"sort"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/pmurley/go-fantrax/models"
)

// BuildMoves groups CLAIM/DROP transactions by transaction-set ID (Transaction.ID),
// valuing each added/dropped player via HKB. TRADE rows are ignored. The result
// is sorted by net value descending, ties broken by team then first added name
// for determinism.
func BuildMoves(txs []models.Transaction, hkbLookup map[string]hkb.Player) []Move {
	byID := map[string]*Move{}
	var order []string

	for _, tx := range txs {
		if tx.Type != "CLAIM" && tx.Type != "DROP" {
			continue
		}
		m, ok := byID[tx.ID]
		if !ok {
			m = &Move{TxID: tx.ID, TeamName: tx.TeamName, TeamID: tx.TeamID, ProcessedDate: tx.ProcessedDate}
			byID[tx.ID] = m
			order = append(order, tx.ID)
		}
		// Team/date may only appear on the group's first (rowspan) row.
		if m.TeamName == "" {
			m.TeamName, m.TeamID = tx.TeamName, tx.TeamID
		}
		if m.ProcessedDate.IsZero() {
			m.ProcessedDate = tx.ProcessedDate
		}
		sp := lookupHKB(tx.PlayerName, tx.PlayerPosition, hkbLookup)
		switch tx.Type {
		case "CLAIM":
			if tx.ClaimType != "" {
				m.ClaimType = tx.ClaimType
			}
			if tx.BidAmount != "" {
				m.BidAmount = tx.BidAmount
			}
			if tx.Priority != "" {
				m.Priority = tx.Priority
			}
			m.Added = append(m.Added, sp)
		case "DROP":
			m.Dropped = append(m.Dropped, sp)
		}
	}

	moves := make([]Move, 0, len(order))
	for _, id := range order {
		moves = append(moves, *byID[id])
	}
	sort.SliceStable(moves, func(i, j int) bool {
		if moves[i].NetValue() != moves[j].NetValue() {
			return moves[i].NetValue() > moves[j].NetValue()
		}
		if moves[i].TeamName != moves[j].TeamName {
			return moves[i].TeamName < moves[j].TeamName
		}
		return firstAddedName(moves[i]) < firstAddedName(moves[j])
	})
	return moves
}

func firstAddedName(m Move) string {
	if len(m.Added) > 0 {
		return m.Added[0].Name
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claims/ -run TestBuildMoves`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claims/group.go internal/claims/group_test.go
git commit -m "feat(claims): pair CLAIM/DROP rows into HKB-valued moves"
```

---

## Task 5: Cursor persistence

**Files:**
- Create: `internal/claims/cursor.go`
- Test: `internal/claims/cursor_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claims/cursor_test.go`:

```go
package claims

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-claims.json")

	if got := loadCursor(path); !got.IsZero() {
		t.Fatalf("missing cursor file should yield zero time, got %v", got)
	}

	want := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	if err := saveCursor(path, want); err != nil {
		t.Fatalf("saveCursor: %v", err)
	}
	if got := loadCursor(path); !got.Equal(want) {
		t.Errorf("want %v, got %v", want, got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claims/ -run TestCursorRoundTrip`
Expected: FAIL — `undefined: loadCursor`.

- [ ] **Step 3: Write the implementation**

Create `internal/claims/cursor.go`:

```go
package claims

import (
	"encoding/json"
	"os"
	"time"
)

// cursorFile is the default path for the claims cursor.
const cursorFile = ".cache/last-claims.json"

type cursor struct {
	LastChecked time.Time `json:"lastChecked"`
}

// loadCursor reads the last-checked timestamp; returns zero time on any error.
func loadCursor(path string) time.Time {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	var c cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return time.Time{}
	}
	return c.LastChecked
}

// saveCursor writes the last-checked timestamp.
func saveCursor(path string, date time.Time) error {
	data, err := json.MarshalIndent(cursor{LastChecked: date}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claims/ -run TestCursorRoundTrip`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claims/cursor.go internal/claims/cursor_test.go
git commit -m "feat(claims): cursor persistence for incremental daily runs"
```

---

## Task 6: Signal + projection enrichment

**Files:**
- Create: `internal/claims/enrich.go`
- Test: `internal/claims/enrich_test.go`

The added players get two enrichments: a Statcast `waivers.Signal` (skippable via `--no-signals`) and a FanGraphs projected FPG. Both are best-effort and mutate `Move.Added` in place. `EnrichSignals` is unit-tested against a fake `SavantBundle`; `projectionLookup` (network) is exercised only via the live `Run` path.

- [ ] **Step 1: Write the failing test**

Create `internal/claims/enrich_test.go`:

```go
package claims

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/waivers"
)

func TestEnrichSignals_TagsHitterBuyLow(t *testing.T) {
	// Build a SavantBundle whose expected stats clear the BUY-LOW hitter rule
	// (xwOBA - wOBA >= 0.030, barrel >= 9, hard-hit >= 42, >= 80 PA).
	const id = 4242
	b := &waivers.SavantBundle{
		HitterExp: map[int]waivers.SavantHitterRow{
			id: {PA: 120, WOBA: 0.300, XwOBA: 0.350},
		},
		HitterSC: map[int]waivers.SavantHitterStatcastRow{
			id: {Barrel: 12, HardHit: 45},
		},
	}
	moves := []Move{
		{Added: []SidePlayer{{Name: "Buy Low Bat", MLBAMID: id, IsPitcher: false}}},
	}

	EnrichSignals(moves, b, waivers.DefaultThresholds())

	if got := moves[0].Added[0].Signal; got != waivers.SignalBuyLow && got != waivers.SignalBoth {
		t.Errorf("expected BUY-LOW (or BOTH), got %q", got.String())
	}
}

func TestEnrichSignals_NilBundleNoop(t *testing.T) {
	moves := []Move{{Added: []SidePlayer{{Name: "x", MLBAMID: 1}}}}
	EnrichSignals(moves, nil, waivers.DefaultThresholds()) // must not panic
	if moves[0].Added[0].Signal != waivers.SignalNone {
		t.Error("nil bundle should leave SignalNone")
	}
}
```

> Confirm the `SavantHitterRow` / `SavantHitterStatcastRow` field names (`PA`, `WOBA`, `XwOBA`, `Barrel`, `HardHit`) against `internal/waivers/types.go` and the rule in `internal/waivers/signals.go:TagHitter` before finalizing the literal — adjust field names/values to whatever actually clears the BUY-LOW guard.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claims/ -run TestEnrichSignals`
Expected: FAIL — `undefined: EnrichSignals`.

- [ ] **Step 3: Write the implementation**

Create `internal/claims/enrich.go`:

```go
package claims

import (
	"github.com/nixon-commits/rosterbot/internal/waivers"
)

// EnrichSignals tags each added player with a Statcast signal using the shared
// waivers tagging rules. No-op when bundle is nil or MLBAMID is unresolved.
func EnrichSignals(moves []Move, bundle *waivers.SavantBundle, th waivers.Thresholds) {
	if bundle == nil {
		return
	}
	for mi := range moves {
		for pi := range moves[mi].Added {
			p := &moves[mi].Added[pi]
			if p.MLBAMID == 0 {
				continue
			}
			var sig waivers.Signal
			if p.IsPitcher {
				sig, _ = waivers.TagPitcher(bundle, p.MLBAMID, th)
			} else {
				sig, _ = waivers.TagHitter(bundle, p.MLBAMID, th)
			}
			p.Signal = sig
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claims/ -run TestEnrichSignals`
Expected: PASS.

- [ ] **Step 5: Add MLBAM-ID resolution + projection helpers (no new test; covered by Run)**

Append to `internal/claims/enrich.go`:

```go
import (
	"strings"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// resolveAddedIDs resolves every added player's name to an MLBAM ID in place.
func resolveAddedIDs(moves []Move, cacheDir string) {
	var names []string
	for _, m := range moves {
		for _, p := range m.Added {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return
	}
	resolved, err := playername.ResolveMLBAMIDs(names, cacheDir)
	if err != nil || resolved == nil {
		return
	}
	for mi := range moves {
		for pi := range moves[mi].Added {
			p := &moves[mi].Added[pi]
			if id, ok := resolved.ByName[playername.Normalize(p.Name)]; ok {
				p.MLBAMID = id
			}
		}
	}
}

// enrichProjections fills ProjectedFPG for added players from FanGraphs
// depthcharts projections, scored with the league weights. Best-effort.
func enrichProjections(moves []Move, weights fantrax.ScoringWeights, cacheDir string, ttl time.Duration) {
	bat, _, err := projections.LoadBattingProjections("depthcharts", cacheDir, ttl)
	if err != nil {
		return
	}
	pit, _, perr := projections.LoadPitcherProjections("depthcharts", cacheDir, ttl)
	for mi := range moves {
		for pi := range moves[mi].Added {
			p := &moves[mi].Added[pi]
			key := playername.Normalize(p.Name)
			if p.IsPitcher {
				if perr != nil {
					continue
				}
				if proj, ok := pit.ByName(key); ok {
					p.ProjectedFPG = projections.PitcherExpectedPtsFromProj(proj, weights)
				}
				continue
			}
			if proj, ok := bat.ByName(key); ok {
				p.ProjectedFPG = projections.ExpectedPtsFromProj(proj, weights)
			}
		}
	}
	_ = strings.TrimSpace // placeholder import guard removed in final edit
}
```

> The projection-lookup-by-name accessor (`bat.ByName`) is illustrative. Before writing, read `internal/projections/fangraphs.go` to find the actual lookup method/field exposed by `*FanGraphsSource` / `*FanGraphsPitcherSource` (it may be a map field or a method with a different name) and adapt `enrichProjections` to the real API. Add the `time` import. Remove the `strings` import + guard line — they are only here to flag that imports must be reconciled when you implement against the real projection API.

- [ ] **Step 6: Verify build + tests pass**

Run: `go test ./internal/claims/ -run TestEnrichSignals && go build ./internal/claims/`
Expected: PASS + clean build.

- [ ] **Step 7: Commit**

```bash
git add internal/claims/enrich.go internal/claims/enrich_test.go
git commit -m "feat(claims): Statcast signal + FanGraphs projection enrichment"
```

---

## Task 7: Report formatting (stdout, leaderboard, drops, bid efficiency)

**Files:**
- Create: `internal/claims/report.go`
- Test: `internal/claims/report_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claims/report_test.go`:

```go
package claims

import (
	"strings"
	"testing"
)

func sampleMoves() []Move {
	return []Move{
		{TeamName: "Aces", ClaimType: "WW", BidAmount: "12",
			Added:   []SidePlayer{{Name: "Added Guy", Position: "OF", Ranked: true, Value: 3000, Rank: 120}},
			Dropped: []SidePlayer{{Name: "Dropped Guy", Position: "SP", Ranked: true, Value: 1000}}},
		{TeamName: "Bandits", ClaimType: "FA",
			Added:   []SidePlayer{{Name: "Reach", Position: "1B", Ranked: true, Value: 200}},
			Dropped: []SidePlayer{{Name: "Good Drop", Position: "OF", Ranked: true, Value: 2500}}},
	}
}

func TestNotableDrops_FiltersByThreshold(t *testing.T) {
	drops := notableDrops(sampleMoves(), 2000)
	if len(drops) != 1 || drops[0].Name != "Good Drop" {
		t.Fatalf("want only Good Drop above 2000, got %+v", drops)
	}
}

func TestFormatReport_IncludesMovesAndLeaderboard(t *testing.T) {
	out := FormatReport(sampleMoves(), 2000, false)
	for _, want := range []string{"Aces", "Bandits", "Added Guy", "Good Drop", "+2,000"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}

func TestFormatPushover_Truncates(t *testing.T) {
	msg := FormatPushover(sampleMoves(), 2000)
	if len(msg) > 1024 {
		t.Errorf("pushover message exceeds 1024 chars: %d", len(msg))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claims/ -run 'TestNotableDrops|TestFormatReport|TestFormatPushover'`
Expected: FAIL — `undefined: notableDrops`.

- [ ] **Step 3: Write the implementation**

Create `internal/claims/report.go`. Reuse the value-formatting style from `internal/transactions/transactions.go` (comma grouping, NBSP indent, ANSI colors). Implement:

```go
package claims

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/waivers"
)

const (
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
	colorDim   = "\033[2m"
	colorReset = "\033[0m"
	nbsp       = " "
)

// FormatReport renders the full stdout report: per-move blocks, the daily value
// leaderboard, and the notable-drops watch.
func FormatReport(moves []Move, dropsMin int, color bool) string {
	var b strings.Builder
	b.WriteString("Waiver Claims Recap\n")
	for _, m := range moves {
		writeMove(&b, m, color)
	}
	writeLeaderboard(&b, moves, color)
	writeDropsWatch(&b, notableDrops(moves, dropsMin))
	return b.String()
}

func writeMove(b *strings.Builder, m Move, color bool) {
	b.WriteString("\n")
	claimLabel := "FA"
	if m.ClaimType == "WW" {
		claimLabel = "Waiver"
	}
	fmt.Fprintf(b, "%s — %s claim", m.TeamName, claimLabel)
	if m.BidAmount != "" {
		fmt.Fprintf(b, " ($%s)", m.BidAmount)
	} else if m.Priority != "" {
		fmt.Fprintf(b, " (priority %s)", m.Priority)
	}
	b.WriteString("\n")
	for _, p := range m.Added {
		fmt.Fprintf(b, "%s+ %s\n", nbsp, formatSidePlayer(p, true))
	}
	for _, p := range m.Dropped {
		fmt.Fprintf(b, "%s- %s\n", nbsp, formatSidePlayer(p, false))
	}
	fmt.Fprintf(b, "%sNet: %s\n", nbsp, formatSignedValue(m.NetValue(), color))
}

func formatSidePlayer(p SidePlayer, added bool) string {
	if !p.Ranked {
		return fmt.Sprintf("%s (%s) — unranked", p.Name, p.Position)
	}
	s := fmt.Sprintf("%s (%s) · #%d · %s", p.Name, p.Position, p.Rank, formatValue(p.Value))
	if added && p.Signal != waivers.SignalNone {
		s += " · " + p.Signal.String()
	}
	if added && p.ProjectedFPG > 0 {
		s += fmt.Sprintf(" · %.1f FPG", p.ProjectedFPG)
	}
	return s
}

func writeLeaderboard(b *strings.Builder, moves []Move, color bool) {
	if len(moves) == 0 {
		return
	}
	// moves arrive sorted by net value desc (BuildMoves guarantees this).
	b.WriteString("\nValue Leaderboard\n")
	for i, m := range moves {
		added := "—"
		if len(m.Added) > 0 {
			added = m.Added[0].Name
		}
		fmt.Fprintf(b, "%s%d. %s (%s) %s\n", nbsp, i+1, added, m.TeamName, formatSignedValue(m.NetValue(), color))
	}
}

// notableDrops returns dropped players whose HKB value exceeds `min`, sorted desc.
func notableDrops(moves []Move, min int) []SidePlayer {
	var out []SidePlayer
	for _, m := range moves {
		for _, p := range m.Dropped {
			if p.Ranked && p.Value > min {
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

func writeDropsWatch(b *strings.Builder, drops []SidePlayer) {
	if len(drops) == 0 {
		return
	}
	b.WriteString("\nNotable Drops (now available)\n")
	for _, p := range drops {
		fmt.Fprintf(b, "%s%s (%s) · %s\n", nbsp, p.Name, p.Position, formatValue(p.Value))
	}
}

// FormatPushover renders a compact digest, truncated to Pushover's 1024-char limit.
func FormatPushover(moves []Move, dropsMin int) string {
	var b strings.Builder
	for _, m := range moves {
		added := "—"
		if len(m.Added) > 0 {
			added = m.Added[0].Name
		}
		fmt.Fprintf(&b, "%s: +%s (%+d)\n", m.TeamName, added, m.NetValue())
	}
	s := b.String()
	if len(s) > 1024 {
		s = s[:1021] + "..."
	}
	return s
}

func formatSignedValue(v int, color bool) string {
	sign := "+"
	mag := v
	if v < 0 {
		sign, mag = "-", -v
	}
	s := sign + formatValue(mag)
	if !color {
		return s
	}
	switch {
	case v > 0:
		return colorGreen + s + colorReset
	case v < 0:
		return colorRed + s + colorReset
	default:
		return colorDim + s + colorReset
	}
}

// formatValue adds comma separators (e.g. 12345 -> "12,345").
func formatValue(v int) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return strings.Join(append([]string{s}, parts...), ",")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claims/ -run 'TestNotableDrops|TestFormatReport|TestFormatPushover'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claims/report.go internal/claims/report_test.go
git commit -m "feat(claims): stdout report, value leaderboard, drops watch, pushover digest"
```

---

## Task 8: Audit ledger

**Files:**
- Create: `internal/claims/ledger.go`
- Test: `internal/claims/ledger_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/claims/ledger_test.go`:

```go
package claims

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/waivers"
)

func TestBuildAndWriteLedger(t *testing.T) {
	day := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	moves := []Move{
		{TeamName: "Aces", TeamID: "t1", ClaimType: "WW", BidAmount: "12", Priority: "3",
			Added:   []SidePlayer{{Name: "Added Guy", Position: "OF", MLBAMID: 99, Value: 3000, Rank: 120, Signal: waivers.SignalHot, ProjectedFPG: 4.2}},
			Dropped: []SidePlayer{{Name: "Dropped Guy", Value: 1000}}},
	}
	led := BuildLedger(day, moves)
	if led.Date != "2026-06-12" || len(led.Entries) != 1 {
		t.Fatalf("unexpected ledger: %+v", led)
	}
	e := led.Entries[0]
	if e.Added.Signal != "HOT" || e.Added.ProjectedFPG != 4.2 || e.NetValue != 2000 {
		t.Errorf("unexpected entry: %+v", e)
	}

	dir := t.TempDir()
	if err := WriteLedger(dir, led); err != nil {
		t.Fatalf("WriteLedger: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "2026-06-12.json"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var round Ledger
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(round.Entries) != 1 || round.Entries[0].Team != "Aces" {
		t.Errorf("round-trip mismatch: %+v", round)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claims/ -run TestBuildAndWriteLedger`
Expected: FAIL — `undefined: BuildLedger`.

- [ ] **Step 3: Write the implementation**

Create `internal/claims/ledger.go`:

```go
package claims

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Ledger is the persisted daily audit record of processed claims.
type Ledger struct {
	Date        string        `json:"date"`
	GeneratedAt time.Time     `json:"generated_at"`
	Entries     []LedgerEntry `json:"entries"`
}

type LedgerEntry struct {
	Team      string       `json:"team"`
	TeamID    string       `json:"team_id"`
	ClaimType string       `json:"claim_type"`
	Added     LedgerPlayer `json:"added"`
	Dropped   *LedgerPlayer `json:"dropped,omitempty"`
	NetValue  int          `json:"net_value"`
	BidAmount string       `json:"bid_amount,omitempty"`
	Priority  string       `json:"priority,omitempty"`
}

type LedgerPlayer struct {
	Name         string  `json:"name"`
	Pos          string  `json:"pos"`
	MLBAMID      int     `json:"mlbam_id,omitempty"`
	HKBValue     int     `json:"hkb_value"`
	HKBRank      int     `json:"hkb_rank,omitempty"`
	Signal       string  `json:"signal,omitempty"`
	ProjectedFPG float64 `json:"projected_pts_per_game,omitempty"`
}

// BuildLedger flattens moves into ledger entries (one per added player).
func BuildLedger(day time.Time, moves []Move) Ledger {
	led := Ledger{Date: day.Format("2006-01-02"), GeneratedAt: time.Now().UTC()}
	for _, m := range moves {
		var dropped *LedgerPlayer
		if len(m.Dropped) > 0 {
			d := ledgerPlayer(m.Dropped[0])
			dropped = &d
		}
		// One entry per added player; net value attributed to the move.
		for _, a := range m.Added {
			led.Entries = append(led.Entries, LedgerEntry{
				Team:      m.TeamName,
				TeamID:    m.TeamID,
				ClaimType: m.ClaimType,
				Added:     ledgerPlayer(a),
				Dropped:   dropped,
				NetValue:  m.NetValue(),
				BidAmount: m.BidAmount,
				Priority:  m.Priority,
			})
		}
	}
	return led
}

func ledgerPlayer(p SidePlayer) LedgerPlayer {
	return LedgerPlayer{
		Name: p.Name, Pos: p.Position, MLBAMID: p.MLBAMID,
		HKBValue: p.Value, HKBRank: p.Rank,
		Signal: p.Signal.String(), ProjectedFPG: p.ProjectedFPG,
	}
}

// WriteLedger writes the ledger to <dir>/<date>.json, creating dir as needed.
func WriteLedger(dir string, led Ledger) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(led, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, led.Date+".json"), data, 0o644)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claims/ -run TestBuildAndWriteLedger`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claims/ledger.go internal/claims/ledger_test.go
git commit -m "feat(claims): persisted daily audit ledger"
```

---

## Task 9: Run orchestration + no-op

**Files:**
- Create: `internal/claims/run.go`
- Test: `internal/claims/run_test.go`

- [ ] **Step 1: Write the failing test (no-op path)**

Create `internal/claims/run_test.go`:

```go
package claims

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"
)

type fakeClient struct{ txs []models.Transaction }

func (f fakeClient) GetRecentTransactions(since time.Time) ([]models.Transaction, error) {
	return f.txs, nil
}

func TestRun_NoClaimsIsNoop(t *testing.T) {
	dir := t.TempDir()
	ledgerDir := filepath.Join(dir, "claims")
	today := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	opts := Options{
		CacheDir:  dir,
		DryRun:    true,
		NoSignals: true,
		Since:     today.AddDate(0, 0, -1),
		LedgerDir: ledgerDir,
		CursorPath: filepath.Join(dir, "last-claims.json"),
	}
	if err := Run(fakeClient{txs: nil}, today, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// No-op: no ledger file written.
	if _, err := os.Stat(ledgerDir); !os.IsNotExist(err) {
		t.Errorf("expected no ledger dir on no-op, stat err = %v", err)
	}
	// Cursor still advances.
	if loadCursor(opts.CursorPath).IsZero() {
		t.Error("cursor should advance even on no-op")
	}
}
```

> This test requires `Options` to grow `LedgerDir` and `CursorPath` fields (for hermetic testing). Add them in Step 3 and also to `internal/claims/types.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claims/ -run TestRun_NoClaims`
Expected: FAIL — `undefined: Run` / unknown fields `LedgerDir`,`CursorPath`.

- [ ] **Step 3: Extend Options, then write Run**

In `internal/claims/types.go`, add to `Options`:

```go
	LedgerDir  string // defaults to ".waivers/claims"
	CursorPath string // defaults to ".cache/last-claims.json"
```

Create `internal/claims/run.go`:

```go
package claims

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/waivers"
)

// projectionTTL matches the FanGraphs 12h cache cadence used elsewhere.
const projectionTTL = 12 * time.Hour

// WeightsProvider lets Run fetch league scoring weights for projection scoring.
// Satisfied by *fantrax.Client.
type WeightsProvider interface {
	GetScoringWeights() (fantrax.ScoringWeights, error)
}

// Run fetches claims since the cursor, builds the recap, emits output, and
// writes the audit ledger. No-op (early return) when there are no new claims.
func Run(ft ClaimsClient, today time.Time, opts Options) error {
	if opts.CacheDir == "" {
		opts.CacheDir = ".cache"
	}
	if opts.LedgerDir == "" {
		opts.LedgerDir = ".waivers/claims"
	}
	if opts.CursorPath == "" {
		opts.CursorPath = cursorFile
	}
	if opts.DropsMin == 0 {
		opts.DropsMin = 2000
	}

	since := opts.Since
	if since.IsZero() {
		since = loadCursor(opts.CursorPath)
		if since.IsZero() {
			since = today.AddDate(0, 0, -3)
		}
	}

	txs, err := ft.GetRecentTransactions(since)
	if err != nil {
		return fmt.Errorf("get recent transactions: %w", err)
	}

	players, err := hkb.GetPlayers(opts.CacheDir)
	if err != nil {
		return fmt.Errorf("get HKB players: %w", err)
	}
	moves := BuildMoves(txs, buildHKBLookup(players))

	// No-op: nothing processed since the cursor. Advance cursor and return.
	if len(moves) == 0 {
		log.Println("No waiver claims processed since last run.")
		if err := saveCursor(opts.CursorPath, today); err != nil {
			log.Printf("WARNING: failed to save claims cursor: %v", err)
		}
		return nil
	}

	// Enrichment: MLBAM IDs, Statcast signals, projections (best-effort).
	resolveAddedIDs(moves, opts.CacheDir)
	if !opts.NoSignals {
		ttl := projectionTTL
		if opts.NoCacheTTL() {
			ttl = 0
		}
		if bundle, berr := waivers.LoadSavant(opts.CacheDir, today.Year(), today, ttl); berr == nil {
			EnrichSignals(moves, bundle, waivers.DefaultThresholds())
		} else {
			log.Printf("WARNING: signal enrichment skipped: %v", berr)
		}
	}
	if wp, ok := ft.(WeightsProvider); ok {
		if weights, werr := wp.GetScoringWeights(); werr == nil {
			enrichProjections(moves, weights, opts.CacheDir, projectionTTL)
		}
	}

	// Output.
	fmt.Println(FormatReport(moves, opts.DropsMin, true))
	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		if f, ferr := os.OpenFile(summaryPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); ferr == nil {
			fmt.Fprint(f, FormatReport(moves, opts.DropsMin, false))
			f.Close()
		}
	}

	// Ledger.
	if !opts.DryRun {
		if err := WriteLedger(opts.LedgerDir, BuildLedger(today, moves)); err != nil {
			log.Printf("WARNING: failed to write ledger: %v", err)
		}
	}

	// Pushover.
	if !opts.DryRun && opts.PushoverUserKey != "" && opts.PushoverAPIToken != "" {
		if err := notify.SendPushover(opts.PushoverUserKey, opts.PushoverAPIToken, "Waiver Claims", FormatPushover(moves, opts.DropsMin)); err != nil {
			log.Printf("notification failed: %v", err)
		}
	}

	// Advance cursor last so a mid-run failure re-processes next time.
	if err := saveCursor(opts.CursorPath, today); err != nil {
		log.Printf("WARNING: failed to save claims cursor: %v", err)
	}
	return nil
}
```

> Two reconciliations when implementing:
> 1. `opts.NoCacheTTL()` is a stand-in — drop it and just use `projectionTTL`; signal cache freshness is fine. Remove that helper reference.
> 2. Add the `fantrax` import (used in `WeightsProvider`/`enrichProjections`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claims/ -run TestRun_NoClaims`
Expected: PASS.

- [ ] **Step 5: Add a test for the populated path (ledger written)**

Append to `internal/claims/run_test.go`:

```go
func TestRun_WritesLedgerWhenClaimsExist(t *testing.T) {
	dir := t.TempDir()
	ledgerDir := filepath.Join(dir, "claims")
	today := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	d := today.Add(-2 * time.Hour)

	// HKB lookup will miss (no network in test) — players are unranked, which is fine.
	txs := []models.Transaction{
		{ID: "s1", Type: "CLAIM", ClaimType: "FA", TeamName: "Aces", TeamID: "t1",
			PlayerName: "Some Guy", PlayerPosition: "OF", ProcessedDate: d},
	}
	opts := Options{
		CacheDir: dir, DryRun: false, NoSignals: true,
		Since:      today.AddDate(0, 0, -1),
		LedgerDir:  ledgerDir,
		CursorPath: filepath.Join(dir, "last-claims.json"),
	}
	if err := Run(fakeClient{txs: txs}, today, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ledgerDir, "2026-06-12.json")); err != nil {
		t.Errorf("expected ledger file written: %v", err)
	}
}
```

> `hkb.GetPlayers(dir)` with an empty cache dir performs a network fetch. To keep this test hermetic, the implementer must confirm `hkb.GetPlayers` behavior with `cacheDir` set but empty — if it hits the network, either (a) pre-seed a cache file in `dir`, or (b) inject the HKB lookup via an optional `Options.HKBPlayers []hkb.Player` field that `Run` uses when non-nil (preferred — add this field and short-circuit the fetch). Implement option (b): add `HKBPlayers []hkb.Player` to `Options`, and in `Run` use it directly when set instead of calling `hkb.GetPlayers`. Update this test to pass `HKBPlayers: []hkb.Player{{Name: "Some Guy"}}`.

- [ ] **Step 6: Run full package tests**

Run: `go test ./internal/claims/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/claims/run.go internal/claims/types.go internal/claims/run_test.go
git commit -m "feat(claims): Run orchestration with no-op, enrichment, ledger, pushover"
```

---

## Task 10: CLI command wiring

**Files:**
- Create: `cmd/claims.go`
- Test: manual (build + dry-run).

- [ ] **Step 1: Write the command**

Create `cmd/claims.go` (model on `cmd/transactions.go` and `cmd/waivers.go`):

```go
package cmd

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/claims"
	"github.com/spf13/cobra"
)

var (
	claimsNoSignals bool
	claimsDropsMin  int
	claimsSince     string
)

var claimsCmd = &cobra.Command{
	Use:   "claims",
	Short: "Daily league-wide recap of processed waiver/FA claims",
	RunE:  runClaims,
}

func init() {
	claimsCmd.Flags().BoolVar(&claimsNoSignals, "no-signals", false, "skip the Statcast signal tie-in (faster)")
	claimsCmd.Flags().IntVar(&claimsDropsMin, "drops-min", 2000, "min HKB value for a dropped player to appear in the drops watch")
	claimsCmd.Flags().StringVar(&claimsSince, "since", "", "override cursor; report claims processed after YYYY-MM-DD")
	rootCmd.AddCommand(claimsCmd)
}

func runClaims(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	var since time.Time
	if claimsSince != "" {
		since, err = time.Parse("2006-01-02", claimsSince)
		if err != nil {
			return err
		}
	}

	opts := claims.Options{
		CacheDir:         ".cache",
		DryRun:           cfg.DryRun,
		NoSignals:        claimsNoSignals,
		Since:            since,
		DropsMin:         claimsDropsMin,
		PushoverUserKey:  cfg.PushoverUserKey,
		PushoverAPIToken: cfg.PushoverAPIToken,
	}
	return claims.Run(ft, today, opts)
}
```

> Confirm `todayET()` and `initApp` signatures against `cmd/waivers.go` (they match). `ft` is `*fantrax.Client`, which satisfies both `claims.ClaimsClient` and `claims.WeightsProvider`.

- [ ] **Step 2: Build and run dry-run**

Run:
```bash
go build -o rosterbot . && ./rosterbot claims --dry-run --no-signals --since 2026-06-01
```
Expected: prints a report or "No waiver claims processed since last run." with no panic. (Requires `.env` creds; if unavailable, just confirm `go build` succeeds and the command is registered via `./rosterbot claims --help`.)

- [ ] **Step 3: Verify vet + tidy**

Run: `go vet ./... && go mod tidy`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/claims.go go.mod go.sum
git commit -m "feat(cmd): wire claims command"
```

---

## Task 11: GHA workflow + docs + smoke target

**Files:**
- Create: `.github/workflows/claims.yml`
- Modify: `Makefile`, `README.md`, `CLAUDE.md`

- [ ] **Step 1: Create the workflow**

Model on `.github/workflows/waivers.yml`. Create `.github/workflows/claims.yml`:

```yaml
name: Waiver Claims Recap

on:
  schedule:
    - cron: '0 14 * * *' # 2pm UTC, after waivers.yml (1pm) warms the Savant cache
  workflow_dispatch:
    inputs:
      dry_run:
        description: 'Run without sending Pushover / writing ledger'
        type: boolean
        default: false

jobs:
  claims:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: browser-actions/setup-chrome@v2
      - name: Restore fantrax session
        uses: actions/cache@v4
        with:
          path: .fantrax-cache
          key: fantrax-session-${{ github.run_id }}
          restore-keys: fantrax-session-
      - name: Restore data caches
        uses: actions/cache@v4
        with:
          path: |
            .cache
            .waivers/claims
          key: claims-${{ github.run_id }}
          restore-keys: |
            claims-
            projections-
      - name: Run claims recap
        env:
          FANTRAX_USERNAME: ${{ secrets.FANTRAX_USERNAME }}
          FANTRAX_PASSWORD: ${{ secrets.FANTRAX_PASSWORD }}
          FANTRAX_LEAGUE_ID: ${{ secrets.FANTRAX_LEAGUE_ID }}
          FANTRAX_TEAM_ID: ${{ secrets.FANTRAX_TEAM_ID }}
          FANTRAX_IL_SLOTS: ${{ secrets.FANTRAX_IL_SLOTS }}
          FANTRAX_MINORS_SLOTS: ${{ secrets.FANTRAX_MINORS_SLOTS }}
          PUSHOVER_USER_KEY: ${{ secrets.PUSHOVER_USER_KEY }}
          PUSHOVER_API_TOKEN: ${{ secrets.PUSHOVER_API_TOKEN }}
        run: |
          if [ "${{ inputs.dry_run }}" = "true" ]; then
            go run . claims --dry-run
          else
            go run . claims
          fi
```

> Verify the exact step shapes (cache key prefixes, secret names, chrome setup) against `.github/workflows/waivers.yml` and copy its conventions precisely — especially the cache-save step if that repo splits restore/save.

- [ ] **Step 2: Append to the Makefile `run-all` recipe**

In `Makefile`, add a line to the `run-all` target alongside the other command smoke lines:

```make
	time go run . claims --dry-run --no-signals
```

- [ ] **Step 3: Update README.md**

Add a `claims` entry to the commands section and a workflow note, matching the style of the existing `waivers`/`transactions` entries. Include the flags (`--dry-run`, `--no-signals`, `--drops-min`, `--since`).

- [ ] **Step 4: Update CLAUDE.md**

- Add an `internal/claims` package description (after `internal/waivers`).
- Add `keyAllTransactions` (`fantrax-all-transactions-<leagueID>`, `todayTTL`) to the caching table.
- Add the `.waivers/claims/<date>.json` ledger dir and `.cache/last-claims.json` cursor to the relevant sections.
- Add the `claims.yml` workflow to the GHA section.

- [ ] **Step 5: Smoke + full test**

Run:
```bash
go build ./... && go test ./internal/... && go vet ./...
```
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/claims.yml Makefile README.md CLAUDE.md
git commit -m "ci(claims): daily workflow + run-all smoke + docs"
```

---

## Self-Review

**Spec coverage:**
- League-wide scope → Task 4 (`BuildMoves` groups all teams). ✓
- Rich text + GHA summary + Pushover → Task 7 + Task 9. ✓
- No-op on empty → Task 9 (`len(moves) == 0` early return, cursor still advances). ✓
- Cursor-based daily run → Task 5 + Task 9 + Task 11. ✓
- Net HKB value gained → Task 2 (`Move.NetValue`) + Task 4. ✓
- Daily value leaderboard → Task 7 (`writeLeaderboard`). ✓
- Statcast signal tie-in → Task 6 (`EnrichSignals`, reuses `waivers`). ✓
- Notable drops watch → Task 7 (`notableDrops`/`writeDropsWatch`, `--drops-min`). ✓
- Bid/FAAB efficiency → Task 7 (`writeMove` shows bid or priority). ✓
- Audit ledger (GHA-cache only) → Task 8 + Task 11 cache paths. ✓
- `GetRecentTransactions` wrapper → Task 1. ✓
- Tests hermetic / no creds → injected `HKBPlayers`, fake client, fake SavantBundle. ✓

**Known reconciliations the implementer MUST resolve against real APIs (flagged inline):**
1. `hkb.Player` field names (Task 3) — verify against `internal/hkb/`.
2. `SavantBundle`/`SavantHitterRow` field names + BUY-LOW rule values (Task 6) — verify against `internal/waivers/{types,signals}.go`.
3. FanGraphs projection lookup-by-name API (`bat.ByName`) (Task 6) — verify against `internal/projections/`; adapt `enrichProjections`.
4. `Options.HKBPlayers` injection field (Task 9) — add for hermetic testing; `Run` uses it when non-nil.
5. Remove the `opts.NoCacheTTL()` stand-in and `strings` placeholder guard (Tasks 6, 9).
6. `waivers.yml` cache/step conventions (Task 11) — copy precisely.

**Type consistency:** `Move`, `SidePlayer`, `Options`, `Ledger`/`LedgerEntry`/`LedgerPlayer`, `ClaimsClient`, `WeightsProvider` are defined once (Tasks 2, 8, 9) and referenced consistently. `Move.NetValue()` used identically in group/report/ledger.
