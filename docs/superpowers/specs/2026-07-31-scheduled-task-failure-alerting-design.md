# Scheduled task failure alerting (rosterbot-naz)

**Status:** approved 2026-07-31
**Issue:** rosterbot-naz — "No alerting on scheduled task failure — 11-hour outage passed silently"

## Problem

Between 2026-07-01T17:00Z and 2026-07-02T03:00Z, eleven consecutive hourly
`optimize --matchup --archive-projections` runs and the 23:41Z `shadow` run all
exited 1 with `fantrax API error STALE_CLIENT`. No notification of any kind was
sent. `aws cloudwatch describe-alarms` returns zero alarms and `aws sns
list-topics` returns zero topics in us-west-1. The only existing notifier, the
`buildnotify` Lambda, fires on CodeBuild state-change events — image builds, not
ECS task outcomes.

`optimize` is the lineup-apply hot path. A silent multi-hour outage there means
lineups silently stop being applied on game days.

## Evidence

All 1,181 terminal records in `s3://…/runledger/` (2026-06-17 → 2026-07-31):

| command | runs | failures |
|---|---|---|
| `optimize --matchup --archive-projections` | 616 | 11 |
| `shadow` | 31 | 1 |
| the other 11 commands | 534 | 0 |

Two facts from that scan drive the design:

1. **Zero isolated transient failures.** All 12 failures are the one incident,
   contiguous. The ticket's design note suggested counting consecutive failures
   "to avoid noise from a single transient" — the history contains no such
   transient to protect against. Requiring two consecutive failures buys almost
   nothing and costs an hour of latency on the hourly hot path, a full day on
   the daily jobs.
2. **Zero missing launches.** Every one of the 616 expected hourly Lineup slots
   has a ledger record. Tasks that never start are not an observed failure mode.

The failing records already carry a 50-line `log_tail` whose last line is the
literal `STALE_CLIENT` error. The alert can therefore carry the cause, not just
the fact.

## Design

### Placement

Rename `buildnotify/` → `opsnotify/` and dispatch on `detail-type`, so one
Lambda owns all ops notification. This keeps the repo at three nested Go modules
rather than four — CLAUDE.md records nested-module `replace ../` staleness
breaking the deploy twice already (first `lambda/`, then `buildnotify/`), so a
fourth module is a real recurring cost.

```
internal/opsalert/          NEW — pure leaf, root module, covered by `make test`
  streak.go    Streak(recs []lineupapi.RunDetail, command string) Verdict
  message.go   FormatTask(Verdict) (title, body string)

opsnotify/                  RENAMED from buildnotify/
  main.go      SSM creds at cold start; dispatch on detail-type
  build.go     formatBuild(CodeBuildEvent)      — today's message.go, unchanged
  task.go      handleTask(ECS event) → read ledger → opsalert → Pushover

infra/infra.go
  opsNotifyFn      UNGATED  (+ s3:ListBucket + s3:GetObject on runledger/*, + ssm)
  TaskFailRule     UNGATED  → opsNotifyFn
  BuildNotifyRule  still inside `if enableBuild` → opsNotifyFn
```

**The ungated-function / gated-rule split is load-bearing.** `BuildNotify` today
is constructed entirely inside `if enableBuild`, so a stack deployed without
that context flag has no notifier at all. ECS failure alerting must survive
that, so the function moves out of the gate and only the CodeBuild *rule* stays
behind it.

The Lambda stays thin — parse, fetch, format, send. Every judgement is a pure
function in `internal/opsalert`, which is in the root module and therefore
actually exercised by `make test`.

### Why not the alternatives

- **In-bot** (`entrypoint.sh` calls `rosterbot alert-failure` after a FAILED
  ledger write): zero new AWS resources, but the thing reporting the outage is
  the thing that is broken. It cannot observe OOM, SIGKILL to pid 1, or an
  image-pull failure, and the ticket's acceptance criteria explicitly ask for
  ECS-level coverage.
- **CloudWatch alarm on a run-ledger-derived custom metric**: new metric
  plumbing, and an alarm cannot name the failing job or carry the log tail.

### The streak verdict

`Streak` reads terminal (`SUCCESS`/`FAILED`) ledger records for one `command`,
newest-first. `RUNNING` records are ignored — they are the start-of-run write
that the end-of-run write later overwrites at the same key, and an in-flight
sibling job must not break a streak.

| Condition | Verdict | Push |
|---|---|---|
| newest `FAILED`, streak == 1 | `Started` | ❌ first failure + log tail |
| newest `FAILED`, streak == 3 | `Escalated` | 🔥 not transient, it's an outage |
| newest `SUCCESS`, previous `FAILED` | `Recovered` | ✅ recovered after N failures |
| anything else | `None` | silent |

