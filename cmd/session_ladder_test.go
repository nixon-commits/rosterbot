package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

type recordingConns struct {
	conn *lineupapi.FantraxConnection
	put  []lineupapi.FantraxConnection
	err  error
}

func (c *recordingConns) GetConnection(context.Context, lineupapi.UserID) (*lineupapi.FantraxConnection, bool, error) {
	if c.err != nil {
		return nil, false, c.err
	}
	if c.conn == nil {
		return nil, false, nil
	}
	cp := *c.conn
	return &cp, true, nil
}

func (c *recordingConns) PutConnection(_ context.Context, conn *lineupapi.FantraxConnection) error {
	c.put = append(c.put, *conn)
	return nil
}

type stubSealer struct{}

func (stubSealer) Seal(_ context.Context, _ lineupapi.UserID, plain []byte) ([]byte, error) {
	return append([]byte("sealed:"), plain...), nil
}

func tenantCfg() *config.Config {
	return &config.Config{
		Username: "tenant@example.test",
		Password: "tenant-pass",
		LeagueID: "league-1",
		TeamID:   "tenant-team",
	}
}

// TestClassifySessionProbe is the rule that decides whether a browser login is
// allowed to happen at all, and getting it wrong is expensive in a specific
// way: repeated failed logins are what trigger Fantrax lockout and Cloudflare
// bot-blocking, which is why the design forbids blind retry.
//
// Only ONE upstream signal means "your session aged out". Everything else --
// an outage, a timeout, a stale API version -- means we do not know, and the
// safe reading of "we do not know" is to leave the session alone.
func TestClassifySessionProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want sessionState
	}{
		{"no error is a live session", nil, sessionLive},
		{
			name: "WARNING_NOT_LOGGED_IN is the one signal that means expired",
			err:  errors.New("fantrax API error WARNING_NOT_LOGGED_IN: please log in"),
			want: sessionExpired,
		},
		{
			name: "wrapped by NewClient it is still recognised",
			err: fmt.Errorf("failed to fetch user info during client initialization: %w",
				errors.New("fantrax API error WARNING_NOT_LOGGED_IN: please log in")),
			want: sessionExpired,
		},
		{
			// STALE_CLIENT is our API version pin being out of date. It affects
			// EVERY tenant at once and no login can fix it, so treating it as an
			// expired session would drive a browser login on every job of every
			// tenant against a login page that is already refusing us.
			name: "STALE_CLIENT is not a session problem",
			err:  errors.New("fantrax API error STALE_CLIENT: please refresh"),
			want: sessionUnknown,
		},
		{
			name: "a transport failure is an outage, not an expiry",
			err:  errors.New("dial tcp: connection refused"),
			want: sessionUnknown,
		},
		{
			name: "a 500 is an outage, not an expiry",
			err:  errors.New("login API returned non-200 status code: 503"),
			want: sessionUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySessionProbe(tc.err); got != tc.want {
				t.Errorf("classifySessionProbe(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassifySessionProbe_MatchesTheForksActualError is a CONTRACT test
// against go-fantrax rather than against a string we made up.
//
// The detector matches on the code Fantrax puts on the wire, but it only ever
// sees that code because auth_client.ReadBody chooses to include it in the
// error text. If a fork change stopped doing that, the ladder would silently
// stop firing -- every expired session would classify as "unknown", no
// re-login would ever happen, and the failure would look like Fantrax being
// flaky. This drives the real ReadBody so that becomes a failing test instead.
func TestClassifySessionProbe_MatchesTheForksActualError(t *testing.T) {
	body := `{"pageError":{"code":"WARNING_NOT_LOGGED_IN","title":"Not logged in","text":"Please log in."}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}

	_, err := auth_client.ReadBody(resp)
	if err == nil {
		t.Fatal("auth_client.ReadBody accepted a pageError body; the probe has nothing to classify")
	}
	if got := classifySessionProbe(err); got != sessionExpired {
		t.Fatalf("classifySessionProbe(%q) = %v, want sessionExpired — the fork's error "+
			"no longer carries the code the ladder keys on", err, got)
	}
}

// TestSessionLadder_LiveSessionMintsNothing: the common case must cost nothing
// beyond the probe. A browser login on every run would be both slow and exactly
// the behaviour that gets an account locked.
func TestSessionLadder_LiveSessionMintsNothing(t *testing.T) {
	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	mints := 0
	lad := sessionLadder{
		conns:  conns,
		sealer: stubSealer{},
		probe:  func(string) error { return nil },
		mint: func(lineupapi.FantraxCreds) loginEvidence {
			mints++
			return loginEvidence{}
		},
	}

	if err := lad.refresh(context.Background(), "alice", tenantCfg()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if mints != 0 {
		t.Errorf("minted %d sessions for a live one; want 0", mints)
	}
	if len(conns.put) != 0 {
		t.Errorf("wrote the connection record %d times for a live session; want 0", len(conns.put))
	}
}

// TestSessionLadder_UnknownStateNeverMints is the anti-lockout rule.
//
// During a Fantrax outage every tenant's probe fails. If that counted as an
// expired session, every job of every tenant would drive a fresh browser login
// for as long as the outage lasted -- turning someone else's downtime into our
// accounts being blocked.
func TestSessionLadder_UnknownStateNeverMints(t *testing.T) {
	for _, probeErr := range []error{
		errors.New("dial tcp: connection refused"),
		errors.New("fantrax API error STALE_CLIENT: please refresh"),
	} {
		conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
		mints := 0
		lad := sessionLadder{
			conns:  conns,
			sealer: stubSealer{},
			probe:  func(string) error { return probeErr },
			mint: func(lineupapi.FantraxCreds) loginEvidence {
				mints++
				return loginEvidence{}
			},
		}

		if err := lad.refresh(context.Background(), "alice", tenantCfg()); err != nil {
			t.Errorf("refresh returned %v for %v; an outage is not this function's failure "+
				"to report, and must not mark the tenant", err, probeErr)
		}
		if mints != 0 {
			t.Errorf("minted a session after %v; want 0", probeErr)
		}
		if len(conns.put) != 0 {
			t.Errorf("touched the connection record after %v; want untouched", probeErr)
		}
	}
}

// TestSessionLadder_ExpiredMintsExactlyOnceAndReseals is the happy half of the
// ladder: one re-login, the new cookie sealed back so the NEXT run starts from
// it, and the status left verified.
func TestSessionLadder_ExpiredMintsExactlyOnceAndReseals(t *testing.T) {
	t.Setenv("FANTRAX_COOKIES", "FX_RM=expired")
	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	mints := 0
	lad := sessionLadder{
		conns:  conns,
		sealer: stubSealer{},
		probe:  func(string) error { return errors.New("fantrax API error WARNING_NOT_LOGGED_IN: x") },
		mint: func(lineupapi.FantraxCreds) loginEvidence {
			mints++
			return loginEvidence{FXRM: "fresh-cookie", Info: &fantraxUserInfo{UserID: "fx-1"}}
		},
	}

	if err := lad.refresh(context.Background(), "alice", tenantCfg()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if mints != 1 {
		t.Fatalf("minted %d times; the ladder allows exactly one re-login", mints)
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote the record %d times; want 1", len(conns.put))
	}
	got := conns.put[0]
	if string(got.FXRMCiphertext) != "sealed:fresh-cookie" {
		t.Errorf("FXRMCiphertext = %q; the new session was not sealed back, so the next "+
			"run would start from the expired one again", got.FXRMCiphertext)
	}
	if got.Status != lineupapi.ConnVerified {
		t.Errorf("Status = %q after a SUCCESSFUL re-login; want verified", got.Status)
	}
	if v := os.Getenv("FANTRAX_COOKIES"); v != "FX_RM=fresh-cookie" {
		t.Errorf("FANTRAX_COOKIES = %q; this run would keep using the expired session", v)
	}
}

// TestSessionLadder_FailedReLoginStopsAndDoesNotRetry is the load-bearing half.
//
// A failed re-login means the stored password no longer works. Retrying is
// actively harmful -- repeated failed logins trigger Fantrax lockout and
// Cloudflare bot-blocking, so an hourly schedule would lock a pilot out of
// their own account using credentials they handed us. The stop is structural:
// needs_reconnect makes AuthorizeRun refuse every subsequent run, so there is
// no timer and no attempt counter anywhere.
func TestSessionLadder_FailedReLoginStopsAndDoesNotRetry(t *testing.T) {
	t.Setenv("FANTRAX_COOKIES", "FX_RM=expired")
	t.Setenv("FANTRAX_USERNAME", "tenant@example.test")
	t.Setenv("FANTRAX_PASSWORD", "tenant-pass")

	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	lad := sessionLadder{
		conns:  conns,
		sealer: stubSealer{},
		probe:  func(string) error { return errors.New("fantrax API error WARNING_NOT_LOGGED_IN: x") },
		mint: func(lineupapi.FantraxCreds) loginEvidence {
			return loginEvidence{
				Matched: map[string]bool{"login_form": true, "form_error": true},
				Texts:   map[string]string{"form_error": "Invalid username or password"},
			}
		},
	}

	err := lad.refresh(context.Background(), "alice", tenantCfg())
	if err == nil {
		t.Fatal("refresh accepted a failed re-login; the run must stop")
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote the record %d times; want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.Status != lineupapi.ConnNeedsReconnect {
		t.Errorf("Status = %q; without needs_reconnect AuthorizeRun would let the next "+
			"scheduled run try again, which is the lockout path", got.Status)
	}
	if got.LastError != lineupapi.ConnErrBadCredentials {
		t.Errorf("LastError = %q, want %q — the class comes from the same classifier the "+
			"connect task uses, so the two cannot disagree", got.LastError, lineupapi.ConnErrBadCredentials)
	}
	for _, v := range []string{"FANTRAX_USERNAME", "FANTRAX_PASSWORD", "FANTRAX_COOKIES"} {
		if s := os.Getenv(v); s != "" {
			t.Errorf("%s = %q after a failed re-login; anything downstream could still try "+
				"to authenticate", v, s)
		}
	}
}

// TestSessionLadder_OperatorActionableFailureDoesNotBlameTheTenant: a
// Cloudflare block during re-login is not the tenant's credentials failing, and
// marking needs_reconnect would tell them to re-enter a working password. The
// run still stops -- we have no session -- but the record must not accuse them.
func TestSessionLadder_OperatorActionableFailureDoesNotBlameTheTenant(t *testing.T) {
	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	lad := sessionLadder{
		conns:  conns,
		sealer: stubSealer{},
		probe:  func(string) error { return errors.New("fantrax API error WARNING_NOT_LOGGED_IN: x") },
		mint: func(lineupapi.FantraxCreds) loginEvidence {
			return loginEvidence{Title: "Just a moment...", Matched: map[string]bool{"cloudflare": true}}
		},
	}

	if err := lad.refresh(context.Background(), "alice", tenantCfg()); err == nil {
		t.Fatal("refresh accepted a Cloudflare block; there is no usable session")
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote the record %d times; want 1", len(conns.put))
	}
	if got := conns.put[0]; got.Status == lineupapi.ConnNeedsReconnect {
		t.Errorf("Status = needs_reconnect for %s; the tenant's credentials are not "+
			"implicated and re-entering them cannot help", got.LastError)
	}
	if got := conns.put[0].LastError; got != lineupapi.ConnErrBotChallenge {
		t.Errorf("LastError = %q, want %q", got, lineupapi.ConnErrBotChallenge)
	}
}
