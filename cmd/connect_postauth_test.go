package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/pmurley/go-fantrax/auth_client"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// postAuthConns is a connectStore that records every write.
//
// The whole bead is about WHAT GETS WRITTEN to a tenant's record when something
// after a successful Fantrax login fails, so the assertions below are all about
// this slice. The prior attempt at rosterbot-ch0s wrote nothing at all on its
// new path and no test noticed.
type postAuthConns struct {
	conn *lineupapi.FantraxConnection
	put  []lineupapi.FantraxConnection

	claimErr error
	putErr   error
}

func (c *postAuthConns) GetConnection(context.Context, lineupapi.UserID) (*lineupapi.FantraxConnection, bool, error) {
	if c.conn == nil {
		return nil, false, nil
	}
	cp := *c.conn
	return &cp, true, nil
}

func (c *postAuthConns) PutConnection(_ context.Context, conn *lineupapi.FantraxConnection) error {
	if c.putErr != nil {
		return c.putErr
	}
	c.put = append(c.put, *conn)
	return nil
}

func (c *postAuthConns) ClaimTeam(context.Context, lineupapi.UserID, string) error {
	return c.claimErr
}

func (c *postAuthConns) GetUser(context.Context, lineupapi.UserID) (*lineupapi.User, bool, error) {
	return nil, false, nil
}

// provenLogin is what a healthy headless login returns: a cookie plus the
// account it belongs to.
func provenLogin(lineupapi.FantraxCreds) loginEvidence {
	return loginEvidence{FXRM: "fx-1", Info: &fantraxUserInfo{UserID: "fantrax-user-1"}}
}

// postAuthDeps wires connectTenant against fakes only. Every field is set, so a
// test cannot accidentally reach the network through a nil default.
func postAuthDeps(t *testing.T, conns *postAuthConns, feed *memFeed, out *bytes.Buffer) connectDeps {
	t.Helper()
	return connectDeps{
		conns:  conns,
		opener: stubOpener{},
		sealer: stubSealer{},
		login:  provenLogin,
		teams:  func() ([]string, error) { return []string{"tenant-team"}, nil },
		feed:   feed,
		push:   func(string) { t.Error("a post-auth failure pushed to the operator's phone") },
		out:    out,
		now:    func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) },
	}
}

func pendingConn(t *testing.T) *postAuthConns {
	t.Helper()
	return &postAuthConns{conn: connFor(t, "alice", lineupapi.ConnPending)}
}

// TestConnectTenant_PostLoginTeamLookupFailureDoesNotBlameTheTenant is the
// bead's headline case, asserted AT THE CALL SITE.
//
// A network blip fetching MyTeamIDs happens after Fantrax has already issued a
// session cookie, i.e. after Fantrax itself has said the credentials are good.
// It used to record login_challenge_or_timeout and needs_reconnect, which tells
// the tenant their password no longer works and stops every later run until
// they re-enter one that was never wrong.
//
// It drives connectTenant rather than a helper deliberately. The 2026-08-26
// attempt at this bead tested its decision function, so reverting the call site
// to the buggy one-liner left the whole new test file green.
func TestConnectTenant_PostLoginTeamLookupFailureDoesNotBlameTheTenant(t *testing.T) {
	conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
	d := postAuthDeps(t, conns, feed, out)
	d.teams = func() ([]string, error) { return nil, errors.New("fantrax: connection reset by peer") }

	err := connectTenant(context.Background(), "alice", d)

	// t.Error, not t.Fatal: the status assertions below are the heart of the
	// bead, and a mutation that changes the route must be reported against all
	// of them rather than stopping at the first.
	if err == nil {
		t.Error("returned nil; only the operator can act on a Fantrax outage, and the " +
			"non-zero exit is the channel that reaches them")
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d connection records, want exactly 1 — not demoting is not the "+
			"same as saying nothing", len(conns.put))
	}
	got := conns.put[0]
	if got.Status == lineupapi.ConnNeedsReconnect {
		t.Errorf("recorded %q: the tenant is told to re-enter credentials Fantrax had "+
			"just accepted", got.Status)
	}
	if got.Status != lineupapi.ConnInterrupted {
		t.Errorf("Status = %q, want %q", got.Status, lineupapi.ConnInterrupted)
	}
	if got.LastError != lineupapi.ConnErrVerificationInterrupted {
		t.Errorf("LastError = %q, want %q", got.LastError, lineupapi.ConnErrVerificationInterrupted)
	}
	if len(feed.written) != 1 {
		t.Fatalf("wrote %d feed entries, want 1 — the tenant's connection is unconfirmed "+
			"and only they can retry it", len(feed.written))
	}
	if err != nil && !strings.Contains(err.Error(), "connection reset by peer") {
		t.Errorf("returned error %q does not quote the underlying failure; that string is "+
			"the last line of log_tail and therefore what opsalert pages with", err)
	}
}

