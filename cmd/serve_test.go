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

	mux := newServeMux("test-token", lineupDir, webDir)

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
	mux := newServeMux("test-token", t.TempDir(), "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET / with no web dir = %d, want 404", rec.Code)
	}
}
