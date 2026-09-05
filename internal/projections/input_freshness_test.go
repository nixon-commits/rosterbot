package projections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// The stale-cache fallback is silent by construction: a capture built on a
// three-day-old FanGraphs copy returns the same shape as a fresh one, and
// nothing downstream could tell them apart (rosterbot-c61b's three
// hand-excluded dates). LoadResult.FetchedAt is what makes the difference
// visible, so these pin BOTH halves — an old stamp on the degraded path AND a
// fresh stamp on the healthy one.

// fgTestServer points both FanGraphs URLs at a test server whose handler the
// test can swap mid-run, and restores every projection-system global after.
func fgTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	prevBase, prevBat, prevPit, prevType := fgBaseURL, fangraphsBattingURL, fangraphsPitchingURL, currentAPIType
	t.Cleanup(func() {
		fgBaseURL, fangraphsBattingURL, fangraphsPitchingURL, currentAPIType = prevBase, prevBat, prevPit, prevType
	})
	fgBaseURL = srv.URL + "?type=%s&stats=%s"
	return srv
}

func batFixture() []map[string]any {
	return []map[string]any{
		{"PlayerName": "Aaron Judge", "Team": "NYY", "G": 141.0, "PA": 633.0, "H": 143.0,
			"1B": 77.0, "2B": 23.0, "3B": 1.0, "HR": 42.0,
			"R": 109.0, "RBI": 102.0, "BB": 112.0, "SB": 9.0, "CS": 2.0, "HBP": 6.0, "SO": 156.0, "GDP": 8.0},
	}
}

func pitFixture() []map[string]any {
	return []map[string]any{
		{"PlayerName": "Gerrit Cole", "Team": "NYY", "G": 30.0, "GS": 30.0, "IP": 190.0,
			"SO": 220.0, "BB": 45.0, "H": 150.0, "ER": 65.0, "HR": 20.0, "W": 14.0, "L": 8.0,
			"QS": 20.0, "SV": 0.0, "HLD": 0.0, "HBP": 5.0},
	}
}

func TestLoadBattingProjections_FreshFetchStampsFetchedAtNow(t *testing.T) {
	fgTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(batFixture())
	})
	before := time.Now()

	src, res, err := LoadBattingProjections(t.Context(), ProjectionATCRoS, t.TempDir(), ProjectionCacheTTL)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if src.Len() == 0 {
		t.Fatal("want a populated source")
	}
	if res.FetchedAt.Before(before) || res.FetchedAt.After(time.Now()) {
		t.Errorf("FetchedAt = %v, want a stamp from this run (>= %v)", res.FetchedAt, before)
	}
}

func TestLoadBattingProjections_StaleFallbackReportsTheCopysAge(t *testing.T) {
	dir := t.TempDir()
	fail := false
	fgTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			// 404 rather than 403: both fail the fetch, but 403 is retryable
			// (the Cloudflare-challenge rule) and would cost the test 5s of
			// backoff to prove the same thing.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(batFixture())
	})

	if _, _, err := LoadBattingProjections(t.Context(), ProjectionATCRoS, dir, ProjectionCacheTTL); err != nil {
		t.Fatalf("seed load: %v", err)
	}

	// Age the cached copy to three days old — the measured gap on the
	// 2026-08-19..21 Shadow captures — then break the upstream so the load
	// must serve it.
	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	key := cache.Key(keyFanGraphs, "bat", fgProjectionType[ProjectionATCRoS])
	found, err := cache.SetFetchedAt(cache.NewFileStore(dir), key, old)
	if err != nil || !found {
		t.Fatalf("age cache entry %s: found=%v err=%v", key, found, err)
	}
	fail = true

	src, res, err := LoadBattingProjections(t.Context(), ProjectionATCRoS, dir, ProjectionCacheTTL)
	if err != nil {
		t.Fatalf("stale load: %v", err)
	}
	if src.Len() == 0 {
		t.Fatal("stale fallback should still serve the cached rows")
	}
	if res.NoData || res.FromCSV {
		t.Fatalf("want the stale-cache path, got NoData=%v FromCSV=%v", res.NoData, res.FromCSV)
	}
	if !res.FetchedAt.Equal(old) {
		t.Errorf("FetchedAt = %v, want the stale copy's stamp %v", res.FetchedAt, old)
	}
}

func TestLoadBattingProjections_NoDataLeavesFetchedAtZero(t *testing.T) {
	fgTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	// Nothing cached and nothing upstream: the stub carries no projections, so
	// there is no fetch time to report. Zero means unknown, and a consumer
	// must not read it as "fetched at the epoch, therefore ancient".
	_, res, err := LoadBattingProjections(t.Context(), ProjectionATCRoS, t.TempDir(), ProjectionCacheTTL)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !res.NoData {
		t.Fatalf("want NoData, got %+v", res)
	}
	if !res.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %v, want zero when no source answered", res.FetchedAt)
	}
}

func TestLoadPitcherProjections_FreshFetchStampsFetchedAtNow(t *testing.T) {
	fgTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(pitFixture())
	})
	before := time.Now()

	src, res, err := LoadPitcherProjections(t.Context(), ProjectionATCRoS, t.TempDir(), ProjectionCacheTTL)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if src.Len() == 0 {
		t.Fatal("want a populated source")
	}
	if res.FetchedAt.Before(before) || res.FetchedAt.After(time.Now()) {
		t.Errorf("FetchedAt = %v, want a stamp from this run (>= %v)", res.FetchedAt, before)
	}
}

func TestLoadPitcherProjections_StaleFallbackReportsTheCopysAge(t *testing.T) {
	dir := t.TempDir()
	fail := false
	fgTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(pitFixture())
	})

	if _, _, err := LoadPitcherProjections(t.Context(), ProjectionATCRoS, dir, ProjectionCacheTTL); err != nil {
		t.Fatalf("seed load: %v", err)
	}

	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	key := cache.Key(keyFanGraphs, "pit", fgProjectionType[ProjectionATCRoS])
	found, err := cache.SetFetchedAt(cache.NewFileStore(dir), key, old)
	if err != nil || !found {
		t.Fatalf("age cache entry %s: found=%v err=%v", key, found, err)
	}
	fail = true

	src, res, err := LoadPitcherProjections(t.Context(), ProjectionATCRoS, dir, ProjectionCacheTTL)
	if err != nil {
		t.Fatalf("stale load: %v", err)
	}
	if src.Len() == 0 {
		t.Fatal("stale fallback should still serve the cached rows")
	}
	if !res.FetchedAt.Equal(old) {
		t.Errorf("FetchedAt = %v, want the stale copy's stamp %v", res.FetchedAt, old)
	}
}
