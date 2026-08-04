package fantrax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

// Real Fantrax responses captured 2026-08-04. The version gate is checked ahead
// of auth, which is what makes an unauthenticated probe a clean discriminator:
// a current version gets as far as the login check, a stale one does not.
const (
	staleBody   = `{"pageError":{"onScreen":false,"code":"STALE_CLIENT","text":"Your browser is using an outdated cached version.","title":"App Update Required","forceReload":false}}`
	currentBody = `{"data":{"sDate":1785868118338},"roles":["03"],"pageError":{"onScreen":false,"code":"WARNING_NOT_LOGGED_IN","text":"Sorry, you must be logged in to perform that action."}}`
	noErrorBody = `{"data":{"sDate":1785868118338}}`
	oddBody     = `{"pageError":{"code":"SOME_FUTURE_CODE","text":"who knows"}}`
)

func probeServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withProbeURL(t *testing.T, url string) {
	t.Helper()
	orig := versionProbeURL
	versionProbeURL = url
	t.Cleanup(func() { versionProbeURL = orig })
}

func TestCheckAPIVersion_StaleClient(t *testing.T) {
	srv := probeServer(t, staleBody)
	withProbeURL(t, srv.URL)

	status, code, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionStale {
		t.Errorf("status = %v, want VersionStale", status)
	}
	if code != "STALE_CLIENT" {
		t.Errorf("code = %q, want STALE_CLIENT", code)
	}
}

func TestCheckAPIVersion_CurrentVersionPassesGate(t *testing.T) {
	srv := probeServer(t, currentBody)
	withProbeURL(t, srv.URL)

	status, code, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionOK {
		t.Errorf("status = %v, want VersionOK", status)
	}
	if code != "WARNING_NOT_LOGGED_IN" {
		t.Errorf("code = %q, want WARNING_NOT_LOGGED_IN", code)
	}
}

func TestCheckAPIVersion_NoPageErrorIsOK(t *testing.T) {
	srv := probeServer(t, noErrorBody)
	withProbeURL(t, srv.URL)

	status, _, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionOK {
		t.Errorf("status = %v, want VersionOK", status)
	}
}

// An unrecognized code must NOT be reported as stale. The probe is only allowed
// to page for the one condition it can positively identify; anything else is
// VersionUnknown and the command exits 0.
func TestCheckAPIVersion_UnrecognizedCodeIsUnknownNotStale(t *testing.T) {
	srv := probeServer(t, oddBody)
	withProbeURL(t, srv.URL)

	status, code, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionUnknown {
		t.Errorf("status = %v, want VersionUnknown", status)
	}
	if code != "SOME_FUTURE_CODE" {
		t.Errorf("code = %q, want SOME_FUTURE_CODE", code)
	}
}

func TestCheckAPIVersion_NonOKStatusIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	withProbeURL(t, srv.URL)

	status, _, err := CheckAPIVersion(context.Background())
	if status != VersionUnknown {
		t.Errorf("status = %v, want VersionUnknown", status)
	}
	if err == nil {
		t.Error("expected an error describing the non-200 status")
	}
}

// The probe must send the pinned version — that is the whole point. Assert the
// wire payload rather than trusting the constant is threaded through.
func TestCheckAPIVersion_SendsThePinnedVersion(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(currentBody))
	}))
	t.Cleanup(srv.Close)
	withProbeURL(t, srv.URL)

	if _, _, err := CheckAPIVersion(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("probe sent unparseable JSON: %v", err)
	}
	if sent["v"] != auth_client.APIVersion {
		t.Errorf(`sent "v" = %v, want %q`, sent["v"], auth_client.APIVersion)
	}
}
