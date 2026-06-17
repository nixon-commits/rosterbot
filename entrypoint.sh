#!/bin/sh
# Entrypoint for Fargate runs: warm state from S3, run the bot, save state back.
# STATE_BUCKET is injected by the task definition. The command (e.g. "optimize
# --matchup") is passed as container args by the EventBridge target override or
# by the API's RunTask override (which also sets RUN_TRIGGER=manual).
set -u

# The TTL Cache (cache/ prefix) is NOT synced here — the bot reads/writes it
# per-key directly to S3 via cache.Store (STATE_BUCKET). Only the chromedp
# session cookie and the claims ledger/cursor need bulk sync.
sync_down() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync "s3://$STATE_BUCKET/session/"  ./.fantrax-cache/ --quiet || true
  aws s3 sync "s3://$STATE_BUCKET/claims/"   ./.waivers/       --quiet || true
  aws s3 sync "s3://$STATE_BUCKET/backtest/" ./.backtest/      --quiet || true
}

sync_up() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync ./.fantrax-cache/ "s3://$STATE_BUCKET/session/"  --quiet || true
  aws s3 sync ./.waivers/       "s3://$STATE_BUCKET/claims/"   --quiet || true
  aws s3 sync ./.backtest/      "s3://$STATE_BUCKET/backtest/" --quiet || true
  # Publish the recap site when present (recap-site writes ./dist).
  [ -d ./dist ] && [ -n "${SITE_BUCKET:-}" ] && aws s3 sync ./dist/ "s3://$SITE_BUCKET/" --delete --quiet || true
}

# run_id derives a stable id from the ECS task metadata (the API returns this
# same id from RunTask so the app can poll the ledger for it). Falls back to a
# timestamp when metadata is unavailable (e.g. local runs).
run_id() {
  if [ -n "${ECS_CONTAINER_METADATA_URI_V4:-}" ]; then
    arn=$(curl -s "$ECS_CONTAINER_METADATA_URI_V4/task" 2>/dev/null \
      | sed -n 's/.*"TaskARN":"\([^"]*\)".*/\1/p')
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
# code through the pipe (POSIX sh has no PIPESTATUS).
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
