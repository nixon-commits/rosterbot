# Roster-Shape Line Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a roster-shape block to the `backtest` report stating, per side, the share of deployable projected value the roster actually fielded, with slot counts and the weekly GS cap alongside.

**Architecture:** Two forward-only fields are added to the existing projection snapshot (`Status` per player, `GSLimit` per day) from data `buildSnapshot` already holds and discards. A new pure file `internal/backtest/roster_shape.go` walks those snapshots — a second pass alongside `SummarizeGSGate`, deliberately independent of it — and produces a `RosterShape` value that `cmd/backtest.go` renders to stdout under the existing GS-gate block.

**Tech Stack:** Go, stdlib only. No new dependency, store, schedule, or IAM.

**Spec:** `docs/superpowers/specs/2026-08-06-roster-shape-line-design.md`

## Global Constraints

- Go module `github.com/nixon-commits/rosterbot`. Package under test is `package backtest` (internal test package, so unexported helpers are directly testable).
- `internal/backtest` stays pure over `LoadSnapshot` — no network, no credentials, no new store. All tests run with no env vars set.
- After any code change run `go vet ./...` and `go mod tidy`. `gofmt` and `go vet` also run automatically via PostToolUse hooks on every Edit/Write.
- Do **not** modify `internal/lineuprun/testdata/*.golden`. Those cover `renderDateResult` (board output), not snapshot JSON, and nothing in this plan changes board rendering.
- Doc comments carry the *why*, matching the density already in `internal/backtest/gs_gate_summary.go`. A comment that only restates the code is a defect here.
- Every commit message ends with:
  `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`
- Deployable roster statuses are exactly the strings `"Active"` and `"Reserve"`. The other two values `fantrax.Player.Status` can take are `"Injured Reserve"` and `"Minors"`, both excluded.
- Work happens on branch `feat/rosterbot-hx5-roster-shape` (already created).

---

### Task 1: Persist roster status and the GS cap on the snapshot

The deployability filter needs a player's roster status, and the header line needs the weekly cap. `buildSnapshot` already holds both and throws them away — `sp.Player.Status` is collapsed into the single boolean `WasStarted`, and `dr.pitcherResult.GateReport.Limit` is never read. This task persists them and nothing else.

**Files:**
- Modify: `internal/backtest/backtest.go:104-142` (the `SnapshotPlayer` and `Snapshot` structs)
- Modify: `internal/lineuprun/snapshot.go:45-97` (`buildSnapshot`)
- Test: `internal/lineuprun/snapshot_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `backtest.SnapshotPlayer.Status string` (JSON key `status`) and `backtest.Snapshot.GSLimit int` (JSON key `gs_limit`). Tasks 2 and 3 read both.

- [ ] **Step 1: Write the failing test**

Append to `internal/lineuprun/snapshot_test.go`:

```go
// TestBuildSnapshot_RecordsStatusAndGSCap pins the two fields the roster-shape
// report reads. Status is what separates an IL/Minors player from a healthy
// benched one — without it an injury reads as a structural surplus. GSLimit is
// recorded per day rather than fetched once at report time because Fantrax
// rescales the cap for merged periods, and because the gate runs for today's
// date only (optimize_dates.go nils the budget for every other date), so a
// --matchup pre-write correctly carries no cap at all.
func TestBuildSnapshot_RecordsStatusAndGSCap(t *testing.T) {
	dr := dateResult{
		date: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
		hitterResult: optimizer.Result{
			Scored: []optimizer.ScoredPlayer{
				{
					Player: fantrax.Player{
						ID: "h1", Name: "Healthy Bench", Status: "Reserve",
					},
					ExpectedPts: 4.0, HasGame: true,
				},
				{
					Player: fantrax.Player{
						ID: "h2", Name: "Hurt Bat", Status: "Injured Reserve",
					},
					ExpectedPts: 9.0, HasGame: true,
				},
			},
		},
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{
				{
					Player: fantrax.Player{
						ID: "p1", Name: "Ace", PosShortNames: "SP", Status: "Active",
					},
					ExpectedPts: 16.0, HasGame: true, IsStarter: true,
				},
			},
			GateReport: optimizer.GSGateReport{Limit: 12, Used: 9, Remaining: 3},
		},
	}

	snap := buildSnapshot(dr, "depthcharts-ros", nil, false, false)

	if snap.GSLimit != 12 {
		t.Errorf("GSLimit = %d, want 12", snap.GSLimit)
	}
	if got := snap.Hitters[0].Status; got != "Reserve" {
		t.Errorf("hitter[0].Status = %q, want Reserve", got)
	}
	if got := snap.Hitters[1].Status; got != "Injured Reserve" {
		t.Errorf("hitter[1].Status = %q, want Injured Reserve", got)
	}
	if got := snap.Pitchers[0].Status; got != "Active" {
		t.Errorf("pitcher[0].Status = %q, want Active", got)
	}
}

