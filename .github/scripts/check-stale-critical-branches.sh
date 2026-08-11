#!/usr/bin/env bash
# Finds remote branches whose *content* differs from main on safety-critical
# paths — and where that content is actually the branch's own unlanded
# work, not just main having moved forward since the branch forked — and
# have sat that way past a staleness threshold.
#
# Content diff (not commit-ancestry) is deliberate: a squash-merged branch
# stays "ahead" of main by commit count forever, but once its content on the
# critical paths matches main, it's no longer a real gap. That two-tree diff
# is symmetric, though, so it's intersected with the branch's own
# diff-since-fork to recover direction — otherwise a branch that's merely
# behind main gets every file main has since touched misattributed to it.
#
# See docs/superpowers/specs/2026-08-11-stale-critical-branch-alert-design.md.
set -euo pipefail

BASE_REF="${BASE_REF:-origin/main}"
CRITICAL_PATHS="${CRITICAL_PATHS:-infra/ internal/opsalert/ opsnotify/}"
STALE_HOURS="${STALE_HOURS:-24}"

now=$(date +%s)
stale_found=0
report=""

# shellcheck disable=SC2086
for branch in $(git for-each-ref --format='%(refname:short)' refs/remotes/origin \
  | grep -v -F -x -e 'origin' -e "${BASE_REF}"); do

  # Direct two-tree diff (no dots) — deliberately NOT `A...B`, which diffs
  # from the merge-base to B and ignores whatever main did after the fork.
  # That symmetric form is right for "what did this branch change" (a PR
  # view) but wrong here: it can't tell a real gap from a squash-merged
  # branch whose content main already carries under a different SHA.
  #
  # Filtered to *.go: any long-lived branch drifts from main's go.mod/
  # go.sum via routine dependabot bumps regardless of whether it carries a
  # real unlanded fix, which made two ancient, unrelated branches
  # (feat/recap-access-logs, feat/recap-views-tab) false-positive on the
  # unfiltered directory diff. Only source changes are the actual signal.
  # shellcheck disable=SC2086
  still_differs=$(git diff --name-only "${BASE_REF}" "${branch}" -- ${CRITICAL_PATHS} 2>/dev/null | grep -E '\.go$' | sort || true)
  if [ -z "$still_differs" ]; then
    continue
  fi

  # The two-tree diff above is symmetric — it can't tell "the branch
  # introduced this" from "main moved forward on this after the branch
  # forked and never rebased". Intersect with the branch's OWN diff since
  # its fork point so only files the branch itself actually changed can be
  # attributed to it. This recovers direction without regressing the
  # squash-merge case above: a branch's diff-since-fork can still differ
  # from main after main independently squash-merges the same content under
  # a new SHA, so both checks are needed together, not either alone.
  merge_base=$(git merge-base "${BASE_REF}" "${branch}" 2>/dev/null || true)
  if [ -z "$merge_base" ]; then
    continue
  fi

  # shellcheck disable=SC2086
  branch_changed=$(git diff --name-only "${merge_base}" "${branch}" -- ${CRITICAL_PATHS} 2>/dev/null | grep -E '\.go$' | sort || true)
  # comm -12 requires both inputs sorted, hence the `sort` above on both sides.
  diff_files=$(comm -12 <(printf '%s\n' "$still_differs") <(printf '%s\n' "$branch_changed") | grep -v '^$' || true)
  if [ -z "$diff_files" ]; then
    continue
  fi

  # shellcheck disable=SC2086
  last_touch=$(git log -1 --format=%ct "${merge_base}..${branch}" -- $diff_files 2>/dev/null || true)
  if [ -z "$last_touch" ]; then
    continue
  fi

  age_hours=$(( (now - last_touch) / 3600 ))
  if [ "$age_hours" -le "$STALE_HOURS" ]; then
    continue
  fi

  stale_found=1
  file_count=$(echo "$diff_files" | wc -l | tr -d ' ')
  # Cap the file list per branch: a branch with a dozen+ touched files (seen
  # on real long-abandoned branches in this repo) blew the whole report past
  # Pushover's 1024-char cap after only two branches, silently dropping the
  # rest of the report before the caller-side truncation even ran.
  files_oneline=$(echo "$diff_files" | head -4 | tr '\n' ',' | sed 's/,$//; s/,/, /g')
  if [ "$file_count" -gt 4 ]; then
    files_oneline="${files_oneline} (+$((file_count - 4)) more)"
  fi
  short_name="${branch#origin/}"
  line="${short_name} — ${age_hours}h stale — ${files_oneline}"
  echo "STALE: $line"
  report="${report}${line}"$'\n'
done

if [ "$stale_found" -eq 0 ]; then
  echo "No stale critical-path branches found."
  exit 0
fi

echo ""
echo "Critical-path branches stale > ${STALE_HOURS}h:"
printf '%s' "$report"

pushover_message="$report"
if [ ${#pushover_message} -gt 1024 ]; then
  pushover_message="${pushover_message:0:1021}..."
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "report<<STALE_REPORT_EOF"
    printf '%s' "$pushover_message"
    echo "STALE_REPORT_EOF"
  } >> "$GITHUB_OUTPUT"
fi

exit 1
