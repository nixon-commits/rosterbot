# Durable Ephemeral-Data Archive — Design

**Date:** 2026-06-30
**Status:** Approved (pending spec review)

## Problem

The bot depends on several **ephemeral, point-in-time** upstream data sources — data
that only exists "as of now" and is unrecoverable once the day passes:

- **FanGraphs projections** — the API serves only the *current* projection; yesterday's is gone.
- **HKB rankings** (harryknowsball.com) — only current player values/ranks are served.
- **Baseball Savant** rolling windows (14d/30d expected stats) — roll off permanently as the window moves.
- **Prospect rankings** — the FanGraphs board (the source actually wired in `prospects/run.go`);
  current rankings only.

The existing `FileCache` (S3 `cache/` prefix) is a **TTL cache**, not an archive: on
expiry the next fetch **overwrites** the key with the current value. Lengthening the TTL
does not preserve history — it only delays the overwrite. So none of the four sources has
a durable historical record, with one partial exception: projections are archived *for the
players the optimizer scores* via the snapshot/analysis store, but not the full projection set.

By contrast, **historical games and probable starters** (MLB statsapi) are immutable and
always re-fetchable, so they need no durable archive — a long-TTL cache is purely a speed-up.

**Goal:** capture one faithful daily snapshot of each ephemeral source into a durable,
date-partitioned archive, so we can later reconstruct "what did HKB / Steamer / Savant / the
prospect board say on day X."

## Non-goals

- Not a replacement for the TTL cache (that stays as-is for performance).
- Not Athena/SQL analytics in v1 — raw blobs only. (Layout is Hive-friendly so SQL can be added later without re-archiving.)
- Not changing the projection snapshot/analysis store — this is additive and independent.

## Decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Sources to archive | HKB, **full** projection set, Savant windows, prospect rankings |
| Storage format | **Raw blobs by date** (lossless, zero schema upkeep) |
| Bytes captured | **Raw upstream response bytes** (decoupled from our parser structs) |
| Capture mechanism | **Dedicated `archive` command + its own EventBridge schedule** |
| Retention | **Keep forever** (~1–2 GB/year; trivial S3 cost) |

## Architecture

New leaf package **`internal/archive`** + a top-level **`archive` command**.

```
cmd/archive.go ──▶ internal/archive
                     ├── type Artifact struct { Filename string; Bytes []byte }
                     ├── type Source interface { Name() string; Fetch(ctx) ([]Artifact, error) }
                     ├── Writer.Write(date time.Time, sourceName string, artifacts []Artifact) error
                     └── concrete sources wired in cmd/archive.go, each delegating to its
                         home package's exported ArchiveArtifacts(ctx) ([]archive.Artifact, error)
```

Each source's URL knowledge stays in its home package (consistent with the repo's
leaf-package discipline). The home packages already expose their upstream URLs as
overridable `var`s:

- `internal/hkb` — `fetchURL` (`https://harryknowsball.com/rankings`)
- `internal/projections` — `fangraphsBattingURL`, `fangraphsPitchingURL`, `fgBaseURL` (`type=%s&stats=%s…`)
- `internal/waivers` — `savantHitterExpURL`, `savantHitterExp14dURL`, `savantHitterSCURL`, `savantPitcherExpURL`, `savantPitcherExp30URL`
- `internal/prospects` — `fgProspectURL` (the wired FanGraphs board source; MLB Pipeline can be added later if wired)

Each package gains **one** small exported `ArchiveArtifacts(ctx) ([]archive.Artifact, error)`
that performs the raw HTTP GET(s) and returns the response bytes untouched.

## Storage layout

