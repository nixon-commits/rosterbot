#!/usr/bin/env bash
# Test harness for check-stale-critical-branches.sh. Builds a throwaway
# bare "origin" + working clone with controlled, backdated commits so the
# detection logic is verified deterministically, independent of this repo's
# real (constantly-changing) branch state.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/check-stale-critical-branches.sh"

fail() {
  echo "FAIL: $1"
  exit 1
}

# Emits git's raw "@<unix-epoch> <tz-offset>" date form so GIT_AUTHOR_DATE/
# GIT_COMMITTER_DATE are unambiguous — an ISO string with no offset gets
# parsed in the local timezone, which silently skews "hours ago" by however
# far local time sits from UTC and made this test flaky on a UTC-7 machine.
epoch_hours_ago() {
  local hours="$1"
  echo "@$(( $(date +%s) - hours * 3600 )) +0000"
}

setup_repo() {
  local tmpdir bare work
  tmpdir=$(mktemp -d)
  bare="$tmpdir/origin.git"
  work="$tmpdir/work"

  # The branch name is pinned explicitly rather than inherited from
  # init.defaultBranch. That setting is a per-machine preference: a developer
  # who has set it to "main" sees this pass, while a runner using git's
  # built-in default puts the first commit on "master" and every
  # `git push origin main` below fails with "src refspec main does not match
  # any" — green locally, red in CI, for a reason nothing in the output names.
  #
  # -b covers the clone (which adopts the bare repo's HEAD), and the
  # symbolic-ref covers the work tree directly, so neither depends on the
  # other's behaviour on an empty repository.
  git init --bare -q -b main "$bare"
  git clone -q "$bare" "$work"
  (
    cd "$work"
    git symbolic-ref HEAD refs/heads/main
    git config user.email test@example.com
    git config user.name Test
    git config commit.gpgsign false
  )
  echo "$tmpdir"
}

commit_file() {
  # commit_file <workdir> <path> <content> <iso-date>
  local work="$1" path="$2" content="$3" date="$4"
  mkdir -p "$(dirname "${work}/${path}")"
  echo "$content" > "${work}/${path}"
  (
    cd "$work"
    git add "$path"
    GIT_AUTHOR_DATE="$date" GIT_COMMITTER_DATE="$date" git commit -q -m "commit ${path}=${content} @ ${date}"
  )
}

test_flags_and_filters_correctly() {
  local tmpdir work old_date recent_date
  tmpdir=$(setup_repo)
  work="$tmpdir/work"
  old_date=$(epoch_hours_ago 26)
  recent_date=$(epoch_hours_ago 1)

  commit_file "$work" "infra/infra.go" "v1" "$(epoch_hours_ago 48)"
  (cd "$work" && git push -q origin main)

  # Case: stale-branch — old, unmerged critical-path change -> SHOULD flag.
  # Content value is unique (not reused by any later commit on main) so this
  # case can't accidentally coincide with the squash-merge case below.
  (cd "$work" && git checkout -q -b stale-branch)
  commit_file "$work" "infra/infra.go" "v-stale" "$old_date"
  (cd "$work" && git push -q origin stale-branch)

  # Case: fresh-branch — recent critical-path change -> should NOT flag
  (cd "$work" && git checkout -q main && git checkout -q -b fresh-branch)
  commit_file "$work" "internal/opsalert/x.go" "v1" "$recent_date"
  (cd "$work" && git push -q origin fresh-branch)

  # Case: irrelevant-branch — old change, but outside critical paths -> should NOT flag
  (cd "$work" && git checkout -q main && git checkout -q -b irrelevant-branch)
  commit_file "$work" "README.md" "docs" "$old_date"
  (cd "$work" && git push -q origin irrelevant-branch)

  # Case: squash-merged-branch — old critical-path commit, but main already
  # carries identical content under a different SHA (simulating a squash
  # merge) -> should NOT flag
  (cd "$work" && git checkout -q main && git checkout -q -b squash-merged-branch)
  commit_file "$work" "infra/infra.go" "v-squashed" "$old_date"
  (cd "$work" && git push -q origin squash-merged-branch)
  (cd "$work" && git checkout -q main)
  commit_file "$work" "infra/infra.go" "v-squashed" "$(epoch_hours_ago 2)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 1 ] || fail "expected exit 1 (stale branch present), got $status. Output:\n$output"
  echo "$output" | grep -q "stale-branch" || fail "expected stale-branch to be flagged. Output:\n$output"
  echo "$output" | grep -q "fresh-branch" && fail "fresh-branch should not be flagged. Output:\n$output"
  echo "$output" | grep -q "irrelevant-branch" && fail "irrelevant-branch should not be flagged. Output:\n$output"
  echo "$output" | grep -q "squash-merged-branch" && fail "squash-merged-branch should not be flagged (content already matches main). Output:\n$output"

  echo "PASS: test_flags_and_filters_correctly"
}

