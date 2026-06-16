#!/bin/sh
# Entrypoint for Fargate runs: warm state from S3, run the bot, save state back.
# STATE_BUCKET is injected by the task definition. The command (e.g. "optimize
# --matchup") is passed as container args by the EventBridge target override.
set -u

# The TTL Cache (cache/ prefix) is NOT synced here — the bot reads/writes it
# per-key directly to S3 via cache.Store (STATE_BUCKET). Only the chromedp
# session cookie and the claims ledger/cursor need bulk sync.
sync_down() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync "s3://$STATE_BUCKET/session/" ./.fantrax-cache/ --quiet || true
  aws s3 sync "s3://$STATE_BUCKET/claims/"  ./.waivers/       --quiet || true
}

sync_up() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync ./.fantrax-cache/ "s3://$STATE_BUCKET/session/" --quiet || true
  aws s3 sync ./.waivers/       "s3://$STATE_BUCKET/claims/"  --quiet || true
  # Publish the recap site when present (recap-site writes ./dist).
  [ -d ./dist ] && [ -n "${SITE_BUCKET:-}" ] && aws s3 sync ./dist/ "s3://$SITE_BUCKET/" --delete --quiet || true
}

sync_down
./rosterbot "$@"
rc=$?
sync_up
exit $rc
