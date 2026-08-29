package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	feed := &memFeed{}
	var log bytes.Buffer
	lad := sessionLadder{
		conns:  conns,
		sealer: stubSealer{},
		feed:   feed,
		out:    &log,
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

	// THE PERSON MUST BE TOLD. needs_reconnect makes AuthorizeRun refuse every
	// later run, so this is the last moment anything runs for this tenant until
	// they act -- and until rosterbot-zi4u nothing here wrote the feed at all.
	// A session that died mid-week left the lineups silently unmanaged with the
	// only record of why on a connection row nobody reads.
	if len(feed.written) != 1 {
		t.Fatalf("wrote %d feed entries, want exactly 1; the tenant is stopped and "+
			"never told why", len(feed.written))
	}
	n := feed.written[0]
	if n.UserID != "alice" {
		t.Errorf("UserID = %q, want alice — a record that cannot say whose it is once "+
			"read is unverifiable", n.UserID)
	}
	if n.Status != "failure" {
		t.Errorf("Status = %q, want failure", n.Status)
	}
	if n.Message == lineupapi.ConnErrBadCredentials || n.Message == "" {
		t.Errorf("Message = %q; a user needs an answer, not the raw class", n.Message)
	}
}

// TestSessionLadder_OperatorActionableFailureDoesNotBlameTheTenant: a
// Cloudflare block during re-login is not the tenant's credentials failing, and
// marking needs_reconnect would tell them to re-enter a working password. The
// run still stops -- we have no session -- but the record must not accuse them.
func TestSessionLadder_OperatorActionableFailureDoesNotBlameTheTenant(t *testing.T) {
	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	feed := &memFeed{}
	var log bytes.Buffer
	lad := sessionLadder{
		conns:  conns,
		sealer: stubSealer{},
		feed:   feed,
		out:    &log,
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
	if len(feed.written) != 0 {
		t.Errorf("told the tenant about a failure they cannot act on: %+v", feed.written)
	}

	// THE CONSOLE IS THE OPERATOR CHANNEL ON THIS PATH, and the wording has to
	// name the session refresh. An operator told "connect blocked" goes to the
	// connect task and the dashboard connect flow and finds nothing happened
	// there, because nothing did -- this fired inside a scheduled optimize.
	got := log.String()
	for _, want := range []string{"session re-login blocked", lineupapi.ConnErrBotChallenge, "alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("console record missing %q; operator sees:\n%s", want, got)
		}
	}
	if strings.Contains(got, "connect blocked") {
		t.Errorf("console names the connect task for a scheduled session refresh:\n%s", got)
	}
	// A PUSH HERE WOULD BE LEVEL-TRIGGERED. stop leaves Status=ConnVerified on a
	// bot challenge, so AuthorizeRun keeps granting and every one of the day's
	// ~24 scheduled jobs re-runs this ladder. Pushing would restate the same
	// standing condition on each -- the "30 pushes in 24 hours" failure -- while
	// the operator is already told once per outage by opsalert.Streak off the
	// non-zero exit this returns. Asserting the console line above is what makes
	// re-adding a push visible: the push has no console artifact.
	if !strings.Contains(got, "not pushed") {
		t.Errorf("the console record does not state that no push was sent, so a "+
			"reinstated level-triggered push would leave no trace:\n%s", got)
	}
}

// TestSessionLadder_NotifyingIsSoft: the feed is a best-effort side channel.
// The run is already stopping with a diagnosed class on the connection record,
// and a notification store hiccup must not replace that outcome with a crash --
// nor silently swallow itself, which is why recordConnectFailure prints.
func TestSessionLadder_NotifyingIsSoft(t *testing.T) {
	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	lad := sessionLadder{
		conns:  conns,
		sealer: stubSealer{},
		feed:   &memFeed{err: errNotWritable},
		out:    &bytes.Buffer{},
		probe:  func(string) error { return errors.New("fantrax API error WARNING_NOT_LOGGED_IN: x") },
		mint: func(lineupapi.FantraxCreds) loginEvidence {
			return loginEvidence{
				Matched: map[string]bool{"login_form": true, "form_error": true},
				Texts:   map[string]string{"form_error": "Invalid username or password"},
			}
		},
	}

	if err := lad.refresh(context.Background(), "alice", tenantCfg()); err == nil {
		t.Fatal("refresh accepted a failed re-login; the run must stop")
	}
	if len(conns.put) != 1 || conns.put[0].Status != lineupapi.ConnNeedsReconnect {
		t.Errorf("an unwritable feed changed the recorded outcome: %+v", conns.put)
	}
}

// TestSessionLadder_LiveSessionTellsNobody. This runs on EVERY scheduled job of
// every tenant, so a notification on the healthy path would bury the one that
// matters.
func TestSessionLadder_LiveSessionTellsNobody(t *testing.T) {
	feed := &memFeed{}
	var log bytes.Buffer
	lad := sessionLadder{
		conns:  &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified)},
		sealer: stubSealer{},
		feed:   feed,
		out:    &log,
		probe:  func(string) error { return nil },
		mint:   func(lineupapi.FantraxCreds) loginEvidence { return loginEvidence{} },
	}

	if err := lad.refresh(context.Background(), "alice", tenantCfg()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(feed.written) != 0 || log.Len() != 0 {
		t.Errorf("a healthy session notified somebody: feed=%+v console=%q",
			feed.written, log.String())
	}
}
