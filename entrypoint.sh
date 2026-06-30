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
CMD="$*"

# Record the run as started (best-effort; never block the actual job on it).
./rosterbot run-ledger --id "$ID" --command "$CMD" --status RUNNING \
  --started "$STARTED" --trigger "$TRIGGER" || true

# Run the bot, mirroring output to both the container logs (CloudWatch) and a
# file for the ledger's log_tail. The braces+echo capture the bot's real exit
# code through the pipe (POSIX sh has no PIPESTATUS). RUN_ID lets the bot tag
# activity-feed events with the run that produced them.
export RUN_ID="$ID"
{ ./rosterbot "$@" 2>&1; echo $? >/tmp/rosterbot.rc; } | tee /tmp/rosterbot.log
rc=$(cat /tmp/rosterbot.rc 2>/dev/null || echo 1)

ENDED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
STATUS=SUCCESS
[ "$rc" = "0" ] || STATUS=FAILED

./rosterbot run-ledger --id "$ID" --command "$CMD" --status "$STATUS" \
  --exit-code "$rc" --started "$STARTED" --ended "$ENDED" \
  --trigger "$TRIGGER" --log-file /tmp/rosterbot.log || true

sync_up
exit "$rc"
