# Stale Critical-Branch Alert Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Page Pushover when a branch carrying real, unlanded source changes to `infra/`, `internal/opsalert/`, or `opsnotify/` has sat unmerged for more than 24 hours, so a fix like rosterbot-naz's task-failure alerting can never again sit invisibly on a branch through an outage.

**Architecture:** A new GitHub Actions scheduled workflow (`.github/workflows/stale-critical-branch.yml`) runs a standalone bash script (`.github/scripts/check-stale-critical-branches.sh`) every 6 hours. The script inspects every `origin/*` branch's **content** (not commit ancestry — see Global Constraints) against `main` on the critical paths; if any branch is stale past the threshold, the workflow posts to Pushover using the pre-existing (currently unused) `PUSHOVER_USER_KEY`/`PUSHOVER_APP_TOKEN` repo secrets and the job exits non-zero.

**Tech Stack:** bash (GNU/POSIX-compatible — must run identically on the ubuntu-latest GHA runner and on a contributor's local shell for testing), GitHub Actions YAML, `git`, `curl`.

## Global Constraints

- **Content diff, not commit-ancestry.** Use `git diff --name-only <base> <branch> -- <paths>` (direct two-tree diff, no dots) — never `<base>...<branch>` (triple-dot, which diffs from the merge-base and ignores what `main` did after the branch forked) and never a raw `git rev-list --count` ahead-check. This repo has several branches that are permanently "ahead" of `main` by commit count after a squash-merge (verified: `fix/rosterbot-0gm-*`, `fix/rosterbot-abd-*`, `feat/trades-tab`); only a direct content comparison correctly treats those as resolved.
- **Filter to `*.go`.** Diffing whole directories false-positived on `go.mod`/`go.sum` drift from routine dependency bumps and on a stray committed binary (verified live against `feat/recap-access-logs`/`feat/recap-views-tab`, both ancient/unrelated branches that only looked "stale" because of lockfile churn). Grep the diff to `\.go$` before doing anything else with it.
- **Cap the per-branch file list at 4, with a "(+N more)" suffix.** Verified live: without this cap, the concatenated report for the real repo's current 3 stale branches (which include two 20-file diffs) blew past Pushover's 1024-char message limit after only 2 of 3 branches, silently dropping the rest of the report.
- **No PR-based detection.** Do not use `gh pr list` / the GitHub PR API as the branch source. A survey of this repo's last 40 first-parent `main` commits found only 14 carry a PR reference; the rest — including the exact rosterbot-naz merge this ticket is about — are direct `git merge` commits with no corresponding PR. The check must enumerate raw `origin/*` refs.
- **No new secrets.** `PUSHOVER_USER_KEY`, `PUSHOVER_APP_TOKEN`, `PUSHOVER_GROUP_KEY` already exist as GitHub Actions repo secrets (leftover, currently unused, from the pre-AWS-migration era). Reuse `PUSHOVER_USER_KEY` + `PUSHOVER_APP_TOKEN` — do not add or rotate anything in AWS SSM for this.
- **Silent on the clean path.** Exit 0 with no Pushover call when nothing is stale, matching this repo's existing alert-only-on-problem convention (e.g. `gs-check`'s clean no-op).
- **No dedup/throttle in v1, by design.** A branch that remains stale re-alerts on every 6-hourly run. This is deliberate (see the design spec) — the situation is low-frequency and fully user-resolvable, so repeat nagging is the intended forcing function, not an oversight to fix. Do not add a marker-file/label dedup mechanism as part of this plan.

---

### Task 1: Detection script + test harness

**Files:**
- Create: `.github/scripts/check-stale-critical-branches.sh`
- Create: `.github/scripts/check-stale-critical-branches.test.sh`

**Interfaces:**
- Produces: an executable script at `.github/scripts/check-stale-critical-branches.sh` that reads env vars `BASE_REF` (default `origin/main`), `CRITICAL_PATHS` (default `infra/ internal/opsalert/ opsnotify/`), `STALE_HOURS` (default `24`); prints `STALE: <branch> — <N>h stale — <files>` lines to stdout for each stale branch found, exits `1` if any were found and `0` otherwise ("No stale critical-path branches found." on the clean path); when `$GITHUB_OUTPUT` is set in the environment, also appends a multiline `report` output (the same stale-branch lines, newline-joined, truncated to 1024 chars) in the GitHub Actions output-file format. This is the only interface Task 2 depends on.

