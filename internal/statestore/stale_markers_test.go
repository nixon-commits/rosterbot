package statestore

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// The unit tests in internal/cache drive the alert policy through a fake. This
// drives it through the REAL store statestore hands cmd, because the whole
// point of the marker is surviving a process boundary, and a seam that
// type-checks can still round-trip wrong.
func TestStaleCacheMarkers_DedupTheAlertAcrossRuns(t *testing.T) {
	t.Chdir(t.TempDir()) // markers land under ./.alerts/stale-cache, not the repo
	t.Setenv("STATE_BUCKET", "")
	t.Setenv("ROSTERBOT_USER_ID", "")

	markers, err := FromEnv().StaleCacheMarkers()
	if err != nil {
		t.Fatalf("build marker store: %v", err)
	}

	var alerts []string
	prevNotify, prevMarkers, prevAfter := cache.Notify, cache.StaleMarkers, cache.StaleAlertAfter
	t.Cleanup(func() { cache.Notify, cache.StaleMarkers, cache.StaleAlertAfter = prevNotify, prevMarkers, prevAfter })
	cache.Notify = func(title, _ string) { alerts = append(alerts, title) }
	cache.StaleMarkers = markers
	cache.StaleAlertAfter = 24 * time.Hour

	store := cache.NewMemStore()
	cache.SetDefaultStore(store)
	t.Cleanup(func() { cache.SetDefaultStore(nil) })

	env, err := json.Marshal(struct {
		FetchedAt time.Time `json:"fetched_at"`
		Data      string    `json:"data"`
	}{time.Now().Add(-30 * time.Hour), "cached"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("fangraphs-bat", env); err != nil {
		t.Fatal(err)
	}

	fail := func() (string, error) { return "", errors.New("fangraphs: status 403") }

	// Three separate FileCache values stand in for three separate containers
	// seeing the same standing outage.
	for i := 0; i < 3; i++ {
		c := cache.New[string]("", time.Hour)
		if _, err := c.GetWithStaleFallback("fangraphs-bat", fail); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if len(alerts) != 1 {
		t.Fatalf("three runs, one standing outage, want 1 alert; got %d: %v", len(alerts), alerts)
	}
}
