# Recap Expansion Design

**Date:** 2026-05-05
**Status:** Approved (pending implementation plan)

## Goal

Expand the weekly recap (`internal/recap`) with three additions that increase entertainment value, analytical depth, and storytelling:

1. **Win Probability chart** — MLB-style WP curve over the 7 days of a matchup, featured as a "Game of the Week" hero plus inline sparklines on every matchup row.
2. **Roster Activity log** — per-team transactions (adds, drops, swaps, trades) over the matchup week, rendered as a standalone section.
3. **Four new "silly" awards** — Heart Attack 💓, Comeback ↩️, Whale 🐳, Dud 😴.

All three ship as one bundled change.

## Page layout (final)

```
Header (existing)
[NEW] Game of the Week         featured matchup, full WP chart with dated x-axis
                                hidden when no matchup has any lead changes
Most & Least Efficient (existing)
League Awards (existing 6 + Whale + Dud → 8 cards in 2×4 grid)
Pitching Highlights (existing)
Top Single Day Performances (existing)
Team Performance (existing)
Matchup Results (existing)     each row gains a sparkline + Heart Attack/Comeback badges
[NEW] Roster Activity          per-team cards; hidden if no team made moves
Season Awards (existing)       4 new categories naturally join the cumulative tally
Footer (existing)
```

## Module organization

New files in `internal/recap/`:

- `wp.go` — pure functions: `ComputeWPCurve`, `LeagueDailySigma`, `LeadChangeCount`, `MinWinnerWP`
- `wp_test.go`
- `roster_activity.go` — transaction fetch, swap detection, team grouping
- `roster_activity_test.go`

Extended files:

- `awards.go` — new selectors: `HeartAttack`, `Comeback`, `Whale`, `Dud`, plus `awardOrder` and `awardEmoji` updates so the season leaderboard picks them up
- `awards_test.go`
- `template.html` — Game of the Week hero, sparkline column on `.matchup`, Roster Activity section, two new award cards
- `types.go` — new types listed below
- `recap.go` — orchestrates the new collectors into the existing `Recap` struct
- `site.go` — passes the new sections through to the multi-week renderer (no logic change)

## Data dependencies

1. **Transactions** — `client.GetTransactionDetailsHistory()` (already vendored in `go-fantrax@v0.1.13`). Returns flat `[]models.Transaction` with `Type` (CLAIM | DROP | TRADE), `TeamID`, `PlayerName`, `ProcessedDate`, `TradeGroupID`. Filter by `ProcessedDate ∈ [WeekStart, WeekEnd]` (inclusive).
2. **League daily-score history for σ** — `recap.Run` already aggregates per-team daily totals for the current week × 12 teams × 7 days = 84 data points. σ is computed from those points; no extra fetches required. This is "current-week σ" — noisy in absolute precision but adequate for narrative-quality WP curves. Future extension (out of scope): expand to season-wide σ by iterating over earlier weeks via cached `DailyFantasyPoints`, accepting a one-time bootstrap cost.
3. **MLB schedule** — already used (opponent annotation). Nothing new.

## Type additions (`types.go`)

```go
type MatchupWPCurve struct {
    HomeTeamID string
    AwayTeamID string
    Points     []WPPoint  // 8 points: index 0 = pre-week, 1..7 = end of each day
    LeadChanges int
}

type WPPoint struct {
    Date         time.Time  // day boundary; index 0 == WeekStart - 1day or zero time
    HomeWP       float64    // [0,1]
    HomeRunning  float64    // cumulative actual FPts through this day
    AwayRunning  float64
}

type RosterActivity struct {
    Teams []TeamActivity  // sorted by team name asc; only teams with entries
}

type TeamActivity struct {
    TeamID, TeamName string
    Entries          []ActivityEntry
}

type ActivityEntry struct {
    Date       time.Time
    Kind       string   // "claim" | "drop" | "swap" | "trade"
    Player     string   // for claim, drop
    SwapIn     string   // for swap (the player added)
    SwapOut    string   // for swap (the player dropped)
    OtherTeam  string   // for trade
    Received   []string // for trade
    Sent       []string // for trade
    ClaimType  string   // "FA" | "WW" — for claim only
}

type TeamDay struct {       // for Whale award
    TeamID, TeamName string
    Date             time.Time
    Pts              float64
}
```

