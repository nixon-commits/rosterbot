package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
)

// doGet runs one request through the API handler, optionally authenticated.
func doGet(t *testing.T, h http.Handler, path, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// readReport pulls one published report back out of a blob store.
func readReport(t *testing.T, store lineupapi.BlobStore, key string) []byte {
	t.Helper()
	data, ok, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s report: %v", key, err)
	}
	if !ok {
		t.Fatalf("no %s report published", key)
	}
	return data
}

func TestRenderGapSite_WritesModelFromStore(t *testing.T) {
	stateDir := t.TempDir()
	reports := lineupapi.NewFileBlobStore(t.TempDir(), "")

	w := lineupgap.NewFileWriter(stateDir)
	d := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGaps(d, []lineupgap.Row{{
		Dt: "2026-07-20", ActualPts: 90, OptimalPts: 100, Gap: -10, StartedN: 13, BenchedN: 2,
	}}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := writeGapModel(lineupgap.NewFileReader(stateDir), reports); err != nil {
		t.Fatalf("writeGapModel: %v", err)
	}

	var m lineupgap.Model
	if err := json.Unmarshal(readReport(t, reports, lineupapi.ReportGapKey), &m); err != nil {
		t.Fatalf("gap report is not valid JSON: %v", err)
	}
	if m.LatestDate != "2026-07-20" {
		t.Errorf("LatestDate = %q, want 2026-07-20", m.LatestDate)
	}
	if len(m.Series) != 1 || m.Series[0].GapPts != -10 {
		t.Errorf("Series = %+v, want one point with gap -10", m.Series)
	}
}

// A fresh deploy has an empty store. That must still produce a valid report, so
// the dashboard's headline block renders "no data yet" instead of erroring.
func TestRenderGapSite_EmptyStoreStillWritesValidJSON(t *testing.T) {
	reports := lineupapi.NewFileBlobStore(t.TempDir(), "")
	if err := writeGapModel(lineupgap.NewFileReader(t.TempDir()), reports); err != nil {
		t.Fatalf("writeGapModel on empty store: %v", err)
	}

	var m lineupgap.Model
	if err := json.Unmarshal(readReport(t, reports, lineupapi.ReportGapKey), &m); err != nil {
		t.Fatalf("gap report is not valid JSON: %v", err)
	}
	if len(m.Series) != 0 {
		t.Errorf("Series = %d points, want 0", len(m.Series))
	}
	if _, ok := m.Windows["30"]; !ok {
		t.Error(`Windows["30"] must be present even when empty`)
	}
}

// TestPublishedGapReport_IsServedOnlyBehindAuth is the end-to-end shape of
// rosterbot-crq.3: the producer publishes into a store, and the only way back
// to those bytes is an authenticated GET. It wires the real producer output to
// the real handler so a future change that reintroduces a public file path has
// to break this test to do it.
func TestPublishedGapReport_IsServedOnlyBehindAuth(t *testing.T) {
	dir := t.TempDir()
	store := lineupapi.NewFileBlobStore(dir, "")
	if err := writeGapModel(lineupgap.NewFileReader(t.TempDir()), store); err != nil {
		t.Fatalf("writeGapModel: %v", err)
	}

	h := lineupapi.Handler(lineupapi.Config{Token: "t", Reports: store})

	anon := doGet(t, h, "/v1/reports/"+lineupapi.ReportGapKey, "")
	if anon.Code != 401 {
		t.Fatalf("anonymous read of the gap report = %d, want 401", anon.Code)
	}
	authed := doGet(t, h, "/v1/reports/"+lineupapi.ReportGapKey, "Bearer t")
	if authed.Code != 200 {
		t.Fatalf("authenticated read of the gap report = %d, want 200", authed.Code)
	}
	if got, want := authed.Body.String(), string(readReport(t, store, lineupapi.ReportGapKey)); got != want {
		t.Fatalf("served bytes differ from stored bytes:\ngot  %q\nwant %q", got, want)
	}
}
