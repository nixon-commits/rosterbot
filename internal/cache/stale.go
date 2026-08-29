package cache

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
)

// StaleAlertAfter is how old a served-stale copy must be before serving it is
// worth waking anyone. Below it the degraded read is logged and nothing is
// pushed: every upstream this path guards (FanGraphs, Savant, HKB) refreshes
// at most daily, so a copy younger than this is the same data a successful
// fetch would have returned, and paging on it reports a non-event.
var StaleAlertAfter = 24 * time.Hour

// StaleMarkerStore is the dedup seam for the stale-cache alert. The interface
// now lives in internal/alertmarker — itself a stdlib-only leaf — so this
// package's transitive dependencies stay stdlib-only and cmd/root.go's wiring
// is unchanged: lineupapi.BlobStore still satisfies it structurally.
//
// A nil store disables dedup, which is the correct local-dev behavior: alert
// every run rather than stay quiet about something there is no record of. It
// is the same seam, and the same nil-means-noisy rule, as lineuprun's IL-start
// and GS-floor alerts — all three now type their marker field as
// alertmarker.Store.
type StaleMarkerStore = alertmarker.Store

// StaleMarkers, when set, deduplicates the stale-cache alert ACROSS PROCESSES.
// In-process state would do nothing here: every scheduled run is a fresh
// container, so each one would re-alert on the same standing outage — which is
// exactly the flood this exists to stop.
var StaleMarkers StaleMarkerStore

// staleMarkerKey names the marker for one cache key. The key is stable and the
// EPISODE identity lives in the marker BODY, which is what keeps the recovery
// path cheap: clearing a standing alert then costs one small marker read
// rather than re-reading a multi-megabyte cached payload just to date it.
func staleMarkerKey(cacheKey string) string { return "stale-" + cacheKey }

// episodeID identifies one continuous run of staleness by the timestamp of the
// copy being served. It changes the moment a fresh fetch lands, so a later
// outage is a new episode and alerts again on its own.
func episodeID(fetchedAt time.Time) string { return fetchedAt.UTC().Format(time.RFC3339) }

// staleMarker builds a *Marker over the current StaleMarkers store, routing
// its degrade warnings to stderr with the same "warning: stale-alert " prefix
// the hand-written version used. Built fresh per call rather than cached,
// since StaleMarkers is a package var callers (and tests) can swap.
func staleMarker() *alertmarker.Marker {
	return alertmarker.New(StaleMarkers, alertmarker.WithLogf(func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "warning: stale-alert "+format+"\n", args...)
	}))
}

// reportStale decides whether this degraded read is worth a push, and sends it.
//
// Order is check -> send -> mark, never claim-then-send (rosterbot-chs): a
// marker written before a failed send would suppress an alert that never went
// out. Every marker-store failure therefore degrades to a DUPLICATE alert, and
// never to silence.
func reportStale(key string, fetchedAt time.Time, age time.Duration, cause error) {
	if Notify == nil {
		return
	}
	// An undated copy cannot be called old. Absence of evidence is not
	// evidence, so this reports nothing rather than pushing an age it made up.
	if fetchedAt.IsZero() {
		fmt.Fprintf(os.Stderr, "⚠️ stale cache: %s has no fetched_at; cannot judge its age\n", key)
		return
	}
	if age <= StaleAlertAfter {
		return
	}

	marker := staleMarkerKey(key)
	episode := []byte(episodeID(fetchedAt))

	_, _ = staleMarker().SendOnChange(context.Background(), marker, episode, func() error {
		Notify("⚠️ Stale cache", fmt.Sprintf("Serving stale %s — %s old (%v)", key, roundAge(age), cause))
		return nil
	})
}

// reportRecovery closes out a standing stale alert once a fresh fetch lands.
// It reports nothing when no alert is standing, so a blip that never paged
// anyone never announces its own recovery either.
func reportRecovery(key string) {
	if Notify == nil {
		return
	}
	marker := staleMarkerKey(key)
	m := staleMarker()

	// A read failure reads as "no standing alert" via Token — the same skip
	// outcome as the old log-and-return.
	tok, found := m.Token(context.Background(), marker)
	if !found || tok == "" {
		return
	}

	// Send delivers the recovery notice first and clears the marker (an empty
	// body) only after, so a failed send never masks the standing alert.
	_ = m.Send(marker, nil, func() error {
		Notify("✅ Cache fresh again", fmt.Sprintf("%s refreshed; serving live data", key))
		return nil
	})
}

// roundAge renders a duration at the coarsest unit that still says something
// useful — hours, not 30h17m42.9s.
func roundAge(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