Updated `Awards`:

```go
type Awards struct {
    // ... existing fields ...
    HeartAttack *MatchupResult
    Comeback    *MatchupTeamSide
    Whale       *TeamDay
    Dud         *PlayerLine
    GameOfWeek  *MatchupResult  // == HeartAttack target by construction
}
```

Updated `Recap`:

```go
type Recap struct {
    // ... existing fields ...
    WPCurves         []MatchupWPCurve  // one per matchup
    RosterActivity   *RosterActivity   // nil-safe; section hidden when nil/empty
}
```

`σ_league` is computed once per recap run (in `wp.go` from cached daily totals) and passed to each per-matchup `ComputeWPCurve` call. It is not surfaced on the `Recap` struct since the template does not render it.

## Win probability simulation

### Methodology — team-level Monte Carlo

For each matchup, compute the WP curve at 8 timestamps: index 0 (pre-week, before any actual FPts) and indices 1..7 (end of each completed day).

```
σ_league   = stddev of daily team-FPts across all 12 teams × all 7 days of the current week (84 points)
mean_h     = home team's season-to-date FPts / completed days
mean_a     = away team's season-to-date FPts / completed days

For day N in 0..7:
    sum_h_actual = sum of home actual FPts for days 1..N (0 when N==0)
    sum_a_actual = sum of away actual FPts for days 1..N
    days_left    = 7 - N

    if days_left == 0:
        WP_home[N] = 1.0 if sum_h > sum_a else 0.0  (0.5 on exact tie)
    else:
        wins = 0
        for sim in 0..N_SIMS:
            sim_h = sum_h_actual + sum(days_left samples ~ N(mean_h, σ²))
            sim_a = sum_a_actual + sum(days_left samples ~ N(mean_a, σ²))
            if sim_h > sim_a: wins++
        WP_home[N] = wins / N_SIMS
```

### Constants

- `N_SIMS = 5000` — single tunable; rationale: ~0.014 SE at p=0.5, invisible at chart resolution
- Mid-week WP threshold for Comeback: `0.30`
- Variance is league-wide (single σ), not per-team — avoids small-sample noise early in the season

### Determinism

- RNG seeded per matchup: `hash(homeID + "|" + awayID + "|" + weekNumber)` via `math/rand.NewSource`
- All sorts use stable tiebreakers (`TeamID` asc, `ProcessedDate` asc)
- Award selectors use `eps = 1e-9` where comparing WP floats
- Two recap runs over the same week produce byte-identical HTML (modulo `GeneratedAt` timestamp)

### Lead-change count

Walk the 8 WP points; count transitions where `(WP[i] > 0.5) != (WP[i-1] > 0.5)`. Days at exactly 0.5 do not contribute a transition (treated as no change either direction).

### Min winner WP

Among the 6 mid-week points (Days 1..6, excluding the pre-week 0 and final 7), take the minimum WP for the eventual winner. Used by the Comeback award.

## Award rules

### Per-week (new)

| Award | Source | Rule | Tiebreak |
|---|---|---|---|
| 💓 Heart Attack | `WPCurves` | Most lead changes across matchups | Smallest final margin → home `TeamID` asc |
| ↩️ Comeback | `WPCurves` | Eventual winner with the lowest mid-week WP, gated `< 0.30`. Returns `MatchupTeamSide` for the winning team | Smallest min WP → `TeamID` asc |
| 🐳 Whale | per-team daily totals | Highest single-day team total across the league × week | Earliest date → `TeamID` asc |
| 😴 Dud | per-player active-starter day scores | Lowest single-day score by an active starter (any roster); negatives eligible | Lowest pts → earliest date → name asc |
| 🎯 Game of the Week | `WPCurves` | Same matchup as Heart Attack — co-derived from one `pickHeartAttack(curves)` call | Same tiebreak as Heart Attack |

