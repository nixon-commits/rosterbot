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
