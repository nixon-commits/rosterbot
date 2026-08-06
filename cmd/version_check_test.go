package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
)

// The exit mapping is the load-bearing policy. The probe may fail the job for
// exactly two conditions it can positively identify: Fantrax rejecting the pin,
// and Fantrax no longer rejecting anything. Everything else exits 0, so a
// Fantrax outage — which already fails every other job and pages through them —
// does not raise a second alert for a cause this probe did not diagnose.
func TestVersionCheckResultLine_ExitPolicy(t *testing.T) {
	staleControl := &probe{status: fantrax.VersionStale, code: "STALE_CLIENT"}

	tests := []struct {
		name        string
		pinned      probe
		control     *probe
		wantFailure bool
		wantContain string
	}{
		{
			name:        "stale pin fails",
			pinned:      probe{status: fantrax.VersionStale, code: "STALE_CLIENT"},
			wantFailure: true,
			wantContain: "STALE_CLIENT",
		},
		{
			name:        "current pin with a discriminating gate succeeds",
			pinned:      probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
			control:     staleControl,
			wantFailure: false,
			wantContain: fantrax.ControlVersion,
		},
		{
			name:        "unrecognized code does not fail the job",
			pinned:      probe{status: fantrax.VersionUnknown, code: "SOME_FUTURE_CODE"},
			wantFailure: false,
			wantContain: "SOME_FUTURE_CODE",
		},
		{
			name:        "transport error does not fail the job",
			pinned:      probe{status: fantrax.VersionUnknown, err: errors.New("dial tcp: connection refused")},
			wantFailure: false,
		},
		// The rosterbot-0a1 case: the gate stopped rejecting an obsolete client,
		// so the pinned probe's "OK" carries no information. Without this the
		// job reads green forever and no heartbeat can tell, since it does
		// launch and does exit 0.
		{
			name:        "control accepted by the gate fails the job",
			pinned:      probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
			control:     &probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
			wantFailure: true,
			wantContain: "PROBE_BLIND",
		},
		{
			name:        "control answering an unrecognized code fails the job",
			pinned:      probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
			control:     &probe{status: fantrax.VersionUnknown, code: "SOME_FUTURE_CODE"},
			wantFailure: true,
			wantContain: "PROBE_BLIND",
		},
		// A control that could not be delivered proves nothing about the gate.
		// The pinned probe was answered moments earlier, so this is a blip, not
		// a broken mechanism — same rule that keeps an outage silent.
		{
			name:        "control transport error warns but does not fail",
			pinned:      probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
			control:     &probe{status: fantrax.VersionUnknown, err: errors.New("dial tcp: connection refused")},
			wantFailure: false,
			wantContain: "control probe inconclusive",
		},
		{
			name:        "no control run still succeeds",
			pinned:      probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
			control:     nil,
			wantFailure: false,
			wantContain: auth_client.APIVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, exitErr := versionCheckResultLine(tt.pinned, tt.control)
			if tt.wantFailure && exitErr == nil {
				t.Fatal("expected a non-nil error so the command exits non-zero")
			}
			if !tt.wantFailure && exitErr != nil {
				t.Fatalf("expected exit 0, got error: %v", exitErr)
			}
			if tt.wantContain == "" {
				return
			}
			// runVersionCheck discards `line` entirely on the failure path (only
			// exitErr.Error() reaches stderr and the exit), so the two fields must
			// be checked on the branch that actually carries the text — asserting
			// against a concatenation of both would pass even if the failure path
			// silently dropped the diagnostic into the discarded field.
			if tt.wantFailure {
				if !strings.Contains(exitErr.Error(), tt.wantContain) {
					t.Errorf("error %q missing %q", exitErr.Error(), tt.wantContain)
				}
				return
			}
			if !strings.Contains(line, tt.wantContain) {
				t.Errorf("line %q missing %q", line, tt.wantContain)
			}
		})
	}
}

// The stale message is quoted verbatim into the Pushover by opsalert, which
// takes the last non-empty line of the log. It has to name the version.
func TestVersionCheckResultLine_StaleMessageNamesThePinnedVersion(t *testing.T) {
	_, exitErr := versionCheckResultLine(probe{status: fantrax.VersionStale, code: "STALE_CLIENT"}, nil)
	if exitErr == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(exitErr.Error(), auth_client.APIVersion) {
		t.Errorf("stale error %q does not name the pinned version", exitErr.Error())
	}
}

// The blind-probe message is the alert body too, and its whole job is to say
// that the green readings were worthless — it has to name both versions so the
// operator can reproduce the two probes by hand from the message alone.
func TestVersionCheckResultLine_BlindMessageNamesBothVersions(t *testing.T) {
	_, exitErr := versionCheckResultLine(
		probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
		&probe{status: fantrax.VersionOK, code: "WARNING_NOT_LOGGED_IN"},
	)
	if exitErr == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{fantrax.ControlVersion, auth_client.APIVersion} {
		if !strings.Contains(exitErr.Error(), want) {
			t.Errorf("blind error %q does not name %q", exitErr.Error(), want)
		}
	}
}

// An empty pageError code is a different diagnosis from an unrecognized one,
// and %q on "" renders as an empty pair of quotes that reads like a truncated
// message in a Pushover.
func TestProbeCode_NamesTheEmptyCase(t *testing.T) {
	if got := probeCode(probe{}); got != "(no pageError)" {
		t.Errorf("probeCode(empty) = %q, want %q", got, "(no pageError)")
	}
	if got := probeCode(probe{code: "STALE_CLIENT"}); got != "STALE_CLIENT" {
		t.Errorf("probeCode = %q, want STALE_CLIENT", got)
	}
}
