# Scheduled Task Failure Alerting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Push a Pushover alert to the ops channel when a scheduled ECS task fails, so a multi-hour outage of the lineup hot path can never again pass silently.

**Architecture:** An EventBridge rule on `ECS Task State Change` targets a Lambda (today's `buildnotify`, renamed `opsnotify`, now dispatching on `detail-type` so it serves both CodeBuild and ECS events). The Lambda reads the newest run-ledger records from S3 and hands them to a pure `internal/opsalert` package, which decides whether this failure is the *start* of a streak, the third-in-a-row *escalation*, or a *recovery*. State lives nowhere — streak transitions are derived from an append-only ledger, so they self-deduplicate.

**Tech Stack:** Go 1.26.1, AWS CDK v2 (Go), aws-lambda-go, aws-sdk-go-v2 (S3 + SSM), Pushover.

**Spec:** `docs/superpowers/specs/2026-07-31-scheduled-task-failure-alerting-design.md`
**Issue:** rosterbot-naz

## Global Constraints

- Go version for all modules: `1.26.1` (match the existing `go.mod` files).
- `internal/opsalert` must stay a **stdlib-only leaf**. It must not import `internal/lineupapi`, `internal/fantrax`, or any AWS SDK package. `internal/lineupapi` transitively pulls `internal/fantrax` and therefore chromedp; dragging that into a notification Lambda is the thing this package exists to avoid.
- `opsnotify` may import only: `internal/notify`, `internal/opsalert`, `internal/s3blob`, `aws-lambda-go`, `aws-sdk-go-v2/config`, `aws-sdk-go-v2/service/ssm`, `aws-sdk-go-v2/service/s3`.
- Alert channel is `/rosterbot/PUSHOVER_USER_KEY` (personal ops). Never `PUSHOVER_GROUP_KEY`.
- Escalation threshold is exactly `3`, tested as `==` not `>=`.
- Log-tail excerpt capped at 300 characters **before** it is appended to the body.
- `lambda/`, `buildnotify/`/`opsnotify/`, and `infra/` are separate Go modules. The root `go build ./...` / `go vet ./...` / `go mod tidy` do **not** descend into them. After touching any of them run `make build-modules`.
- Commit after every task. Do not push until the whole plan is done and verified.
- Deploy, when it happens, is `cdk deploy -c enableBuild=true`. Omitting the flag destroys the CodeBuild project.

---

### Task 1: `make test` runs nested-module tests

`make test` is `go test ./internal/...`, and CI's `make build-modules` only *builds* the nested modules. `buildnotify/message_test.go` has therefore never executed in this repo. Fix that first, so every test added by later tasks is actually run.

**Files:**
- Modify: `Makefile:24-25`

- [ ] **Step 1: Confirm the gap is real**

Run: `make test 2>&1 | grep -c buildnotify`
Expected: `0` — the buildnotify package is never mentioned, because it is never tested.

- [ ] **Step 2: Add a `test-modules` target and depend on it**

Replace the existing target at `Makefile:24-25`:

```makefile
test:
	go test ./internal/...
```

with:

```makefile
# Mirrors `build: build-modules`. The root `go test ./internal/...` never
# descends into the nested modules (lambda/, opsnotify/, infra/), so their
# tests silently never ran — buildnotify/message_test.go sat dead for months.
test: test-modules
	go test ./internal/...

test-modules:
	@for d in $$(find . -name go.mod -not -path './.git/*' -not -path './.claude/*' | grep -v '^\./go\.mod$$' | xargs -n1 dirname | sort); do \
	  printf '  test %s\n' "$$d"; \
	  ( cd "$$d" && go test ./... ) || exit 1; \
	done
```

- [ ] **Step 3: Verify the previously-dead test now runs**

Run: `make test 2>&1 | grep -E "test \./(buildnotify|lambda|infra)|ok.*buildnotify"`
Expected: a `test ./buildnotify` line appears, and `TestFormatMessage` passes as part of the run.

- [ ] **Step 4: Verify the whole suite still passes**

Run: `make test`
Expected: exit 0, no `FAIL` lines.

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "build: run nested-module tests in \`make test\`

go test ./internal/... never descends into lambda/, buildnotify/ or infra/,
so buildnotify/message_test.go has never executed. Mirror the existing
build: build-modules split with a test-modules target.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: `internal/opsalert` — the streak verdict

**Files:**
- Create: `internal/opsalert/streak.go`
- Test: `internal/opsalert/streak_test.go`
- Test: `internal/opsalert/contract_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Record struct { ID, Command, Status string; ExitCode *int; LogTail string }`
  - `const StatusRunning = "RUNNING"`, `StatusSuccess = "SUCCESS"`, `StatusFailed = "FAILED"`
  - `type Kind int` with `None`, `Started`, `Escalated`, `Recovered`
  - `const EscalateAt = 3`
  - `type Verdict struct { Kind Kind; Command string; Streak int; Failure *Record }`
  - `func Streak(recs []Record, command string) Verdict` — `recs` newest-first

- [ ] **Step 1: Write the failing tests**

Create `internal/opsalert/streak_test.go`:

```go
package opsalert

import "testing"

func rec(command, status string) Record {
	return Record{ID: command + "-" + status, Command: command, Status: status}
}

// hist builds a newest-first history for one command from a status string,
// where 'F' is FAILED, 'S' is SUCCESS and 'R' is RUNNING. Left-most is newest.
func hist(command, statuses string) []Record {
	var out []Record
	for _, c := range statuses {
		switch c {
		case 'F':
			out = append(out, rec(command, StatusFailed))
		case 'S':
			out = append(out, rec(command, StatusSuccess))
		case 'R':
			out = append(out, rec(command, StatusRunning))
		}
	}
	return out
}

func TestStreak(t *testing.T) {
	tests := []struct {
		name       string
		statuses   string
		wantKind   Kind
		wantStreak int
	}{
		{"first failure after a success", "FSSS", Started, 1},
		{"first failure ever, no history behind it", "F", Started, 1},
		{"second consecutive failure is silent", "FFSSS", None, 2},
		{"third consecutive failure escalates", "FFFSSS", Escalated, 3},
		{"fourth consecutive failure is silent again", "FFFFSSS", None, 4},
		{"eleventh consecutive failure is silent", "FFFFFFFFFFFSSS", None, 11},
		{"success after failures recovers", "SFFFSS", Recovered, 3},
		{"success after a success says nothing", "SSSS", None, 0},
		{"single success, no history", "S", None, 0},
		{"no history at all", "", None, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Streak(hist("optimize", tt.statuses), "optimize")
			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tt.wantKind)
			}
			if got.Streak != tt.wantStreak {
				t.Errorf("Streak = %d, want %d", got.Streak, tt.wantStreak)
			}
			if got.Command != "optimize" {
				t.Errorf("Command = %q, want %q", got.Command, "optimize")
			}
		})
	}
}

// RUNNING records are the start-of-run ledger write, later overwritten at the
// same key by the terminal write. An in-flight run must not break a streak.
func TestStreak_IgnoresRunningRecords(t *testing.T) {
	got := Streak(hist("optimize", "RFFRFSS"), "optimize")
	if got.Kind != Escalated || got.Streak != 3 {
		t.Fatalf("got Kind=%v Streak=%d, want Escalated/3", got.Kind, got.Streak)
	}
}

// Interleaved jobs share one ledger; each command owns its own streak.
func TestStreak_OtherCommandsDoNotContaminate(t *testing.T) {
	recs := []Record{
		rec("optimize", StatusFailed),
		rec("grade", StatusFailed),
		rec("shadow", StatusFailed),
		rec("optimize", StatusSuccess),
		rec("grade", StatusFailed),
	}
	got := Streak(recs, "optimize")
	if got.Kind != Started || got.Streak != 1 {
		t.Fatalf("optimize: got Kind=%v Streak=%d, want Started/1", got.Kind, got.Streak)
	}
	if g := Streak(recs, "grade"); g.Streak != 2 || g.Kind != None {
		t.Fatalf("grade: got Kind=%v Streak=%d, want None/2", g.Kind, g.Streak)
	}
}

// The failing record is carried through so the caller can quote its log tail.
func TestStreak_CarriesTheFailingRecord(t *testing.T) {
	exit := 1
	recs := []Record{
		{ID: "task-a", Command: "optimize", Status: StatusFailed, ExitCode: &exit, LogTail: "boom"},
		{ID: "task-b", Command: "optimize", Status: StatusSuccess},
	}
	got := Streak(recs, "optimize")
	if got.Failure == nil {
		t.Fatal("Failure is nil, want the newest failed record")
	}
	if got.Failure.ID != "task-a" || got.Failure.LogTail != "boom" {
		t.Errorf("Failure = %+v, want task-a/boom", *got.Failure)
	}
}

func TestStreak_RecoveredCarriesNoFailingRecord(t *testing.T) {
	got := Streak(hist("optimize", "SFF"), "optimize")
	if got.Kind != Recovered {
		t.Fatalf("Kind = %v, want Recovered", got.Kind)
	}
	if got.Failure != nil {
		t.Errorf("Failure = %+v, want nil on recovery", *got.Failure)
	}
}

// Replay of the real 2026-07-01 incident: 11 consecutive hourly optimize
// failures, one shadow failure, then recovery. The whole point of the streak
// design is that this produces exactly four pushes, not twelve.
func TestStreak_ReplaysTheRealIncident(t *testing.T) {
	const optimize = "optimize --matchup --archive-projections"

	// Ledger grows newest-first, so prepend as the outage progresses.
	var ledger []Record
	prepend := func(r Record) { ledger = append([]Record{r}, ledger...) }

	// Healthy history before the incident.
	for i := 0; i < 5; i++ {
		prepend(rec(optimize, StatusSuccess))
	}

	var pushes []Kind
	record := func(command string) {
		if v := Streak(ledger, command); v.Kind != None {
			pushes = append(pushes, v.Kind)
		}
	}

	// 17:00Z..23:00Z — seven hourly optimize failures.
	for i := 0; i < 7; i++ {
		prepend(rec(optimize, StatusFailed))
		record(optimize)
	}
	// 23:41Z — shadow fails once, its own streak.
	prepend(rec("shadow", StatusFailed))
	record("shadow")
	// 00:00Z..03:00Z — four more optimize failures.
	for i := 0; i < 4; i++ {
		prepend(rec(optimize, StatusFailed))
		record(optimize)
	}
	// 04:00Z — optimize recovers.
	prepend(rec(optimize, StatusSuccess))
	record(optimize)

	want := []Kind{Started, Escalated, Started, Recovered}
	if len(pushes) != len(want) {
		t.Fatalf("got %d pushes %v, want %d %v", len(pushes), pushes, len(want), want)
	}
	for i := range want {
		if pushes[i] != want[i] {
			t.Errorf("push %d = %v, want %v", i, pushes[i], want[i])
		}
	}

	// And the recovery reports the true streak length.
	if v := Streak(ledger, optimize); v.Streak != 11 {
		t.Errorf("recovered streak = %d, want 11", v.Streak)
	}
}
```

Create `internal/opsalert/contract_test.go`. This is the guard that keeps `Record` honest against the ledger's real wire type. It lives in an external test package so it never becomes a build dependency of `opsnotify`:

```go
package opsalert_test

import (
	"encoding/json"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// opsalert.Record redeclares a subset of lineupapi.RunDetail rather than
// importing it, because lineupapi transitively pulls internal/fantrax (and
// therefore chromedp) into anything that imports it — unacceptable weight for
// a notification Lambda. This test is what makes that duplication safe: it
// fails the moment the two wire contracts drift.
func TestRecordDecodesARealLedgerRecord(t *testing.T) {
	exit := 1
	want := lineupapi.RunDetail{
		Run: lineupapi.Run{
			ID:        "abc123",
			Command:   "optimize --matchup --archive-projections",
			Status:    "FAILED",
			ExitCode:  &exit,
			StartedAt: "2026-07-01T17:00:53Z",
			EndedAt:   "2026-07-01T17:00:53Z",
			Trigger:   "schedule",
		},
		LogTail: "fantrax API error STALE_CLIENT",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	var got opsalert.Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Command != want.Command {
		t.Errorf("Command = %q, want %q", got.Command, want.Command)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.ExitCode == nil || *got.ExitCode != exit {
		t.Errorf("ExitCode = %v, want %d", got.ExitCode, exit)
	}
	if got.LogTail != want.LogTail {
		t.Errorf("LogTail = %q, want %q", got.LogTail, want.LogTail)
	}
}

// A RUNNING record has no exit code and no log tail; decoding must leave them
// zero rather than erroring, because the streak logic sees these too.
func TestRecordDecodesARunningLedgerRecord(t *testing.T) {
	data, err := json.Marshal(lineupapi.RunDetail{
		Run: lineupapi.Run{ID: "x", Command: "grade", Status: "RUNNING", StartedAt: "2026-07-01T17:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got opsalert.Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil", got.ExitCode)
	}
	if got.LogTail != "" {
		t.Errorf("LogTail = %q, want empty", got.LogTail)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/opsalert/...`
Expected: FAIL — the package does not exist yet (`no Go files` / undefined identifiers).

- [ ] **Step 3: Write the implementation**

Create `internal/opsalert/streak.go`:

```go
// Package opsalert decides whether a finished job run is worth waking an
// operator for, and renders the message when it is.
//
// It is a stdlib-only leaf on purpose. The only consumer is the opsnotify
// Lambda, and internal/lineupapi — where the run ledger's wire types live —
// transitively pulls internal/fantrax and therefore chromedp. A notification
// Lambda has no business carrying a headless browser, so Record redeclares the
// five fields this package needs and contract_test.go guards the duplication.
//
// Everything here is pure: the Lambda fetches, opsalert judges.
package opsalert

// Ledger record statuses, as written by the run-ledger command in entrypoint.sh.
const (
	StatusRunning = "RUNNING"
	StatusSuccess = "SUCCESS"
	StatusFailed  = "FAILED"
)

// Record is the subset of a run ledger record the alerting logic needs. The
// json tags mirror internal/lineupapi.RunDetail's wire contract.
type Record struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	LogTail  string `json:"log_tail,omitempty"`
}

// Kind is what, if anything, the operator should be told.
type Kind int

const (
	// None means stay quiet: a mid-streak repeat, or an ordinary success.
	None Kind = iota
	// Started is the first failure after a success — the outage begins.
	Started
	// Escalated is the EscalateAt'th consecutive failure — this is not a blip.
	Escalated
	// Recovered is the first success after one or more failures.
	Recovered
)

func (k Kind) String() string {
	switch k {
	case Started:
		return "Started"
	case Escalated:
		return "Escalated"
	case Recovered:
		return "Recovered"
	default:
		return "None"
	}
}

// EscalateAt is the streak length that earns a second push. Three hourly runs
// is roughly two hours of the lineup hot path down — past any plausible
// transient. Streak fires on equality, not on >=, so an eleven-failure outage
// escalates exactly once.
const EscalateAt = 3

// Verdict is the decision for one command. Failure is the record that triggered
// a Started or Escalated verdict, so the caller can quote its log tail; it is
// nil for None and Recovered.
type Verdict struct {
	Kind    Kind
	Command string
	Streak  int
	Failure *Record
}

// Streak decides what to say about the newest run of command, given recs in
// newest-first order (the run ledger's inverted-timestamp keys list that way
// natively).
//
// Only terminal records count. RUNNING is the start-of-run write that the
// end-of-run write later overwrites at the same key, so an in-flight sibling
// run must not be mistaken for a break in the streak.
//
// The whole decision is derived from the ledger, which is why there is no
// counter object to keep consistent and no cooldown to tune: streak
// transitions deduplicate themselves.
func Streak(recs []Record, command string) Verdict {
	var hist []Record
	for _, r := range recs {
		if r.Command != command {
			continue
		}
		if r.Status != StatusSuccess && r.Status != StatusFailed {
			continue
		}
		hist = append(hist, r)
	}
	if len(hist) == 0 {
		return Verdict{Command: command}
	}

	if hist[0].Status == StatusSuccess {
		n := leadingFailures(hist[1:])
		if n == 0 {
			return Verdict{Command: command}
		}
		return Verdict{Kind: Recovered, Command: command, Streak: n}
	}

	n := leadingFailures(hist)
	v := Verdict{Command: command, Streak: n, Failure: &hist[0]}
	switch n {
	case 1:
		v.Kind = Started
	case EscalateAt:
		v.Kind = Escalated
	}
	return v
}

// leadingFailures counts consecutive FAILED records from the front of recs.
func leadingFailures(recs []Record) int {
	n := 0
	for _, r := range recs {
		if r.Status != StatusFailed {
			break
		}
		n++
	}
	return n
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/opsalert/... -v`
Expected: PASS, including `TestStreak_ReplaysTheRealIncident` and both contract tests.

- [ ] **Step 5: Verify the leaf stays clean**

Run: `go list -deps ./internal/opsalert | grep '\.'`

Expected: exactly one line, `github.com/nixon-commits/rosterbot/internal/opsalert` (the package itself). Standard-library import paths contain no dot in their first element, so any *other* dotted path is an external dependency and violates the stdlib-only constraint.

Note this checks the package's build deps, not its test deps — `contract_test.go` imports `lineupapi` deliberately and is external-test-package (`package opsalert_test`) precisely so it never becomes a build dependency.

- [ ] **Step 6: Commit**

```bash
go vet ./... && go mod tidy
git add internal/opsalert/
git commit -m "feat(opsalert): ledger-derived failure streak verdicts

Streak transitions (first failure, third consecutive, recovery) are derived
from the append-only run ledger, so there is no counter object and no cooldown
timer — the transitions deduplicate themselves. Replays the real 2026-07-01
incident: 12 failures produce exactly 4 pushes.

Refs rosterbot-naz

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: `internal/opsalert` — message rendering

**Files:**
- Create: `internal/opsalert/message.go`
- Test: `internal/opsalert/message_test.go`

**Interfaces:**
- Consumes: `Verdict`, `Kind`, `Record` from Task 2.
- Produces:
  - `func FormatTask(v Verdict) (title, body string)` — returns `("", "")` for `Kind == None`
  - `func FormatCrash(command, taskID, stoppedReason string) (title, body string)`
  - `func JobName(command string) string`
  - `const MaxCause = 300`

- [ ] **Step 1: Write the failing tests**

Create `internal/opsalert/message_test.go`:

```go
package opsalert

import (
	"strings"
	"testing"
)

func failed(command, logTail string, exit int) *Record {
	return &Record{ID: "t1", Command: command, Status: StatusFailed, ExitCode: &exit, LogTail: logTail}
}

const staleClient = `Usage:
  rosterbot optimize [flags]

fantrax client: fantrax auth client: failed to fetch user info during client initialization: failed to read login response body: fantrax API error STALE_CLIENT: Your browser is using an outdated cached version.`

func TestFormatTask_Started(t *testing.T) {
	v := Verdict{
		Kind:    Started,
		Command: "optimize --matchup --archive-projections",
		Streak:  1,
		Failure: failed("optimize --matchup --archive-projections", staleClient, 1),
	}
	title, body := FormatTask(v)

	if title != "Rosterbot: optimize failed" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"❌", "--matchup", "exit 1", "STALE_CLIENT"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
	// Only the last non-empty line of the tail is quoted, not the usage banner.
	if strings.Contains(body, "Usage:") {
		t.Errorf("body quoted the whole log tail, want only its last line: %q", body)
	}
}

func TestFormatTask_Escalated(t *testing.T) {
	v := Verdict{
		Kind:    Escalated,
		Command: "optimize --matchup",
		Streak:  3,
		Failure: failed("optimize --matchup", "boom", 1),
	}
	title, body := FormatTask(v)
	if title != "Rosterbot: optimize still failing" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"🔥", "3", "in a row"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

func TestFormatTask_Recovered(t *testing.T) {
	title, body := FormatTask(Verdict{Kind: Recovered, Command: "shadow", Streak: 11})
	if title != "Rosterbot: shadow recovered" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"✅", "11 failures"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

func TestFormatTask_RecoveredSingularFailure(t *testing.T) {
	_, body := FormatTask(Verdict{Kind: Recovered, Command: "grade", Streak: 1})
	if !strings.Contains(body, "1 failure") || strings.Contains(body, "1 failures") {
		t.Errorf("body %q should say %q", body, "1 failure")
	}
}

// None must render nothing at all — the caller uses the empty title as its
// "do not send" signal, so a stray body here becomes a spurious push.
func TestFormatTask_NoneRendersNothing(t *testing.T) {
	title, body := FormatTask(Verdict{Kind: None, Command: "optimize", Streak: 2})
	if title != "" || body != "" {
		t.Errorf("got (%q, %q), want both empty", title, body)
	}
}

// SendPushover truncates silently at 1024 chars, so the cause must be bounded
// before it is appended — otherwise a long tail eats the command and exit code.
func TestFormatTask_CauseIsTruncated(t *testing.T) {
	long := strings.Repeat("x", 5000)
	_, body := FormatTask(Verdict{
		Kind:    Started,
		Command: "optimize",
		Streak:  1,
		Failure: failed("optimize", long, 1),
	})
	if len(body) > MaxCause+200 {
		t.Errorf("body is %d chars, want bounded near MaxCause=%d", len(body), MaxCause)
	}
	if !strings.Contains(body, "…") {
		t.Errorf("truncated body %q should be marked with an ellipsis", body)
	}
	if !strings.Contains(body, "exit 1") {
		t.Errorf("truncation ate the exit code: %q", body)
	}
}

func TestFormatTask_NoLogTail(t *testing.T) {
	v := Verdict{Kind: Started, Command: "grade", Streak: 1, Failure: failed("grade", "", 2)}
	_, body := FormatTask(v)
	if !strings.Contains(body, "exit 2") {
		t.Errorf("body %q missing exit code", body)
	}
	if strings.HasSuffix(body, "\n") {
		t.Errorf("body %q has a dangling newline where the cause would go", body)
	}
}

// A Started verdict with no Failure record should not panic.
func TestFormatTask_StartedWithNilFailure(t *testing.T) {
	_, body := FormatTask(Verdict{Kind: Started, Command: "grade", Streak: 1})
	if !strings.Contains(body, "grade") {
		t.Errorf("body = %q", body)
	}
}

func TestFormatCrash(t *testing.T) {
	title, body := FormatCrash("optimize --matchup", "abc123", "OutOfMemoryError: Container killed due to memory usage")
	if title != "Rosterbot: optimize died" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"💀", "no ledger record", "OutOfMemoryError", "abc123"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

func TestJobName(t *testing.T) {
	tests := map[string]string{
		"optimize --matchup --archive-projections": "optimize",
		"shadow":               "shadow",
		"recap-site --out dist": "recap-site",
		"":                     "task",
		"   ":                  "task",
	}
	for in, want := range tests {
		if got := JobName(in); got != want {
			t.Errorf("JobName(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/opsalert/... -run 'Format|JobName'`
Expected: FAIL — `undefined: FormatTask`, `undefined: FormatCrash`, `undefined: JobName`, `undefined: MaxCause`.

- [ ] **Step 3: Write the implementation**

Create `internal/opsalert/message.go`:

```go
package opsalert

import (
	"fmt"
	"strings"
)

// MaxCause bounds the log-tail excerpt. notify.SendPushover truncates the whole
// message at 1024 characters silently, so the cause is capped before it is
// appended rather than after — otherwise a long stack trace would eat the
// command and exit code that make the alert triageable.
const MaxCause = 300

// FormatTask renders the Pushover (title, body) for a verdict. Kind None
// returns two empty strings; callers treat an empty title as "do not send".
func FormatTask(v Verdict) (title, body string) {
	job := JobName(v.Command)

	switch v.Kind {
	case Started:
		title = "Rosterbot: " + job + " failed"
		body = "❌ " + v.Command + exitSuffix(v.Failure)
	case Escalated:
		title = "Rosterbot: " + job + " still failing"
		body = fmt.Sprintf("🔥 %s failed %d× in a row%s", v.Command, v.Streak, exitSuffix(v.Failure))
	case Recovered:
		title = "Rosterbot: " + job + " recovered"
		return title, fmt.Sprintf("✅ %s recovered after %d %s",
			v.Command, v.Streak, plural(v.Streak, "failure", "failures"))
	default:
		return "", ""
	}

	if c := cause(v.Failure); c != "" {
		body += "\n" + c
	}
	return title, body
}

// FormatCrash renders the alert for a task that stopped without leaving a
// ledger record at all — the entrypoint never reached its final run-ledger
// write. That means OOM, an image-pull failure, or SIGKILL to pid 1: the class
// of failure the bot structurally cannot report on itself, and the reason this
// detector lives in a Lambda rather than in a bot subcommand.
//
// No streak is computable, so this always sends.
func FormatCrash(command, taskID, stoppedReason string) (title, body string) {
	title = "Rosterbot: " + JobName(command) + " died"

	body = "💀 " + strings.TrimSpace(command) + " · no ledger record"
	if stoppedReason != "" {
		body += "\n" + truncate(strings.TrimSpace(stoppedReason), MaxCause)
	}
	if taskID != "" {
		body += "\ntask " + taskID
	}
	return title, body
}

// JobName is the leading word of a command — "optimize" out of
// "optimize --matchup --archive-projections" — so the Pushover title is
// triageable from a lock screen.
func JobName(command string) string {
	if f := strings.Fields(command); len(f) > 0 {
		return f[0]
	}
	return "task"
}

// exitSuffix renders " · exit N" when the record carries an exit code.
func exitSuffix(r *Record) string {
	if r == nil || r.ExitCode == nil {
		return ""
	}
	return fmt.Sprintf(" · exit %d", *r.ExitCode)
}

// cause is the last non-empty line of the record's captured log tail — for the
// STALE_CLIENT incident that is the error itself, which is the difference
// between an alert that says "something broke" and one that says what.
func cause(r *Record) string {
	if r == nil {
		return ""
	}
	lines := strings.Split(r.LogTail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return truncate(s, MaxCause)
		}
	}
	return ""
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/opsalert/... -v`
Expected: PASS, all tests from Tasks 2 and 3.

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add internal/opsalert/
git commit -m "feat(opsalert): render task failure/recovery Pushover messages

Body carries the last non-empty line of the ledger's captured log tail, capped
at 300 chars before append — SendPushover truncates at 1024 silently, so an
unbounded tail would eat the command and exit code.

Refs rosterbot-naz

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: Rename `buildnotify/` → `opsnotify/` and dispatch on event type

Pure rename plus a dispatch seam. No behaviour change yet — the CodeBuild path must keep working identically, which the (now actually-running) `TestFormatMessage` proves.

**Files:**
- Rename: `buildnotify/` → `opsnotify/` (whole directory, via `git mv`)
- Rename: `opsnotify/message.go` → `opsnotify/build.go`
- Rename: `opsnotify/message_test.go` → `opsnotify/build_test.go`
- Modify: `opsnotify/go.mod` (module path)
- Modify: `opsnotify/main.go` (dispatch)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - module path `github.com/nixon-commits/rosterbot/opsnotify`
  - `func formatMessage(ev events.CodeBuildEvent) (title, body string)` — unchanged, moved to `build.go`
  - `var send func(title, message string) error` — package-level seam so handlers are testable without network
  - `func dispatch(ctx context.Context, raw json.RawMessage) error`

- [ ] **Step 1: Move the directory and files**

```bash
git mv buildnotify opsnotify
git mv opsnotify/message.go opsnotify/build.go
git mv opsnotify/message_test.go opsnotify/build_test.go
```

- [ ] **Step 2: Update the module path**

In `opsnotify/go.mod`, change the first non-comment line from:

```
module github.com/nixon-commits/rosterbot/buildnotify
```

to:

```
module github.com/nixon-commits/rosterbot/opsnotify
```

- [ ] **Step 3: Rewrite `opsnotify/main.go` with the dispatch seam**

Replace the whole file:

```go
// Command opsnotify is the AWS Lambda that turns an ops event into a Pushover
// notification on the personal ops channel. It serves two event sources:
//
//   - "CodeBuild Build State Change" — image build outcomes (rosterbot-00j)
//   - "ECS Task State Change"        — scheduled job failures (rosterbot-naz)
//
// Both are wired by the CDK in infra/ (Entry: ../opsnotify). The function
// itself is created unconditionally; only the CodeBuild rule sits behind the
// enableBuild context gate, because job-failure alerting must survive a stack
// deployed without it.
//
// Separate module so aws-lambda-go stays out of the main rosterbot binary's
// dependency graph (mirrors lambda/). It deliberately does NOT import
// internal/lineupapi — that would drag internal/fantrax and chromedp into a
// notifier — so the ledger's records are decoded into internal/opsalert.Record.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/nixon-commits/rosterbot/internal/notify"
)

// send is the Pushover seam. Replaced in tests so handler tests never reach the
// network; wired to the real sender in main once the SSM creds are read.
var send = func(title, message string) error { return nil }

// ledger is the run-ledger reader, nil until main wires it (and in the
// CodeBuild-only tests, which never touch the ECS path).
var ledger *ledgerReader

func main() {
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	ssmc := ssm.NewFromConfig(cfg)
	// Read once at cold start; the alert path being down is itself worth a hard
	// init failure so it surfaces in CloudWatch.
	userKey := mustParam(ctx, ssmc, "/rosterbot/PUSHOVER_USER_KEY")
	apiToken := mustParam(ctx, ssmc, "/rosterbot/PUSHOVER_API_TOKEN")
	send = func(title, message string) error {
		return notify.SendPushover(userKey, apiToken, title, message)
	}

	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		l, err := newLedgerReader(ctx, bucket)
		if err != nil {
			log.Fatalf("ledger reader: %v", err)
		}
		ledger = l
	}

	lambda.Start(dispatch)
}

// dispatch routes an EventBridge event to the handler for its detail-type. The
// raw message is decoded twice — once for the envelope, once into the concrete
// event — because aws-lambda-go ships no ECS Task State Change type, so the two
// sources cannot share one typed parameter.
func dispatch(ctx context.Context, raw json.RawMessage) error {
	var env events.CloudWatchEvent
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}

	switch env.DetailType {
	case "CodeBuild Build State Change":
		var ev events.CodeBuildEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		title, body := formatMessage(ev)
		return sendOrLog(title, body)

	case "ECS Task State Change":
		return handleTask(ctx, env.Detail)

	default:
		log.Printf("ignoring unhandled detail-type %q", env.DetailType)
		return nil
	}
}

// sendOrLog sends unless the title is empty (the "stay quiet" signal), and
// returns the error so EventBridge async-invoke retries rather than silently
// swallowing a missed alert.
func sendOrLog(title, body string) error {
	if title == "" {
		return nil
	}
	if err := send(title, body); err != nil {
		log.Printf("send pushover: %v", err)
		return err
	}
	return nil
}

func mustParam(ctx context.Context, c *ssm.Client, name string) string {
	withDecryption := true
	out, err := c.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           &name,
		WithDecryption: &withDecryption,
	})
	if err != nil {
		log.Fatalf("read %s: %v", name, err)
	}
	return *out.Parameter.Value
}
```

This will not compile yet — `handleTask` and `ledgerReader` arrive in Task 5. That is expected; Step 5 below only checks the CodeBuild formatter, and the module build is verified at the end of Task 5.

- [ ] **Step 4: Add a dispatch test**

Create `opsnotify/dispatch_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// capture swaps the Pushover seam for a recorder and restores it after the test.
func capture(t *testing.T) *[]string {
	t.Helper()
	prev := send
	var got []string
	send = func(title, message string) error {
		got = append(got, title+"|"+message)
		return nil
	}
	t.Cleanup(func() { send = prev })
	return &got
}

const codeBuildEvent = `{
  "version": "0",
  "detail-type": "CodeBuild Build State Change",
  "source": "aws.codebuild",
  "detail": {
    "build-status": "FAILED",
    "project-name": "Build",
    "additional-information": {
      "source-version": "abc1234def",
      "logs": {"deep-link": "https://logs.example/x"}
    }
  }
}`

func TestDispatch_RoutesCodeBuild(t *testing.T) {
	got := capture(t)
	if err := dispatch(context.Background(), json.RawMessage(codeBuildEvent)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "Rosterbot build FAILED") {
		t.Errorf("send = %q", (*got)[0])
	}
}

// An unrecognised source must be a quiet no-op, not an error — returning an
// error would make EventBridge retry it forever.
func TestDispatch_IgnoresUnknownDetailType(t *testing.T) {
	got := capture(t)
	ev := `{"detail-type":"EC2 Instance State-change Notification","source":"aws.ec2","detail":{}}`
	if err := dispatch(context.Background(), json.RawMessage(ev)); err != nil {
		t.Fatalf("unknown detail-type returned an error: %v", err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0: %v", len(*got), *got)
	}
}

func TestDispatch_RejectsMalformedEnvelope(t *testing.T) {
	if err := dispatch(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("want an error for a malformed envelope, got nil")
	}
}
```

- [ ] **Step 5: Deferred verification**

The module cannot build until Task 5 adds `handleTask`. Do not run `make test` here.

- [ ] **Step 6: Commit the rename**

```bash
git add -A buildnotify opsnotify
git commit -m "refactor(opsnotify): rename buildnotify, dispatch on detail-type

One Lambda for all ops notification rather than a fourth nested Go module —
CLAUDE.md records nested-module \`replace ../\` staleness breaking the deploy
twice already. CodeBuild formatting is unchanged, just moved to build.go.

Refs rosterbot-naz

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: `opsnotify` — the ECS task handler and ledger reader

**Files:**
- Create: `opsnotify/task.go`
- Create: `opsnotify/ledger.go`
- Test: `opsnotify/task_test.go`
- Modify: `opsnotify/go.mod` (via `go mod tidy`)

**Interfaces:**
- Consumes: `send`, `sendOrLog`, `ledger` from Task 4; `opsalert.Record`, `opsalert.Streak`, `opsalert.FormatTask`, `opsalert.FormatCrash` from Tasks 2–3.
- Produces:
  - `type ecsTaskDetail struct{…}` with methods `failed() bool`, `taskID() string`, `command() string`
  - `func handleTask(ctx context.Context, detail json.RawMessage) error`
  - `type ledgerReader struct{…}`, `func newLedgerReader(ctx context.Context, bucket string) (*ledgerReader, error)`, `func (l *ledgerReader) recent(ctx context.Context, limit int) ([]opsalert.Record, error)`
  - `const ledgerWindow = 200`

- [ ] **Step 1: Write the failing tests**

Create `opsnotify/task_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// NOTE: this file must NOT import internal/lineupapi, even in a test. `go mod
// tidy` in this module resolves the test imports of the module's own packages,
// so a test-only import of lineupapi would pull fantrax and chromedp into
// opsnotify/go.sum — the exact weight opsalert.Record exists to avoid. The
// fidelity of Record against the real ledger type is guarded instead by
// internal/opsalert/contract_test.go, which lives in the root module where that
// dependency is free.

// ecsDetail builds an ECS Task State Change detail body.
func ecsDetail(taskID, lastStatus, stoppedReason string, exitCode *int, command []string) json.RawMessage {
	container := map[string]any{"name": "bot", "lastStatus": lastStatus}
	if exitCode != nil {
		container["exitCode"] = *exitCode
	}
	d := map[string]any{
		"clusterArn":    "arn:aws:ecs:us-west-1:476646938644:cluster/InfraStack-Cluster",
		"taskArn":       "arn:aws:ecs:us-west-1:476646938644:task/InfraStack-Cluster/" + taskID,
		"lastStatus":    lastStatus,
		"stoppedReason": stoppedReason,
		"containers":    []any{container},
		"overrides": map[string]any{
			"containerOverrides": []any{map[string]any{"name": "bot", "command": command}},
		},
	}
	b, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	return b
}

// fakeLedger installs a ledgerReader backed by an in-memory S3, seeded with the
// given records newest-first, and restores the previous reader afterwards.
func fakeLedger(t *testing.T, recs []opsalert.Record) {
	t.Helper()
	objects := map[string][]byte{}
	for i, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		// Ledger keys sort newest-first ascending, so an increasing index
		// reproduces the real inverted-timestamp ordering.
		objects[fmt.Sprintf("runledger/%010d-%s.json", i, r.ID)] = b
	}
	fake := s3blobtest.With(objects)
	prev := ledger
	ledger = &ledgerReader{blob: fake.Blob("state", "runledger/")}
	t.Cleanup(func() { ledger = prev })
}

func run(id, command, status string, exit int) opsalert.Record {
	r := opsalert.Record{ID: id, Command: command, Status: status}
	if status != opsalert.StatusRunning {
		e := exit
		r.ExitCode = &e
	}
	if status == opsalert.StatusFailed {
		r.LogTail = "fantrax API error STALE_CLIENT: outdated cached version"
	}
	return r
}

const optimizeCmd = "optimize --matchup --archive-projections"

func TestHandleTask_FirstFailureAlerts(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	one := 1
	detail := ecsDetail("task1", "STOPPED", "Essential container in task exited", &one,
		strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	msg := (*got)[0]
	for _, want := range []string{"optimize failed", "exit 1", "STALE_CLIENT"} {
		if !strings.Contains(msg, want) {
			t.Errorf("send %q missing %q", msg, want)
		}
	}
}

func TestHandleTask_SecondFailureIsSilent(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task2", optimizeCmd, opsalert.StatusFailed, 1),
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	one := 1
	if err := handleTask(context.Background(),
		ecsDetail("task2", "STOPPED", "", &one, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0: %v", len(*got), *got)
	}
}

func TestHandleTask_SuccessAfterFailuresRecovers(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task3", optimizeCmd, opsalert.StatusSuccess, 0),
		run("task2", optimizeCmd, opsalert.StatusFailed, 1),
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	zero := 0
	if err := handleTask(context.Background(),
		ecsDetail("task3", "STOPPED", "", &zero, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "recovered after 2 failures") {
		t.Errorf("send = %q", (*got)[0])
	}
}

// A task that stopped with no ledger record never reached the entrypoint's
// final run-ledger write: OOM, image-pull failure, SIGKILL to pid 1. No streak
// is computable, so it always alerts.
func TestHandleTask_NoLedgerRecordAlertsUnconditionally(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	detail := ecsDetail("ghost", "STOPPED", "OutOfMemoryError: Container killed", nil,
		strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	for _, want := range []string{"died", "no ledger record", "OutOfMemoryError", "ghost"} {
		if !strings.Contains((*got)[0], want) {
			t.Errorf("send %q missing %q", (*got)[0], want)
		}
	}
}

// Non-terminal transitions arrive on the same rule; only STOPPED is actionable.
func TestHandleTask_IgnoresNonStoppedStates(t *testing.T) {
	fakeLedger(t, nil)
	got := capture(t)
	if err := handleTask(context.Background(),
		ecsDetail("task1", "RUNNING", "", nil, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0", len(*got))
	}
}

// A task with no container command override is not one of ours.
func TestHandleTask_IgnoresTasksWithNoCommand(t *testing.T) {
	fakeLedger(t, nil)
	got := capture(t)
	one := 1
	if err := handleTask(context.Background(),
		ecsDetail("task1", "STOPPED", "", &one, nil)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0", len(*got))
	}
}

func TestEcsTaskDetail_Failed(t *testing.T) {
	zero, one := 0, 1
	tests := []struct {
		name string
		exit *int
		want bool
	}{
		{"clean exit is not a failure", &zero, false},
		{"non-zero exit is a failure", &one, true},
		{"absent exit code means the container never ran", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d ecsTaskDetail
			if err := json.Unmarshal(ecsDetail("t", "STOPPED", "", tt.exit, []string{"grade"}), &d); err != nil {
				t.Fatal(err)
			}
			if got := d.failed(); got != tt.want {
				t.Errorf("failed() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A task that never placed has no containers at all — treat that as a failure
// rather than silently reading it as "everything exited zero".
func TestEcsTaskDetail_NoContainersIsAFailure(t *testing.T) {
	var d ecsTaskDetail
	if err := json.Unmarshal([]byte(`{"lastStatus":"STOPPED","containers":[]}`), &d); err != nil {
		t.Fatal(err)
	}
	if !d.failed() {
		t.Error("failed() = false for a task with no containers, want true")
	}
}

func TestEcsTaskDetail_TaskIDAndCommand(t *testing.T) {
	var d ecsTaskDetail
	one := 1
	if err := json.Unmarshal(ecsDetail("abc123", "STOPPED", "", &one, strings.Fields(optimizeCmd)), &d); err != nil {
		t.Fatal(err)
	}
	if got := d.taskID(); got != "abc123" {
		t.Errorf("taskID() = %q, want %q", got, "abc123")
	}
	if got := d.command(); got != optimizeCmd {
		t.Errorf("command() = %q, want %q", got, optimizeCmd)
	}
}

func TestLedgerReader_ReadsNewestFirst(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("newest", optimizeCmd, opsalert.StatusFailed, 1),
		run("middle", optimizeCmd, opsalert.StatusSuccess, 0),
		run("oldest", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	recs, err := ledger.recent(context.Background(), ledgerWindow)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	if recs[0].ID != "newest" {
		t.Errorf("recs[0].ID = %q, want %q", recs[0].ID, "newest")
	}
	if recs[0].LogTail == "" {
		t.Error("log tail was dropped in decoding")
	}
}

func TestLedgerReader_RespectsTheWindow(t *testing.T) {
	var recs []opsalert.Record
	for i := 0; i < 10; i++ {
		recs = append(recs, run(fmt.Sprintf("t%02d", i), optimizeCmd, opsalert.StatusSuccess, 0))
	}
	fakeLedger(t, recs)
	got, err := ledger.recent(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("got %d records, want 4", len(got))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd opsnotify && go test ./... 2>&1 | head -20`
Expected: FAIL — `undefined: ecsTaskDetail`, `undefined: handleTask`, `undefined: ledgerReader`, `undefined: ledgerWindow`.

- [ ] **Step 3: Write `opsnotify/ledger.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// ledgerWindow is how many of the newest ledger records to read when computing
// a streak. At ~26 runs/day across all jobs that is roughly a week of history —
// far past any outage whose exact length still matters, and bounded so the
// Lambda's work does not grow with the ledger.
const ledgerWindow = 200

// ledgerReader reads run ledger records straight out of S3.
//
// It goes through internal/s3blob rather than internal/lineupapi/s3lineup —
// which already has this code — because s3lineup imports internal/lineupapi,
// and that transitively pulls internal/fantrax and chromedp into the Lambda.
// s3blob depends only on the AWS S3 SDK.
type ledgerReader struct{ blob *s3blob.Blob }

func newLedgerReader(ctx context.Context, bucket string) (*ledgerReader, error) {
	b, err := s3blob.New(ctx, bucket, "runledger/")
	if err != nil {
		return nil, err
	}
	return &ledgerReader{blob: b}, nil
}

// recent returns the newest `limit` records, newest first. Ledger keys carry an
// inverted-timestamp prefix, so an ascending listing is reverse-chronological.
//
// Sub-objects that share the prefix are skipped, and the walk follows
// continuation tokens before stopping early — the combination that makes a
// "newest N" listing safe (rosterbot-432).
func (l *ledgerReader) recent(ctx context.Context, limit int) ([]opsalert.Record, error) {
	var keys []string
	err := l.blob.Walk(ctx, "", func(o s3blob.Object) bool {
		if strings.Contains(o.Key, "/") {
			return true
		}
		keys = append(keys, o.Key)
		return len(keys) < limit
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys) // defensive: ensure newest-first ordering

	var recs []opsalert.Record
	for _, k := range keys {
		data, found, err := l.blob.Get(ctx, k)
		if err != nil || !found {
			continue
		}
		var r opsalert.Record
		if json.Unmarshal(data, &r) == nil {
			recs = append(recs, r)
		}
	}
	return recs, nil
}
```

- [ ] **Step 4: Write `opsnotify/task.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// botContainer is the container name the task definition and every EventBridge
// target override use.
const botContainer = "bot"

// ecsTaskDetail is the subset of an "ECS Task State Change" detail body this
// handler reads. aws-lambda-go ships no type for this event, so it is declared
// here.
type ecsTaskDetail struct {
	ClusterArn    string `json:"clusterArn"`
	TaskArn       string `json:"taskArn"`
	LastStatus    string `json:"lastStatus"`
	StoppedReason string `json:"stoppedReason"`
	Containers    []struct {
		Name     string `json:"name"`
		ExitCode *int   `json:"exitCode"`
	} `json:"containers"`
	Overrides struct {
		ContainerOverrides []struct {
			Name    string   `json:"name"`
			Command []string `json:"command"`
		} `json:"containerOverrides"`
	} `json:"overrides"`
}

// failed reports whether this stopped task is a failure. The judgement lives in
// Go rather than in the EventBridge event pattern: patterns cannot express
// "exit code absent OR non-zero" over an array of objects without subtlety, and
// a table-testable function is worth ~700 extra invocations a month.
//
// A task with no containers at all never placed, which is a failure — not
// vacuously "everything exited zero".
func (d ecsTaskDetail) failed() bool {
	if len(d.Containers) == 0 {
		return true
	}
	for _, c := range d.Containers {
		if c.ExitCode == nil || *c.ExitCode != 0 {
			return true
		}
	}
	return false
}

// taskID is the ledger's run id: the last ARN segment, matching how
// entrypoint.sh's run_id() derives it from the task metadata endpoint.
func (d ecsTaskDetail) taskID() string {
	if i := strings.LastIndex(d.TaskArn, "/"); i >= 0 {
		return d.TaskArn[i+1:]
	}
	return d.TaskArn
}

// command is the joined container command override, which is exactly the string
// entrypoint.sh passes to `run-ledger --command` — so it is the key the two
// sides agree on. Empty means the task carried no override and is not ours.
func (d ecsTaskDetail) command() string {
	for _, o := range d.Overrides.ContainerOverrides {
		if o.Name == botContainer && len(o.Command) > 0 {
			return strings.Join(o.Command, " ")
		}
	}
	for _, o := range d.Overrides.ContainerOverrides {
		if len(o.Command) > 0 {
			return strings.Join(o.Command, " ")
		}
	}
	return ""
}

// handleTask turns an ECS Task State Change into at most one Pushover.
func handleTask(ctx context.Context, detail json.RawMessage) error {
	var d ecsTaskDetail
	if err := json.Unmarshal(detail, &d); err != nil {
		return err
	}
	if d.LastStatus != "STOPPED" {
		return nil
	}
	command := d.command()
	if command == "" {
		log.Printf("task %s has no container command override; ignoring", d.taskID())
		return nil
	}
	if ledger == nil {
		log.Printf("no ledger reader configured (STATE_BUCKET unset); ignoring task %s", d.taskID())
		return nil
	}

	recs, err := ledger.recent(ctx, ledgerWindow)
	if err != nil {
		return err
	}

	// No ledger record for this task means the entrypoint never reached its
	// final run-ledger write. No streak is computable, and this class of
	// failure is severe and rare, so it always alerts.
	if d.failed() && !hasID(recs, d.taskID()) {
		title, body := opsalert.FormatCrash(command, d.taskID(), d.StoppedReason)
		return sendOrLog(title, body)
	}

	title, body := opsalert.FormatTask(opsalert.Streak(recs, command))
	return sendOrLog(title, body)
}

func hasID(recs []opsalert.Record, id string) bool {
	for _, r := range recs {
		if r.ID == id {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Tidy the module and check its dependency weight**

```bash
cd opsnotify && go mod tidy && cd ..
grep -cE 'chromedp|go-webauthn|go-fantrax' opsnotify/go.sum
```

Expected: `0`. A non-zero count means `internal/lineupapi` leaked in — most likely via the `contract_test.go` added in Task 2. If that happens, move `internal/opsalert/contract_test.go` to `internal/lineupapi/opsalert_contract_test.go` (package `lineupapi_test`) and re-tidy.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd opsnotify && go test ./... -v && cd ..`
Expected: PASS — all of `TestDispatch_*`, `TestHandleTask_*`, `TestEcsTaskDetail_*`, `TestLedgerReader_*`, and the pre-existing `TestFormatMessage`.

- [ ] **Step 7: Verify the whole tree builds and tests**

```bash
go vet ./... && go mod tidy && make build-modules && make test
```

Expected: exit 0 throughout.

- [ ] **Step 8: Commit**

```bash
git add opsnotify/
git commit -m "feat(opsnotify): alert on ECS task failure via run-ledger streaks

Reads the newest 200 ledger records through internal/s3blob (not s3lineup,
which would drag lineupapi -> fantrax -> chromedp into the Lambda) and defers
every judgement to internal/opsalert. A stopped task with no ledger record
never reached the entrypoint's final write — OOM, image-pull, SIGKILL — so it
alerts unconditionally.

Refs rosterbot-naz

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: CDK wiring

**Files:**
- Modify: `infra/infra.go` — new ungated function + ECS rule before the `// --- Phase 2: CodeBuild` comment (currently line 383); rewire the gated `BuildNotifyRule`

**Interfaces:**
- Consumes: the `opsnotify` module from Tasks 4–5 (CDK `Entry: ../opsnotify`).
- Produces: CloudFormation resources `OpsNotify`, `TaskFailRule`, and a rewired `BuildNotifyRule`.

- [ ] **Step 1: Add the ungated function and the ECS rule**

Insert immediately **before** the `// --- Phase 2: CodeBuild (build + push image to ECR on push to main) ---` comment in `infra/infra.go`:

```go
	// --- Ops notification: Pushover for CodeBuild + ECS task outcomes ---
	// Created UNCONDITIONALLY, unlike the CodeBuild rule below. Job-failure
	// alerting must survive a stack deployed without `-c enableBuild=true`,
	// and the previous BuildNotify function lived entirely inside that gate.
	opsNotifyFn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String("OpsNotify"), &awscdklambdagoalpha.GoFunctionProps{
		Entry:        jsii.String("../opsnotify"),
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(15)),
		Environment: &map[string]*string{
			"STATE_BUCKET": stateBucket.BucketName(),
		},
	})
	opsNotifyFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: jsii.Strings("ssm:GetParameter"),
		Resources: jsii.Strings(
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_USER_KEY",
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_API_TOKEN",
		),
	}))
	// Read-only on the run ledger: the notifier derives failure streaks from it
	// and writes nothing. Mirrors the API Lambda's own runledger/ grant.
	stateBucket.GrantRead(opsNotifyFn, jsii.String("runledger/*"))

	// Every scheduled job failure (rosterbot-naz). The pattern deliberately
	// stops at "a task in our cluster reached STOPPED" — whether that stop was
	// a failure is decided in Go (opsnotify/task.go), where "exit code absent
	// OR non-zero" is table-testable instead of an event-pattern puzzle over an
	// array of objects. Cost is ~700 invocations/month, inside the free tier.
	awsevents.NewRule(stack, jsii.String("TaskFailRule"), &awsevents.RuleProps{
		EventPattern: &awsevents.EventPattern{
			Source:     jsii.Strings("aws.ecs"),
			DetailType: jsii.Strings("ECS Task State Change"),
			Detail: &map[string]interface{}{
				"clusterArn": []interface{}{cluster.ClusterArn()},
				"lastStatus": []interface{}{"STOPPED"},
			},
		},
		Targets: &[]awsevents.IRuleTarget{
			awseventstargets.NewLambdaFunction(opsNotifyFn, &awseventstargets.LambdaFunctionProps{}),
		},
	})
```

- [ ] **Step 2: Rewire the gated CodeBuild rule onto the shared function**

Inside the `if v, ok := stack.Node().TryGetContext(jsii.String("enableBuild"))…` block, delete the whole `buildNotifyFn := awscdklambdagoalpha.NewGoFunction(…)` declaration and the `buildNotifyFn.AddToRolePolicy(…)` statement that follows it (currently `infra/infra.go:449-461`), and change the rule's target from `buildNotifyFn` to `opsNotifyFn`.

The remaining rule, with its comment updated:

```go
		// Pushover on every terminal build outcome (rosterbot-00j). This catches
		// every failure phase (install/pre_build/build/deploy) + success — unlike
		// a buildspec curl, which never runs if install/pre_build fail. The
		// target is the shared OpsNotify function created above; only this rule
		// is gated, because it references the gated CodeBuild project.
		awsevents.NewRule(stack, jsii.String("BuildNotifyRule"), &awsevents.RuleProps{
			EventPattern: &awsevents.EventPattern{
				Source:     jsii.Strings("aws.codebuild"),
				DetailType: jsii.Strings("CodeBuild Build State Change"),
				Detail: &map[string]interface{}{
					"project-name": []interface{}{project.ProjectName()},
					"build-status": []interface{}{"SUCCEEDED", "FAILED", "STOPPED"},
				},
			},
			Targets: &[]awsevents.IRuleTarget{
				awseventstargets.NewLambdaFunction(opsNotifyFn, &awseventstargets.LambdaFunctionProps{}),
			},
		})
```

- [ ] **Step 3: Synthesize and verify the template**

```bash
cd infra && npx --yes aws-cdk@2 synth -c enableBuild=true --quiet > /dev/null && cd ..
```

Expected: exit 0, no "Circular dependency" error.

- [ ] **Step 4: Assert the resources are what we intended**

```bash
cd infra && npx --yes aws-cdk@2 synth -c enableBuild=true 2>/dev/null > /tmp/tpl.yaml && cd ..
grep -c "ECS Task State Change"        /tmp/tpl.yaml  # expect 1
grep -c "CodeBuild Build State Change" /tmp/tpl.yaml  # expect 1
grep -c "OpsNotify"                    /tmp/tpl.yaml  # expect >= 1
```

Then verify the notifier survives **without** the build gate — the whole point of moving the function out of it:

```bash
cd infra && npx --yes aws-cdk@2 synth 2>/dev/null > /tmp/tpl-nobuild.yaml && cd ..
grep -c "ECS Task State Change"        /tmp/tpl-nobuild.yaml  # expect 1
grep -c "OpsNotify"                    /tmp/tpl-nobuild.yaml  # expect >= 1
grep -c "CodeBuild Build State Change" /tmp/tpl-nobuild.yaml  # expect 0
```

Finally confirm the old `BuildNotify` **function** is gone rather than left alongside the new one. It should now appear only as part of the rule's identifiers:

```bash
grep -o 'BuildNotify[A-Za-z0-9]*' /tmp/tpl.yaml | sort -u
```

Expected: every result begins `BuildNotifyRule`. A bare `BuildNotify<hex>` means the old function declaration survived the edit in Step 2.

- [ ] **Step 5: Build all modules**

Run: `make build-modules && make test`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add infra/infra.go
git commit -m "feat(infra): EventBridge ECS task failure rule -> OpsNotify

The notifier function moves out of the enableBuild gate — job-failure alerting
must survive a stack deployed without it — and only the CodeBuild rule stays
behind it. Grants read-only on runledger/ so the Lambda can derive streaks.

Closes rosterbot-naz

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 7: Documentation and follow-up issue

**Files:**
- Modify: `CLAUDE.md` — the GHA/AWS section's buildnotify sentence, plus a new `internal/opsalert` architecture entry
- Modify: `docs/aws-deployment.md` — buildnotify references
- Create: bd issue for the out-of-scope heartbeat

- [ ] **Step 1: Find every stale reference**

```bash
grep -rn "buildnotify" --include="*.md" --include="Makefile" --include="*.yml" --include="*.go" . | grep -v "^./opsnotify/" | grep -v "^./.git/"
```

Expected: hits in `CLAUDE.md`, `docs/aws-deployment.md`, `.github/dependabot.yml`, and the `Makefile` comment at line 6. Fix every one.

- [ ] **Step 2: Update `.github/dependabot.yml`**

Change the `/buildnotify` directory entry to `/opsnotify`. The grouping that keeps root and nested modules in one PR must keep covering it.

- [ ] **Step 3: Update the `Makefile` comment**

At `Makefile:6`, change `# lambda/, buildnotify/ and infra/ are SEPARATE Go modules` to `# lambda/, opsnotify/ and infra/ are SEPARATE Go modules`, and update the "(lambda/, then buildnotify/)" parenthetical on the following lines to say `opsnotify/`.

- [ ] **Step 4: Add the `internal/opsalert` architecture entry to `CLAUDE.md`**

Insert after the `**internal/notify**` bullet:

```markdown
**`internal/opsalert`** — decides whether a finished job run is worth waking an operator, and renders the message. A **stdlib-only leaf**: its only consumer is the `opsnotify` Lambda, and `internal/lineupapi` (where the run ledger's wire types live) transitively pulls `internal/fantrax` and therefore chromedp, so `opsalert.Record` redeclares the five ledger fields it needs and `contract_test.go` marshals a real `lineupapi.RunDetail` into it to guard the duplication. `Streak(recs, command)` reads newest-first ledger records and returns one of four verdicts: `Started` (first failure after a success), `Escalated` (the `EscalateAt`=3rd consecutive failure, matched on `==` so an eleven-failure outage escalates exactly once), `Recovered` (first success after failures), or `None`. **The whole decision is derived from the append-only ledger**, which is what removes the counter object and the cooldown timer a naive design needs — streak transitions deduplicate themselves. `RUNNING` records are skipped: they are the start-of-run write that the terminal write later overwrites at the same key, so an in-flight sibling job must not break a streak. The message body quotes the **last non-empty line** of the record's `log_tail`, capped at 300 chars *before* append because `SendPushover` truncates at 1024 silently — for the 2026-07-01 outage that line is the `STALE_CLIENT` error itself, which is the difference between "something broke" and a diagnosis.
```

- [ ] **Step 5: Update the buildnotify sentence in `CLAUDE.md`'s GHA section**

Replace the sentence beginning "A push to `main` also triggers a Pushover alert on build success/failure…" with:

```markdown
> A push to `main` triggers a Pushover alert on build success/failure via an
> EventBridge rule on the `Build` project's CodeBuild state-change events, and
> **every scheduled ECS task that stops is checked for failure** via a second
> rule on `ECS Task State Change` — both target the one `opsnotify/` Lambda
> (SUCCEEDED/FAILED/STOPPED, priority 0, personal ops channel). The function is
> created **unconditionally**; only the CodeBuild rule sits behind
> `enableBuild`, because job-failure alerting must survive a stack deployed
> without that flag. Task failures are judged in Go from the run ledger, not by
> the event pattern — see `internal/opsalert`.
```

- [ ] **Step 6: Update `docs/aws-deployment.md`**

Two edits.

At **line 12**, the architecture bullet reads `- **EventBridge rules** (×9) — 1:1 port of the old GitHub Actions crons (UTC), plus \`ProjectionSite\``. The stack now carries 13 schedule rules plus two notification rules; correct the count and note the notification rules separately:

```markdown
- **EventBridge rules** — 13 schedule rules (1:1 port of the old GitHub Actions crons, UTC, plus `ProjectionSite`, `Archive`, `TeamValues`, `Shadow`), plus two notification rules (`BuildNotifyRule`, `TaskFailRule`) that both target the `OpsNotify` Lambda
```

At **line 64**, replace the whole "Build notifications" bullet with:

```markdown
- **Ops notifications** — two EventBridge rules target one Lambda (`OpsNotify`, built from `opsnotify/`), which reads `PUSHOVER_USER_KEY` / `PUSHOVER_API_TOKEN` from SSM and posts to the personal ops channel at priority 0.
  - `BuildNotifyRule` matches the `Build` project's `CodeBuild Build State Change` events for `SUCCEEDED` / `FAILED` / `STOPPED`. This catches every failure phase (install/pre_build/build/deploy) — a buildspec curl would miss install/pre_build failures since `post_build` never runs then. Created only under `-c enableBuild=true`; the first build that introduces the rule won't notify itself, every subsequent build does.
  - `TaskFailRule` matches `ECS Task State Change` for any task in the cluster reaching `STOPPED` (rosterbot-naz). Whether that stop was a *failure* is decided in Go, not by the event pattern — see `internal/opsalert`. The Lambda derives a failure streak from the run ledger and pushes only on transitions: first failure, third consecutive, and recovery. A stopped task with no ledger record at all never reached the entrypoint's final write (OOM, image-pull, SIGKILL) and always alerts.
  - **The `OpsNotify` function itself is created unconditionally** — only `BuildNotifyRule` sits behind `enableBuild`, because job-failure alerting must survive a stack deployed without that flag.
```

- [ ] **Step 7: File the out-of-scope follow-up**

```bash
bd create --title="Heartbeat alert for a scheduled job that never launches" \
  --type=feature --priority=3 \
  --description="rosterbot-naz covers ECS tasks that run and fail. A job that never launches at all — EventBridge rule disabled, schedule broken, cluster unreachable — emits no ECS event and writes no ledger record, so nothing in that design sees it. Detecting it needs a scheduled heartbeat that asserts each job ran within its expected window (the run ledger already carries command + started_at, so the check is a listing plus a per-job expected-cadence table).

Deferred deliberately, not overlooked: zero occurrences across all 616 expected hourly Lineup slots in the 44 days to 2026-07-31, and the dashboard Infra tab already renders per-artifact staleness against layout.Artifact.MaxAge." \
  --acceptance="A job that has not run within its expected cadence produces one Pushover alert, deduplicated the same way rosterbot-naz's streak transitions are."
```

- [ ] **Step 8: Verify no stale references remain**

```bash
grep -rn "buildnotify" --include="*.md" --include="Makefile" --include="*.yml" --include="*.go" . | grep -v "^./.git/"
```

Expected: no output.

- [ ] **Step 9: Full verification**

```bash
go vet ./... && go mod tidy && make build-modules && make test
```

Expected: exit 0 throughout, no diff left in `go.mod`/`go.sum` from tidy.

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "docs: opsnotify rename + internal/opsalert architecture entry

Refs rosterbot-naz

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Deployment and live verification

Not part of the implementation tasks — run after the branch merges to `main`, since CodeBuild only builds on push to `main` and the buildspec runs `cdk deploy` itself.

1. Confirm the push-to-main build succeeded (a Pushover from the *existing* build path, which now flows through `OpsNotify` — its arrival is itself the CodeBuild regression test).
2. Trigger a deliberate failure and confirm one alert:
   ```bash
   aws ecs run-task --region us-west-1 --cluster <ClusterName> \
     --task-definition <TaskDefArn> --launch-type FARGATE \
     --network-configuration 'awsvpcConfiguration={subnets=[<subnet>],securityGroups=[<sg>],assignPublicIp=ENABLED}' \
     --overrides '{"containerOverrides":[{"name":"bot","command":["definitely-not-a-command"]}]}'
   ```
   Expected: one Pushover titled `Rosterbot: definitely-not-a-command failed`.
3. Re-run the same bogus command twice more. Expected: silent on the second, `still failing` on the third.
4. Run it once with a valid command (`["waivers","--dry-run"]` — same command string, so same streak). Expected: one `recovered after 3 failures` Pushover.
5. Confirm the ordinary hourly `Lineup` run produces **no** alert.

## Rollback

`TaskFailRule` is the only new signal path. Disabling it (`aws events disable-rule --name <TaskFailRule>`) stops all task alerting instantly without touching the CodeBuild path or any bot behaviour. Nothing in the bot, the entrypoint, or the ledger changes in this plan, so there is no data migration to unwind.