// TestConnectTenant_IdentityFailureAfterAProvenCookieIsRetryable is the bead's
// second named case at the call site: a Fantrax 5xx inside auth_client.NewClient
// after the browser login has already produced an FX_RM cookie.
//
// It used to reach ConnErrBadCredentials — "Fantrax rejected that username or
// password", the harshest class in the vocabulary — for an outage.
func TestConnectTenant_IdentityFailureAfterAProvenCookieIsRetryable(t *testing.T) {
	conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
	d := postAuthDeps(t, conns, feed, out)
	d.login = func(lineupapi.FantraxCreds) loginEvidence {
		return loginEvidence{FXRM: "fx-1", IdentityErr: errors.New("fantrax API error 503")}
	}

	if err := connectTenant(context.Background(), "alice", d); err == nil {
		t.Fatal("returned nil for a Fantrax outage; the operator hears about it only " +
			"through the non-zero exit")
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d connection records, want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.LastError == lineupapi.ConnErrBadCredentials {
		t.Error("recorded bad_credentials for a Fantrax 5xx: Fantrax had already issued " +
			"the cookie, so it had already accepted the password")
	}
	if got.Status != lineupapi.ConnInterrupted {
		t.Errorf("Status = %q, want %q", got.Status, lineupapi.ConnInterrupted)
	}
}

// TestConnectTenant_NoPostProofFailureIsLeftPending covers finding 3 of the
// bead across every failure that can happen after the proof.
//
// NAMED FOR WHAT IT COVERS, not for a universal it does not: the failures
// BEFORE the login (KMS Open, a malformed credential blob, an unreadable store)
// still return with the record at ConnPending, which settings.js renders as
// "Checking your credentials…" indefinitely. Those are out of this bead's scope
// and are recorded as follow-up work; what is closed here is every path past
// the point where Fantrax accepted the sign-in.
func TestConnectTenant_NoPostProofFailureIsLeftPending(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*connectDeps, *postAuthConns)
	}{
		{
			name: "the team-ownership lookup fails",
			apply: func(d *connectDeps, _ *postAuthConns) {
				d.teams = func() ([]string, error) { return nil, errors.New("fantrax 502") }
			},
		},
		{
			name: "the team claim fails on something that is not a conflict",
			apply: func(_ *connectDeps, c *postAuthConns) {
				c.claimErr = errors.New("dynamodb: throughput exceeded")
			},
		},
		{
			name: "sealing the session fails",
			apply: func(d *connectDeps, _ *postAuthConns) {
				d.sealer = failingSealer{}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
			d := postAuthDeps(t, conns, feed, out)
			tc.apply(&d, conns)

			err := connectTenant(context.Background(), "alice", d)

			if err == nil {
				t.Error("returned nil; nobody is paged for a silent success")
			}
			if len(conns.put) == 0 {
				t.Fatal("wrote no connection record: the tenant's page shows " +
					"\"Checking your credentials…\" with nothing that will ever resolve it")
			}
			got := conns.put[len(conns.put)-1]
			if got.Status == lineupapi.ConnPending {
				t.Errorf("left the record at %q", got.Status)
			}
			if got.Status != lineupapi.ConnInterrupted {
				t.Errorf("Status = %q, want %q", got.Status, lineupapi.ConnInterrupted)
			}
			if len(feed.written) == 0 {
				t.Error("told the tenant nothing about their own unconfirmed connection")
			}
		})
	}
}

