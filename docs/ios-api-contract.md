# RosterBot iOS API Contract

The thin-client HTTP contract served by the lineup/control Lambda (Function URL).
Single-user app, one bearer token. All responses are JSON, snake_case — decode
with `JSONDecoder.keyDecodingStrategy = .convertFromSnakeCase`.

Backend implementation: `internal/lineupapi` (contract + handler), `lambda/`
(Function URL entry), `cmd/optimize.go` (lineup producer), `entrypoint.sh` +
`cmd/ledger.go` (run ledger). Deploy details: [`aws-deployment.md`](aws-deployment.md).

## Connection

- **Server**: the Function URL base, no trailing slash. Current value is the
  CDK stack output `LineupApiUrl` (e.g. `https://<id>.lambda-url.us-west-1.on.aws`).
- **Auth**: every request must send `Authorization: Bearer <token>`. The token
  lives in SSM at `/rosterbot/ROSTERBOT_API_TOKEN`.
- **Status codes**: `401` missing/bad token, `404` not found, `400` bad job
  name, `501` route not available (local `serve` only), `502` backend error.
  Error bodies: `{"error":"..."}`.

## Endpoints

### `GET /v1/lineup/today`

Today's optimized lineup (precomputed by the hourly `optimize` run).

```json
{
  "date": "2026-06-17",
  "league_id": "...", "team_id": "...",
  "slots": [
    { "slot": "C",  "player": { "id": "1001", "name": "Adley Rutschman",
                                "team": "BAL", "pos": ["C"], "proj": 3.6,
                                "status": "OK" } },
    { "slot": "BN", "player": null }
  ],
  "projected_points": 112.2,
  "warnings": ["Rafael Devers benched in real lineup"]
}
```

- `slot`: roster slot label (`C,1B,2B,3B,SS,INF,OF,UT,SP,RP,P,BN`). `BN` = bench.
  The **same label repeats** across multi-slot positions (4× `OF`, 3× `UT`,
  multiple `P`), so do not use `slot` as a list id — use the array index.
- `player`: **nullable** (open/empty slot).
- `player.status`: `OK` | `LOCKED` (game started/final) | `BENCHED` (out of the
  real MLB starting lineup).
- `player.proj`: projected fantasy points (Double). `pos`: position codes.
- `warnings`: array of strings (may be empty).

### `GET /v1/runs`

Run history (scheduled + manual), newest first.

```json
{ "runs": [
  { "id": "57ad20259d5a457bb390628afd92f50e", "command": "optimize --matchup",
    "status": "SUCCESS", "exit_code": 0, "started_at": "2026-06-17T21:00:04Z",
    "ended_at": "2026-06-17T21:00:41Z", "trigger": "schedule" }
] }
```

- `status`: `RUNNING` | `SUCCESS` | `FAILED`. While `RUNNING`, `exit_code` and
  `ended_at` are omitted.
- `trigger`: `schedule` | `manual`.
- "Errors" view = filter to `status == "FAILED"`.
- Optional `?limit=N` (default 25, max 200).

### `GET /v1/runs/{id}`

One run plus its captured output tail.

```json
{ "id": "...", "command": "...", "status": "FAILED", "exit_code": 1,
  "started_at": "...", "ended_at": "...", "trigger": "manual",
  "log_tail": "...last ~50 lines of output..." }
```

- `log_tail` is populated on failures (empty/omitted otherwise).
- `id` is the ECS task id — the same id `POST /v1/jobs` returns, so you can poll
  this endpoint for a run you just triggered. (Right after a POST it may `404`
  for a few seconds until the task starts; treat that as still `RUNNING`.)
- For a typed result (not raw logs), see `GET /v1/runs/{id}/output`.

### `GET /v1/runs/{id}/output`

Structured, typed result for a completed job — the data behind a per-job result
view. `404` for runs that produced no output (older runs, `optimize`, jobs that
finished before this existed). Distinct from `log_tail` (raw stdout), which stays
on `GET /v1/runs/{id}` for diagnostics.

