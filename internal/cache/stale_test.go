package cache

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// seedStale writes an envelope for key stamped at fetchedAt, so a test can
// choose the age of the copy the stale path will serve without a fake clock.
func seedStale(t *testing.T, s Store, key string, fetchedAt time.Time, data string) {
	t.Helper()
	b, err := json.Marshal(envelope[string]{FetchedAt: fetchedAt, Data: data})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := s.Put(key, b); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// fakeMarkers is the dedup seam under test. errOn makes a read fail so the
// "degrade to a duplicate, never to silence" rule can be asserted.
type fakeMarkers struct {
	m     map[string][]byte
	errOn bool
}

func newFakeMarkers() *fakeMarkers { return &fakeMarkers{m: map[string][]byte{}} }

func (f *fakeMarkers) Get(_ context.Context, key string) ([]byte, bool, error) {
	if f.errOn {
		return nil, false, errors.New("marker store unreachable")
	}
	b, ok := f.m[key]
	return b, ok, nil
}

func (f *fakeMarkers) Publish(key string, data []byte) error {
	f.m[key] = data
	return nil
}

// staleHarness wires a cache over a MemStore with a recording Notify, and
// restores every package global on cleanup.
type staleHarness struct {
	cache   *FileCache[string]
	store   Store
	markers *fakeMarkers
	alerts  []string
}

func newStaleHarness(t *testing.T) *staleHarness {
	t.Helper()
	h := &staleHarness{store: NewMemStore(), markers: newFakeMarkers()}
	h.cache = &FileCache[string]{store: h.store, ttl: time.Hour}

	prevNotify, prevMarkers, prevAfter := Notify, StaleMarkers, StaleAlertAfter
	t.Cleanup(func() { Notify, StaleMarkers, StaleAlertAfter = prevNotify, prevMarkers, prevAfter })

	Notify = func(title, message string) { h.alerts = append(h.alerts, title+" | "+message) }
	StaleMarkers = h.markers
	StaleAlertAfter = 24 * time.Hour
	return h
}

func failingFetch() (string, error) { return "", errors.New("fangraphs: status 403") }

func TestStaleFallback_StaysQuietWhileTheCopyIsYoungerThanTheThreshold(t *testing.T) {
	h := newStaleHarness(t)
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-1*time.Hour), "cached")

	got, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch)
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if got != "cached" {
		t.Fatalf("got %q, want the cached copy", got)
	}
	if len(h.alerts) != 0 {
		t.Fatalf("a 1h-old copy is not worth waking anyone; got %v", h.alerts)
	}
}

func TestStaleFallback_AlertsOnceForOneStalenessEpisode(t *testing.T) {
	h := newStaleHarness(t)
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-30*time.Hour), "cached")

	for i := 0; i < 5; i++ {
		if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(h.alerts) != 1 {
		t.Fatalf("one episode is one alert, got %d: %v", len(h.alerts), h.alerts)
	}
}

func TestStaleFallback_AlertsAgainForANewStalenessEpisode(t *testing.T) {
	h := newStaleHarness(t)
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-30*time.Hour), "old")

	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch); err != nil {
		t.Fatal(err)
	}
	// Upstream recovers, then breaks again with a copy that has since re-aged.
	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", func() (string, error) { return "fresh", nil }); err != nil {
		t.Fatal(err)
	}
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-40*time.Hour), "fresh")
	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch); err != nil {
		t.Fatal(err)
	}

	stale := 0
	for _, a := range h.alerts {
		if !isRecovery(a) {
			stale++
		}
	}
	if stale != 2 {
		t.Fatalf("a second episode must alert again, got %d stale alerts: %v", stale, h.alerts)
	}
}

func TestStaleFallback_ReportsRecoveryOnlyAfterItAlerted(t *testing.T) {
	h := newStaleHarness(t)
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-30*time.Hour), "old")

	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", func() (string, error) { return "fresh", nil }); err != nil {
		t.Fatal(err)
	}

	var recoveries int
	for _, a := range h.alerts {
		if isRecovery(a) {
			recoveries++
		}
	}
	if recoveries != 1 {
		t.Fatalf("want exactly one recovery notice, got %d: %v", recoveries, h.alerts)
	}
}

func TestStaleFallback_DoesNotReportRecoveryItNeverAlertedFor(t *testing.T) {
	h := newStaleHarness(t)
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-1*time.Hour), "young")

	// Went stale briefly (under threshold, so no alert), then recovered.
	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", func() (string, error) { return "fresh", nil }); err != nil {
		t.Fatal(err)
	}
	if len(h.alerts) != 0 {
		t.Fatalf("nothing was ever reported broken, so nothing recovered; got %v", h.alerts)
	}
}

