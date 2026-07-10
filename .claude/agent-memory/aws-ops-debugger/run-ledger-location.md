---
name: run-ledger-location
description: Where the live run-ledger status records actually live in S3, and a related trigger-field gotcha
metadata:
  type: reference
---

The run-ledger status records (RUNNING/SUCCESS/FAILED, exit_code, log_tail) live at
`s3://<StateBucketName>/runledger/<invTs>-<taskId>.json` — **not** `runs/` as this
agent's own system-prompt briefing states. Moved there in commit `aec08ed` ("Move
run ledger to its own runledger/ prefix", rosterbot-432), deployed 2026-07-03.
`docs/aws-deployment.md`'s "Run ledger" section documents this correctly; the
agent definition at `.claude/agents/aws-ops-debugger.md` still says `runs/` and
needs the same fix.

The `runs/` prefix still exists but now holds a *different* artifact:
`runs/<taskId>/output.json`, a structured per-command result blob (e.g.
`{"type":"claims","data":{...claims list...}}`) written directly by the command
via the `RUN_ID` env var — unrelated to run status/exit code. Don't conflate the
two: listing `runs/` for "recent activity" mixes flat pre-migration status-ledger
keys (frozen at 2026-07-03T22:01:08Z, will never update again) with these per-run
subfolders. Plain `aws s3 ls runs/` also interleaves CommonPrefixes (folders)
with flat keys alphabetically, which is easy to misread as newest-first when it
isn't — use `aws s3api list-objects-v2` with an explicit sort instead, or just
target `runledger/` directly.

**Why:** discovered during a 2026-07-10 proactive health check — nearly misdiagnosed
a 7-day "ledger gap" (and separately, two apparently-missing daily jobs) that was
actually just querying the wrong, frozen prefix. Cost real time to untangle.

**How to apply:** always list `runledger/` (not `runs/`) for status/exit-code/log-tail
triage — `s3://<bucket>/runledger/` and, for the API path, `GET {LineupApiUrl}/v1/runs`
(the handler already points at the right prefix). If `runledger/` ever looks wrong or
empty, check `docs/aws-deployment.md` first — it's kept current — before trusting this
agent's own embedded workflow instructions on this specific point.

**Related gotcha — trigger field:** `RUN_TRIGGER` only reads `manual` in the ledger
when a run is launched through the Lineup API's `POST /v1/jobs/{name}` (which sets
`RUN_TRIGGER=manual` on the task override). A raw `aws ecs run-task` invoked by hand
— e.g. ad-hoc testing during a deploy — still records `trigger: schedule`, because
that's entrypoint.sh's default (`TRIGGER=${RUN_TRIGGER:-schedule}`). A burst of
same-day "schedule"-triggered runs outside the normal cron cadence (observed:
projection-site fired 13× on 2026-06-30 during initial rollout, and a few extra
times 2026-07-06 to 07-08 during active development matching commits from that
window) is more likely manual dev/test activity than an EventBridge misconfiguration
— cross-check timestamps against `git log` / ECR push history before calling it a
scheduling bug.
