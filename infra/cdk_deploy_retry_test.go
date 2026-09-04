package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests exercise infra/cdk_deploy_retry.sh as running shell code
// (rosterbot-udd) rather than only asserting on buildspec.yml's prose —
// TestBuildspec_RetriesCdkDeployOnConcurrentUpdate (buildspec_test.go) pins
// that the buildspec wires this script in and that its signature list is
// literally present; these pin that the DECISION the script makes is
// actually correct for canned inputs.

// checkMatchScript sources cdk_deploy_retry.sh (which, per its own
// BASH_SOURCE guard, does nothing but define functions when sourced rather
// than executed) and calls is_concurrent_update_failure against the given
// logfile, exiting with that function's own exit status.
const checkMatchScript = `
source "$1"
is_concurrent_update_failure "$2"
`

func writeScratchScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
}

func TestIsConcurrentUpdateFailure_MatchesKnownSignatures(t *testing.T) {
	requireBash(t)
	scriptPath, err := filepath.Abs("cdk_deploy_retry.sh")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	cases := []string{
		`Resource is in UPDATE_IN_PROGRESS state and can not be updated`,
		`Stack InfraStack is in CREATE_IN_PROGRESS state and can not be updated`,
		`some wrapper text ... _IN_PROGRESS state ... more text`,
		`InfraStack is currently being updated`,
		`software.amazon.awssdk.services.cloudformation.model.OperationInProgressException: boom`,
		`com.amazonaws.services.cloudformation.model.ResourceConflictException: conflict`,
		`Resource cannot be updated right now`,
		`Resource can not be updated right now`,
	}

	dir := t.TempDir()
	for i, text := range cases {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			logfile := filepath.Join(dir, fmt.Sprintf("match-%d.log", i))
			if err := os.WriteFile(logfile, []byte(text), 0o644); err != nil {
				t.Fatalf("write logfile: %v", err)
			}
			runner := writeScratchScript(t, dir, fmt.Sprintf("check-%d.sh", i), checkMatchScript)
			cmd := exec.Command("bash", runner, scriptPath, logfile)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("is_concurrent_update_failure did not match %q (should have): %v\noutput: %s", text, err, out)
			}
		})
	}
}

func TestIsConcurrentUpdateFailure_DoesNotMatchUnrelatedFailures(t *testing.T) {
	requireBash(t)
	scriptPath, err := filepath.Abs("cdk_deploy_retry.sh")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	cases := []string{
		"Error: template validation failed: unresolved parameter Foo",
		"panic: runtime error: index out of range",
		"AccessDenied: User is not authorized to perform sts:AssumeRole",
		"",
	}

	dir := t.TempDir()
	for i, text := range cases {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			logfile := filepath.Join(dir, fmt.Sprintf("nomatch-%d.log", i))
			if err := os.WriteFile(logfile, []byte(text), 0o644); err != nil {
				t.Fatalf("write logfile: %v", err)
			}
			runner := writeScratchScript(t, dir, fmt.Sprintf("check-%d.sh", i), checkMatchScript)
			cmd := exec.Command("bash", runner, scriptPath, logfile)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("is_concurrent_update_failure matched %q (should not have)\noutput: %s", text, out)
			}
		})
	}
}

// fakeCdkScript simulates `cdk deploy`: it increments a counter file on
// every invocation and fails with the given message until the counter
// reaches succeedAfter, at which point it prints a success line and exits 0.
const fakeCdkScript = `#!/usr/bin/env bash
set -eu
counterfile="$1"; succeed_after="$2"; msg="$3"
n=0
if [ -f "$counterfile" ]; then
  n=$(cat "$counterfile")
fi
n=$((n + 1))
echo "$n" > "$counterfile"
if [ "$n" -ge "$succeed_after" ]; then
  echo "Deployment successful"
  exit 0
fi
echo "$msg"
exit 1
`

func readCounter(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "0"
	}
	return strings.TrimSpace(string(b))
}