```
.archive/
  hkb/dt=2026-06-30/rankings.html
  projections/dt=2026-06-30/steamer-ros-bat.json
  projections/dt=2026-06-30/steamer-ros-pit.json
  projections/dt=2026-06-30/depthcharts-ros-bat.json
  projections/dt=2026-06-30/depthcharts-ros-pit.json
  projections/dt=2026-06-30/thebatx-ros-bat.json
  projections/dt=2026-06-30/thebatx-ros-pit.json
  projections/dt=2026-06-30/atc-ros-bat.json
  projections/dt=2026-06-30/atc-ros-pit.json
  savant/dt=2026-06-30/hitter-exp.csv
  savant/dt=2026-06-30/hitter-exp-14d.csv
  savant/dt=2026-06-30/hitter-statcast.csv
  savant/dt=2026-06-30/pitcher-exp.csv
  savant/dt=2026-06-30/pitcher-exp-30d.csv
  prospects/dt=2026-06-30/fangraphs-board.json
```

- `dt=YYYY-MM-DD` (Hive-style) — glob-friendly now, Athena-partitionable later.
- **Projections cover all four RoS systems** (steamer-ros, depthcharts-ros, thebatx-ros,
  atc-ros × bat+pit = 8 blobs/day), not just the bot's configured system — "full projection
  set" + keep-forever argues for capturing the whole landscape while we're already fetching.
- Synced to S3 `archive/` prefix via **one new `statePairs` entry** in `cmd/sync.go`
  (`{".archive/", "archive/"}`), uploaded with `del=false` → append-only / keep-forever.
  No `internal/statesync` change needed (it already does arbitrary dir↔prefix copies).
- Re-running a date overwrites that day's blobs (last-write-wins, same as snapshots).

## Capture path, cadence & wiring

- **Command:** `archive`, registered on `rootCmd`. Flags:
  - `--date YYYY-MM-DD` (default: today, UTC) — the `dt=` partition to write.
  - `--dry-run` — fetch and report artifact counts/sizes, write nothing. Lets it join `make run-all`.
- **Local vs S3:** writes `.archive/` locally; `sync-up` ships it to S3 `archive/`. Mirrors
  how `.backtest/` already round-trips between Fargate tasks.
- **Cadence:** one new EventBridge schedule in `infra/` (CDK, Go), ~`cron(0 14 * * ? *)`
  (14:00 UTC, late morning ET) — after FanGraphs/Savant post their once-daily refresh so each
  blob is the settled version for that date. Runs 7 days/week. Gated by the existing
  `schedulesEnabled` flag.
- **`make run-all`:** append an `archive --dry-run` line (per the CLAUDE.md rule for new
  top-level commands) and account for `.archive/` in the cache-size print if relevant.

## Error handling

- **Per-source isolation:** the command runs each source independently, collecting per-source
  errors and continuing. It exits **non-zero only if *all* sources failed** — so EventBridge
  alerts on a total outage but tolerates a single flaky upstream (e.g. a FanGraphs 429).
- **No partial-day corruption:** a source's artifacts are written to a temp dir and renamed
  into `dt=<date>/` only after *all* its artifacts fetched successfully, so a half-fetched
  projections set never lands as if complete.
- **Idempotent:** re-running a date replaces that day's blobs cleanly.
- **Best-effort sync:** `sync-up` already swallows errors, so an S3 hiccup won't fail the job;
  the next run re-uploads.

## Testing

All hermetic — no credentials, no live network (matches existing repo discipline).

- **Source tests:** point each source's URL `var`(s) at an `httptest.Server`; assert the
  returned artifacts' filenames and that bytes are passed through byte-for-byte unmodified.
- **Writer tests:** temp dir only; assert the `dt=` path layout, temp-then-rename atomicity,
  and last-write-wins on a repeated date.
- **Command test:** wire fake `Source`s (including one that returns an error) into the command;
  assert per-source isolation (good sources still write) and the all-failed → non-zero exit.

## Rollout

1. `internal/archive` package (`Artifact`, `Source`, `Writer`) + tests.
2. `ArchiveArtifacts` in each of `hkb`, `projections`, `waivers`, `prospects` + tests.
3. `cmd/archive.go` wiring the four sources + command test.
4. `statePairs` entry in `cmd/sync.go`.
5. `make run-all` line.
6. `infra/` EventBridge schedule (CDK).
7. README + CLAUDE.md updates (new command, new S3 `archive/` prefix, new schedule).

## Open questions

None blocking. The projection-system scope (all four RoS systems vs configured only) is
called out above and can be trimmed in implementation if desired.