// TestBuildSnapshot_NoGateLeavesCapUnset pins that a date optimized without a
// GS budget in force records no cap rather than a misleading zero-that-means-
// twelve. optimize_dates.go nils the budget for every non-today date, so this
// is the ordinary state of a --matchup pre-write.
func TestBuildSnapshot_NoGateLeavesCapUnset(t *testing.T) {
	dr := dateResult{
		date: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{
				{
					Player:      fantrax.Player{ID: "p1", PosShortNames: "SP", Status: "Active"},
					ExpectedPts: 10.0, HasGame: true,
				},
			},
		},
	}

	if snap := buildSnapshot(dr, "depthcharts-ros", nil, false, false); snap.GSLimit != 0 {
		t.Errorf("GSLimit = %d, want 0 when no budget was in force", snap.GSLimit)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/lineuprun/ -run 'TestBuildSnapshot_(RecordsStatusAndGSCap|NoGateLeavesCapUnset)' -v`
Expected: FAIL to **compile**, with `snap.GSLimit undefined` and `snap.Hitters[0].Status undefined`.

- [ ] **Step 3: Add the two fields**

In `internal/backtest/backtest.go`, inside `type SnapshotPlayer struct`, after the `Locked` field:

```go
	// Status is the raw Fantrax roster status ("Active", "Reserve",
	// "Injured Reserve", "Minors") at the moment the snapshot was written.
	// WasStarted collapses this to a single boolean, which cannot distinguish
	// a healthy benched player from an injured or minor-league one — so an
	// injury would read as owned-but-not-fielded value, i.e. as a structural
	// roster surplus, which is precisely what the roster-shape report exists
	// to measure. Present only from 2026-08 forward; earlier snapshots read
	// as "" and are excluded from that report as pre-schema rather than
	// silently counted as a fully-fielded day.
	Status string `json:"status,omitempty"`
```

In the same file, inside `type Snapshot struct`, after `PitchersNoData`:

```go
	// GSLimit is the weekly game-start cap in force when this date was
	// optimized, copied from optimizer.GSGateReport.Limit. Recorded per day
	// rather than fetched once at report time because Fantrax rescales the cap
	// whenever a period spans more than one calendar week. Zero means no
	// budget was in force — the ordinary state of a --matchup pre-write, since
	// optimize_dates.go applies the gate to today's date only.
	GSLimit int `json:"gs_limit,omitempty"`
```

- [ ] **Step 4: Populate them in `buildSnapshot`**

In `internal/lineuprun/snapshot.go`, add `GSLimit` to the `backtest.Snapshot` literal:

```go
	snap := backtest.Snapshot{
		Date:             dr.date.Format("2006-01-02"),
		ProjectionSystem: projSystem,
		GeneratedAt:      time.Now().UTC(),
		HittersNoData:    hittersNoData,
		PitchersNoData:   pitchersNoData,
		GSLimit:          dr.pitcherResult.GateReport.Limit,
	}
```

Add `Status: sp.Player.Status,` to the hitter `backtest.SnapshotPlayer` literal (after `Locked`), and the identical line to the pitcher literal (after `Locked`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/lineuprun/ ./internal/backtest/ -v -run 'TestBuildSnapshot'`
Expected: PASS, including the pre-existing `TestBuildSnapshot_MapsRichFields`.

- [ ] **Step 6: Verify nothing else regressed**

Run: `go vet ./... && go test ./internal/...`
Expected: all PASS. The golden board tests are untouched.

- [ ] **Step 7: Commit**

```bash
git add internal/backtest/backtest.go internal/lineuprun/snapshot.go internal/lineuprun/snapshot_test.go
git commit -m "$(cat <<'EOF'
feat(backtest): record roster status and GS cap on projection snapshots

buildSnapshot already held both and discarded them: Status was
collapsed into the WasStarted boolean, and GateReport.Limit was never
read. The roster-shape report needs Status to keep an IL or Minors
player from reading as owned-but-unfielded value, and the cap per day
because Fantrax rescales it for merged periods.

Forward-only, like gs_suppressed before it.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: The deployability model

The core of the feature: which players count as deployable value, and how a side's rate is derived. Kept separate from the window walk so the two regression guards that matter — SP rest days and IL/Minors — are tested against the predicates directly, with no file I/O in the way.

**Files:**
- Create: `internal/backtest/roster_shape.go`
- Test: `internal/backtest/roster_shape_test.go`

**Interfaces:**
- Consumes: `backtest.SnapshotPlayer` including `Status` from Task 1.
- Produces: `SideShape{OwnedPts, FieldedPts float64}` with methods `StrandedPts() float64` and `FieldedRate() (float64, bool)`; unexported predicates `statusIsRosterable(string) bool`, `hitterIsDeployable(SnapshotPlayer) bool`, `pitcherIsDeployable(SnapshotPlayer) bool`. Task 3 calls all of these; Task 4 calls the two `SideShape` methods.

- [ ] **Step 1: Write the failing test**

Create `internal/backtest/roster_shape_test.go`:

```go
package backtest

import (
	"testing"
)

// TestPitcherIsDeployable_ExcludesRestDayStarters is the guard that keeps this
// metric from degenerating into a measurement of rotation cadence.
//
// SnapshotPlayer.ProjPtsPerGame for a pitcher is ScoredPitcher.ExpectedPts,
// which is UNDISCOUNTED — NonStarterSPDiscount (0.05x) is applied downstream,
// only to the local `generic` slice inside OptimizePitcherLineup, and never
// reaches the snapshot. HasGame means only that the pitcher's MLB team plays.
// So a HasGame-based denominator credits an ace his full ~16pt projection on
// every rest day, which against a five-man rotation is four days in five.
func TestPitcherIsDeployable_ExcludesRestDayStarters(t *testing.T) {
	restDayAce := SnapshotPlayer{
		Status: "Active", Role: "SP", IsPitcher: true,
		ProjPtsPerGame: 16.0, HasGame: true, IsStarter: false, GSSuppressed: false,
	}
	if pitcherIsDeployable(restDayAce) {
		t.Error("an SP whose team plays but who is not a probable starter must not count as deployable value")
	}
}

// TestPitcherIsDeployable_IncludesGateSuppressedStart pins that the gate's own
// suppressions land in the pitcher denominator. applyGSGate flips IsStarter to
// false on its way out, so IsStarter||GSSuppressed is what reconstructs the
// pre-gate probable-starter set. This is what makes the roster-shape block
// cohere with the GS-gate block printed directly above it.
func TestPitcherIsDeployable_IncludesGateSuppressedStart(t *testing.T) {
	suppressed := SnapshotPlayer{
		Status: "Active", Role: "SP", IsPitcher: true,
		ProjPtsPerGame: 11.0, HasGame: true, IsStarter: false, GSSuppressed: true,
	}
	if !pitcherIsDeployable(suppressed) {
		t.Error("a start the GS gate declined is owned value the roster could not field")
	}
}

func TestDeployability_TableCases(t *testing.T) {
	tests := []struct {
		name    string
		player  SnapshotPlayer
		hitter  bool // run through hitterIsDeployable rather than pitcherIsDeployable
		want    bool
	}{
		{
			name:   "hitter active with a game",
			player: SnapshotPlayer{Status: "Active", HasGame: true},
			hitter: true, want: true,
		},
		{
			name:   "hitter on the bench with a game is still owned value",
			player: SnapshotPlayer{Status: "Reserve", HasGame: true},
			hitter: true, want: true,
		},
		{
			name:   "hitter whose team is idle",
			player: SnapshotPlayer{Status: "Active", HasGame: false},
			hitter: true, want: false,
		},
		{
			name:   "injured hitter is not a roster shape",
			player: SnapshotPlayer{Status: "Injured Reserve", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "minor-league hitter cannot be fielded",
			player: SnapshotPlayer{Status: "Minors", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "pre-schema hitter has no status to judge",
			player: SnapshotPlayer{Status: "", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "probable starter",
			player: SnapshotPlayer{Status: "Active", Role: "SP", IsStarter: true, HasGame: true},
			want:   true,
		},
		{
			name:   "reliever whose team plays",
			player: SnapshotPlayer{Status: "Reserve", Role: "RP", HasGame: true},
			want:   true,
		},
		{
			name:   "reliever whose team is idle",
			player: SnapshotPlayer{Status: "Active", Role: "RP", HasGame: false},
			want:   false,
		},
		{
			name:   "injured probable starter",
			player: SnapshotPlayer{Status: "Injured Reserve", Role: "SP", IsStarter: true, HasGame: true},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			if tt.hitter {
				got = hitterIsDeployable(tt.player)
			} else {
				got = pitcherIsDeployable(tt.player)
			}
			if got != tt.want {
				t.Errorf("deployable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSideShape_StrandedAndRate(t *testing.T) {
	s := SideShape{OwnedPts: 200, FieldedPts: 150}
	if got := s.StrandedPts(); got != 50 {
		t.Errorf("StrandedPts = %v, want 50", got)
	}
	rate, ok := s.FieldedRate()
	if !ok {
		t.Fatal("FieldedRate should be defined when owned value is positive")
	}
	if rate != 0.75 {
		t.Errorf("FieldedRate = %v, want 0.75", rate)
	}
}

// TestSideShape_ZeroOwnedHasNoRate pins that an empty side reports "undefined"
// rather than 0%. A window in which nothing was deployable is not a window in
// which everything was stranded, and rendering it as 0% would read as a
// catastrophic roster failure.
func TestSideShape_ZeroOwnedHasNoRate(t *testing.T) {
	if _, ok := (SideShape{}).FieldedRate(); ok {
		t.Error("FieldedRate must be undefined when no deployable value exists")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backtest/ -run 'TestPitcherIsDeployable|TestDeployability|TestSideShape' -v`
Expected: FAIL to compile — `undefined: pitcherIsDeployable`, `undefined: hitterIsDeployable`, `undefined: SideShape`.

- [ ] **Step 3: Write the implementation**

Create `internal/backtest/roster_shape.go`:

```go
package backtest

// SideShape holds one role's projected value over a window: what the roster
// could have fielded, and what it did field.
//
// Both terms are restricted to DEPLOYABLE value (see hitterIsDeployable /
// pitcherIsDeployable). That restriction is the whole content of the measure —
// an unrestricted denominator produces a number that looks like roster shape
// and is actually rotation cadence.
type SideShape struct {
	OwnedPts   float64
	FieldedPts float64
}

// StrandedPts is deployable projected value the roster owned and did not field.
//
// It is directly comparable between the two sides — same unit, same window —
// but the two must never be SUMMED, because they are not the same kind of loss.
// A benched hitter starts tomorrow; the pool rotates. A start declined above
// the weekly game-start cap is dead for the week, since GS budget is
// use-it-or-lose-it. This is the same asymmetry GSGateReport.SuppressedPts
// documents as GROSS-not-net.
func (s SideShape) StrandedPts() float64 { return s.OwnedPts - s.FieldedPts }

// FieldedRate is the share of deployable projected value the roster fielded.
// ok is false when there was no deployable value at all, which callers must
// render as "undefined" rather than as 0% — a window in which nothing could be
// fielded is not a window in which everything was stranded.
//
// This is the normalization the whole report rests on: each side is measured
// against its OWN owned value, never against the other side. A roster carrying
// exactly its slot count reads 100% on both sides whether the league fields 13
// hitters or 9, so the gap between the two rates cannot be recovered from the
// league's slot split.
func (s SideShape) FieldedRate() (float64, bool) {
	if s.OwnedPts <= 0 {
		return 0, false
	}
	return s.FieldedPts / s.OwnedPts, true
}

// statusIsRosterable reports whether a roster status describes a player who
// could have been fielded that day. Injured Reserve and Minors are excluded:
// their value is unavailable for reasons that are not the league's roster
// shape, and counting them would report an injury as a structural surplus.
//
// An empty status is a snapshot written before Status was persisted. It is
// excluded here, and SummarizeRosterShape detects those days explicitly rather
// than letting them fall through as zero-value days.
func statusIsRosterable(status string) bool {
	return status == "Active" || status == "Reserve"
}

// hitterIsDeployable reports whether a hitter's projection counts toward owned
// value on the snapshot's date. Hitters appear in essentially every game their
// team plays, so HasGame is the right availability test for them.
func hitterIsDeployable(p SnapshotPlayer) bool {
	return statusIsRosterable(p.Status) && p.HasGame
}

// pitcherIsDeployable reports whether a pitcher's projection counts toward
// owned value on the snapshot's date.
//
// HasGame is NOT the test for a starter. It means only that the pitcher's MLB
// team plays, and SnapshotPlayer.ProjPtsPerGame is the undiscounted projection
// (NonStarterSPDiscount is applied downstream of the snapshot, inside
// OptimizePitcherLineup's local slice). Counting an ace's full projection on
// his four rest days out of five would make every roster in the league report
// roughly the same rate — a measurement of rotation cadence, not roster shape.
//
// IsStarter||GSSuppressed reconstructs the PRE-gate probable-starter set, since
// applyGSGate flips IsStarter to false on the starts it declines. That is also
// what makes every start reported by FormatGateSummary appear in this side's
// stranded total.
//
// The branch keys on Role, which buildSnapshot sets to "SP" when PosShortNames
// contains SP — so a player with any SP eligibility takes the starter branch,
// matching how the gate itself treats them.
func pitcherIsDeployable(p SnapshotPlayer) bool {
	if !statusIsRosterable(p.Status) {
		return false
	}
	if p.Role == "SP" {
		return p.IsStarter || p.GSSuppressed
	}
	return p.HasGame
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backtest/ -run 'TestPitcherIsDeployable|TestDeployability|TestSideShape' -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backtest/roster_shape.go internal/backtest/roster_shape_test.go
git commit -m "$(cat <<'EOF'
feat(backtest): deployability model for the roster-shape report

SideShape plus the two predicates that decide what counts as owned
value. The pitcher predicate deliberately does not use HasGame:
ProjPtsPerGame is undiscounted and HasGame means only "team plays", so
a HasGame denominator credits an ace his full projection on four rest
days in five and reports rotation cadence for every roster alike.

IsStarter||GSSuppressed reconstructs the pre-gate probable-starter set,
which is what puts every gate-declined start into the stranded total.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Walk the window

Accumulate the two sides across the dates, applying the same freshness guard `SummarizeGSGate` uses, and detect the pre-schema day explicitly so it cannot masquerade as a fully-fielded one.

**Files:**
- Modify: `internal/backtest/roster_shape.go`
- Test: `internal/backtest/roster_shape_test.go`

**Interfaces:**
- Consumes: `SideShape`, `hitterIsDeployable`, `pitcherIsDeployable` (Task 2); `LoadSnapshot(dir string, date time.Time) (Snapshot, bool)` and `sameETDate(t, date time.Time) bool` (existing, `internal/backtest/backtest.go:662,671`); `Snapshot.GSLimit` and `SnapshotPlayer.Status` (Task 1).
- Produces: `RosterShape` struct and `SummarizeRosterShape(dir string, dates []time.Time, hitterSlots, pitcherSlots int) RosterShape`. Task 4 formats the struct; Task 5 calls the function.

- [ ] **Step 1: Write the failing test**

Append to `internal/backtest/roster_shape_test.go` (`day` comes from `range_test.go`, `sameDayGeneratedAt` from `gs_gate_summary_test.go`, both already in this package):

```go
// writeShapeSnapshot writes a snapshot carrying both roles plus a GS cap.
// The existing writeTestSnapshot helper is pitcher-only, which the roster-shape
// tests cannot use.
func writeShapeSnapshot(t *testing.T, dir string, date time.Time, gsLimit int, hitters, pitchers []SnapshotPlayer) {
	t.Helper()
	writeShapeSnapshotGenerated(t, dir, date, sameDayGeneratedAt(date), gsLimit, hitters, pitchers)
}

func writeShapeSnapshotGenerated(t *testing.T, dir string, date, generatedAt time.Time, gsLimit int, hitters, pitchers []SnapshotPlayer) {
	t.Helper()
	snap := Snapshot{
		Date:        date.Format("2006-01-02"),
		GeneratedAt: generatedAt,
		GSLimit:     gsLimit,
		Hitters:     hitters,
		Pitchers:    pitchers,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(dir, date.Format("2006-01-02")+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func TestSummarizeRosterShape_AccumulatesBothSides(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12,
		[]SnapshotPlayer{
			{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10},
			{PlayerID: "h2", Status: "Reserve", HasGame: true, ProjPtsPerGame: 4},
			{PlayerID: "h3", Status: "Minors", HasGame: true, ProjPtsPerGame: 99},
		},
		[]SnapshotPlayer{
			{PlayerID: "p1", IsPitcher: true, Role: "SP", Status: "Active", IsStarter: true, HasGame: true, WasStarted: true, ProjPtsPerGame: 16},
			{PlayerID: "p2", IsPitcher: true, Role: "SP", Status: "Active", GSSuppressed: true, HasGame: true, ProjPtsPerGame: 11},
			{PlayerID: "p3", IsPitcher: true, Role: "SP", Status: "Active", HasGame: true, ProjPtsPerGame: 20},
		},
	)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06")}, 13, 6)

	if got.DaysWithSnapshot != 1 || got.Days != 1 {
		t.Errorf("Days = %d / DaysWithSnapshot = %d, want 1 / 1", got.Days, got.DaysWithSnapshot)
	}
	if got.HitterSlots != 13 || got.PitcherSlots != 6 {
		t.Errorf("slots = %d/%d, want 13/6", got.HitterSlots, got.PitcherSlots)
	}
	if got.GSCapMin != 12 || got.GSCapMax != 12 {
		t.Errorf("cap = %d–%d, want 12–12", got.GSCapMin, got.GSCapMax)
	}
	// Minors hitter excluded: owned is 10+4, fielded is 10.
	if got.Hitters.OwnedPts != 14 || got.Hitters.FieldedPts != 10 {
		t.Errorf("hitters = %+v, want owned 14 fielded 10", got.Hitters)
	}
	// p3 is a rest-day SP and excluded entirely; owned is 16+11, fielded is 16.
	if got.Pitchers.OwnedPts != 27 || got.Pitchers.FieldedPts != 16 {
		t.Errorf("pitchers = %+v, want owned 27 fielded 16", got.Pitchers)
	}
}

// TestSummarizeRosterShape_ExcludesPreSchemaDay pins the trap this field
// introduces. On a snapshot written before Status existed, every status is ""
// and the deployable filter yields zero owned value — which is indistinguishable
// from a day measured as fully fielded. It has to be counted and excluded
// explicitly, the same way DaysStale is one layer up.
func TestSummarizeRosterShape_ExcludesPreSchemaDay(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 0,
		[]SnapshotPlayer{{PlayerID: "h1", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}},
		[]SnapshotPlayer{{PlayerID: "p1", IsPitcher: true, Role: "SP", IsStarter: true, HasGame: true, ProjPtsPerGame: 16}},
	)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06")}, 13, 6)

	if got.DaysPreSchema != 1 {
		t.Errorf("DaysPreSchema = %d, want 1", got.DaysPreSchema)
	}
	if got.DaysWithSnapshot != 0 {
		t.Errorf("DaysWithSnapshot = %d, want 0 — a pre-schema day is not a measured day", got.DaysWithSnapshot)
	}
	if _, ok := got.Hitters.FieldedRate(); ok {
		t.Error("a pre-schema-only window must not produce a defined rate")
	}
}

// TestSummarizeRosterShape_ExcludesStaleDay reuses the sameETDate rule: a
// --matchup pre-write never overwritten by that day's own run carries a roster
// state from a different day and must not be measured.
func TestSummarizeRosterShape_ExcludesStaleDay(t *testing.T) {
	dir := t.TempDir()
	d := day("2026-08-06")
	writeShapeSnapshotGenerated(t, dir, d, sameDayGeneratedAt(d.AddDate(0, 0, -3)), 12,
		[]SnapshotPlayer{{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}},
		nil,
	)

	got := SummarizeRosterShape(dir, []time.Time{d}, 13, 6)

	if got.DaysStale != 1 || got.DaysWithSnapshot != 0 {
		t.Errorf("DaysStale = %d / DaysWithSnapshot = %d, want 1 / 0", got.DaysStale, got.DaysWithSnapshot)
	}
	if got.Hitters.OwnedPts != 0 {
		t.Errorf("stale day contributed %v owned pts, want 0", got.Hitters.OwnedPts)
	}
}

// TestSummarizeRosterShape_MissingDayIsNotMeasured pins that a date with no
// snapshot at all counts toward Days but contributes nothing, matching
// SummarizeGSGate.
func TestSummarizeRosterShape_MissingDayIsNotMeasured(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12,
		[]SnapshotPlayer{{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}},
		nil,
	)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06"), day("2026-08-07")}, 13, 6)

	if got.Days != 2 || got.DaysWithSnapshot != 1 {
		t.Errorf("Days = %d / DaysWithSnapshot = %d, want 2 / 1", got.Days, got.DaysWithSnapshot)
	}
}

// TestSummarizeRosterShape_IsValueWeightedNotMeanOfRates pins that rates come
// from summed totals rather than averaged daily rates. A quiet Monday with one
// player having a game must not weigh the same as a full Saturday.
//
// The two days are sized to separate the formulas: day 1 owns 100 and fields
// none (0%), day 2 owns 10 and fields all of it (100%). Mean-of-daily-rates
// gives 50%; value-weighted gives 10/110 ≈ 9.1%.
func TestSummarizeRosterShape_IsValueWeightedNotMeanOfRates(t *testing.T) {
	dir := t.TempDir()
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12,
		[]SnapshotPlayer{{PlayerID: "h1", Status: "Reserve", HasGame: true, ProjPtsPerGame: 100}}, nil)
	writeShapeSnapshot(t, dir, day("2026-08-07"), 12,
		[]SnapshotPlayer{{PlayerID: "h2", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 10}}, nil)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06"), day("2026-08-07")}, 13, 6)

	rate, ok := got.Hitters.FieldedRate()
	if !ok {
		t.Fatal("rate should be defined")
	}
	if want := 10.0 / 110.0; rate != want {
		t.Errorf("FieldedRate = %v, want %v (value-weighted, not the 50%% mean of daily rates)", rate, want)
	}
}

// TestSummarizeRosterShape_CapRangeIgnoresUntrackedDays pins that a day with GS
// tracking off records 0 and is skipped rather than dragging the minimum to
// zero and rendering a bogus "GS cap 0–18/wk".
func TestSummarizeRosterShape_CapRangeIgnoresUntrackedDays(t *testing.T) {
	dir := t.TempDir()
	hitters := []SnapshotPlayer{{PlayerID: "h1", Status: "Active", HasGame: true, WasStarted: true, ProjPtsPerGame: 5}}
	writeShapeSnapshot(t, dir, day("2026-08-06"), 12, hitters, nil)
	writeShapeSnapshot(t, dir, day("2026-08-07"), 0, hitters, nil)
	writeShapeSnapshot(t, dir, day("2026-08-08"), 18, hitters, nil)

	got := SummarizeRosterShape(dir, []time.Time{day("2026-08-06"), day("2026-08-07"), day("2026-08-08")}, 13, 6)

	if got.GSCapMin != 12 || got.GSCapMax != 18 {
		t.Errorf("cap = %d–%d, want 12–18", got.GSCapMin, got.GSCapMax)
	}
}
```

Add the imports this file now needs — change the import block at the top of `internal/backtest/roster_shape_test.go` to:

```go
import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backtest/ -run TestSummarizeRosterShape -v`
Expected: FAIL to compile — `undefined: SummarizeRosterShape`.

- [ ] **Step 3: Write the implementation**

Append to `internal/backtest/roster_shape.go` (and add `import "time"` at the top):

```go
// RosterShape reports how much deployable projected value a roster fielded on
// each side of the ball over a window, against the league's slot counts and
// weekly game-start cap.
//
// It is the analytical companion to GateSummary: the gate measures which starts
// the cap declined, this measures the structural imbalance that keeps producing
// them. The two print together.
type RosterShape struct {
	// Days is the size of the window asked for.
	Days int
	// DaysWithSnapshot is how many of those days had a snapshot that was fresh
	// (same Eastern-time calendar day, per sameETDate) AND carried roster
	// status. Only these contribute to the totals.
	DaysWithSnapshot int
	// DaysStale is how many days had a snapshot whose GeneratedAt falls on a
	// different ET calendar day than the date it projects — a --matchup
	// pre-write never overwritten by that day's own run. Its roster state
	// belongs to a different day, so it is excluded.
	DaysStale int
	// DaysPreSchema is how many days had a fresh snapshot written before
	// SnapshotPlayer.Status existed. Excluded, and counted separately, because
	// an empty status fails the deployable filter for every player — leaving a
	// day with zero owned value that is otherwise indistinguishable from a day
	// measured as fully fielded.
	DaysPreSchema int

	HitterSlots, PitcherSlots int

	// GSCapMin/GSCapMax bound the weekly game-start cap over the counted days.
	// They range over NON-ZERO caps only: a day with GS tracking disabled
	// records 0, and letting that into the minimum would render "GS cap
	// 0–18/wk". Both zero means no counted day recorded a cap at all.
	GSCapMin, GSCapMax int

	Hitters, Pitchers SideShape
}

// SummarizeRosterShape reads the projection snapshots for the given dates and
// totals deployable versus fielded projected value on each side.
//
// It is a second, independent pass over the same snapshots SummarizeGSGate
// reads. They are kept separate so each owns its own failure policy and doc
// burden; the cost is one extra read of a handful of small JSON files.
//
// hitterSlots and pitcherSlots are carried through for rendering only — they
// are never divided by. Normalizing by slot count is exactly the reduction this
// measure exists to avoid (see SideShape.FieldedRate).
func SummarizeRosterShape(dir string, dates []time.Time, hitterSlots, pitcherSlots int) RosterShape {
	s := RosterShape{Days: len(dates), HitterSlots: hitterSlots, PitcherSlots: pitcherSlots}

	for _, d := range dates {
		snap, ok := LoadSnapshot(dir, d)
		if !ok {
			continue
		}
		// A zero GeneratedAt predates the field and can't be judged for
		// staleness, so it is treated as fresh — matching RunProjectionAnalysis
		// and SummarizeGSGate.
		if !snap.GeneratedAt.IsZero() && !sameETDate(snap.GeneratedAt, d) {
			s.DaysStale++
			continue
		}
		if isPreStatusSnapshot(snap) {
			s.DaysPreSchema++
			continue
		}
		s.DaysWithSnapshot++

		for _, h := range snap.Hitters {
			if !hitterIsDeployable(h) {
				continue
			}
			s.Hitters.OwnedPts += h.ProjPtsPerGame
			if h.WasStarted {
				s.Hitters.FieldedPts += h.ProjPtsPerGame
			}
		}
		for _, p := range snap.Pitchers {
			if !pitcherIsDeployable(p) {
				continue
			}
			s.Pitchers.OwnedPts += p.ProjPtsPerGame
			if p.WasStarted {
				s.Pitchers.FieldedPts += p.ProjPtsPerGame
			}
		}

		if snap.GSLimit > 0 {
			if s.GSCapMin == 0 || snap.GSLimit < s.GSCapMin {
				s.GSCapMin = snap.GSLimit
			}
			if snap.GSLimit > s.GSCapMax {
				s.GSCapMax = snap.GSLimit
			}
		}
	}
	return s
}

// isPreStatusSnapshot reports whether a snapshot was written before
// SnapshotPlayer.Status existed.
//
// The test is all-or-nothing rather than "any player missing a status" because
// that is the true shape of the transition: Status is copied straight off the
// roster for every player, so a real write sets it for all of them or the code
// predates the field entirely. A snapshot with no players at all is not marked
// pre-schema — it contributes nothing either way, and mislabelling it would
// misreport why the window is thin.
func isPreStatusSnapshot(s Snapshot) bool {
	seen := 0
	for _, p := range s.Hitters {
		if p.Status != "" {
			return false
		}
		seen++
	}
	for _, p := range s.Pitchers {
		if p.Status != "" {
			return false
		}
		seen++
	}
	return seen > 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backtest/ -run TestSummarizeRosterShape -v`
Expected: all six PASS.

- [ ] **Step 5: Run the full package**

Run: `go vet ./... && go test ./internal/backtest/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backtest/roster_shape.go internal/backtest/roster_shape_test.go
git commit -m "$(cat <<'EOF'
feat(backtest): SummarizeRosterShape walks the window

Reuses the sameETDate freshness guard, and detects the pre-schema day
explicitly: on a snapshot written before Status existed every status is
empty, the deployable filter yields zero owned value, and the day is
otherwise indistinguishable from one measured as fully fielded. Counted
and excluded, same as DaysStale one layer up.

Cap range spans non-zero caps only, so a day with GS tracking off does
not render "GS cap 0-18/wk".

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Render the block

**Files:**
- Modify: `internal/backtest/roster_shape.go`
- Test: `internal/backtest/roster_shape_test.go`

**Interfaces:**
- Consumes: `RosterShape` (Task 3), `SideShape.FieldedRate`/`StrandedPts` (Task 2).
- Produces: `FormatRosterShape(s RosterShape) string`. Task 5 prints it.

- [ ] **Step 1: Write the failing test**

Append to `internal/backtest/roster_shape_test.go` (add `"strings"` to the import block):

```go
func TestFormatRosterShape_RendersBothRatesAndCap(t *testing.T) {
	out := FormatRosterShape(RosterShape{
		Days: 7, DaysWithSnapshot: 7,
		HitterSlots: 13, PitcherSlots: 6,
		GSCapMin: 12, GSCapMax: 12,
		Hitters:  SideShape{OwnedPts: 1000, FieldedPts: 910},
		Pitchers: SideShape{OwnedPts: 500, FieldedPts: 340},
	})

	for _, want := range []string{
		"13 hitter slots",
		"6 pitcher slots",
		"GS cap 12/wk",
		"Measured over 7 of 7 days.",
		"Hitters",
		"91%",
		"90.0 stranded",
		"Pitchers",
		"68%",
		"160.0 stranded",
		"not the 13:6 slot ratio",
		"not summed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestFormatRosterShape_CapVariants(t *testing.T) {
	tests := []struct {
		name           string
		min, max       int
		want, notWant  string
	}{
		{name: "single cap", min: 12, max: 12, want: "GS cap 12/wk"},
		{name: "range", min: 12, max: 18, want: "GS cap 12–18/wk"},
		{name: "untracked", min: 0, max: 0, want: "GS cap not tracked", notWant: "/wk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := FormatRosterShape(RosterShape{
				Days: 1, DaysWithSnapshot: 1, HitterSlots: 13, PitcherSlots: 6,
				GSCapMin: tt.min, GSCapMax: tt.max,
				Hitters: SideShape{OwnedPts: 10, FieldedPts: 5},
			})
			if !strings.Contains(out, tt.want) {
				t.Errorf("output missing %q:\n%s", tt.want, out)
			}
			if tt.notWant != "" && strings.Contains(out, tt.notWant) {
				t.Errorf("output should not contain %q:\n%s", tt.notWant, out)
			}
		})
	}
}

// TestFormatRosterShape_EmptySideIsNotZeroPercent pins that a side with no
// deployable value reads as such. Rendering it as 0% would claim the roster
// stranded everything it owned.
func TestFormatRosterShape_EmptySideIsNotZeroPercent(t *testing.T) {
	out := FormatRosterShape(RosterShape{
		Days: 1, DaysWithSnapshot: 1, HitterSlots: 13, PitcherSlots: 6,
		GSCapMin: 12, GSCapMax: 12,
		Hitters: SideShape{OwnedPts: 100, FieldedPts: 90},
	})

	if !strings.Contains(out, "no deployable value in window") {
		t.Errorf("empty pitcher side should say so explicitly:\n%s", out)
	}
	if strings.Contains(out, "0% of owned") {
		t.Errorf("empty side must not render as 0%%:\n%s", out)
	}
}

func TestFormatRosterShape_NamesExcludedDays(t *testing.T) {
	out := FormatRosterShape(RosterShape{
		Days: 7, DaysWithSnapshot: 4, DaysStale: 1, DaysPreSchema: 2,
		HitterSlots: 13, PitcherSlots: 6, GSCapMin: 12, GSCapMax: 12,
		Hitters: SideShape{OwnedPts: 100, FieldedPts: 90},
	})

	if !strings.Contains(out, "Measured over 4 of 7 days.") {
		t.Errorf("missing measured-days line:\n%s", out)
	}
	if !strings.Contains(out, "1 stale") || !strings.Contains(out, "2 predating roster-status capture") {
		t.Errorf("excluded days not named:\n%s", out)
	}
}

func TestFormatRosterShape_NoMeasurableDaysExplainsWhy(t *testing.T) {
	tests := []struct {
		name  string
		shape RosterShape
		want  string
	}{
		{
			name:  "all pre-schema",
			shape: RosterShape{Days: 3, DaysPreSchema: 3},
			want:  "predate roster-status capture",
		},
		{
			name:  "all stale",
			shape: RosterShape{Days: 3, DaysStale: 3},
			want:  "stale",
		},
		{
			name:  "none on disk",
			shape: RosterShape{Days: 3},
			want:  "No snapshots on disk",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := FormatRosterShape(tt.shape)
			if !strings.Contains(out, tt.want) {
				t.Errorf("output missing %q:\n%s", tt.want, out)
			}
			if strings.Contains(out, "% of owned") {
				t.Errorf("must not report a rate with no measurable days:\n%s", out)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backtest/ -run TestFormatRosterShape -v`
Expected: FAIL to compile — `undefined: FormatRosterShape`.

- [ ] **Step 3: Write the implementation**

Append to `internal/backtest/roster_shape.go` (add `"fmt"` and `"strings"` to the import block):

```go
// FormatRosterShape renders the roster-shape section of the backtest report.
//
// The prose is part of the measure, not decoration: a bare pair of percentages
// invites exactly the two misreadings the design rules out — that the gap is
// the league's slot ratio, and that the two stranded figures can be added into
// a weekly loss.
func FormatRosterShape(s RosterShape) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nROSTER SHAPE — value owned vs value the league lets you field\n")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 64))
	fmt.Fprintf(&b, "%d hitter slots · %d pitcher slots · %s\n",
		s.HitterSlots, s.PitcherSlots, formatGSCap(s))

	if s.DaysWithSnapshot == 0 {
		fmt.Fprintf(&b, "%s\n", noMeasurableDaysReason(s))
		return b.String()
	}

	fmt.Fprintf(&b, "Measured over %d of %d days.%s\n\n", s.DaysWithSnapshot, s.Days, excludedClause(s))
	fmt.Fprint(&b, formatSideLine("Hitters", s.Hitters))
	fmt.Fprint(&b, formatSideLine("Pitchers", s.Pitchers))
	fmt.Fprintf(&b, "\nEach side is normalized against its own owned value, not against the\n")
	fmt.Fprintf(&b, "other, so the gap is not the %d:%d slot ratio — a roster carrying exactly\n",
		s.HitterSlots, s.PitcherSlots)
	fmt.Fprintf(&b, "its slots reads 100%% on both sides.\n")
	fmt.Fprintf(&b, "Hitter stranding rotates day to day; pitcher stranding above the cap is\n")
	fmt.Fprintf(&b, "dead for the week. The two are not summed.\n")
	return b.String()
}

func formatGSCap(s RosterShape) string {
	switch {
	case s.GSCapMax == 0:
		return "GS cap not tracked"
	case s.GSCapMin == s.GSCapMax:
		return fmt.Sprintf("GS cap %d/wk", s.GSCapMax)
	default:
		return fmt.Sprintf("GS cap %d–%d/wk", s.GSCapMin, s.GSCapMax)
	}
}

// formatSideLine renders one role's row, or says plainly that the side had no
// deployable value. It must never fall back to 0%: no value to field and all
// value stranded are opposite readings.
func formatSideLine(label string, side SideShape) string {
	rate, ok := side.FieldedRate()
	if !ok {
		return fmt.Sprintf("%-10s no deployable value in window\n", label)
	}
	return fmt.Sprintf("%-10s fielded %3.0f%% of owned projected value   (%.1f stranded)\n",
		label, rate*100, side.StrandedPts())
}

// excludedClause names days dropped from the sample, so a window thinned by
// failed runs or by pre-schema history is visible rather than silently
// shrinking the denominator.
func excludedClause(s RosterShape) string {
	var parts []string
	if s.DaysStale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", s.DaysStale))
	}
	if s.DaysPreSchema > 0 {
		parts = append(parts, fmt.Sprintf("%d predating roster-status capture", s.DaysPreSchema))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf(" Excluded: %s.", strings.Join(parts, ", "))
}

func noMeasurableDaysReason(s RosterShape) string {
	switch {
	case s.DaysStale > 0 && s.DaysPreSchema > 0:
		return fmt.Sprintf("No measurable days: %d stale, %d predate roster-status capture.",
			s.DaysStale, s.DaysPreSchema)
	case s.DaysPreSchema > 0:
		return fmt.Sprintf("All %d day(s) with snapshots predate roster-status capture — nothing to report.",
			s.DaysPreSchema)
	case s.DaysStale > 0:
		return fmt.Sprintf("All %d day(s) with snapshots are stale (never overwritten by that day's own run) — nothing to report.",
			s.DaysStale)
	default:
		return fmt.Sprintf("No snapshots on disk for these %d days — nothing to report.", s.Days)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backtest/ -run TestFormatRosterShape -v`
Expected: all PASS.

- [ ] **Step 5: Run the full package**

Run: `go vet ./... && go test ./internal/backtest/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backtest/roster_shape.go internal/backtest/roster_shape_test.go
git commit -m "$(cat <<'EOF'
feat(backtest): render the roster-shape block

An empty side reads "no deployable value in window", never 0% — no
value to field and all value stranded are opposite readings. Excluded
days are named in the header so a thinned window is visible rather than
quietly shrinking the sample.

The prose is part of the measure: it rules out the two misreadings a
bare pair of percentages invites.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Wire into the backtest command and verify live

**Files:**
- Modify: `cmd/backtest.go:125-132`
- Modify: `README.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: `backtest.SummarizeRosterShape` and `backtest.FormatRosterShape` (Tasks 3–4); `hitterSlots`/`pitcherSlots` already in scope at `cmd/backtest.go:80-86`; `dates` already built at `cmd/backtest.go:125-128`.
- Produces: nothing downstream.

- [ ] **Step 1: Add the call site**

In `cmd/backtest.go`, immediately after the existing gate block:

```go
	if gate := backtest.SummarizeGSGate(backtestSnapshotDir, dates); gate.Days > 0 {
		fmt.Print(backtest.FormatGateSummary(gate))
	}
	// Roster shape is the analytical companion to the gate summary: the gate
	// measures which starts the cap declined, this measures the structural
	// imbalance that keeps producing them. Printed after so the measurement
	// comes before its explanation.
	if shape := backtest.SummarizeRosterShape(backtestSnapshotDir, dates, len(hitterSlots), len(pitcherSlots)); shape.Days > 0 {
		fmt.Print(backtest.FormatRosterShape(shape))
	}
	return nil
```

- [ ] **Step 2: Build and run the full test suite**

Run: `go vet ./... && go mod tidy && make check-pins && go test ./internal/...`
Expected: all PASS.

- [ ] **Step 3: Verify the block renders against real snapshots**

Run: `go run . backtest 2>&1 | tail -30`

Expected: the `ROSTER SHAPE` block prints under `GS GATE`. Because `status`/`gs_limit` only exist from this change forward, the honest expected output on the first run is the pre-schema branch — something like `All 7 day(s) with snapshots predate roster-status capture — nothing to report.` **That is a pass, not a failure**, and confirms the pre-schema guard is doing its job rather than reporting a confident fabricated number.

To see the populated form, first generate a snapshot carrying the new fields:

```bash
go run . optimize --dry-run --snapshot
go run . backtest --dates $(date -u +%Y-%m-%d):$(date -u +%Y-%m-%d) 2>&1 | tail -20
```

Record the actual output in the commit message.

- [ ] **Step 4: Update `README.md`**

Find the section describing `backtest` output and add a sentence to it:

```markdown
The report also prints a **roster shape** block: the share of deployable
projected value each side of the roster actually fielded, against the league's
slot counts and weekly game-start cap. Each side is normalized against its own
owned value, so the two rates are comparable without reducing to the slot ratio.
Days whose snapshots predate roster-status capture are excluded and named.
```

- [ ] **Step 5: Update `CLAUDE.md`**

In the `internal/backtest` section, after the paragraph describing the GS-gate summary, add:

```markdown
**Roster shape** (`roster_shape.go`) is the analytical companion to `GateSummary`: per side, the share of *deployable* projected value the roster fielded, with slot counts and the weekly GS cap alongside. Two denominator rules are load-bearing. **`HasGame` is not the availability test for a starter** — `SnapshotPlayer.ProjPtsPerGame` is the *undiscounted* projection (`NonStarterSPDiscount` is applied downstream of the snapshot, inside `OptimizePitcherLineup`'s local slice) and `HasGame` means only "team plays", so a `HasGame` denominator credits an ace his full projection on four rest days in five and reports rotation cadence identically for every roster in the league; the test is `IsStarter || GSSuppressed`, which reconstructs the *pre-gate* probable-starter set and is also what puts every start `FormatGateSummary` names into this side's stranded total. **IL and Minors are excluded** via the new `SnapshotPlayer.Status`, since `WasStarted` collapses roster status to one boolean and an injury would otherwise read as a structural surplus. Each side is normalized against its own owned value, never against the other, so a roster carrying exactly its slot count reads 100% on both sides whether the league fields 13 hitters or 9 — the comparison cannot reduce to the slot ratio. Rates are value-weighted (`Σfielded / Σowned`), not a mean of daily rates. The two stranded figures share a unit but are never summed: a benched hitter starts tomorrow, a start above the weekly cap is dead, the same GROSS-not-net asymmetry `GSGateReport.SuppressedPts` documents. `Status` and `GSLimit` exist only from 2026-08-06 forward; a fresh snapshot in which *every* status is empty is counted as `DaysPreSchema` and excluded, because the deployable filter would otherwise leave it with zero owned value and make it indistinguishable from a fully-fielded day.
```

- [ ] **Step 6: Final gates**

Run: `go vet ./... && go mod tidy && make build && go test ./internal/...`
Expected: all PASS.

- [ ] **Step 7: Commit and push**

```bash
git add cmd/backtest.go README.md CLAUDE.md
git commit -m "$(cat <<'EOF'
feat(backtest): print roster shape under the GS-gate summary

Closes rosterbot-hx5.

Wires SummarizeRosterShape into the backtest report, passing the slot
counts already in scope. Slot counts are rendered, never divided by.

Because status/gs_limit are forward-only, the first runs report the
pre-schema branch rather than a fabricated number.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
git push -u origin feat/rosterbot-hx5-roster-shape
```

- [ ] **Step 8: Close the issue**

```bash
bd close rosterbot-hx5 --reason "Roster-shape block renders under the GS-gate summary in the backtest report. Normalization is per-side fielded/owned, so it cannot reduce to the 13:6 slot ratio — a roster carrying exactly its slot count reads 100% on both sides at any league shape. Two denominator findings drove the design: snapshot ProjPtsPerGame for pitchers is undiscounted and HasGame means only 'team plays', so the obvious denominator would have measured five-man rotation cadence identically for every roster; and IL/Minors players were invisible in the snapshot, so an injury would have read as structural surplus. Added SnapshotPlayer.Status and Snapshot.GSLimit (forward-only, like gs_suppressed) to fix both. Pre-schema days are counted and excluded rather than contributing a zero that reads as fully-fielded. Stdout only — rosterbot-5cx still covers --json and the dashboard."
```

---

## Self-Review

**Spec coverage:** model → Task 2; window walk, pre-schema, cap range, value-weighting → Task 3; all rendering rules → Task 4; schema → Task 1; wiring, docs, verification → Task 5. All eight spec test cases appear: 1→Task 2, 2→Task 2, 3→Task 2, 4→Task 3, 5→Task 3, 6→Task 3, 7→Tasks 2 and 4, 8→Tasks 3 and 4. Round-trip snapshot test → Task 1.

**Placeholders:** none. Every code step carries complete code.

**Type consistency:** `SideShape`/`RosterShape` field names, `FieldedRate() (float64, bool)`, `StrandedPts()`, `SummarizeRosterShape(dir, dates, hitterSlots, pitcherSlots)`, `FormatRosterShape(s)`, `Status`, `GSLimit`, `isPreStatusSnapshot` — used identically in every task that references them.
