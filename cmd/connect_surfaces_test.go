package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// TestConnectStatusReachesEveryRenderingSurface.
//
// A new ConnStatus is inert everywhere in Go — every consumer goes through
// Usable() and there is no exhaustive switch — which is exactly what makes it
// dangerous on the dashboard, where each surface keys off the raw string and
// falls through to something benign for anything it does not recognise:
// settings.js renders an unknown status as the bare word with badge-info,
// tenants.js's attentionFrom returns "—" (a red-free, owner-free row on the one
// screen the operator uses to find failures), and app.js's global banner
// returns early and never renders at all. Each fallthrough is silence on the
// surface that most needed to speak.
//
// A grep test rather than a DOM test because there is no JS harness in this
// repo, and a checked claim beats a comment: this is the third finding of
// rosterbot-ch0s ("it degraded to silence") asserted rather than promised.
func TestConnectStatusReachesEveryRenderingSurface(t *testing.T) {
	// The wants are precise CODE TOKENS, not the bare status word, and that is
	// not fussiness: the first version asked only whether the file mentioned
	// "interrupted" anywhere, and passed against a mutation that deleted the
	// operator row's handling while leaving its badge entry in place. A file
	// with two places to update needs two assertions.
	for _, tc := range []struct {
		file string
		want []string
		why  string
	}{
		{
			// The three maps moved out of settings.js into connstate.js when the
			// Runs tab started rendering the same vocabulary (rosterbot-jg92).
			// The guard follows the copy: this file is now what both the tenant's
			// account page and the runs chip read.
			file: "connstate.js",
			want: []string{
				// CONNECTION_COPY: the badge and its wording.
				`  interrupted: [`,
				// FAILURE_COPY: what the class means to the person reading it.
				// The key sits alone on its line above a wrapped sentence, which
				// is what distinguishes it from the terse map below.
				`  ` + lineupapi.ConnErrVerificationInterrupted + ":\n",
				// CONNECT_CHIP: the short form the runs table can fit. Its value
				// is a single string on the same line as the key.
				`  ` + lineupapi.ConnErrVerificationInterrupted + `: "`,
			},
			why: "the tenant's own account page renders an unknown status as the raw " +
				"string and an unknown class as the raw class, and the runs chip " +
				"falls back to the raw class with no short label",
		},
		{
			// settings.js and runs.js must keep READING that shared vocabulary.
			// A page that re-declared its own copy would drift from the feed's
			// wording in cmd/connect_feed.go and from the other page's.
			file: "settings.js",
			want: []string{
				`from "./connstate.js"`,
				// The failure paragraph is gated on the status, not on the
				// presence of a class. session_ladder.go's stop() writes
				// conn.LastError on all three routes but moves the status only
				// on the tenant-actionable one, so an ungated render puts a red
				// "Try connecting again in a minute" under a green "Connected"
				// badge — and that retry is a fresh chromedp login, the
				// documented Fantrax lockout trigger.
				`conn.last_error && conn.status === "verified"`,
			},
			why: "the account page must render the shared connection vocabulary rather " +
				"than a private copy of it, and must not invite a reconnect on a " +
				"connection that is verified and working",
		},
		{
			file: "runs.js",
			want: []string{`from "./connstate.js"`, `run.connect`},
			why: "without the connect verdict the Runs tab shows only the task exit " +
				"status, which reads SUCCESS on every tenant-actionable connect failure",
		},
		{
			file: "tenants.js",
			want: []string{
				// CONN_TONE: the badge on the operator's row.
				`  interrupted: [`,
				// attentionFrom: who has to act. Without it the row reads "—",
				// i.e. healthy, on the one screen built to find failures.
				`t.conn_status === "interrupted"`,
			},
			why: "attentionFrom returns \"—\" for a status it does not know, so the " +
				"operator's row reads as healthy",
		},
		{
			file: "app.js",
			want: []string{
				// BANNER_COPY: the global, unmissable tenant banner.
				`  interrupted: [`,
			},
			why: "showConnectionBanner returns early for a status it does not know, so " +
				"the tenant learns the bot stopped managing their team only by " +
				"visiting Settings",
		},
	} {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("..", "web", "dashboard", tc.file)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			code := stripLineComments(string(b))
			for _, want := range tc.want {
				if !strings.Contains(code, want) {
					t.Errorf("%s never mentions %q outside its comments — %s",
						tc.file, want, tc.why)
				}
			}
		})
	}
}

// TestConnectFailureMessage_NamesTheRemedyForAnInterruptedCheck.
//
// The class exists so a person can be told WHICH failure happened; the whole
// point of this one is that the remedy is the opposite of every other
// tenant-facing class — retry, do NOT re-enter a password. A message that fell
// through to the default ("the sign-in did not complete and Fantrax did not say
// why") would throw that away at the last step.
func TestConnectFailureMessage_NamesTheRemedyForAnInterruptedCheck(t *testing.T) {
	got := connectFailureMessage(lineupapi.ConnErrVerificationInterrupted)

	if got == connectFailureMessage("some-class-that-does-not-exist") {
		t.Fatal("fell through to the default wording; the tenant is told nothing about " +
			"the one failure whose remedy differs from every other class")
	}
	if !strings.Contains(strings.ToLower(got), "password is not the problem") {
		t.Errorf("message does not say the password is fine: %q", got)
	}
}

// stripLineComments drops whole-line // comments.
//
// It exists because the first version of the test above passed against a
// mutation that deleted the banner's handling of the new status: these files
// discuss their own decisions at length, so the word survived in the prose that
// explained it. A guard that matches its own documentation is not a guard.
//
// Whole-line only, deliberately: stripping a trailing comment needs to know
// about "https://" and string literals, and every declaration these tests care
// about is a table entry on a line of its own.
func stripLineComments(src string) string {
	var keep []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}
