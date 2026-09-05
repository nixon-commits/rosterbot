#!/usr/bin/env bash
# cdk_deploy_retry.sh — bounded retry wrapper around `cdk deploy`, guarding
# against CodeBuild's lack of build serialization (rosterbot-udd).
#
# ConcurrentBuildLimit stays -1 in infra.go (rosterbot-7p1i / rosterbot-ill):
# CodeBuild's limit THROTTLES the excess build rather than queueing it, so a
# second build landing inside the limit is DISCARDED — no build record, no
# log, no Pushover, nothing red anywhere. That is strictly worse than the
# race this script mitigates, so it must never be turned back on; see the
# comment on ConcurrentBuildLimit at infra/infra.go for the measured history.
#
# The remaining, real problem is that nothing serializes two builds that
# both reach `cdk deploy` on the SAME stack within a few minutes of each
# other. CloudFormation itself refuses the second UpdateStack while the first
# is still in progress, so the loser's `cdk deploy` fails with the stack in
# an `..._IN_PROGRESS` state — a lock that clears in minutes on its own. This
# script retries ONLY that specific, self-clearing failure; anything else
# (a real deploy error, a stack in ROLLBACK, a syntax error) exits non-zero
# immediately so `set -e` upstream still fails the build loudly — the half
# of rosterbot-ill that must not regress. Two builds racing must each end
# either SUCCEEDED or FAILED; neither may be silently dropped nor silently
# waved through as green over an undeployed change.
#
# Usage:
#   cdk_deploy_retry.sh <logfile> -- <deploy command and args...>
#
# Env overrides (tests only — production relies on the defaults):
#   CDK_DEPLOY_RETRY_MAX_ATTEMPTS    default 5
#   CDK_DEPLOY_RETRY_BACKOFF_SECONDS default 30 (attempt * this many seconds)

set -u

CDK_DEPLOY_RETRY_MAX_ATTEMPTS="${CDK_DEPLOY_RETRY_MAX_ATTEMPTS:-5}"
CDK_DEPLOY_RETRY_BACKOFF_SECONDS="${CDK_DEPLOY_RETRY_BACKOFF_SECONDS:-30}"

# is_concurrent_update_failure <logfile>
#
# True iff the captured deploy output carries one of the known signatures of
# a CloudFormation stack already being mutated by another build. Exposed as
# its own function (rather than inlined in the loop below) so it can be
# exercised directly against canned log text — the retry decision is the
# part that must never silently drift, so it needs a test that reads this
# exact matcher rather than one that greps buildspec.yml's prose.
#
# The list is deliberately over-inclusive rather than keyed to one exact
# CloudFormation error string: the CDK CLI surfaces CloudFormation's message
# TEXT far more often than a named exception class (e.g. "is in
# UPDATE_IN_PROGRESS state and can not be updated"), and the exact wording
# has already been observed to vary (can not / cannot, IN_PROGRESS state vs
# "is currently being updated", the SDK's own OperationInProgressException /
# ResourceConflictException). Matching broadly is safe here because the
# failure mode on the other side of a false match is just one more retry
# attempt against a deploy that was going to fail again anyway for a
# different reason — it still exits non-zero once attempts are exhausted.
is_concurrent_update_failure() {
  local logfile="$1"
  grep -qE \
    'UPDATE_IN_PROGRESS|CREATE_IN_PROGRESS|_IN_PROGRESS state|is currently being updated|OperationInProgressException|ResourceConflictException|cannot be updated|can not be updated' \
    "$logfile"
}

# cdk_deploy_with_retry <logfile> -- <command...>
#
# Runs <command...>, teeing its combined output to <logfile> so CodeBuild
# still streams it live (rather than buffering until the whole retry loop
# finishes). Retries only on is_concurrent_update_failure, up to
# CDK_DEPLOY_RETRY_MAX_ATTEMPTS attempts, with an attempt*BACKOFF_SECONDS
# sleep between tries so the other build's stack lock has time to clear.
#
# Deliberately does NOT rely on the caller's `set -e` to react to a failed
# deploy: this function is invoked as its own process (a separate `bash
# cdk_deploy_retry.sh` command in buildspec.yml), so a failing deploy inside
# the loop cannot abort the enclosing buildspec command block before this
# function has made its retry-or-give-up decision. `${PIPESTATUS[0]}` reads
# the exit code of "$@" rather than of `tee`, which always succeeds.
cdk_deploy_with_retry() {
  local logfile="$1"
  shift
  if [ "${1:-}" = "--" ]; then
    shift
  fi

  local attempt=1
  local rc
  while :; do
    "$@" 2>&1 | tee "$logfile"
    rc=${PIPESTATUS[0]}
    if [ "$rc" -eq 0 ]; then
      return 0
    fi

    if [ "$attempt" -ge "$CDK_DEPLOY_RETRY_MAX_ATTEMPTS" ]; then
      echo "cdk_deploy_retry: attempt ${attempt}/${CDK_DEPLOY_RETRY_MAX_ATTEMPTS} failed (exit ${rc}); attempts exhausted, giving up" >&2
      return "$rc"
    fi

    if ! is_concurrent_update_failure "$logfile"; then
      echo "cdk_deploy_retry: attempt ${attempt} failed (exit ${rc}) with no concurrent-update signature; not retrying" >&2
      return "$rc"
    fi

    local backoff=$((attempt * CDK_DEPLOY_RETRY_BACKOFF_SECONDS))
    echo "cdk_deploy_retry: attempt ${attempt}/${CDK_DEPLOY_RETRY_MAX_ATTEMPTS} hit a concurrent-update signature; retrying in ${backoff}s" >&2
    sleep "$backoff"
    attempt=$((attempt + 1))
  done
}

# Only run the loop when executed directly, so a test can `source` this file
# to reach is_concurrent_update_failure / cdk_deploy_with_retry without also
# triggering a live deploy attempt.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  if [ "$#" -lt 2 ]; then
    echo "usage: cdk_deploy_retry.sh <logfile> -- <command...>" >&2
    exit 2
  fi
  cdk_deploy_with_retry "$@"
  exit $?
fi
