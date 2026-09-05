package lineupapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reportsConfig wires the three private report artifacts against an in-memory
// store holding one recognisable body per key.
func reportsConfig() Config {
	return Config{
		Token: "secret-token",
		Reports: fakeStore{data: map[string][]byte{
			ReportModelKey: []byte(`{"kind":"model"}`),
			ReportGapKey:   []byte(`{"kind":"gap"}`),
			ReportViewsKey: []byte(`{"kind":"views"}`),
		}},
	}
}

// TestReports_RequireAuth is the whole point of rosterbot-crq.3: these three
// artifacts used to sit on CloudFront's unauthenticated default behavior under
// report/. An unauthenticated GET must now be refused, not merely unadvertised.
func TestReports_RequireAuth(t *testing.T) {
	h := Handler(reportsConfig())

	for _, name := range []string{ReportModelKey, ReportGapKey, ReportViewsKey} {
		t.Run(name+"/anonymous", func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/"+name, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous GET /v1/reports/%s = %d, want 401", name, rec.Code)
			}
			// The point of this assertion is that the 401 carries no report
			// content, so it compares the body's CONTENT, not its framing —
			// writeErr goes through writeJSON, whose encoder terminates every
			// response with a newline.
			if body := strings.TrimSpace(rec.Body.String()); body != "" && body != `{"error":"unauthorized"}` {
				t.Fatalf("anonymous GET leaked a body: %q", rec.Body.String())
			}
		})
		t.Run(name+"/wrong-token", func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/"+name, nil)
			req.Header.Set("Authorization", "Bearer nope")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("bad-token GET /v1/reports/%s = %d, want 401", name, rec.Code)
			}
		})
	}
}

// TestReports_PassthroughBytes pins the same contract as GET /v1/trades: the
// handler returns exactly what the producer stored, never decoding it, so a
// model-shape change ships without a Lambda deploy.
func TestReports_PassthroughBytes(t *testing.T) {
	h := Handler(reportsConfig())

	cases := map[string]string{
		ReportModelKey: `{"kind":"model"}`,
		ReportGapKey:   `{"kind":"gap"}`,
		ReportViewsKey: `{"kind":"views"}`,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/"+name, nil)
			req.Header.Set("Authorization", "Bearer secret-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content-type = %q, want application/json", ct)
			}
			if rec.Body.String() != want {
				t.Fatalf("body = %q, want %q", rec.Body.String(), want)
			}
		})
	}
}

// TestReports_UnknownName keeps {name} from becoming an arbitrary key in the
// reports/ prefix. Go's ServeMux already refuses a "/" inside the segment, so
// this is the second lock rather than the first.
func TestReports_UnknownName(t *testing.T) {
	h := Handler(reportsConfig())

	for _, name := range []string{"value", "football", "webauthn", "session"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/"+name, nil)
		req.Header.Set("Authorization", "Bearer secret-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET /v1/reports/%s = %d, want 404", name, rec.Code)
		}
	}

	// A traversal attempt never reaches handleReport at all: ServeMux cleans
	// the path and answers 307 to the cleaned location, which serves no bytes.
	// Asserted as "not 200" rather than "404" so the test pins the property
	// (nothing is served) instead of which layer refuses.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/../lineup/today", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("traversal GET returned 200 with body %q", rec.Body.String())
	}
}

// TestReports_MissingAndUnconfigured covers the two ordinary absences: a store
// that has nothing yet (fresh deploy) and a store that was never wired (a
// `serve` run with no local dir).
func TestReports_MissingAndUnconfigured(t *testing.T) {
	t.Run("missing object", func(t *testing.T) {
		h := Handler(Config{Token: "t", Reports: fakeStore{data: map[string][]byte{}}})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/"+ReportGapKey, nil)
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("store not configured", func(t *testing.T) {
		h := Handler(Config{Token: "t"})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/"+ReportModelKey, nil)
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501", rec.Code)
		}
	})

	t.Run("store error", func(t *testing.T) {
		h := Handler(Config{Token: "t", Reports: fakeStore{err: errFakeList}})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reports/"+ReportViewsKey, nil)
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
	})
}