- [ ] **Step 1: Write the test harness**

Create `.github/scripts/check-stale-critical-branches.test.sh`:

```bash
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

test_flags_and_filters_correctly
test_clean_when_nothing_stale
echo "All tests passed."
```

- [ ] **Step 2: Make it executable and run it to verify it fails**

```bash
chmod +x .github/scripts/check-stale-critical-branches.test.sh
bash .github/scripts/check-stale-critical-branches.test.sh
```

Expected: fails with something like `.../check-stale-critical-branches.sh: No such file or directory` (exit 127), since the detection script doesn't exist yet.

- [ ] **Step 3: Write the detection script**

Create `.github/scripts/check-stale-critical-branches.sh`:

```bash
#!/usr/bin/env bash
# Finds remote branches whose *content* differs from main on safety-critical
# paths and have sat that way past a staleness threshold.
#
# Content diff (not commit-ancestry) is deliberate: a squash-merged branch
# stays "ahead" of main by commit count forever, but once its content on the
# critical paths matches main, it's no longer a real gap.
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
  | grep -v -E '^origin/(HEAD|main)$'); do

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
  diff_files=$(git diff --name-only "${BASE_REF}" "${branch}" -- ${CRITICAL_PATHS} 2>/dev/null | grep -E '\.go$' || true)
  if [ -z "$diff_files" ]; then
    continue
  fi

  # shellcheck disable=SC2086
  last_touch=$(git log -1 --format=%ct "${BASE_REF}..${branch}" -- ${CRITICAL_PATHS} 2>/dev/null || true)
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
```

- [ ] **Step 4: Make it executable and run the tests to verify they pass**

```bash
chmod +x .github/scripts/check-stale-critical-branches.sh
bash .github/scripts/check-stale-critical-branches.test.sh
```

Expected: `PASS: test_flags_and_filters_correctly`, `PASS: test_clean_when_nothing_stale`, `All tests passed.`, exit 0.

- [ ] **Step 5: Sanity-check against this actual repo**

```bash
git fetch origin --prune
STALE_HOURS=24 CRITICAL_PATHS="infra/ internal/opsalert/ opsnotify/" BASE_REF=origin/main \
  .github/scripts/check-stale-critical-branches.sh
```

