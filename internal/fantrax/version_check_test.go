package fantrax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// The control probe must send ControlVersion and not the pinned constant.
// auth_client.BuildFullRequest always stamps the pinned version, so the
// override is the only thing making the control a control — and a control that
// silently sends the pinned version tests nothing.
func TestCheckControlVersion_SendsTheControlVersion(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(staleBody))
	}))
	t.Cleanup(srv.Close)
	withProbeURL(t, srv.URL)

	status, _, err := CheckControlVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionStale {
		t.Errorf("status = %v, want stale", status)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("control probe sent unparseable JSON: %v", err)
	}
	if sent["v"] != ControlVersion {
		t.Errorf(`sent "v" = %v, want %q`, sent["v"], ControlVersion)
	}
	if sent["v"] == auth_client.APIVersion {
		t.Error("control probe sent the pinned version — it would pass the gate and never act as a control")
	}
}

// The control has to sit far enough below Fantrax's floor that a threshold move
// can never turn it green. Measured 2026-08-05, the floor was between 181.0.0
// (STALE_CLIENT) and 182.0.0 (passes) — note the fork's own main sat at 182.0.0
// that week, i.e. one release above the floor, which is exactly how little
// headroom a "just below the pin" control would have had. A control anywhere
// near there would false-alarm the day Fantrax lowered the threshold. A leading
// major of 1 is the cheap, checkable form of "far below".
func TestControlVersion_IsFarBelowTheGateFloor(t *testing.T) {
	major, _, ok := strings.Cut(ControlVersion, ".")
	if !ok {
		t.Fatalf("ControlVersion %q is not dotted", ControlVersion)
	}
	n, err := strconv.Atoi(major)
	if err != nil {
		t.Fatalf("ControlVersion %q has a non-numeric major: %v", ControlVersion, err)
	}
	if n > 100 {
		t.Errorf("ControlVersion major %d is close to the observed gate floor (180-184); "+
			"the control must only fail when the mechanism breaks, never when the threshold moves", n)
	}
}