test_clean_when_nothing_stale() {
  local tmpdir work
  tmpdir=$(setup_repo)
  work="$tmpdir/work"

  commit_file "$work" "infra/infra.go" "v1" "$(epoch_hours_ago 1)"
  (cd "$work" && git push -q origin main)
  (cd "$work" && git checkout -q -b quiet-branch)
  commit_file "$work" "README.md" "docs" "$(epoch_hours_ago 1)"
  (cd "$work" && git push -q origin quiet-branch)
  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 0 ] || fail "expected exit 0 (nothing stale), got $status. Output:\n$output"
  echo "$output" | grep -q "No stale critical-path branches found." || fail "expected clean message. Output:\n$output"

  echo "PASS: test_clean_when_nothing_stale"
}

test_stale_go_masked_by_recent_non_go() {
  # Regression test for bug where last_touch wasn't filtered to .go files.
  # A branch with an old .go change plus a more recent non-.go commit in the
  # same critical path would have its age masked by the fresher commit.
  # This test verifies the branch is still correctly flagged as stale.
  local tmpdir work old_go_date recent_gomod_date
  tmpdir=$(setup_repo)
  work="$tmpdir/work"
  old_go_date=$(epoch_hours_ago 30)
  recent_gomod_date=$(epoch_hours_ago 1)

  commit_file "$work" "infra/infra.go" "v1" "$(epoch_hours_ago 48)"
  (cd "$work" && git push -q origin main)

  # Create branch with old .go change
  (cd "$work" && git checkout -q -b masked-branch)
  commit_file "$work" "infra/safety.go" "stale-code" "$old_go_date"
  # Then add a more recent non-.go commit (go.mod) in the same critical path
  commit_file "$work" "infra/go.mod" "module infra" "$recent_gomod_date"
  (cd "$work" && git push -q origin masked-branch)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  # Should flag as stale (exit 1) because the actual .go change is 30h old,
  # even though go.mod is only 1h old
  [ "$status" -eq 1 ] || fail "expected exit 1 (stale .go change should be detected), got $status. Output:\n$output"
  echo "$output" | grep -q "masked-branch" || fail "expected masked-branch to be flagged despite recent go.mod. Output:\n$output"

  echo "PASS: test_stale_go_masked_by_recent_non_go"
}

test_excludes_files_the_branch_never_touched() {
  # Regression test for the direction-blind two-tree diff (Finding 1 of the
  # 2026-08-11 final review): a branch with its OWN real unlanded
  # critical-path change must still be flagged, but the reported file list
  # must not also include a *different* critical-path file that only main
  # touched after the branch forked. The plain two-tree diff can't tell
  # "the branch introduced this" from "main moved forward on this" — it
  # conflated both into one file list. Verified this reproduces against the
  # pre-fix script: exit 1, with the report listing BOTH infra/infra.go
  # (which behind-branch never touched) and internal/opsalert/y.go (which it
  # did). The fix intersects the two-tree diff with the branch's
  # diff-since-fork so only files the branch itself changed can appear.
  local tmpdir work old_date main_only_date
  tmpdir=$(setup_repo)
  work="$tmpdir/work"

  commit_file "$work" "infra/infra.go" "v1" "$(epoch_hours_ago 72)"
  (cd "$work" && git push -q origin main)

  # Branch forks from main and adds its own, genuinely unlanded, old change
  # to a DIFFERENT critical-path file.
  (cd "$work" && git checkout -q -b behind-branch)
  old_date=$(epoch_hours_ago 47)
  commit_file "$work" "internal/opsalert/y.go" "own-work" "$old_date"
  (cd "$work" && git push -q origin behind-branch)

  # main moves on independently: a NEW value on infra/infra.go, a file
  # behind-branch never touched.
  (cd "$work" && git checkout -q main)
  main_only_date=$(epoch_hours_ago 30)
  commit_file "$work" "infra/infra.go" "v2" "$main_only_date"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 1 ] || fail "expected exit 1 (behind-branch has its own real unlanded change), got $status. Output:\n$output"
  echo "$output" | grep -q "behind-branch" || fail "expected behind-branch to be flagged for its own y.go change. Output:\n$output"
  echo "$output" | grep -q "internal/opsalert/y.go" || fail "expected the branch's own file to appear in the report. Output:\n$output"
  echo "$output" | grep -q "infra/infra.go" && fail "infra/infra.go should NOT be attributed to behind-branch -- the branch never touched it, only main moved it forward. Output:\n$output"

  echo "PASS: test_excludes_files_the_branch_never_touched"
}

