# Daily Waiver-Claims Recap — Design

**Date:** 2026-06-13
**Status:** Approved (pending spec review)

## Problem

The existing `waivers` command is forward-looking: it surfaces Statcast-driven
free agents *worth claiming*. There is no backward-looking report of waiver
claims that **actually cleared** across the league. We want a daily recap that
answers "who grabbed whom, what did it cost, and was it a good claim?" — and a
persisted ledger so we can later audit how rosterbot's own recommendations fared
against the league's real claim activity.

## Scope

- **League-wide**: every team's processed claims/drops, not just our team.
- **Delivery**: rich text (stdout) + GHA markdown summary + concise Pushover
  digest. No HTML page (YAGNI for v1).
- **Cadence**: cursor-based daily run. Robust to waiver-processing time drifting
  relative to the cron — never misses or double-reports.
- **No-op on empty**: if no new claims since the cursor, log one line and return
  early. No Pushover, no GHA summary, no ledger file. The cursor still advances.

## Data availability (verified)

The `go-fantrax` lib (`v0.1.16`) exposes `auth_client.GetAllTransactions()` —
the `CLAIM_DROP` transaction view — returning `models.Transaction` rows with:

- `Type` ∈ {`CLAIM`, `DROP`} (also `TRADE`, which we ignore here)
- `ClaimType` ∈ {`FA` (free agent), `WW` (waiver wire)}
- `TeamName`, `TeamID`, `PlayerName`, `PlayerID`, `PlayerPosition`
- `BidAmount`, `Priority`, `ProcessedDate`, `Period`, `Executed`

Our `internal/fantrax` client currently only wraps `GetAllTrades()` (the `TRADE`
view), so a new wrapper is required.

## Command + package

- New command: **`claims`** (distinct from the forward-looking `waivers`).
- New package: **`internal/claims`**, mirroring `internal/transactions`.
- Flags:
  - `--dry-run` — no Pushover; no ledger write. stdout only.
  - `--no-signals` — skip the Savant signal tie-in (the slow part) for fast
    local runs. Defaults to on (signals enabled).
  - `--since <YYYY-MM-DD>` — override the cursor for backfill/testing.

## Architecture

```
fantrax (CLAIM_DROP view) ──┐
hkb player values         ──┼──► claims.Run ──► stdout + GHA summary + Pushover
waivers signal tie-in     ──┘                └─► .waivers/claims/<date>.json (ledger)
```

### Signal tie-in dependency

`internal/claims` **imports `internal/waivers`** and reuses `LoadSavant`,
`TagHitter`/`TagPitcher`, and `DefaultThresholds`. This creates a one-way
`claims → waivers` dependency (waivers does not depend back). Gated behind
`--no-signals` because loading five Savant CSVs cold is the slow path; in CI the
`waivers.yml` job warms the shared `.cache` first, so the load is usually a hit.

## Data flow

1. **Fetch**: new `fantrax.Client.GetRecentTransactions(since time.Time)` wraps
   `auth.GetAllTransactions()`, filtered to `ProcessedDate > since`. Cached at
   `todayTTL` like `allTrades`, keyed `fantrax-all-transactions-<leagueID>`
   (added to `cachekeys.go`).
2. **Cursor**: `.cache/last-claims.json`, mirroring the prospects tracker
   (`loadTxnCursor`/`saveTxnCursor`). First run (zero cursor) defaults to
   `today − 3d`. `--since` overrides. End of run saves `today` (even on no-op).
3. **Group**: partition rows league-wide by team; within a team, pair each
   `CLAIM` with the `DROP` in the same transaction set → an add/drop **move**.
   A claim with no matching drop is a bare add; a drop with no claim is a bare
   drop.
4. **Value**: join player names to `hkb.GetPlayers(cacheDir)` via normalized-name
   lookup (the same join the trade monitor uses; `playername.Normalize`).
   `netValue = addedHKB − droppedHKB`, per move and summed per team.
5. **Signals** (unless `--no-signals`): resolve added players → MLBAM IDs via
   `playername.ResolveMLBAMIDs`, build the `waivers.SavantBundle` with
   `waivers.LoadSavant`, tag each added player BUY-LOW / HOT / BOTH with
   `waivers.DefaultThresholds()`.

## Output

All sections degrade gracefully when a join misses (unranked player, no MLBAM
match, no Savant row).