// TestConnectTenant_ClaimTeamErrorsAreSplitThreeWays.
//
// ClaimTeam returns three different things and the call site used to collapse
// them into one: ErrTeamTaken (a real conflict — the tenant's problem),
// ErrUserConflict (the profile it must update is missing or moved — our bug,
// and retrying cannot help), and a raw store error (transient). A DynamoDB blip
// used to tell the tenant "that Fantrax team is already claimed by another
// account".
func TestConnectTenant_ClaimTeamErrorsAreSplitThreeWays(t *testing.T) {
	for _, tc := range []struct {
		name       string
		claimErr   error
		wantStatus lineupapi.ConnStatus
		wantClass  string
		wantWrite  bool
	}{
		{
			name:       "a genuine conflict is the tenant's to resolve",
			claimErr:   lineupapi.ErrTeamTaken,
			wantStatus: lineupapi.ConnNeedsReconnect,
			wantClass:  lineupapi.ConnErrTeamClaimed,
			wantWrite:  true,
		},
		{
			name:       "a store blip is not a conflict",
			claimErr:   errors.New("dynamodb: throughput exceeded"),
			wantStatus: lineupapi.ConnInterrupted,
			wantClass:  lineupapi.ConnErrVerificationInterrupted,
			wantWrite:  true,
		},
		{
			// Deliberately writes nothing: a claim that succeeded against a
			// profile that is not there is our bug, and dressing it as
			// "try again in a minute" would send the tenant round a loop that
			// cannot resolve. The non-zero exit is the honest channel.
			name:      "a missing profile is our bug, not a transient",
			claimErr:  lineupapi.ErrUserConflict,
			wantWrite: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
			conns.claimErr = tc.claimErr
			d := postAuthDeps(t, conns, feed, out)

			err := connectTenant(context.Background(), "alice", d)

			if !tc.wantWrite {
				if err == nil {
					t.Fatal("returned nil for a condition only we can fix")
				}
				if len(conns.put) != 0 {
					t.Errorf("wrote %+v; there is no outcome to record for our own bug", conns.put)
				}
				return
			}
			if len(conns.put) != 1 {
				t.Fatalf("wrote %d connection records, want 1", len(conns.put))
			}
			got := conns.put[0]
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.LastError != tc.wantClass {
				t.Errorf("LastError = %q, want %q", got.LastError, tc.wantClass)
			}
		})
	}
}

// TestConnectRunFail_RefusesAPostAuthVerdictWithoutProof.
//
// Go cannot stop an in-package literal from forging loginProof{}, exactly as it
// cannot for teamProof, so the non-demoting route refuses the inert value at
// runtime. Refusing BEFORE any write or notification matters: a record that
// said "interrupted" without evidence would be a claim that Fantrax accepted a
// sign-in nobody watched succeed.
func TestConnectRunFail_RefusesAPostAuthVerdictWithoutProof(t *testing.T) {
	conns, feed := pendingConn(t), &memFeed{}
	c := connectRun{
		connectDeps: postAuthDeps(t, conns, feed, &bytes.Buffer{}),
		ctx:         context.Background(),
		uid:         "alice",
		conn:        conns.conn,
	}

	err := c.fail(connectVerdict{class: lineupapi.ConnErrVerificationInterrupted})

	if err == nil {
		t.Fatal("accepted a post-auth verdict carrying a zero loginProof")
	}
	for _, w := range conns.put {
		if w.Status == lineupapi.ConnInterrupted {
			t.Fatalf("a refused verdict still recorded %q", w.Status)
		}
	}
	if len(feed.written) != 0 {
		t.Errorf("a refused verdict still told the tenant: %+v", feed.written)
	}
}