test_purely_behind_branch_not_flagged() {
  # Companion invariant: a branch with ZERO commits of its own touching any
  # critical-path file must never be flagged at all, even though a symmetric
  # two-tree diff shows the critical-path file as "different" once main
  # moves it forward after the fork. Note: this exact shape happens to
  # already pass on the pre-fix script too (its last_touch lookup already
  # scopes to commits reachable only from the branch, which is empty here),
  # so on its own it doesn't distinguish pre/post-fix behavior -- it's kept
  # as a correctness pin for the intersection logic going forward.
  # test_excludes_files_the_branch_never_touched above is the test that
  # actually reproduces and proves the fix for Finding 1.
  local tmpdir work
  tmpdir=$(setup_repo)
  work="$tmpdir/work"

  commit_file "$work" "infra/infra.go" "v1" "$(epoch_hours_ago 72)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git checkout -q -b behind-branch)
  commit_file "$work" "README.md" "unrelated" "$(epoch_hours_ago 47)"
  (cd "$work" && git push -q origin behind-branch)

  (cd "$work" && git checkout -q main)
  commit_file "$work" "infra/infra.go" "v2" "$(epoch_hours_ago 30)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 0 ] || fail "expected exit 0 (behind-branch never touched a critical-path file itself), got $status. Output:\n$output"
  echo "$output" | grep -q "behind-branch" && fail "behind-branch should not be flagged. Output:\n$output"

  echo "PASS: test_purely_behind_branch_not_flagged"
}

# Shared fixture bodies for the two content-attribution tests below. Written
# as realistic Go with stable context either side of the edit point — see the
# comment in test_landed_branch_not_reflagged_when_main_touches_same_file.
FIXTURE_BASE='package main

func Stack() {
	grantA()
	grantB()
	grantC()

	sched("Lineup")
	sched("Grade")
}'

FIXTURE_WITH_TRADES='package main

func Stack() {
	grantA()
	grantB()
	grantC()
	grantTrades()
	grantTradeValues()

	sched("Lineup")
	sched("Grade")
}'

FIXTURE_WITH_TRADES_AND_FOOTBALL='package main

func Stack() {
	grantA()
	grantB()
	grantC()
	grantTrades()
	grantTradeValues()

	sched("Lineup")
	sched("Grade")
	sched("FootballValues")
	sched("FootballTrades")
}'

# main took the branch's grants AND then inserted a line immediately adjacent
# to them, inside the patch's context window. This is the live shape from
# 2026-08-13: crq.3 added reports/ grants at infra/infra.go:269 while
# feat/trades-tab's two lines sat at 279-280.
FIXTURE_WITH_REPORTS_AND_TRADES='package main

func Stack() {
	grantA()
	grantB()
	grantC()
	grantReports()
	grantTrades()
	grantTradeValues()

	sched("Lineup")
	sched("Grade")
}'

# Branch adds nothing but a closing brace — a line main trivially also has.
FIXTURE_BRACE_ONLY='package main

func Stack() {
	grantA()
	grantB()
	grantC()

	sched("Lineup")
	sched("Grade")
}
}'

# main moved the file forward but NEVER took the branch's grants.
FIXTURE_FOOTBALL_ONLY='package main

func Stack() {
	grantA()
	grantB()
	grantC()

	sched("Lineup")
	sched("Grade")
	sched("FootballValues")
	sched("FootballTrades")
}'