1. **Per-move breakdown** (stdout, ANSI-colored like `transactions`): claiming
   team, FA vs waiver, player added (HKB value / rank / 30d trend / key stat /
   signal badge), player dropped (HKB value), **net value gained**; bid/priority
   shown when present.
2. **Daily value leaderboard**: moves ranked by `netValue` — top "heists"
   (largest positive) and "reaches" (largest negative).
3. **Notable drops watch**: dropped players whose HKB value exceeds a threshold
   and are now free agents — actionable pickups.
4. **Bid/FAAB efficiency**: value-per-bid-dollar when `BidAmount` is populated;
   falls back to waiver `Priority` ordering when not (league may run rolling
   priority rather than FAAB — block degrades, never assumes FAAB).
5. **GHA markdown summary** when `GITHUB_STEP_SUMMARY` set.
6. **Pushover digest** when `!DryRun` and `PushoverUserKey`/`PushoverAPIToken`
   set; truncated to the 1024-char limit (reuse the `transactions` truncation
   approach).

## Audit ledger

Per run, write `.waivers/claims/<YYYY-MM-DD>.json`:

```jsonc
{
  "date": "2026-06-13",
  "generated_at": "2026-06-13T14:02:11Z",
  "entries": [
    {
      "team": "Team Name",
      "team_id": "abc123",
      "claim_type": "WW",
      "added":   { "name": "...", "pos": "OF", "mlbam_id": 123456,
                   "hkb_value": 4200, "hkb_rank": 180,
                   "signal": "BUY-LOW", "projected_pts_per_game": 4.7 },
      "dropped": { "name": "...", "hkb_value": 1100 },
      "net_value": 3100,
      "bid_amount": "12",
      "priority": "3"
    }
  ]
}
```

Recording `signal` and `projected_pts_per_game` **at claim time** is what makes
the ledger auditable later: rosterbot flagged player X BUY-LOW → which team
claimed them → how did they perform afterward. `projected_pts_per_game` is
sourced from the same FanGraphs path the `waivers` command uses
(`projections.ExpectedPtsFromProj`); if unavailable it is omitted.

**Persistence**: GHA-cache only. The workflow restores/saves `.waivers/claims/`
under a stable `actions/cache` key (alongside `.cache`), exactly like
`.backtest/snapshots/`. Not committed to the repo. Daily runs keep the key warm,
so eviction risk is low.

## GHA workflow

New `.github/workflows/claims.yml`:

- `cron`: daily ~2pm UTC (after `waivers.yml` at 1pm UTC warms the Savant cache);
  plus `workflow_dispatch` with a `dry_run` input.
- Chrome install + `.fantrax-cache/` restore/save under the shared
  `fantrax-session-` key (same as the other five workflows).
- `actions/cache@v4` with a stable key (`claims-`, falling back to `projections-`)
  over multi-path `.cache` + `.waivers/claims`.
- Secrets: `FANTRAX_*` (the standard set) + `PUSHOVER_USER_KEY` +
  `PUSHOVER_API_TOKEN`.

## Housekeeping

- Append `go run . claims --dry-run` to the `run-all` Makefile recipe.
- Update `README.md` (user-facing command/flags/workflow) and `CLAUDE.md`
  (new package, new cache key, new workflow, ledger directory).

## Testing

Hermetic, no credentials — consistent with the repo convention:

- **Grouping/pairing**: `CLAIM`+`DROP` → moves, including bare add and bare drop.
- **Net value**: added − dropped, per move and per team.
- **Leaderboard**: ranking + heist/reach selection, with ties broken
  deterministically (by team then player name).
- **Drops watch**: threshold filter.
- **Bid efficiency**: value-per-dollar present vs priority-fallback.
- **Ledger**: round-trip JSON serialization.
- **Cursor**: load/save, zero-cursor default, `--since` override.
- **No-op**: empty claim set → no ledger, no notify, cursor advances.
- **Signals**: tie-in against a fake `SavantBundle`.

Mock the fantrax client via a `ClaimsClient` interface (subset:
`GetRecentTransactions`), mirroring `transactions.TradeClient`.

## Out of scope (v1)

- HTML page / GitHub Pages deploy.
- Committing the ledger to the repo (GHA cache only).
- FAAB-specific assumptions (bid block degrades to priority).
- A separate command to *read back* / analyze the accumulated ledger — that's a
  follow-up once data accumulates.