// TestClassifyLogin_ACookieWithAFailedIdentityCheckIsNotBadCredentials is the
// classifier half of the bead's second case.
//
// Fantrax issues FX_RM only once it has accepted a username and password, so a
// cookie in hand plus a failed identity call is an outage between us and
// Fantrax. Reading it as a rejected password is the harshest possible answer to
// evidence that says the opposite.
func TestClassifyLogin_ACookieWithAFailedIdentityCheckIsNotBadCredentials(t *testing.T) {
	proof, v := classifyLogin(loginEvidence{
		FXRM:        "fx-1",
		IdentityErr: errors.New("fantrax API error 503"),
	})

	if v.class == lineupapi.ConnErrBadCredentials {
		t.Error("classified a Fantrax 5xx as a rejected password")
	}
	if v.class != lineupapi.ConnErrVerificationInterrupted {
		t.Errorf("class = %q, want %q", v.class, lineupapi.ConnErrVerificationInterrupted)
	}
	if v.route != routePostAuth {
		t.Errorf("route = %v, want routePostAuth", v.route)
	}
	if proof.fxrm != "fx-1" {
		t.Errorf("proof = %q, want the cookie Fantrax issued", proof.fxrm)
	}
}

// TestClassifyLogin_ARejectedPasswordStillOutranksALingeringCookie.
//
// The narrow condition on the new branch — a cookie AND a named post-auth
// failure — is what makes this safe, and it is deliberately narrow. A bare
// `FXRM != ""` test placed above the form probe would have been simpler, but
// the pinned fork's LoginWithBrowser says in as many words that "a rejected
// password reaches this line too" immediately before it reads the cookie jar,
// and nobody has measured whether FX_RM is in that jar on a rejection. If it
// is, a bare test would tell every wrong password that the password is fine and
// invite unbounded retries — the documented Fantrax lockout trigger.
func TestClassifyLogin_ARejectedPasswordStillOutranksALingeringCookie(t *testing.T) {
	_, v := classifyLogin(loginEvidence{
		FXRM:    "stale-fx",
		Matched: map[string]bool{"login_form": true, "form_error": true},
		Texts:   map[string]string{"form_error": "Invalid username or password"},
	})
	if v.class != lineupapi.ConnErrBadCredentials {
		t.Errorf("class = %q, want %q — a form that said the password was wrong is "+
			"positive evidence, and no post-auth failure was recorded",
			v.class, lineupapi.ConnErrBadCredentials)
	}
}

// TestDefaultBrowserLogin_KeepsAProvenCookieWhenPersistenceFailed.
//
// HONEST LIMIT, and it is the reason this test says "when" rather than "so
// production is fixed": against the pinned fork this scenario cannot occur.
// auth_client.LoginWithBrowser returns `nil, outcome, err` on all three of its
// post-persistence error paths (os.Create, json.Marshal, f.Write), so the
// cookie it had proved it held is discarded before rosterbot sees it — which is
// exactly why the filed 2026-08-17 incident classified as a challenge/timeout.
// This pins OUR side of that seam so the evidence is read correctly the moment
// the fork stops throwing it away; the fork change is tracked separately.
func TestDefaultBrowserLogin_KeepsAProvenCookieWhenPersistenceFailed(t *testing.T) {
	restore := browserLoginFn
	t.Cleanup(func() { browserLoginFn = restore })
	browserLoginFn = func(string, []auth_client.LoginProbe) ([]*network.Cookie, *auth_client.LoginOutcome, error) {
		return []*network.Cookie{{Name: "FX_RM", Value: "fx-1"}},
			&auth_client.LoginOutcome{FinalURL: "https://www.fantrax.com/fantasy/league/x/team/roster"},
			errors.New("open .fantrax-cache/.fantrax_cookie_cache.json: no such file or directory")
	}

	ev := defaultBrowserLogin()

	if ev.FXRM != "fx-1" {
		t.Fatalf("FXRM = %q, want the cookie the browser returned", ev.FXRM)
	}
	if ev.PersistErr == nil {
		t.Error("PersistErr is nil: an error arriving with a proven cookie is a failure " +
			"AFTER the login, not a failed login")
	}
	if ev.Err != nil {
		t.Errorf("Err = %v: recording it there classifies a cache-write failure as a "+
			"login that never produced a session", ev.Err)
	}
	if _, v := classifyLogin(ev); v.class != lineupapi.ConnErrVerificationInterrupted {
		t.Errorf("class = %q, want %q", v.class, lineupapi.ConnErrVerificationInterrupted)
	}
}