test_landed_branch_not_reflagged_when_main_touches_same_file() {
  # Regression test for the production false positive diagnosed 2026-08-13:
  # feat/trades-tab alerted every 6h ("87h stale — infra/infra.go") for four
  # days after its content had already landed on main.
  #
  # This is the exact shape the two existing guards each half-cover and
  # neither closes. The squash-merge guard (still_differs) clears a landed
  # branch only while main leaves the file alone; the direction guard
  # (branch_changed) excludes only files the branch never touched. When BOTH
  # sides have touched the same file, the file survives both filters forever
  # — because the intersection is computed over FILE NAMES, while the
  # question it stands in for is about CONTENT.
  #
  # Proven in production by holding the branch constant and moving only main:
  # at 312a90f~1 the check passed, at origin/main it alerted, with no commit
  # to the branch in between. The reported age is the branch's own last-touch,
  # which never changes, so the alert fires 4x/day indefinitely.
  local tmpdir work
  tmpdir=$(setup_repo)
  work="$tmpdir/work"

  # The file bodies are deliberately realistic rather than one-liners: the
  # attribution test is a content comparison, and on a degenerate 1-2 line
  # file a change is indistinguishable from a wholesale rewrite for any
  # algorithm (both a reverse-apply and a three-way merge legitimately report
  # a conflict there). infra/infra.go is 700+ lines with the change mid-file,
  # so the fixture mirrors that: the branch's edit sits between stable
  # context, and main's later edit lands elsewhere in the same file.
  commit_file "$work" "infra/infra.go" "$FIXTURE_BASE" "$(epoch_hours_ago 96)"
  (cd "$work" && git push -q origin main)

  # The branch adds its own lines — the analog of feat/trades-tab's two
  # GrantRead lines.
  (cd "$work" && git checkout -q -b landed-branch)
  commit_file "$work" "infra/infra.go" "$FIXTURE_WITH_TRADES" "$(epoch_hours_ago 87)"
  (cd "$work" && git push -q origin landed-branch)

  # main takes that content under a different SHA (squash merge) ...
  (cd "$work" && git checkout -q main)
  commit_file "$work" "infra/infra.go" "$FIXTURE_WITH_TRADES" "$(epoch_hours_ago 80)"
  # ... and then moves the SAME file forward for an unrelated reason, the way
  # the dynasty-football commits did to infra/infra.go on 2026-08-12.
  commit_file "$work" "infra/infra.go" "$FIXTURE_WITH_TRADES_AND_FOOTBALL" "$(epoch_hours_ago 30)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 0 ] || fail "expected exit 0 -- the branch's own change (L2) is already on main, so nothing is unlanded. Got $status. Output:\n$output"
  echo "$output" | grep -q "landed-branch" && fail "landed-branch must not be flagged: its content landed, main merely moved the same file forward afterward. Output:\n$output"

  echo "PASS: test_landed_branch_not_reflagged_when_main_touches_same_file"
}

test_unlanded_work_still_flagged_when_main_touches_same_file() {
  # The false-negative guard, and the reason the content check is a
  # per-file three-state test rather than "skip any file main also touched".
  #
  # Same shape as the test above — branch and main both edit one
  # critical-path file — except the branch's own change was NEVER taken.
  # This must still alert. Suppressing every file both sides touched would
  # make this case silent, which is the exact blindness rosterbot-v4l exists
  # to prevent: alerting infra that sits unlanded is invisible precisely when
  # the outage it guards against happens.
  local tmpdir work
  tmpdir=$(setup_repo)
  work="$tmpdir/work"

  commit_file "$work" "infra/infra.go" "$FIXTURE_BASE" "$(epoch_hours_ago 96)"
  (cd "$work" && git push -q origin main)

  # Branch adds its grants — genuinely unlanded work.
  (cd "$work" && git checkout -q -b unlanded-branch)
  commit_file "$work" "infra/infra.go" "$FIXTURE_WITH_TRADES" "$(epoch_hours_ago 87)"
  (cd "$work" && git push -q origin unlanded-branch)

  # main moves the SAME file forward, but never takes the branch's grants.
  (cd "$work" && git checkout -q main)
  commit_file "$work" "infra/infra.go" "$FIXTURE_FOOTBALL_ONLY" "$(epoch_hours_ago 30)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 1 ] || fail "expected exit 1 -- the branch's grants never landed on main. Got $status. Output:\n$output"
  echo "$output" | grep -q "unlanded-branch" || fail "unlanded-branch must be flagged: its own change is absent from main. Output:\n$output"
  echo "$output" | grep -q "infra/infra.go" || fail "expected infra/infra.go in the report. Output:\n$output"

  echo "PASS: test_unlanded_work_still_flagged_when_main_touches_same_file"
}