Expected (as of 2026-08-11 — will drift as branches are merged/deleted, that's fine): exit 1, reporting `feat/recap-access-logs`, `feat/recap-views-tab`, and `ops-alerting-hardening` as stale. These are real, currently-unmerged branches with genuine `.go` diffs on the critical paths (verified by hand during design) — this is expected output, not a bug, and doubles as a live demonstration that the check works before it's ever wired to a schedule.

- [ ] **Step 6: Commit**

```bash
git add .github/scripts/check-stale-critical-branches.sh .github/scripts/check-stale-critical-branches.test.sh
git commit -m "feat(ci): stale critical-path branch detection script (rosterbot-v4l)"
```

---

### Task 2: GitHub Actions workflow

**Files:**
- Create: `.github/workflows/stale-critical-branch.yml`

**Interfaces:**
- Consumes: `.github/scripts/check-stale-critical-branches.sh` from Task 1 (exit code + `steps.check.outputs.report`).
- Consumes: GitHub Actions repo secrets `PUSHOVER_USER_KEY`, `PUSHOVER_APP_TOKEN` (pre-existing, confirmed present via `gh secret list`, no action needed to create them).

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/stale-critical-branch.yml`:

```yaml
name: Stale critical-path branch check

# Repo-hygiene check, not one of the Fantrax-dependent scheduled jobs — those
# run on ECS Fargate (see docs/aws-deployment.md). This one only needs read
# access to branches, which Actions provides for free via actions/checkout;
# it sits alongside ci.yml/codeql.yml, not in place of them.
on:
  schedule:
    - cron: '0 */6 * * *'
  workflow_dispatch:

permissions:
  contents: read

env:
  STALE_HOURS: '24'
  CRITICAL_PATHS: 'infra/ internal/opsalert/ opsnotify/'

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Check for stale critical-path branches
        id: check
        run: .github/scripts/check-stale-critical-branches.sh

      - name: Notify Pushover
        if: failure()
        env:
          PUSHOVER_USER_KEY: ${{ secrets.PUSHOVER_USER_KEY }}
          PUSHOVER_APP_TOKEN: ${{ secrets.PUSHOVER_APP_TOKEN }}
          REPORT: ${{ steps.check.outputs.report }}
        run: |
          curl -sS \
            --form-string "token=${PUSHOVER_APP_TOKEN}" \
            --form-string "user=${PUSHOVER_USER_KEY}" \
            --form-string "title=Stale critical-path branch" \
            --form-string "message=${REPORT}" \
            --form-string "priority=0" \
            https://api.pushover.net/1/messages.json
```

Note: `fetch-depth: 0` is required — the default shallow checkout has no remote-tracking branches for the script to enumerate.

- [ ] **Step 2: Validate YAML syntax**

```bash
python3 -c "
import yaml
with open('.github/workflows/stale-critical-branch.yml') as f:
    d = yaml.safe_load(f)
assert 'jobs' in d and 'check' in d['jobs']
assert d['jobs']['check']['steps'][0]['uses'] == 'actions/checkout@v4'
print('YAML OK:', list(d.keys()))
"
```

Expected: `YAML OK: ['name', True, 'permissions', 'env', 'jobs']` — the `True` in place of `'on'` is a PyYAML 1.1 quirk (`on`/`off`/`yes`/`no` parse as booleans under YAML 1.1) that GitHub's own parser does not share; it special-cases the `on:` key regardless of case/type. This is cosmetic to this validation step only, not a defect in the workflow file — do not "fix" it by quoting `"on":`, since that would be inconsistent with the unquoted `on:` already used in `.github/workflows/ci.yml`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/stale-critical-branch.yml
git commit -m "feat(ci): schedule stale critical-path branch check (rosterbot-v4l)"
```

---

### Task 3: Post-merge live verification (manual)

This task cannot run inside this session/worktree: `workflow_dispatch` only becomes available for a workflow once its YAML is present on the repository's **default branch** — dispatching it against a feature branch before merge isn't possible. Do this after Task 1 and Task 2 are merged to `main`.

**Files:** none (verification only).

- [ ] **Step 1: Confirm the workflow registered**

```bash
gh workflow list | grep "Stale critical-path branch check"
```

Expected: the workflow appears with state `active`.

- [ ] **Step 2: Dispatch a manual run and watch it**

```bash
gh workflow run "Stale critical-path branch check"
sleep 15
gh run list --workflow="Stale critical-path branch check" --limit 1
```

Given the branches identified during Task 1 Step 5 (`feat/recap-access-logs`, `feat/recap-views-tab`, `ops-alerting-hardening`) are still likely to exist at this point, expect the run to **fail** (red) — this is correct behavior, not a bug — and expect a Pushover push naming those branches to arrive within a few seconds of the run completing. Confirm the push actually lands (check the phone/device Pushover is configured on).

- [ ] **Step 3: Confirm the clean path**

Merge, rebase, or delete one flagged branch (user's call — this plan does not delete branches unilaterally), then re-dispatch:

```bash
gh workflow run "Stale critical-path branch check"
sleep 15
gh run list --workflow="Stale critical-path branch check" --limit 1
```

Expected: the run's report no longer includes the branch that was merged/deleted. Once **all** currently-known stale branches are cleared, a further dispatch should exit 0 (green) with no Pushover push — confirming the silent-on-clean-path behavior from the Global Constraints.

- [ ] **Step 4: Report back**

Summarize in chat: workflow registered, first run's Pushover content, and current state of the three originally-flagged branches (cleared / still open and why). No commit — this task only verifies already-committed work.