Escalation is `== 3`, not `>= 3`, so an eleven-failure outage escalates exactly
once. Three hourly runs is roughly two hours of the lineup hot path down — past
any plausible transient — and the `Started` alert already went out at failure
one, so the escalation's job is only to distinguish "outage" from "blip".

State lives nowhere. "Is this the first failure of a streak?" is answerable from
a newest-N listing of an append-only ledger, so there is no counter object to
keep consistent and no cooldown timer to tune. Streak transitions
self-deduplicate.

Replayed against the real incident, this yields exactly four pushes:

```
17:00Z  ❌ optimize failed · STALE_CLIENT: …
19:00Z  🔥 optimize failed 3× in a row
23:41Z  ❌ shadow failed              (its own streak)
04:00Z  ✅ optimize recovered after 11 failures
```

### Failure judged in Go, not in the event pattern

The EventBridge rule filters `aws.ecs` / `ECS Task State Change` /
`detail.clusterArn` = our cluster / `detail.lastStatus` = `STOPPED`. The Go
handler decides the outcome:

- any container `exitCode != 0` → failure
- container `exitCode` absent (the container never ran) → failure
- all container exit codes zero → success, which feeds the `Recovered` path

Keeping the judgement in Go rather than in the event pattern avoids
EventBridge's array-of-objects matching subtleties and makes the decision
table-testable. The cost is invoking the Lambda on every task stop — roughly 700
per month, inside the free tier.

### The case the ledger cannot see

If the task stopped abnormally and **no ledger record exists for that task id**,
the entrypoint never reached its final `run-ledger` write: OOM, image-pull
failure, SIGKILL to pid 1. No streak is computable, so the handler alerts
unconditionally and reports the ECS `stoppedReason` instead of a log tail.

This is the class that self-reporting structurally cannot catch, and it is the
reason the detector is a Lambda rather than a bot subcommand. It has never
occurred.

### Job identity

Keyed on the ledger's `command` string — the joined container command override,
which the ledger already stores. `optimize --matchup --archive-projections` and
`shadow` are independent streaks, exactly as they behaved during the incident.

Runs with `trigger: manual` are treated identically to scheduled ones. A manual
re-run that succeeds is a genuine recovery, and the streak logic already
suppresses the noise that motivated separating them.

### Message

```
Rosterbot: optimize failed
❌ optimize --matchup --archive-projections · exit 1
fantrax API error STALE_CLIENT: Your browser is using an outdated cached version…
```

Title names the job so the Pushover notification is triageable from the lock
screen. Body carries emoji severity, the full command, the exit code, and the
**last non-empty line** of the ledger record's `log_tail`, truncated to 300
characters. Pushover caps a message at 1024 characters and `SendPushover`
truncates silently, so the cause line must be bounded before it is appended
rather than after.

No dashboard deep link. It would need the dashboard origin from SSM plus another
IAM grant, and the log tail already answers the question the link would be
opened to answer.

### Channel

`PUSHOVER_USER_KEY` — the personal ops channel, matching `buildnotify` and the
existing GS-limit-fetch-failure alert. `PUSHOVER_GROUP_KEY` is the league
channel and carries no infra paging.

## Out of scope

**"Job never launched at all"** — an EventBridge rule disabled or broken emits
no ECS event and writes no ledger record, so nothing in this design sees it.
Detecting it requires a separate scheduled heartbeat that asserts each job ran
within its expected window.

Deliberately excluded: zero occurrences across all 616 expected Lineup slots in
44 days, and the Infra tab already renders per-artifact staleness against
`layout.Artifact.MaxAge`. Filed as a P3 follow-up rather than built.

## Testing

**`internal/opsalert`** — table-driven over synthetic `[]lineupapi.RunDetail`:

- first failure after a success → `Started`
- second and fourth consecutive failures → `None`
- exactly the third → `Escalated`
- success after failures → `Recovered` with the correct prior streak length
- empty history / no prior record for the command
- interleaved commands do not contaminate each other's streaks
- `RUNNING` records ignored
- replay of the real 12-record incident asserting exactly the four pushes above

**`opsnotify`** — event-parse tests against real ECS and CodeBuild event JSON
fixtures, including a stopped task with no `exitCode`.

**`make test` must be extended to run nested-module tests.** Today it is
`go test ./internal/...`, and CI's `make build-modules` only *builds* the nested
modules, so `buildnotify/message_test.go` has never executed in this repo. Any
test added under `opsnotify/` would be dead coverage without this change.

## Verification

`go vet ./...`, `go mod tidy`, `make build-modules`, `make test`.

Deploy with `cdk deploy -c enableBuild=true` (omitting the flag destroys the
CodeBuild project). Confirm live by launching a task with a deliberately failing
command via the API and checking that one Pushover arrives, then a successful
run produces the recovery push.
