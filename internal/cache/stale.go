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

// episodeID identifies one alertable episode of staleness: the timestamp of
// the copy being served, plus the ET calendar day of the run.
//
// The stamp is the edge — it changes the moment a fresh fetch lands, so a
// later outage is a new episode and alerts again on its own. The day is the
// FLOOR (rosterbot-jyt0.6). With the stamp alone, a source that kept failing
// served the same frozen copy with the same fetched_at forever, so the
// episode never changed and the marker's compare-on-change suppressed every
// run after the first: one alert per key per outage, then permanent silence.
// Verified at source during the 2026-09-26 FanGraphs 403. And because
// loadAnyAt ignores TTL, the frozen copy is served indefinitely, so the
// failure could never escalate to a hard error either — a 403 returning in
// the winter would carry a frozen 2026 copy into the 2027 opener unannounced.
// Adding the ET day keeps same-day repeats suppressed (the August flood was
// 104 same-day pushes) while a source still failing tomorrow pushes once
// more tomorrow.
//
// ET, not UTC: the scheduled runs and every other daily boundary in the tree
// are anchored to the ET calendar, and a UTC boundary would re-push the
// 20:00 ET run as "tomorrow".
func episodeID(fetchedAt, now time.Time) string {
	return fetchedAt.UTC().Format(time.RFC3339) + "|" + now.In(etLocation).Format("2006-01-02")
}

// etLocation is the day-boundary zone for episodeID. A load failure (no tzdata)
// falls back to UTC — a boundary a few hours off is a worse day-split, not a
// worse alert, and this is a stdlib-only leaf that must not panic over it.
var etLocation = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// staleNow is the stale path's clock, a var so tests can step the ET day.
var staleNow = time.Now

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
func reportStale(key string, fetchedAt, now time.Time, cause error) {
	age := now.Sub(fetchedAt)
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
	episode := []byte(episodeID(fetchedAt, now))

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