// TestDefaultBrowserLogin_NoCookieStillRecordsTheErrorAsALoginFailure is the
// other half of the same decision: with no cookie there is no proof of
// anything, so the error belongs in Err and the evidence probes decide.
func TestDefaultBrowserLogin_NoCookieStillRecordsTheErrorAsALoginFailure(t *testing.T) {
	restore := browserLoginFn
	t.Cleanup(func() { browserLoginFn = restore })
	browserLoginFn = func(string, []auth_client.LoginProbe) ([]*network.Cookie, *auth_client.LoginOutcome, error) {
		return nil, nil, errors.New("context deadline exceeded")
	}

	ev := defaultBrowserLogin()

	if ev.Err == nil || ev.PersistErr != nil {
		t.Fatalf("Err = %v, PersistErr = %v; with no cookie nothing was proven", ev.Err, ev.PersistErr)
	}
	if _, v := classifyLogin(ev); v.class != lineupapi.ConnErrLoginChallengeOrTimeout {
		t.Errorf("class = %q, want %q", v.class, lineupapi.ConnErrLoginChallengeOrTimeout)
	}
}

// TestDefaultBrowserLogin_CreatesTheCookieCacheDirectoryBeforeLoggingIn.
//
// The 2026-08-17 incident's proximate cause: the fork's os.Create hit ENOENT
// because .fantrax-cache did not exist in the connect task's working directory.
// BELT AND BRACES rather than the fix — internal/statesync already MkdirAlls
// the local sync directories, and internal/fantrax/client.go has always done
// this before its own auth_client calls. connect was the one path that did not.
//
// Asserted through a seam because auth_client.CacheDir is a const relative to
// the working directory, so a test cannot redirect it.
func TestDefaultBrowserLogin_CreatesTheCookieCacheDirectoryBeforeLoggingIn(t *testing.T) {
	restoreMk, restoreLogin := mkdirCacheDir, browserLoginFn
	t.Cleanup(func() { mkdirCacheDir, browserLoginFn = restoreMk, restoreLogin })

	made := false
	mkdirCacheDir = func() error { made = true; return nil }
	loggedIn := false
	browserLoginFn = func(string, []auth_client.LoginProbe) ([]*network.Cookie, *auth_client.LoginOutcome, error) {
		if !made {
			t.Error("logged in before creating the cookie cache directory; the fork's " +
				"os.Create then fails with ENOENT after a successful sign-in")
		}
		loggedIn = true
		return []*network.Cookie{{Name: "FX_RM", Value: "fx-1"}}, nil, nil
	}

	defaultBrowserLogin()

	if !made {
		t.Error("never created the cookie cache directory")
	}
	if !loggedIn {
		t.Error("did not reach the login")
	}
}

