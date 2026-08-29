package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// inviteOutput runs the invite command with a captured stdout, restoring the
// package-level flag vars afterwards so tests cannot leak into each other (the
// flags are cobra globals, shared by every test in this package).
func inviteOutput(t *testing.T, apply func()) string {
	t.Helper()
	email, team, dir, dry := inviteEmail, inviteTeamID, inviteLocalDir, inviteDryRun
	t.Cleanup(func() {
		inviteEmail, inviteTeamID, inviteLocalDir, inviteDryRun = email, team, dir, dry
	})
	apply()

	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&buf)
	if err := runInvite(c, nil); err != nil {
		t.Fatalf("runInvite: %v", err)
	}
	return buf.String()
}

// TestInvite_NamesTheBackendItMintedAgainst.
//
// openEnrollmentStore picks between local files and DynamoDB from the
// environment and says nothing, so a link minted against .lineup/ on a laptop
// is indistinguishable on the terminal — and in the message the admin then
// sends — from a production one. It fails only when the invitee clicks it,
// days later, with the admin no longer looking.
func TestInvite_NamesTheBackendItMintedAgainst(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	dir := t.TempDir()

	out := inviteOutput(t, func() {
		inviteEmail, inviteTeamID, inviteLocalDir, inviteDryRun = "pilot@example.test", "", dir, false
	})

	if !strings.Contains(out, "backend") {
		t.Fatalf("invite printed no backend line:\n%s", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("the backend line does not name the local directory %q; an admin cannot "+
			"tell this link from a production one:\n%s", dir, out)
	}
}

// TestInvite_DryRunNamesTheBackendToo. The dry run exists to answer "who would
// this create" before anything is written, and WHERE is half of that answer —
// it is also the only invite path `make run-all` exercises.
func TestInvite_DryRunNamesTheBackendToo(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	out := inviteOutput(t, func() {
		inviteEmail, inviteLocalDir, inviteDryRun = "pilot@example.test", ".lineup", true
	})
	if !strings.Contains(out, "backend") {
		t.Errorf("dry run printed no backend line:\n%s", out)
	}
}

// TestEnrollmentBackend_DescribesEachBranch pins the description to the same
// two environment variables openEnrollmentStore branches on. A description that
// drifted from the branch would be worse than none: it would state the wrong
// answer confidently.
func TestEnrollmentBackend_DescribesEachBranch(t *testing.T) {
	dir := inviteLocalDir
	t.Cleanup(func() { inviteLocalDir = dir })

	t.Run("local files when STATE_BUCKET is unset", func(t *testing.T) {
		t.Setenv("STATE_BUCKET", "")
		t.Setenv("IDENTITY_TABLE", "rosterbot-identity")
		inviteLocalDir = ".lineup"
		got := enrollmentBackend()
		if !strings.Contains(got, ".lineup") || strings.Contains(got, "rosterbot-identity") {
			t.Errorf("enrollmentBackend() = %q; with no STATE_BUCKET the store is local "+
				"files regardless of IDENTITY_TABLE", got)
		}
	})

	t.Run("dynamodb names the table", func(t *testing.T) {
		t.Setenv("STATE_BUCKET", "rosterbot-state")
		t.Setenv("IDENTITY_TABLE", "rosterbot-identity")
		if got := enrollmentBackend(); !strings.Contains(got, "rosterbot-identity") {
			t.Errorf("enrollmentBackend() = %q; want the table named", got)
		}
	})

	t.Run("a misconfiguration says so rather than looking production-shaped", func(t *testing.T) {
		t.Setenv("STATE_BUCKET", "rosterbot-state")
		t.Setenv("IDENTITY_TABLE", "")
		if got := enrollmentBackend(); !strings.Contains(got, "IDENTITY_TABLE") {
			t.Errorf("enrollmentBackend() = %q; the branch openEnrollmentStore rejects "+
				"must not read as a healthy one", got)
		}
	})
}
