# Roster-shape line: hitter vs pitcher value against slot counts and the GS cap

**Issue:** rosterbot-hx5 (deferred from rosterbot-i1c)
**Date:** 2026-08-06

## Problem

rosterbot-i1c shipped the measurement half of the roster-shape question: the GS
gate now reports which starts it declined and their gross projected value, daily
on the board and weekly in the backtest report via `backtest.SummarizeGSGate`.

What it deferred is the analytical half — a line stating hitter value against
pitcher value, with the league's slot counts and weekly game-start cap alongside,
so the structural imbalance is legible next to the measured suppression.

It was deferred because the comparison is a modelling question, not plumbing.
This league fields 13 active hitter slots against 6 undifferentiated P slots plus
a weekly GS cap. Comparing a 13-slot pool to a 6-slot pool naively reports the
slot ratio back as if it were a finding.

## Decision

Report, per side, the share of **deployable projected value the roster actually
fielded**, with the absolute stranded points alongside, over the backtest window.

```
ROSTER SHAPE — value owned vs value the league lets you field
----------------------------------------------------------------
13 hitter slots · 6 pitcher slots · GS cap 12/wk
Measured over 6 of 7 days. 1 day excluded: predates roster-status capture.

Hitters    fielded  91% of owned projected value   (127.4 stranded)
Pitchers   fielded  68% of owned projected value   (142.1 stranded)

Each side is normalized against its own owned value, not against the
other, so the gap is not the 13:6 slot ratio — a roster carrying exactly
its slots reads 100% on both sides.
Hitter stranding rotates day to day; pitcher stranding above the cap is
dead for the week. The two are not summed.
```

## The model

For each date in the window whose snapshot passes the existing `sameETDate`
freshness guard:

```
HITTERS
  deployable = Status ∈ {Active, Reserve} ∧ HasGame
  owned      = Σ ProjPtsPerGame  over deployable
  fielded    = Σ ProjPtsPerGame  over deployable ∧ WasStarted

PITCHERS
  deployable = Status ∈ {Active, Reserve} ∧
                 ( Role == "SP": IsStarter ∨ GSSuppressed
                   otherwise:    HasGame )
  owned      = Σ ProjPtsPerGame  over deployable
  fielded    = Σ ProjPtsPerGame  over deployable ∧ WasStarted

  stranded   = owned − fielded
  rate       = Σ fielded / Σ owned          (undefined when Σ owned = 0)
```

The pitcher branch keys on `SnapshotPlayer.Role`, which `buildSnapshot` sets to
`"SP"` when `PosShortNames` contains `SP` and `"RP"` otherwise — so a player with
any SP eligibility takes the SP branch, matching how the gate itself treats them.

### Why the pitcher denominator is not `HasGame`

`SnapshotPlayer.ProjPtsPerGame` for a pitcher is `ScoredPitcher.ExpectedPts`,
which is the **undiscounted** full projection — `NonStarterSPDiscount` (0.05x) is
applied downstream, only to the local `generic` slice inside
`OptimizePitcherLineup`, and never reaches the snapshot. `HasGame` means only
that the pitcher's MLB team plays.

So a `HasGame` denominator credits an ace his full ~20-point projection on every
rest day. Against a five-man rotation that is four days in five, and the
resulting "fielded rate" would land in the same narrow band for every roster in
the league — it would measure rotation cadence, not roster shape.

`IsStarter ∨ GSSuppressed` reconstructs the **pre-gate** probable-starter set,
since the gate flips `IsStarter` to false on its way out. This is also what makes
the two report sections cohere: every start `FormatGateSummary` reports as
suppressed appears in the pitcher stranded figure directly above it.

### Why IL and Minors players are excluded

`Player` carries `Status`, `InMinors` and `IsInjured`, but `buildSnapshot`
collapses all of it to `WasStarted: Status == "Active"`. An injured ace whose MLB
team plays therefore reads identically to a healthy benched one, and would
inflate "owned but not fielded" with what is an injury rather than a structural
surplus. Persisting `Status` is what makes the exclusion possible.

### Why this is not the 13:6 slot ratio

Each side is normalized against its own owned value, never against the other
side. A roster carrying exactly its slot count reads 100% on both sides whether
the league fields 13 hitters or 9. The gap between the two rates comes from the
shape of value the roster owns relative to what it can field — the question being
asked — not from how the league divides its slots.

### Value-weighted, not mean-of-daily-rates

Rates aggregate as `Σfielded / Σowned` across the window rather than averaging
each day's rate. A Monday with two rostered players having games must not weigh
the same as a Saturday with fifteen.

### The two stranded figures are never summed

Hitter stranding and pitcher stranding share a unit but not a meaning. A benched
hitter starts tomorrow; the pool rotates. A start declined above the weekly cap
is dead for the week, because GS budget is use-it-or-lose-it. This is the same
asymmetry `GSGateReport.SuppressedPts` already documents as GROSS-not-net, and
the rendered block states it rather than leaving it to be inferred.

## Accepted limitation

`WasStarted` is `Status == "Active"` on the roster **as fetched**, i.e. pre-apply
for that run. It measures "was in an active slot when we looked", not "we chose
to start them".

It is reused rather than replaced because `RunProjectionAnalysis` already slices
on exactly this field, and the repo's idempotency invariant means the last hourly
run of a day normally reports "No changes needed" — on a converged day the
observed status is the applied lineup. Recorded in the doc comment.

