package lineupapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestConnect_RefusesWhileAVerificationIsInFlight: each POST /v1/connect
// launches a chromedp login against the tester's real Fantrax account, so a
// double-click (or an impatient retry) must not stack parallel logins — that
// is how a tester's first minutes with the product trip Fantrax's bot
// defences. While a recent pending verification exists, a second submission is
// refused without touching the stored credentials or launching anything.
func TestConnect_RefusesWhileAVerificationIsInFlight(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{
		UserID: "alice", Status: ConnPending,
		CredsCiphertext: []byte("sealed:original"),
		UpdatedAt:       time.Now().UTC().Add(-time.Minute),
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, testSecret, "/v1/connect", "alice",
		`{"username":"u2","password":"p2"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("connect during a pending verification = %d, want 409: %s", rec.Code, rec.Body)
	}
	if string(conns.conn.CredsCiphertext) != "sealed:original" {
		t.Error("the refused submission overwrote the credentials the running task is verifying")
	}
	if jobs.launched {
		t.Error("the refused submission launched a second verification task")
	}
}

// TestConnect_StalePendingDoesNotBlockAReconnect: a connect task that crashed
// leaves the record pending forever, and a guard with no expiry would lock the
// tenant out of ever retrying. Past the in-flight window, a new submission
// goes through.
func TestConnect_StalePendingDoesNotBlockAReconnect(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{
		UserID: "alice", Status: ConnPending,
		CredsCiphertext: []byte("sealed:original"),
		UpdatedAt:       time.Now().UTC().Add(-connectInFlightWindow - time.Minute),
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, testSecret, "/v1/connect", "alice",
		`{"username":"u2","password":"p2"}`))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("connect after a stale pending = %d, want 202: %s", rec.Code, rec.Body)
	}
	if !jobs.launched {
		t.Error("the retry after a stale pending never launched a verification task")
	}
}

// TestConnect_ThrottlesRetriesAfterAnInterruptedCheck.
//
// ConnInterrupted deliberately does not block resubmission the way ConnPending
// does — the tenant is told to try again and there is no task in flight to wait
// for. But every submission drives a full chromedp login against their real
// Fantrax account, and "try again" during a Fantrax outage is an invitation to
// hold the button down. Repeated logins are the documented trigger for Fantrax
// lockout and Cloudflare bot-blocking (rosterbot-ch0s).
func TestConnect_ThrottlesRetriesAfterAnInterruptedCheck(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{
		UserID: "alice", Status: ConnInterrupted,
		LastError:       ConnErrVerificationInterrupted,
		CredsCiphertext: []byte("sealed:original"),
		UpdatedAt:       time.Now().UTC(),
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, testSecret, "/v1/connect", "alice",
		`{"username":"u2","password":"p2"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("immediate retry after an interrupted check = %d, want 409: %s", rec.Code, rec.Body)
	}
	if jobs.launched {
		t.Error("launched another headless login inside the cooldown")
	}
}

// TestConnect_AnInterruptedCheckIsRetryableOnceTheCooldownPasses is the other
// half, and the one that keeps the tenant-facing copy honest: it says "try
// again in a minute", so a minute later it must work. A guard with no expiry
// would strand a tenant whose only remedy is to retry.
func TestConnect_AnInterruptedCheckIsRetryableOnceTheCooldownPasses(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{
		UserID: "alice", Status: ConnInterrupted,
		LastError:       ConnErrVerificationInterrupted,
		CredsCiphertext: []byte("sealed:original"),
		UpdatedAt:       time.Now().UTC().Add(-connectInterruptedCooldown - time.Second),
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, testSecret, "/v1/connect", "alice",
		`{"username":"u2","password":"p2"}`))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry after the cooldown = %d, want 202: %s", rec.Code, rec.Body)
	}
	if !jobs.launched {
		t.Error("the retry never launched a verification task")
	}
}

// TestConnect_ThrottlesRetriesAfterACheckThatNeverReachedFantrax is
// ConnCheckFailed's mirror of TestConnect_ThrottlesRetriesAfterAnInterruptedCheck
// (rosterbot-spb9): the fault was on OUR side, not Fantrax's, but "try again" is
// still an invitation to drive another full chromedp login on every impatient
// click, so it gets the SAME cooldown as ConnInterrupted rather than
// ConnPending's open-ended block — there is no task in flight to wait for.
func TestConnect_ThrottlesRetriesAfterACheckThatNeverReachedFantrax(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{
		UserID: "alice", Status: ConnCheckFailed,
		LastError:       ConnErrCheckFailed,
		CredsCiphertext: []byte("sealed:original"),
		UpdatedAt:       time.Now().UTC(),
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, testSecret, "/v1/connect", "alice",
		`{"username":"u2","password":"p2"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("immediate retry after a check_failed = %d, want 409: %s", rec.Code, rec.Body)
	}
	if jobs.launched {
		t.Error("launched another headless login inside the cooldown")
	}
}

// TestConnect_ACheckThatNeverReachedFantraxIsRetryableOnceTheCooldownPasses is
// the other half, mirroring TestConnect_AnInterruptedCheckIsRetryableOnceTheCooldownPasses.
func TestConnect_ACheckThatNeverReachedFantraxIsRetryableOnceTheCooldownPasses(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{
		UserID: "alice", Status: ConnCheckFailed,
		LastError:       ConnErrCheckFailed,
		CredsCiphertext: []byte("sealed:original"),
		UpdatedAt:       time.Now().UTC().Add(-connectInterruptedCooldown - time.Second),
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, testSecret, "/v1/connect", "alice",
		`{"username":"u2","password":"p2"}`))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry after the cooldown = %d, want 202: %s", rec.Code, rec.Body)
	}
	if !jobs.launched {
		t.Error("the retry never launched a verification task")
	}
}
