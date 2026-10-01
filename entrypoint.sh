#!/bin/sh
# Entrypoint for Fargate runs: warm state from S3, run the bot, save state back.
# STATE_BUCKET is injected by the task definition. The command (e.g. "optimize
# --matchup") is passed as container args by the EventBridge target override or
# by the API's RunTask override (which also sets RUN_TRIGGER=manual).
set -u

# The TTL Cache (cache/ prefix) is NOT synced here — the bot reads/writes it
# per-key directly to S3 via cache.Store (STATE_BUCKET). Only the chromedp
# session cookie and the claims ledger/cursor need bulk sync.
#
# The S3 dir-sync, --delete site mirroring, and CloudFront invalidation that
# used to shell out to awscli now live in the bot itself (internal/statesync),
# so the runtime image no longer ships python+awscli. Both subcommands are
# best-effort and exit 0 even on a partial failure, so the `|| true` is belt-
# and-suspenders. STATE_BUCKET/SITE_BUCKET/REPORT_BUCKET and the *_CF_DIST_ID
# vars are read from the environment by the bot, same as before.
sync_down() {
  ./rosterbot sync-down || true
}

sync_up() {
  ./rosterbot sync-up || true
}

# run_id derives a stable id from the ECS task metadata (the API returns this
# same id from RunTask so the app can poll the ledger for it). Falls back to a
# timestamp when metadata is unavailable (e.g. local runs).
run_id() {
  if [ -n "${ECS_CONTAINER_METADATA_URI_V4:-}" ]; then
    meta=""
    if command -v curl >/dev/null 2>&1; then
      meta=$(curl -s "$ECS_CONTAINER_METADATA_URI_V4/task" 2>/dev/null)
    elif command -v python3 >/dev/null 2>&1; then
      meta=$(python3 -c "import urllib.request,os,sys; sys.stdout.write(urllib.request.urlopen(os.environ['ECS_CONTAINER_METADATA_URI_V4']+'/task').read().decode())" 2>/dev/null)
    fi
    arn=$(printf '%s' "$meta" | sed -n 's/.*"TaskARN":"\([^"]*\)".*/\1/p')
    if [ -n "$arn" ]; then
      echo "${arn##*/}"
      return
    fi
  fi
  echo "local-$(date -u +%Y%m%d%H%M%S)"
}

sync_down

ID=$(run_id)
STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
TRIGGER=${RUN_TRIGGER:-schedule}
# Whose run this is, under per-tenant fan-out. Empty today (and therefore
# omitted from the record entirely), which internal/opsalert reads as the one
# pre-fan-out tenant. It has to be on the record rather than inferred later:
# every tenant runs the *same command string*, so without it a tenant failing
# every hour grades healthy on its neighbours' successes, and a tenant whose
# task stops launching is invisible.
RUN_USER=${RUN_USER_ID:-}
CMD="$*"

# Record the run as started (best-effort; never block the actual job on it).
./rosterbot run-ledger --id "$ID" --command "$CMD" --status RUNNING \
  --started "$STARTED" --trigger "$TRIGGER" --user "$RUN_USER" || true

# Run the bot, mirroring output to both the container logs (CloudWatch) and a
# file for the ledger's log_tail. The braces+echo capture the bot's real exit
# code through the pipe (POSIX sh has no PIPESTATUS). RUN_ID lets the bot tag
# activity-feed events with the run that produced them.
#
# RUN_OUTCOME_FILE is the other channel a subcommand has for the terminal
# ledger write: an exit code alone cannot say "this run exited 0 but left the
# tenant needing to act" (cmd/connect.go's routeTenant route is deliberately
# exit-0, so opsalert does not page the operator for something only the user
# can fix). Removed before the run so a stale file from an earlier invocation
# of this same binary cannot leak in — belt-and-braces, since the container is
# fresh per run anyway. See lineupapi.RunOutcomeTenantActionable and cmd's
# recordRunOutcome.
export RUN_ID="$ID"
export RUN_OUTCOME_FILE=/tmp/rosterbot.outcome
rm -f "$RUN_OUTCOME_FILE" 2>/dev/null || true
# terminal_ledger writes the run's FINAL ledger row. It is a function because
# two paths reach it -- the normal end of the run and the SIGTERM trap below --
# and a second copy of the command would be a second thing to keep in sync.
# cmd's TestEntrypointRunOutcomeFileFlowsIntoLedgerOutcome also requires exactly
# one terminal (--exit-code) write in this file, which the function preserves.
terminal_ledger() {
  _rc=$1
  _status=SUCCESS
  [ "$_rc" = "0" ] || _status=FAILED
  ./rosterbot run-ledger --id "$ID" --command "$CMD" --status "$_status" \
    --exit-code "$_rc" --started "$STARTED" --ended "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --trigger "$TRIGGER" --user "$RUN_USER" --log-file /tmp/rosterbot.log \
    --outcome "$(cat "$RUN_OUTCOME_FILE" 2>/dev/null || true)" || true
}

# A stopped task must still leave a TERMINAL ledger row. ECS stops a task by
# sending SIGTERM (through tini, see the Dockerfile ENTRYPOINT) and SIGKILLs it
# stopTimeout later; without a trap the shell dies before the write and the row
# stays RUNNING forever. rosterbot-5zp1's killed control run is still sitting in
# the ledger exactly that way, and the audit's "no RUNNING rows" control reads
# it as an incident rather than as a task somebody stopped.
#
# sync_up is deliberately NOT called on this path. A stopped run's ./dist is
# partial or empty, and publishing it would mirror that over the live site --
# which statesync.Up now also refuses on its own (rosterbot-5zp1), belt and
# braces, because these two defences fail independently.
on_term() {
  trap - TERM INT
  echo "entrypoint: stopped by signal; recording the run as FAILED (143), not publishing" >&2
  terminal_ledger 143
  exit 143
}
trap on_term TERM INT

# The bot runs in the BACKGROUND and we wait for it, rather than in the
# foreground as before: a POSIX shell cannot run a trap while it is blocked on a
# foreground child, so a trap alone would never fire for the case that needs it
# most -- a bot that is hung and will never return on its own. `wait` IS
# interrupted by a trapped signal. The braces+echo still capture the bot's real
# exit code through the pipe (POSIX sh has no PIPESTATUS).
{ ./rosterbot "$@" 2>&1; echo $? >/tmp/rosterbot.rc; } | tee /tmp/rosterbot.log &
wait $!
rc=$(cat /tmp/rosterbot.rc 2>/dev/null || echo 1)

terminal_ledger "$rc"

sync_up
exit "$rc"
