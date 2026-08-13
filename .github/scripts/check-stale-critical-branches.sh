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

# Answers, for ONE file, whether the branch's own change to it is still
# absent from main. This is the content question that the two file-name
# filters below can only approximate.
#
# Each of those filters closes half of the attribution problem and neither
# closes the case where both sides touched the same file: the branch's change
# can already be on main under a different SHA (squash merge) while main has
# since moved that same file forward for unrelated reasons. The file then
# survives both filters permanently. That is what made feat/trades-tab alert
# every 6h for four days from 2026-08-12 ("87h stale - infra/infra.go") after
# its two GrantRead lines were already on main: holding the branch constant
# and moving only the base from 312a90f~1 to origin/main flipped the verdict,
# with no commit to the branch in between. Because the reported age is the
# branch's own last-touch, which never moves, such an alert never ages out.
#
# The test is a reverse-apply: if the branch's own patch for this file backs
# out cleanly against main's copy, then main already carries that content.
# Comparing content rather than SHAs is what makes it survive squash merges,
# and scoping to one file at a time is what keeps main's unrelated edits
# elsewhere in the file from defeating it.
#
# Returns: 0 = unlanded (flag it), 1 = landed (skip it), 2 = indeterminate.
branch_change_landed_status() {
  local branch="$1" merge_base="$2" file="$3" tmp
  tmp=$(mktemp -d)
  mkdir -p "${tmp}/$(dirname "$file")"

  # A file main does not have at all plainly is not carrying the branch's
  # change — a new critical-path file is exactly the unlanded case.
  if ! git show "${BASE_REF}:${file}" > "${tmp}/${file}" 2>/dev/null; then
    rm -rf "$tmp"
    return 0
  fi

  if git diff "${merge_base}" "${branch}" -- "$file" \
    | (cd "$tmp" && git apply --reverse --check -p1 - 2>/dev/null); then
    rm -rf "$tmp"
    return 1
  fi

  rm -rf "$tmp"
  return 2
}

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

  # Both filters above are file-name set operations; narrow the survivors to
  # the files whose CONTENT is genuinely still missing from main.
  #
  # An indeterminate result (the patch neither reverse-applies nor is clearly
  # absent — heavy drift, a conflicting rewrite) is reported rather than
  # dropped. This alert exists because nothing paged through the 54-run
  # 08-01..08-03 outage (rosterbot-v4l), so its failure mode has to be a
  # duplicate, never a silence — the same posture as check-pins refusing to
  # skip on missing jq (rosterbot-00e) and the opsalert marker store degrading
  # to a repeat alert rather than a dropped one (rosterbot-chs).
  unlanded_files=""
  for f in $diff_files; do
    rc=0
    branch_change_landed_status "${branch}" "${merge_base}" "$f" || rc=$?
    if [ "$rc" -ne 1 ]; then
      unlanded_files="${unlanded_files}${f}"$'\n'
    fi
  done
  diff_files=$(printf '%s' "$unlanded_files" | grep -v '^$' || true)
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
