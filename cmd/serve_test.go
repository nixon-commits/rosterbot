package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeMux_RoutesAPIAndStatic(t *testing.T) {
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<h1>dashboard</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	lineupDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(lineupDir, "outputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lineupDir, "outputs", "test-run.json"), []byte(`{"type":"waivers","data":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := newServeMux("test-token", []byte("test-session-secret"), lineupDir, webDir)

	// Static file at "/" needs no auth — CloudFront's default behavior doesn't
	// touch the Lambda either.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dashboard") {
		t.Fatalf("GET / body = %q, want it to contain the static file's content", rec.Body.String())
	}

	// /v1/* requires the bearer token, exactly like the deployed Lambda.
	req = httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/jobs (no auth) = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/jobs (authed) = %d, want 200", rec.Code)
	}

	// Real local job runs write output under .lineup/outputs.
	req = httptest.NewRequest(http.MethodGet, "/v1/runs/test-run/output", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/runs/test-run/output = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"type":"waivers","data":{}}` {
		t.Fatalf("GET /v1/runs/test-run/output body = %q, want the seeded fixture verbatim", got)
	}
}

func TestServeMux_NoWebDirConfigured(t *testing.T) {
	mux := newServeMux("test-token", []byte("test-session-secret"), t.TempDir(), "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET / with no web dir = %d, want 404", rec.Code)
	}
}

func TestServeMux_AuthRoutesWork(t *testing.T) {
	lineupDir := t.TempDir()
	mux := newServeMux("test-token", []byte("test-session-secret"), lineupDir, "")

	// No identity registered yet: login/begin is 404, not 401/500.
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /v1/auth/login/begin (no identity) = %d, want 404", rec.Code)
	}

	// register/begin with the bootstrap token succeeds and sets a ceremony cookie.
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/auth/register/begin = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	// Verify ceremony cookie is set (must match internal/lineupapi/webauthn.go ceremonyCookieName).
	var foundCeremony bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "rosterbot_ceremony" {
			foundCeremony = true
			break
		}
	}
	if !foundCeremony {
		t.Fatalf("POST /v1/auth/register/begin did not set rosterbot_ceremony cookie")
	}
}