```json
{ "type": "waivers", "data": { /* job-specific object, see below */ } }
```

- `type` exactly matches the job `name` from `GET /v1/jobs`.
- `data` is a job-specific object (snake_case). Decode generically, then switch on
  `type`. `optimize` and `recap-site` never produce output.

**`prospects`**
```json
{ "alerts": [ { "name": "Jackson Holliday", "team": "BAL", "pos": "SS",
    "kind": "called-up", "priority": "high", "detail": "promoted to MLB", "rank": 1 } ],
  "upgrades": [ { "source": "FanGraphs", "drop": "Old Guy", "drop_rank": 80,
    "add": "New Guy", "add_rank": 12, "rank_gap": 68, "near_term": true } ] }
```
`kind` ∈ `called-up | optioned | performance-hot | performance-cold |
free-agent-buzz | upgrade-available`; `priority` ∈ `high | medium | low`. Split
`alerts` into call-up vs breakout views by `kind`.

**`waivers`**
```json
{ "picks": [ { "name": "...", "team": "BAL", "pos": "OF", "is_pitcher": false,
    "signal": "HOT", "projected_pts_per_game": 4.2, "drop_name": "...", "gap": 1.1,
    "xwoba": 0.40, "woba": 0.36, "barrel_pct": 14, "hard_hit_pct": 48, "rank": 1 } ],
  "total": 12 }
```
`signal` ∈ `BUY-LOW | HOT | BOTH` (omitted if none). Pitcher rows carry `era`/`xera`
instead of the hitter stat fields. `rank` is 1-based. `total` is the count that
passed filters before the top-N cut.

**`claims`**
```json
{ "claims": [ { "team": "...", "claim_type": "FA", "added": "New SS",
    "added_pos": "SS", "dropped": "Old SS", "net_value": 3, "signal": "HOT" } ] }
```
`claim_type` ∈ `FA | WW`. `net_value` = added HKB value − dropped HKB value. One
row per added player.

**`transactions`**
```json
{ "trades": [ { "teams": ["A","B"], "processed_at": "2026-06-20T12:00:00Z",
    "players": [ { "name": "...", "from_team": "A", "pos": "OF", "valuation": 30 } ] } ] }
```
`from_team` is the side the player came from; `valuation` is HKB value.

**`gs-check`**
```json
{ "period": "Week 11", "violations": [ { "team": "...", "kind": "over",
    "used": 7, "limit": 5, "over_by": 2 } ] }
```
`kind` ∈ `over | under`. `over_by` present for `over` only.

**`backtest`**
```json
{ "start": "2026-06-08", "end": "2026-06-14",
  "days": [ { "date": "2026-06-08", "actual": 40.0, "optimal": 42.0, "gap": -2.0 } ],
  "accuracy": { "mae": 1.4, "bias": 0.3, "rmse": 2.1, "n": 240,
    "by_position": [ { "bucket": "OF", "n": 50, "mae": 1.1, "bias": 0.2 } ] } }
```
`gap` = actual − optimal (negative = points left on the bench). `accuracy` omitted
when projection grading didn't run (`--skip-projections`). `bucket` ∈
`C | INF | OF | UT | SP | RP`.

**`grade`**
```json
{ "dates": ["2026-06-19"], "rows_written": 12 }
```
Graded-snapshot rows written to the Analysis Store, by date.

### `GET /v1/notifications`

The activity feed — every event that also went to Pushover (lineup applied,
waiver picks, trades, prospect alerts, GS violations), newest first. This is the
app's replacement for Pushover as the primary surface.

```json
{ "notifications": [
  { "id": "...", "kind": "lineup", "status": "success", "title": "Fantrax Lineup",
    "message": "2 hitter changes (+3.20 pts)\n  ↑ Aaron Judge → OF\n  ↓ Ian Happ → BN",
    "created_at": "2026-06-17T21:00:41Z", "run_id": "57ad2025..." }
] }
```

