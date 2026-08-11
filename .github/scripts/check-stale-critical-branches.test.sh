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

  git init --bare -q "$bare"
  git clone -q "$bare" "$work"
  (
    cd "$work"
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

test_flags_and_filters_correctly
test_clean_when_nothing_stale
test_stale_go_masked_by_recent_non_go
echo "All tests passed."