test_landed_branch_not_reflagged_when_main_drifts_adjacent() {
  # The residual gap in rosterbot-rx4, hit in production within the hour.
  #
  # rx4's reverse-apply answers "does main already carry this change" only
  # while the surrounding context still matches. Merging the crq epic put
  # grantReports-equivalent lines immediately above feat/trades-tab's two
  # GrantRead lines, inside the patch's context window. The lines themselves
  # were still on main at 279-280 -- the branch was landed -- but the patch
  # stopped reverse-applying, so the check returned indeterminate and the
  # fail-closed policy alerted anyway. Same false positive, different route.
  #
  # The fallback: when the reverse-apply is inconclusive, ask the weaker but
  # position-independent question -- is every line the branch ADDED present
  # in main's copy of the file? Only when some are missing is it genuinely
  # indeterminate.
  local tmpdir work
  tmpdir=$(setup_repo)
  work="$tmpdir/work"

  commit_file "$work" "infra/infra.go" "$FIXTURE_BASE" "$(epoch_hours_ago 96)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git checkout -q -b drifted-branch)
  commit_file "$work" "infra/infra.go" "$FIXTURE_WITH_TRADES" "$(epoch_hours_ago 87)"
  (cd "$work" && git push -q origin drifted-branch)

  # main takes the grants AND inserts an adjacent line, breaking patch context.
  (cd "$work" && git checkout -q main)
  commit_file "$work" "infra/infra.go" "$FIXTURE_WITH_REPORTS_AND_TRADES" "$(epoch_hours_ago 30)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 0 ] || fail "expected exit 0 -- both grants are on main, only the surrounding context moved. Got $status. Output:\n$output"
  echo "$output" | grep -q "drifted-branch" && fail "drifted-branch must not be flagged: its lines are present in main, the patch merely no longer applies. Output:\n$output"

  echo "PASS: test_landed_branch_not_reflagged_when_main_drifts_adjacent"
}

test_trivial_only_additions_stay_indeterminate() {
  # Guard on the fallback's own failure direction. Line-presence is a weaker
  # test than a patch apply: a line can appear in main for reasons that have
  # nothing to do with the branch. A branch whose additions are ALL trivial
  # (blank, or punctuation like a lone brace) would otherwise be declared
  # landed on a coincidence, and this alert must never conclude "landed" by
  # accident -- silence is its one unacceptable failure mode.
  #
  # Such a branch must stay indeterminate and therefore still be reported.
  local tmpdir work
  tmpdir=$(setup_repo)
  work="$tmpdir/work"

  commit_file "$work" "infra/infra.go" "$FIXTURE_BASE" "$(epoch_hours_ago 96)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git checkout -q -b brace-branch)
  commit_file "$work" "infra/infra.go" "$FIXTURE_BRACE_ONLY" "$(epoch_hours_ago 87)"
  (cd "$work" && git push -q origin brace-branch)

  # main drifts adjacent so the reverse-apply is inconclusive and the
  # fallback is the thing under test.
  (cd "$work" && git checkout -q main)
  commit_file "$work" "infra/infra.go" "$FIXTURE_WITH_REPORTS_AND_TRADES" "$(epoch_hours_ago 30)"
  (cd "$work" && git push -q origin main)

  (cd "$work" && git fetch -q origin)

  local output status
  set +e
  output=$(cd "$work" && STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main "$SCRIPT" 2>&1)
  status=$?
  set -e

  rm -rf "$tmpdir"

  [ "$status" -eq 1 ] || fail "expected exit 1 -- a brace-only addition must not be read as landed on a coincidental line match. Got $status. Output:\n$output"
  echo "$output" | grep -q "brace-branch" || fail "brace-branch must still be reported. Output:\n$output"

  echo "PASS: test_trivial_only_additions_stay_indeterminate"
}

test_flags_and_filters_correctly
test_clean_when_nothing_stale
test_stale_go_masked_by_recent_non_go
test_excludes_files_the_branch_never_touched
test_purely_behind_branch_not_flagged
test_landed_branch_not_reflagged_when_main_touches_same_file
test_unlanded_work_still_flagged_when_main_touches_same_file
test_landed_branch_not_reflagged_when_main_drifts_adjacent
test_trivial_only_additions_stay_indeterminate
echo "All tests passed."