// TestDefaultBrowserLogin_AFailedMkdirIsNotALoginVerdict.
//
// A broken local filesystem must not classify as a failed login. There is no
// proof of anything at that point, so demoting the tenant would be the exact
// mis-blame this bead exists to remove — the login runs anyway and the fork's
// own error arrives with whatever evidence there is.
func TestDefaultBrowserLogin_AFailedMkdirIsNotALoginVerdict(t *testing.T) {
	restoreMk, restoreLogin := mkdirCacheDir, browserLoginFn
	t.Cleanup(func() { mkdirCacheDir, browserLoginFn = restoreMk, restoreLogin })

	mkdirCacheDir = func() error { return errors.New("read-only file system") }
	browserLoginFn = func(string, []auth_client.LoginProbe) ([]*network.Cookie, *auth_client.LoginOutcome, error) {
		return []*network.Cookie{{Name: "FX_RM", Value: "fx-1"}}, nil, nil
	}

	ev := defaultBrowserLogin()

	if ev.FXRM != "fx-1" {
		t.Fatalf("FXRM = %q: a directory that could not be made ended the attempt", ev.FXRM)
	}
	if ev.Err != nil {
		t.Errorf("Err = %v, want nil — the login itself worked", ev.Err)
	}
}

// TestFantraxLogin_InstallsTheProvenCookieBeforeTheIdentityCheck.
//
// LOAD-BEARING, NOT HYGIENE. auth_client.GetCookies consults FANTRAX_COOKIES,
// then its cache file, and then FALLS BACK TO A FULL SECOND HEADLESS LOGIN. With
// the variable empty and the cache file unwritten, the identity call inside
// NewClient drives another browser login against the tenant's real account —
// and repeated logins are the documented trigger for Fantrax lockout and
// Cloudflare bot-blocking.
func TestFantraxLogin_InstallsTheProvenCookieBeforeTheIdentityCheck(t *testing.T) {
	restoreBrowser, restoreIdentity := runBrowserLogin, fantraxIdentity
	t.Cleanup(func() { runBrowserLogin, fantraxIdentity = restoreBrowser, restoreIdentity })
	t.Setenv("FANTRAX_COOKIES", "")

	runBrowserLogin = func() loginEvidence { return loginEvidence{FXRM: "fx-1"} }
	seen := "unset"
	fantraxIdentity = func(string) (*fantraxUserInfo, error) {
		seen = os.Getenv("FANTRAX_COOKIES")
		return &fantraxUserInfo{UserID: "fantrax-user-1"}, nil
	}

	ev := fantraxLogin(lineupapi.FantraxCreds{Username: "u", Password: "p"})

	if seen != "FX_RM=fx-1" {
		t.Errorf("FANTRAX_COOKIES during the identity check = %q, want %q", seen, "FX_RM=fx-1")
	}
	if ev.Info == nil {
		t.Fatal("no identity recorded")
	}
}

// TestFantraxLogin_AnErrorBesideAProvenCookieDoesNotAbortTheIdentityCheck.
//
// The early return here used to be `ev.Err != nil || ev.FXRM == ""`, which
// abandoned a session Fantrax had already issued because something after the
// login reported a problem. The durable hand-off to the next run is the
// KMS-sealed FX_RM, not any local file, so the only question that should stop
// this function is whether a cookie exists.
//
// The Err row is the load-bearing one and is why this is a table. It is the
// shape defaultBrowserLogin produced BEFORE this change (it recorded the
// cache-write failure in Err), so it is exactly what the old guard bailed on;
// the PersistErr row alone cannot exercise that guard, because the two fields
// are mutually exclusive in today's producer.
func TestFantraxLogin_AnErrorBesideAProvenCookieDoesNotAbortTheIdentityCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   loginEvidence
	}{
		{
			name: "the cookie cache could not be written",
			ev: loginEvidence{
				FXRM:       "fx-1",
				PersistErr: errors.New("no such file or directory"),
			},
		},
		{
			name: "the browser reported an error alongside the cookie",
			ev: loginEvidence{
				FXRM: "fx-1",
				Err:  errors.New("open .fantrax-cache/...: no such file or directory"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreBrowser, restoreIdentity := runBrowserLogin, fantraxIdentity
			t.Cleanup(func() { runBrowserLogin, fantraxIdentity = restoreBrowser, restoreIdentity })

			runBrowserLogin = func() loginEvidence { return tc.ev }
			called := false
			fantraxIdentity = func(string) (*fantraxUserInfo, error) {
				called = true
				return &fantraxUserInfo{UserID: "fantrax-user-1"}, nil
			}

			ev := fantraxLogin(lineupapi.FantraxCreds{Username: "u", Password: "p"})

			if !called {
				t.Fatal("never ran the identity check: a cookie Fantrax issued was " +
					"thrown away over a problem that happened after the login")
			}
			if _, v := classifyLogin(ev); v.class != "" {
				t.Errorf("class = %q, want a success — the session is proven and is "+
					"sealed independently of any local file", v.class)
			}
		})
	}
}