## Schema changes (forward-only)

```go
// backtest.SnapshotPlayer
Status string `json:"status,omitempty"`   // "Active"/"Reserve"/"Injured Reserve"/"Minors"

// backtest.Snapshot
GSLimit int `json:"gs_limit,omitempty"`   // from GateReport.Limit
```

Both are already held and discarded by `buildSnapshot` (`sp.Player.Status`,
`dr.pitcherResult.GateReport.Limit`) — no new fetch, no new store, no IAM.

`GateReport.Limit` is populated only on a date's own run: `optimize_dates.go`
sets `dateBudget = nil` for every non-today date, so a `--matchup` pre-write
carries no cap. That is self-consistent with the `sameETDate` guard, which
already excludes exactly those days.

### The pre-schema day

On every snapshot written before this change `Status` is `""`, which is not in
`{Active, Reserve}`, so the deployable filter yields `owned = 0` — and a day
contributing nothing is indistinguishable from a day measured as fully fielded.

That is the same failure `DaysStale` exists to prevent one layer up, and gets the
same treatment: a snapshot with at least one player in which **every** status is
empty is counted as `DaysPreSchema` and excluded from both terms. All-or-nothing
is the true shape of the transition — status comes straight off the roster, so a
real write sets it for every player — which makes this a sound detector rather
than a heuristic. A zero-player snapshot is not marked pre-schema; it falls
through to `DaysWithSnapshot` and contributes nothing, which is honest for a case
that should not occur.

Historically, `status` and `gs_limit` exist only from the day this ships forward,
so earlier days read as pre-schema. The day counts are what make a thin window
visible rather than quietly reporting a confident number off two days.

## Surface

New file `internal/backtest/roster_shape.go`:

```go
type SideShape struct { OwnedPts, FieldedPts float64 }
func (s SideShape) StrandedPts() float64
func (s SideShape) FieldedRate() (float64, bool)   // ok=false when OwnedPts == 0

type RosterShape struct {
    Days, DaysWithSnapshot, DaysStale, DaysPreSchema int
    HitterSlots, PitcherSlots                        int
    GSCapMin, GSCapMax                               int   // 0 = never tracked
    Hitters, Pitchers                                SideShape
}

func SummarizeRosterShape(dir string, dates []time.Time, hitterSlots, pitcherSlots int) RosterShape
func FormatRosterShape(s RosterShape) string
```

A separate pass over the same snapshots rather than folding into
`SummarizeGSGate`: each function keeps its own failure policy and doc burden, and
both stay pure over `LoadSnapshot`. The cost is a second read of a handful of
small JSON files.

Wired in `cmd/backtest.go` immediately after the gate block, passing
`len(hitterSlots), len(pitcherSlots)` — both already in scope.

Scope is stdout only, matching `FormatGateSummary`. rosterbot-5cx already covers
carrying the whole GS-gate section into `--json` and the dashboard.

## Rendering rules

- A side with `owned = 0` renders `no deployable value in window`, never `0%`.
- `GSCapMin`/`GSCapMax` range over the **non-zero** caps of counted days only. A
  day with GS tracking disabled records `0` and is skipped rather than dragging
  the minimum to zero. Caps differing across a multi-week window render as a
  range (`GS cap 12–18/wk`); no counted day recording a non-zero cap leaves both
  fields `0` and renders `GS cap not tracked`.
- `DaysWithSnapshot == 0` renders a reason line naming the cause (stale,
  pre-schema, or missing), as `FormatGateSummary` does.
- Excluded days are always named in the header, so a window thinned by stale or
  pre-schema days is visible rather than silently shrinking the sample.

## Tests

`internal/backtest/roster_shape_test.go`:

1. **SP rest day is excluded from owned** — an ace with
   `HasGame ∧ ¬IsStarter ∧ ¬GSSuppressed` must not appear in the denominator.
   This is the guard that keeps the metric from degenerating into rotation cadence.
2. A gate-suppressed start **is** in owned, and lands in stranded.
3. IL and Minors players are excluded from both terms.
4. A pre-schema day increments `DaysPreSchema`, is excluded, and is not counted
   as a measured zero.
5. A stale day increments `DaysStale` (existing `sameETDate` rule).
6. Value-weighted aggregation differs from mean-of-daily-rates, using two days
   with deliberately different owned magnitudes.
7. `owned = 0` yields `FieldedRate` `ok=false` and the formatter's prose branch.
8. Caps differing across days render as a range.

`internal/lineuprun`: a round-trip test pinning that `buildSnapshot` writes
`Status` and `GSLimit`, mirroring how `gs_suppressed` was pinned in i1c.

The board golden files under `internal/lineuprun/testdata/` are not touched —
they cover `renderDateResult`, not snapshot JSON.

## Verification

`go vet ./...`, `go mod tidy`, `make check-pins`, `go test ./internal/...`, and a
live `go run . backtest` to see the block render against real snapshots.

## Out of scope

- Daily board line. Roster shape is structural; a single day where three SPs
  happen to start reads nothing like one where one does.
- `--json` and dashboard surfacing — rosterbot-5cx.
- Backfilling `status`/`gs_limit` onto historical snapshots. The roster state of
  a past day is not recoverable.
