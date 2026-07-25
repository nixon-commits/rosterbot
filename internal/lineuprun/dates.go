package lineuprun

import (
	"fmt"
	"time"
)

// DateResolver is the dependency surface of the ResolveDates phase: the two
// lookups needed to expand `--dates all` / `--matchup` into concrete days.
// *fantrax.Client satisfies it, and so does a two-method fake — which is the
// point: this phase is testable without a Fantrax client.
type DateResolver interface {
	GetSeasonDateRange() (time.Time, time.Time, error)
	GetMatchupWeekBounds(date, seasonStart time.Time) (weekStart, weekEnd time.Time, err error)
}

// ResolveDates expands the caller's date selection into the concrete list of
// days a run will optimize, returning it as a VALUE.
//
// Previously this lived inline in Run and appended straight into the caller's
// *config.Config — an undocumented output parameter (rosterbot-6rv criterion 1).
// `base` is copied before extension, so neither the caller's slice nor its
// backing array is touched.
//
// Three cases:
//   - neither lookup flag set: `base` (explicit --dates) passes through and the
//     client is never called;
//   - NeedsMatchupLookup: the remaining days of the matchup week containing
//     today, starting at today so already-played days are skipped;
//   - NeedsSeasonLookup: today (or the season opener, whichever is later)
//     through the season's final day.
//
// seasonStart is returned alongside because the same GetSeasonDateRange call
// serves both, and downstream phases need it for period resolution. It stays
// zero when no lookup was requested — the caller only needs it in the branches
// that trigger one.
//
// logf receives the same progress lines Run used to emit inline, so terminal
// output is unchanged.
func ResolveDates(ft DateResolver, base []time.Time, opts Options, logf func(string, ...any)) ([]time.Time, time.Time, error) {
	if !opts.NeedsSeasonLookup && !opts.NeedsMatchupLookup {
		return base, time.Time{}, nil
	}

	start, end, err := ft.GetSeasonDateRange()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("get season date range: %w", err)
	}
	seasonStart := start

	// Copy rather than append in place: base may have spare capacity, in which
	// case appending would write into the caller's backing array.
	dates := make([]time.Time, len(base), len(base)+8)
	copy(dates, base)

	if opts.NeedsMatchupLookup {
		weekStart, weekEnd, err := ft.GetMatchupWeekBounds(opts.Today, seasonStart)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("get matchup week: %w", err)
		}
		if weekStart.IsZero() {
			return nil, time.Time{}, fmt.Errorf("no matchup week found for today")
		}
		// Start from today (skip past days in the matchup).
		mStart := weekStart
		if mStart.Before(opts.Today) {
			mStart = opts.Today
		}
		for d := mStart; !d.After(weekEnd); d = d.AddDate(0, 0, 1) {
			dates = append(dates, d)
		}
		logf("matchup period: %s to %s (%d days remaining)",
			weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"), len(dates))
		return dates, seasonStart, nil
	}

	if start.Before(opts.Today) {
		start = opts.Today
	}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d)
	}
	logf("season range: %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	return dates, seasonStart, nil
}