// TestFantraxLogin_AnIdentityFailureIsNotABrowserFailure pins which field the
// error lands in, because that choice IS the classification.
func TestFantraxLogin_AnIdentityFailureIsNotABrowserFailure(t *testing.T) {
	restoreBrowser, restoreIdentity := runBrowserLogin, fantraxIdentity
	t.Cleanup(func() { runBrowserLogin, fantraxIdentity = restoreBrowser, restoreIdentity })

	runBrowserLogin = func() loginEvidence { return loginEvidence{FXRM: "fx-1"} }
	fantraxIdentity = func(string) (*fantraxUserInfo, error) {
		return nil, errors.New("fantrax API error 503")
	}

	ev := fantraxLogin(lineupapi.FantraxCreds{Username: "u", Password: "p"})

	if ev.IdentityErr == nil {
		t.Fatal("IdentityErr is nil")
	}
	if ev.Err != nil {
		t.Errorf("Err = %v: recorded there, the classifier reads a Fantrax outage as a "+
			"login that never produced a session", ev.Err)
	}
}

// failingSealer stands in for a KMS blip.
type failingSealer struct{}

func (failingSealer) Seal(context.Context, lineupapi.UserID, []byte) ([]byte, error) {
	return nil, errors.New("kms: throttled")
}

// TestProvenRunOwnershipFault_RefusesANonOwnershipClass.
//
// provenRun exists to narrow what a caller can do once Fantrax has accepted the
// credentials: there is no general "demote this tenant" on it, only
// interrupted (which never touches the credentials verdict) and ownershipFault
// (which does, and may only carry the three classes that genuinely describe the
// tenant's team). The refusal is what stops that narrowing from being a comment
// — a future edit routing a transient through here would otherwise demote
// somebody whose password Fantrax had just accepted, which is this whole bead.
func TestProvenRunOwnershipFault_RefusesANonOwnershipClass(t *testing.T) {
	conns, feed := pendingConn(t), &memFeed{}
	p := provenRun{
		run: connectRun{
			connectDeps: postAuthDeps(t, conns, feed, &bytes.Buffer{}),
			ctx:         context.Background(),
			uid:         "alice",
			conn:        conns.conn,
		},
		proof: loginProof{fxrm: "fx-1"},
	}

	err := p.ownershipFault(lineupapi.ConnErrLoginChallengeOrTimeout)

	if err == nil {
		t.Fatal("accepted a class that says nothing about team ownership")
	}
	for _, w := range conns.put {
		if w.Status == lineupapi.ConnNeedsReconnect {
			t.Fatalf("a refused ownership fault still demoted the tenant to %q", w.Status)
		}
	}

	// The three real ones still go through, or the guard would be a wall.
	for _, class := range []string{
		lineupapi.ConnErrTeamNotOwned,
		lineupapi.ConnErrNoTeam,
		lineupapi.ConnErrTeamClaimed,
	} {
		conns.put = nil
		if err := p.ownershipFault(class); err != nil {
			t.Errorf("ownershipFault(%q) = %v, want it recorded", class, err)
		}
		if len(conns.put) != 1 || conns.put[0].Status != lineupapi.ConnNeedsReconnect {
			t.Errorf("ownershipFault(%q) wrote %+v, want one needs_reconnect record",
				class, conns.put)
		}
	}
}