func TestStaleFallback_AlertsWhenTheMarkerStoreCannotBeRead(t *testing.T) {
	h := newStaleHarness(t)
	h.markers.errOn = true
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-30*time.Hour), "cached")

	if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch); err != nil {
		t.Fatal(err)
	}
	if len(h.alerts) != 1 {
		t.Fatalf("an unreadable marker degrades to a duplicate, never to silence; got %v", h.alerts)
	}
}

func TestStaleFallback_WithNoMarkerStoreAlertsEveryRun(t *testing.T) {
	h := newStaleHarness(t)
	StaleMarkers = nil
	seedStale(t, h.store, "fangraphs-bat", time.Now().Add(-30*time.Hour), "cached")

	for i := 0; i < 3; i++ {
		if _, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.alerts) != 3 {
		t.Fatalf("no marker store means no dedup, not silence; got %d: %v", len(h.alerts), h.alerts)
	}
}

func TestStaleFallback_ErrorsWhenThereIsNoCachedCopyAtAll(t *testing.T) {
	h := newStaleHarness(t)
	if _, err := h.cache.GetWithStaleFallback("never-fetched", failingFetch); err == nil {
		t.Fatal("no cached copy and a failed fetch is a hard error")
	}
	if len(h.alerts) != 0 {
		t.Fatalf("nothing stale was served, so nothing to report; got %v", h.alerts)
	}
}

// isRecovery classifies a recorded alert by its title. The two notices have to
// be tellable apart by a human at a glance, so asserting on the title is
// asserting on the thing that actually matters.
func isRecovery(alert string) bool {
	return strings.Contains(alert, "fresh again")
}

func TestStaleFallback_DoesNotClaimAnAgeForAnUndatedCopy(t *testing.T) {
	h := newStaleHarness(t)
	// A copy with no fetched_at cannot be dated, so it cannot be called old.
	seedStale(t, h.store, "fangraphs-bat", time.Time{}, "undated")

	got, err := h.cache.GetWithStaleFallback("fangraphs-bat", failingFetch)
	if err != nil {
		t.Fatalf("an undated copy is still servable: %v", err)
	}
	if got != "undated" {
		t.Fatalf("got %q, want the cached copy", got)
	}
	if len(h.alerts) != 0 {
		t.Fatalf("no fetched_at means no age to report; got %v", h.alerts)
	}
}

// --- GetWithStaleFallbackAt: the timestamp the degraded read already knows ---
//
// The stale path has always had the envelope's fetched_at in hand (loadAnyAt
// reads it to compute the age it prints) and threw it away at the return. That
// is the one fact a downstream consumer needs to tell a fresh capture from one
// built on a days-old copy, so the *At variant hands it back.

func TestGetWithStaleFallbackAt_FreshFetchReportsTheTimeItWasStored(t *testing.T) {
	h := newStaleHarness(t)
	before := time.Now()

	got, fetchedAt, err := h.cache.GetWithStaleFallbackAt("fangraphs-bat", func() (string, error) { return "fresh", nil })
	if err != nil {
		t.Fatalf("fresh fetch: %v", err)
	}
	if got != "fresh" {
		t.Errorf("data = %q, want fresh", got)
	}
	if fetchedAt.Before(before) || fetchedAt.After(time.Now()) {
		t.Errorf("fetchedAt = %v, want a stamp from this fetch (between %v and now)", fetchedAt, before)
	}
	// The reported stamp must be the one actually stored: a later reader of
	// the same entry has to agree with what this caller recorded.
	stale, stored, ok := h.cache.loadAnyAt("fangraphs-bat")
	if !ok || stale != "fresh" {
		t.Fatalf("entry not stored: ok=%v data=%q", ok, stale)
	}
	if !stored.Equal(fetchedAt) {
		t.Errorf("reported fetchedAt %v != stored %v", fetchedAt, stored)
	}
}

func TestGetWithStaleFallbackAt_StaleServeReportsTheCopysAge(t *testing.T) {
	h := newStaleHarness(t)
	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	seedStale(t, h.store, "fangraphs-bat", old, "cached")

	got, fetchedAt, err := h.cache.GetWithStaleFallbackAt("fangraphs-bat", failingFetch)
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if got != "cached" {
		t.Errorf("data = %q, want cached", got)
	}
	if !fetchedAt.Equal(old) {
		t.Errorf("fetchedAt = %v, want the stale copy's stamp %v", fetchedAt, old)
	}
}

func TestGetWithStaleFallbackAt_UndatedCopyReportsZero(t *testing.T) {
	h := newStaleHarness(t)
	// An entry written before the envelope carried a timestamp. Absence of
	// evidence is not evidence: report zero rather than inventing an age.
	seedStale(t, h.store, "fangraphs-bat", time.Time{}, "cached")

	got, fetchedAt, err := h.cache.GetWithStaleFallbackAt("fangraphs-bat", failingFetch)
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if got != "cached" {
		t.Errorf("data = %q, want cached", got)
	}
	if !fetchedAt.IsZero() {
		t.Errorf("fetchedAt = %v, want zero for an undated copy", fetchedAt)
	}
}
