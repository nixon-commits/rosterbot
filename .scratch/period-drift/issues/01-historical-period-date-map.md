# Exact period↔date map for historical recap/backtest (period-drift Class B)

Status: ready-for-human

## Context

Fantrax inserts extra **daily** scoring periods mid-season (doubleheaders /
postponed-game makeups), so `PeriodForDate(seasonStart, date)` — which assumes
one period per calendar day — drifts behind Fantrax's authoritative numbering by
the number of inserted periods. Confirmed in 2026: matched through 06-07, off by
one by 06-23.

**Class A is fixed** (commits on `main`): the action-affecting near-today paths
(`optimize` today + `--matchup` window, and `GetTeamGS` for the GS budget /
gs-check) now anchor on Fantrax's authoritative current period via
`fantrax.AnchorPeriodForDate(today, currentPeriod, date)`.

## Problem (Class B — still open)

The historical-reporting paths still use season-start day math:
- `DailyFantasyPoints` (`internal/fantrax/daily_fpts.go`) — used by `recap`, `recap-site`, `backtest`, `grade`
- `GetTeamPitcherStarts` (`internal/fantrax/pitcher_starts.go`) — used by `recap`/`recap-site`
- `ttlForPeriod` (`internal/fantrax/client.go`) — cache-TTL cutoff only (harmless drift)

These map each date in a range to a daily period number to fetch that period's
roster snapshot and diff YTD. After an insertion, post-insertion dates map to the
wrong period → daily FP/GS attribution shifts by a day.

**Why a naive fix is wrong here:** `recap-site` rebuilds *every* completed week,
every Monday. Simply re-anchoring on today's current period (the Class A
approach) would fix recent weeks but **mis-map the early, pre-insertion weeks**
(shift them by the insertion count) in the published site. Neither single anchor
(season-start vs today) is globally correct across insertion boundaries.

## What's needed

An authoritative, season-wide **date → daily-period** map that accounts for each
insertion, so any historical date resolves to the period Fantrax actually used.

Investigation notes:
- The roster API (`getTeamRosterInfo`) addresses purely by period **number**; no
  date param. The period's date may live in the opaque
  `TeamRosterResponse...DisplayedSelections` / `MiscData` blobs (parsed as
  `map[string]interface{}` in go-fantrax) — needs spelunking, possibly a
  go-fantrax change to expose it.
- Alternative: probe period snapshots to discover insertion boundaries (the
  offset is a non-decreasing step function), then encode the piecewise map.
- Whatever the source, cache the map (season-stable) so recap-site stays fast.

## Acceptance

- `recap-site` full rebuild: every week's daily FP/GS attribution matches
  Fantrax-authoritative scoring across insertion boundaries (week-1 and a
  post-insertion week both correct in one build).
- `backtest --dates <historical-range>` grades the correct days.
- No per-day extra network calls on the hot path (map is cached).

## References

- Class A fix: `cmd/optimize.go` `resolveDatePeriod`, `internal/fantrax/gs_check.go` `GetTeamGS`, `fantrax.AnchorPeriodForDate`.
- Memory: `period-drift-2026`.