If no matchup has any lead changes (all blowouts), Heart Attack and Game of the Week are both nil. The hero section is hidden in that case (no fallback to "Closest").

### Season cumulative

The 4 new awards are appended to `awardOrder` in `awards.go` and `awardEmoji` in `render.go` rendering helpers. `AggregateSeasonAwards` itself iterates each `Awards` field by an explicit branch (current code shape) — this commit adds 4 branches to that function: HeartAttack (winner team), Comeback (winner team), Whale (team), Dud (owner team via name→ID resolution like the existing pitcher-start awards).

For the **Whale**, attribution is by `TeamID`. For the **Dud**, attribution is by the `OwnerTeam` name resolved against `Recap.Teams` (matching the existing pattern for `BestSingleStart` / `WorstSingleStart`).

## Roster Activity rules

### Source

`client.GetTransactionDetailsHistory()` returns flat `[]models.Transaction`. Filter by `ProcessedDate` falling on a calendar date in `[WeekStart, WeekEnd]` (inclusive both ends). `ProcessedDate` comes from upstream as a `time.Time`; compare via `.Format("2006-01-02") >= startYMD && <= endYMD` to match the canonical-date pattern already used in `pairsForWeek`. This avoids any timezone-equality pitfalls.

### Grouping rules

1. **Trades** — bucket by `TradeGroupID`. For each unique trade group, render once *per team-side* (so each team's card lists their own perspective: what they sent, what they got).
2. **Swaps** (same-day 1 CLAIM + 1 DROP for the same team) — merge into a single `swap` entry. Detection: per (team, date) bucket, if exactly one CLAIM and exactly one DROP exist, merge.
3. **Claims** (otherwise) — render as `claim` entry with `ClaimType` (FA or WW).
4. **Drops** (otherwise) — render as `drop` entry.

### Display formats (template-rendered text)

- Trade: `Traded with {OtherTeam} — got: {Received...} · sent: {Sent...} ({date})`
- Swap: `Swap: +{SwapIn} for −{SwapOut} ({date})`
- Claim: `+{Player} ({date}, {ClaimType})`
- Drop: `−{Player} ({date})`

### Sorting

- Teams in card order: by `TeamName` asc (matches recap's existing tiebreak preference)
- Entries within a team: by `ProcessedDate` asc, then a stable secondary on transaction ID for ties

### Empty state

Teams with zero matching entries are omitted entirely. If no team made moves all week, `RosterActivity` is nil and the whole section is hidden in the template (`{{- if and .RosterActivity .RosterActivity.Teams}}`).

## Template changes (`template.html`)

### Game of the Week (new section, top)

```html
{{- if .GameOfWeek}}
<section>
  <h2>Game of the Week</h2>
  <div class="game-of-week">
    <div class="header">
      <span class="badge">💓 Heart Attack</span>
    </div>
    <div class="scores"> ... home & away team names + final scores ... </div>
    <svg class="wp-chart" viewBox="0 0 320 120" preserveAspectRatio="none">
      <line x1="0" y1="60" x2="320" y2="60" stroke="..." />  <!-- 50% reference -->
      <path d="..." stroke="..." />                          <!-- WP curve -->
    </svg>
    <div class="x-axis">Mon Apr 13 · Tue Apr 14 · Wed Apr 15 · ... · Sun Apr 19</div>
  </div>
</section>
{{- end}}
```

X-axis labels rendered from `WPCurve.Points[i].Date` formatted as `Mon Jan 2`. Eight labels evenly spaced.

### Sparklines on Matchup Results

Each `.matchup` row gains a column for an inline sparkline (60×24 SVG path). The matching `MatchupWPCurve` is looked up by team-pair canonical key. Rows for matchups missing a curve render without the sparkline (graceful degradation).

If a matchup has the Heart Attack badge or Comeback badge, render an inline pill next to the team name. Heart Attack lives on the matchup; Comeback on the specific winning team.

### League Awards grid

Add two cards: Whale 🐳 and Dud 😴, formatted like the existing Highest Score / Lowest Score cards.

### Roster Activity (new section, after Matchup Results)

```html
{{- if and .RosterActivity .RosterActivity.Teams}}
<section>
  <h2>Roster Activity</h2>
  {{- range .RosterActivity.Teams}}
  <div class="activity-card">
    <h3>{{.TeamName}}</h3>
    {{- range .Entries}}
    <div class="activity-row">{{renderActivity .}}</div>
    {{- end}}
  </div>
  {{- end}}
</section>
{{- end}}
```

`renderActivity` is a template helper that picks the right format string based on `Entry.Kind`.

### Season Awards

`awardOrder` slice in `awards.go` gains 4 entries. `awardEmoji` map in `render.go` gets 4 new entries (💓 ↩️ 🐳 😴). Otherwise the existing renderer handles it without changes.

### CSS additions

- `.game-of-week` — full-width card matching `.award` palette
- `.wp-chart` — 320×120 SVG with axis tick labels below
- `.matchup .spark` — 60×24 inline SVG, accent color stroke
- `.activity-card` — same palette as `.perf-row`, with `h3` (team name) + tight entry rows
- `.activity-row` — single line, `font-size: 12px`, `color: var(--text-dim)` for dates
- All new styles reuse existing CSS variables (`--bg-card`, `--accent`, etc.)

## Error handling (soft-fail philosophy)

Match existing recap behavior — never block the page render on optional data:

- `GetTransactionDetailsHistory` failure → `RosterActivity` nil; section hidden; warning to stderr
- WP simulation prerequisites unavailable (e.g., zero completed days for σ) → `WPCurves` nil; Game of the Week and sparklines hidden; everything else renders
- Unparseable transaction date → entry skipped, logged to stderr
- Trade with `TradeGroupSize > 1` but only one side present in returned data → render the partial side, skip missing
- All warnings use `fmt.Fprintf(os.Stderr, "WARNING: ...")` consistent with existing code

## Testing strategy

- `wp_test.go`:
  - Golden-file curve test for one canonical matchup (fixed RNG seed)
  - Lead-change counter on synthetic curves (0 changes, 1, 6 max)
  - Min-winner-WP edge cases (winner never trailed, winner trailed every mid-week point)
  - σ computation with empty input (returns 0, callers handle)
  - Determinism: two runs with same seed → identical output

- `roster_activity_test.go`:
  - Each `Kind` rendered correctly (claim / drop / swap / trade)
  - Swap detection: same-day 1+1 → swap; 2+1 → no swap (renders separately); 1+0 → claim; trade and same-day claim → trade preserved as separate entry
  - Team grouping: stable sort, empty teams omitted
  - Date filter: out-of-window entries excluded
  - Empty input → nil RosterActivity (section hidden)

- `awards_test.go` (extensions):
  - HeartAttack: most-changes wins; ties broken by margin then ID
  - Comeback: gate at 0.30; ineligible matchups not surfaced; tie on min WP
  - Whale: highest team-day across league; ties broken by date then ID
  - Dud: lowest active starter (incl. negatives); ties broken by date then name
  - Nil-safe: empty inputs return nil

- `recap_test.go` integration smoke covering the whole pipeline against a small synthetic league

- No live HTTP calls — all transaction / variance fixtures inline. Existing recap tests already follow this pattern.

## Documentation updates

- **README.md** — add a section under recap mentioning Game of the Week, transaction log, four new awards
- **CLAUDE.md** — extend the `internal/recap` paragraph with: WP methodology summary, `wp.go`/`roster_activity.go` purposes, Game of the Week selection rule (= Heart Attack winner; hidden when no lead changes), transaction soft-fail behavior, the new `awardOrder` entries

## Tunables / future extensions (not in scope)

- Make `N_SIMS` and the Comeback `0.30` threshold env-configurable. Today they're consts; bump to `var` if needed
- Per-player Monte Carlo (current section uses team-level)
- Season-wide σ for WP simulation (current uses current-week σ from 84 points)
- Season-leaderboard counterparts for Whale and Dud (top-N like existing Shellings list)
- "Closest of the Week" fallback when no lead changes (explicitly out of scope per design decision)

## Open questions

None — all design decisions resolved during brainstorming.