// A concurrent-update failure retries and, once the underlying command
// starts succeeding (the other build's lock cleared), the deploy as a whole
// succeeds.
func TestCdkDeployRetry_RetriesOnConcurrentUpdateThenSucceeds(t *testing.T) {
	requireBash(t)
	scriptPath, err := filepath.Abs("cdk_deploy_retry.sh")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	dir := t.TempDir()
	fakeCdk := writeScratchScript(t, dir, "fakecdk.sh", fakeCdkScript)
	counter := filepath.Join(dir, "counter")
	logfile := filepath.Join(dir, "deploy.log")

	cmd := exec.Command("bash", scriptPath, logfile, "--",
		"bash", fakeCdk, counter, "2", "Resource is in UPDATE_IN_PROGRESS state and can not be updated")
	cmd.Env = append(os.Environ(),
		"CDK_DEPLOY_RETRY_MAX_ATTEMPTS=5",
		"CDK_DEPLOY_RETRY_BACKOFF_SECONDS=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected the retry to succeed on the 2nd attempt, got error: %v\noutput:\n%s", err, out)
	}
	if got := readCounter(t, counter); got != "2" {
		t.Errorf("expected exactly 2 attempts, the fake deploy ran %s times\noutput:\n%s", got, out)
	}
	if !strings.Contains(string(out), "Deployment successful") {
		t.Errorf("expected the successful deploy's own output in the result; got:\n%s", out)
	}
}

// A failure that does NOT match a concurrent-update signature must exit
// non-zero after exactly one attempt — retrying an unrelated failure (a
// template error, a real permissions problem) would hide it behind a long
// bounded-retry delay and is exactly the "trade a loud failure for a quiet
// one" mistake rosterbot-udd exists to avoid.
func TestCdkDeployRetry_DoesNotRetryNonMatchingFailure(t *testing.T) {
	requireBash(t)
	scriptPath, err := filepath.Abs("cdk_deploy_retry.sh")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	dir := t.TempDir()
	fakeCdk := writeScratchScript(t, dir, "fakecdk.sh", fakeCdkScript)
	counter := filepath.Join(dir, "counter")
	logfile := filepath.Join(dir, "deploy.log")

	cmd := exec.Command("bash", scriptPath, logfile, "--",
		"bash", fakeCdk, counter, "3", "Error: template validation failed: unresolved parameter Foo")
	cmd.Env = append(os.Environ(),
		"CDK_DEPLOY_RETRY_MAX_ATTEMPTS=5",
		"CDK_DEPLOY_RETRY_BACKOFF_SECONDS=0",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the non-matching failure to exit non-zero, got success\noutput:\n%s", out)
	}
	if got := readCounter(t, counter); got != "1" {
		t.Errorf("expected exactly 1 attempt (no retry) for a non-matching failure, the fake deploy ran %s times\noutput:\n%s", got, out)
	}
}

// A concurrent-update failure that never clears exhausts the bounded
// attempt count and still exits non-zero — the retry must not become an
// infinite loop, and exhaustion must still be a loud failure.
func TestCdkDeployRetry_ExhaustsBoundedAttempts(t *testing.T) {
	requireBash(t)
	scriptPath, err := filepath.Abs("cdk_deploy_retry.sh")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	dir := t.TempDir()
	fakeCdk := writeScratchScript(t, dir, "fakecdk.sh", fakeCdkScript)
	counter := filepath.Join(dir, "counter")
	logfile := filepath.Join(dir, "deploy.log")

	// succeed_after=100 so the fake deploy never actually succeeds within
	// the 3 attempts this test allows.
	cmd := exec.Command("bash", scriptPath, logfile, "--",
		"bash", fakeCdk, counter, "100", "UPDATE_IN_PROGRESS")
	cmd.Env = append(os.Environ(),
		"CDK_DEPLOY_RETRY_MAX_ATTEMPTS=3",
		"CDK_DEPLOY_RETRY_BACKOFF_SECONDS=0",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected exhausted retries to exit non-zero, got success\noutput:\n%s", out)
	}
	if got := readCounter(t, counter); got != "3" {
		t.Errorf("expected exactly 3 attempts (the bound), the fake deploy ran %s times\noutput:\n%s", got, out)
	}
}
