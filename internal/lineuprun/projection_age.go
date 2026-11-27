package lineuprun

import (
	"fmt"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/projections"
)

// MaxProjectionAge is the hard ceiling on how old a projection copy may be and
// still set a lineup.
//
// It exists because the degraded read had no ceiling at all. cache's
// GetWithStaleFallbackAt serves loadAnyAt, which ignores TTL expiry by design
// and refuses at no age, so an upstream outage that outlasts the cache keeps
// serving one frozen copy for as long as it runs. Everything built on top of
// that stamp was about telling the story AFTERWARDS: the snapshot records
// FetchedAt so grading can disown the capture (backtest.StaleInputAfter), and
// internal/analysis carries a table of partitions written before that guard
// existed. Nothing declined to act. The stale-cache alert is a push, and
// cache.StaleAlertAfter only decides whether to send it.
//
// Three refresh cycles, for the same reason backtest.StaleInputAfter is one:
// the threshold should be stated in the upstream's own cadence rather than as
// a bare number of hours. One missed daily refresh is an ordinary blip and is
// exactly what the stale fallback is for. Three is no longer a blip -- roles,
// rotations and injuries have moved under numbers that cannot see them, and a
// lineup set from them is a confident wrong answer rather than a degraded one.
//
// The two thresholds are deliberately different and the gap between them is
// the point: refusing to GRADE a stale capture costs a day of measurement,
// while refusing to ACT costs a day of lineups, so the second is set looser.
const MaxProjectionAge = 3 * projections.ProjectionCacheTTL

// staleProjectionInput reports the roles whose projection copy is past
// MaxProjectionAge as an error, or nil when the run may proceed.
//
// now is the run's own notion of today (Options.Today), not time.Now(). Every
// other date decision in this package is anchored to it, and a wall-clock
// ceiling would refuse any run deliberately pinned to a past date -- including
// every dated fixture in the test suite -- on the age of the date rather than
// the age of the data.
//
// A zero stamp is UNKNOWN and never old, which is the rule
// projections.LoadResult.FetchedAt's own doc sets out: the CSV fallback and the
// empty NoData stub have no upstream fetch behind them, and neither does a
// cache entry written before the envelope carried a timestamp. Refusing over
// an absent stamp would take the lineup out on a non-event.
//
// Both roles are judged, separately, because one can be stale while the other
// is fresh -- that asymmetry is the whole reason the stamps were threaded per
// role -- and either one is enough to refuse. A lineup is one decision built
// from both: dropping the stale role instead would silently change the model's
// inputs, which is harder to notice than a refusal and worse than one.
func staleProjectionInput(now time.Time, bat, pit projections.LoadResult) error {
	roles := []struct {
		name string
		at   time.Time
	}{
		{"batting", bat.FetchedAt},
		{"pitching", pit.FetchedAt},
	}
	var over []string
	for _, r := range roles {
		if r.at.IsZero() {
			continue
		}
		if age := now.Sub(r.at); age > MaxProjectionAge {
			over = append(over, fmt.Sprintf("%s projections last fetched %s (%dh old)",
				r.name, r.at.UTC().Format(time.RFC3339), int(age.Hours())))
		}
	}
	if len(over) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to set a lineup on projections older than %dh: %s",
		int(MaxProjectionAge.Hours()), strings.Join(over, "; "))
}