- `kind` ∈ `lineup | waivers | claims | transactions | prospects | gs-check |
  alert` (badge/icon per kind — which function).
- `status` ∈ `success | failure | info` (severity color — green/red/neutral).
  Note `kind` is the category and `status` is the severity; they are different
  axes. The body text field is **`message`** (not `body`).
- `message` is the human text (the lineup `message` already lists the ↑/↓ moves
  — render it as the "changes" detail).
- `run_id` (optional) deep-links to that run's detail.
- Optional `?limit=N` (default 25, max 200).

### `GET /v1/jobs`

The **job schema** — render Action forms dynamically from this instead of
hardcoding. Static; available without auth-to-runner.

```json
{ "jobs": [
  { "name": "optimize", "label": "Optimize Lineup", "mutating": true,
    "description": "Set the optimal lineup. Applies changes to your real Fantrax roster.",
    "params": [
      { "name": "period", "label": "Period", "type": "enum",
        "options": ["today","matchup","all","custom"], "default": "matchup" },
      { "name": "dates", "label": "Custom date / range", "type": "text",
        "pattern": "^\\d{4}-\\d{2}-\\d{2}(:\\d{4}-\\d{2}-\\d{2})?$",
        "help": "Used when Period = custom" },
      { "name": "projections", "label": "Projection system", "type": "enum",
        "options": ["steamer","depthcharts","thebatx","steamer-ros","depthcharts-ros","thebatx-ros"] },
      { "name": "dry_run", "label": "Dry run (preview only)", "type": "bool" }
    ] }
] }
```

Param `type` → form field: `bool` (toggle), `int` (stepper, honor `min`/`max`),
`enum` (picker from `options`), `text` (validate against `pattern`). `mutating:
true` jobs (optimize, waivers, claims, gs-check, transactions) should show a
confirm dialog.

### `POST /v1/jobs/{name}`

Launch a job as a Fargate task. **Asynchronous** — returns immediately; the job
takes ~30–60s to start. Optional JSON body sets params from the schema:

```
POST /v1/jobs/optimize
{ "params": { "period": "custom", "dates": "2026-04-01:2026-04-07", "dry_run": "true" } }

-> 202 Accepted
{ "id": "57ad2025...", "command": "optimize --dates 2026-04-01:2026-04-07 --dry-run",
  "status": "RUNNING" }
```

- All param values are **strings** (`"true"`/`"false"` for bool, `"25"` for int).
- Empty/no body = job defaults (e.g. `optimize` → `--matchup`).
- **Accept any 2xx (treat 202 as success) and decode the body.**
- Invalid param (bad enum, int out of range, malformed date) → `400` with
  `{"error":"<reason>"}`. Unknown job name → `400`.
- After POST, poll `GET /v1/runs` (or `/v1/runs/{id}`) until `status != RUNNING`.
  After a successful `optimize`, re-fetch `GET /v1/lineup/today`.

> **Non-dry-run jobs run for real.** `optimize` applies your Fantrax lineup and
> pushes; `waivers`/`claims`/`transactions` push. Gate `mutating` jobs behind a
> confirmation dialog (and surface the `dry_run` toggle).

## Suggested screens

1. **Lineup** — `GET /v1/lineup/today`; group hitters/pitchers + bench, show
   `proj`, badge `LOCKED`/`BENCHED`, show `projected_points` + `warnings`.
2. **Runs** — `GET /v1/runs`; rows with command, relative time, status pill.
   Tap → detail (`GET /v1/runs/{id}`) showing `log_tail`.
3. **Errors** — same data filtered to `FAILED`.
4. **Actions** — buttons per job → `POST /v1/jobs/{name}`, then poll + toast.
   Confirm dialog before `optimize`.
5. **Run result** — after a run succeeds, `GET /v1/runs/{id}/output`; switch on
   `type` to render the per-job view (`404` → fall back to `log_tail`).
