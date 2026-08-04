package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// The exit mapping is the load-bearing policy: the probe may only fail the job
// for the one condition it can positively identify. Everything else exits 0, so
// a Fantrax outage — which already fails every other job and pages through
// them — does not raise a second alert for a cause this probe did not diagnose.
func TestVersionCheckResultLine_ExitPolicy(t *testing.T) {
	tests := []struct {
		name        string
		status      fantrax.VersionStatus
		code        string
		err         error
		wantFailure bool
		wantContain string
	}{
		{
			name:        "stale pin is the only failure",
			status:      fantrax.VersionStale,
			code:        "STALE_CLIENT",
			wantFailure: true,
			wantContain: "STALE_CLIENT",
		},
		{
			name:        "current pin succeeds",
			status:      fantrax.VersionOK,
			code:        "WARNING_NOT_LOGGED_IN",
			wantFailure: false,
		},
		{
			name:        "unrecognized code does not fail the job",
			status:      fantrax.VersionUnknown,
			code:        "SOME_FUTURE_CODE",
			wantFailure: false,
			wantContain: "SOME_FUTURE_CODE",
		},
		{
			name:        "transport error does not fail the job",
			status:      fantrax.VersionUnknown,
			err:         errors.New("dial tcp: connection refused"),
			wantFailure: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, exitErr := versionCheckResultLine(tt.status, tt.code, tt.err)
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
	_, exitErr := versionCheckResultLine(fantrax.VersionStale, "STALE_CLIENT", nil)
	if exitErr == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(exitErr.Error(), "185.1.0") {
		t.Errorf("stale error %q does not name the pinned version", exitErr.Error())
	}
}
