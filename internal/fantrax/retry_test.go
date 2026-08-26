package fantrax

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// fastBackoff keeps the retry tests in the microsecond range; the real
// schedule is fantraxBackoff.
var fastBackoff = []time.Duration{time.Microsecond, time.Microsecond}

func forkErr(code int) error {
	return fmt.Errorf("API returned non-200 status code: %d", code)
}

// A 524 is Cloudflare's origin-timeout status and is exactly the blip that
// killed run d9b89a97. One of them must not kill the call.
func TestWithRetry_RecoversFromASingle524(t *testing.T) {
	calls := 0
	got, err := withRetry("getAllMatchups", fastBackoff, func() (string, error) {
		calls++
		if calls == 1 {
			return "", forkErr(524)
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("want recovery, got error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("want %q, got %q", "ok", got)
	}
	if calls != 2 {
		t.Fatalf("want 2 attempts, got %d", calls)
	}
}

// The whole point of not copying projections.retryableStatus: Fantrax is
// authenticated, so a 403 is a real session answer that the session ladder
// re-logins for. Retrying it burns wall-clock and cannot succeed.
func TestWithRetry_DoesNotRetryAnAuthAnswer(t *testing.T) {
	for _, code := range []int{401, 403} {
		calls := 0
		_, err := withRetry("getAllMatchups", fastBackoff, func() (string, error) {
			calls++
			return "", forkErr(code)
		})
		if err == nil {
			t.Fatalf("%d: want error", code)
		}
		if calls != 1 {
			t.Fatalf("%d: want exactly 1 attempt, got %d", code, calls)
		}
	}
}

// A persistently stalled origin must still terminate, and the final error has
// to name the call — the ledger's log_tail is the only diagnostic an operator
// gets for a scheduled run.
func TestWithRetry_ExhaustsAndNamesTheCall(t *testing.T) {
	calls := 0
	_, err := withRetry("getAllMatchups", fastBackoff, func() (string, error) {
		calls++
		return "", forkErr(524)
	})
	if err == nil {
		t.Fatal("want error after exhaustion")
	}
	if calls != len(fastBackoff)+1 {
		t.Fatalf("want %d attempts, got %d", len(fastBackoff)+1, calls)
	}
	if !errors.Is(err, errRetryExhausted) {
		t.Fatalf("want errRetryExhausted, got %v", err)
	}
	if got := err.Error(); !contains(got, "getAllMatchups") || !contains(got, "524") {
		t.Fatalf("error must name the call and the status, got %q", got)
	}
}

// A transport error carries no status at all. It cannot have been applied
// server-side, so it is always worth another attempt.
func TestWithRetry_RetriesATransportError(t *testing.T) {
	calls := 0
	_, err := withRetry("getAllMatchups", fastBackoff, func() (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("dial tcp: connection reset by peer")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("want recovery, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("want 2 attempts, got %d", calls)
	}
}

func TestStatusOf(t *testing.T) {
	cases := []struct {
		err  error
		code int
		ok   bool
	}{
		{forkErr(524), 524, true},
		{fmt.Errorf("get matchup week: %w", forkErr(503)), 503, true},
		{errors.New("dial tcp: timeout"), 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		code, ok := statusOf(c.err)
		if code != c.code || ok != c.ok {
			t.Fatalf("statusOf(%v) = (%d,%v), want (%d,%v)", c.err, code, ok, c.code, c.ok)
		}
	}
}

func TestRetryableFantraxStatus(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504, 520, 524}
	for _, c := range retryable {
		if !retryableFantraxStatus(c) {
			t.Errorf("want %d retryable", c)
		}
	}
	permanent := []int{200, 400, 401, 403, 404}
	for _, c := range permanent {
		if retryableFantraxStatus(c) {
			t.Errorf("want %d permanent", c)
		}
	}
}

// Exhaustion must keep the ORIGINAL error reachable, not just say "retries
// exhausted". The wrapper uses two %w verbs for exactly this: an operator
// reading the ledger's log_tail needs the cause, and a caller matching on a
// sentinel needs errors.Is to still reach through (rosterbot-exaf).
func TestWithRetry_ExhaustionKeepsTheUnderlyingErrorReachable(t *testing.T) {
	underlying := errors.New("read tcp 192.168.50.2:61462->104.18.7.8:443: operation timed out")

	_, err := withRetry("getTeamRosterInfoRaw", fastBackoff, func() (string, error) {
		return "", underlying
	})
	if err == nil {
		t.Fatal("want error after exhaustion")
	}
	if !errors.Is(err, errRetryExhausted) {
		t.Errorf("want errRetryExhausted, got %v", err)
	}
	if !errors.Is(err, underlying) {
		t.Errorf("the original error must stay reachable through errors.Is, got %v", err)
	}
	if !contains(err.Error(), "getTeamRosterInfoRaw") {
		t.Errorf("error must name the call, got %q", err.Error())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
