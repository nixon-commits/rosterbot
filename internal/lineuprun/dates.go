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

// OutOfSeasonError reports that today falls outside the fantasy season, so
// there is no matchup week to resolve and nothing to optimize. It is a
// distinguishable condition rather than a fault: the caller ends the run
// cleanly on it instead of exiting non-zero.
//
// It exists because both season boundaries land on the same zero weekStart
// from GetMatchupWeekBounds as a genuine mid-season schedule gap does, and the
// two deserve opposite responses. The 2026 season ended 2026-09-06 and the
// daily 13:45Z `optimize --matchup` job then failed every day with a cobra
// usage dump, which poisons the run ledger's failure signal; the identical
// branch fires before the 2027 opener, when no matchup row is published yet.
//
// Start and End are the season's own bounds, carried so the caller can say
// which side of the season it is on without a second lookup.
type OutOfSeasonError struct{ Today, Start, End time.Time }

func (e *OutOfSeasonError) Error() string {
	if e.BeforeOpener() {
		return fmt.Sprintf("season starts %s — nothing to optimize yet", e.Start.Format("2006-01-02"))
	}
	return fmt.Sprintf("season ended %s — nothing to optimize", e.End.Format("2006-01-02"))
}

// BeforeOpener distinguishes the two boundaries. Opening day itself counts as
// in season: the window between the opener and the first published matchup row
// is exactly where a silently disabled GS gate costs real points, so it must
// keep reaching the loud path rather than being swept in here.
func (e *OutOfSeasonError) BeforeOpener() bool { return e.Today.Before(e.Start) }

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
// A matchup lookup outside the season returns an *OutOfSeasonError, which the
// caller ends the run cleanly on rather than treating as a fault.
//
// seasonStart and seasonEnd are returned alongside because the same
// GetSeasonDateRange call serves both, and downstream phases need them — the
// former for period resolution, the latter so the GS-budget phase can tell an
// off-season run from a mid-season lookup failure. Both stay zero when no
// lookup was requested; the caller only needs them in the branches that
// trigger one.
//
// logf receives the same progress lines Run used to emit inline, so terminal
// output is unchanged.
func ResolveDates(ft DateResolver, base []time.Time, opts Options, logf func(string, ...any)) (dates []time.Time, seasonStart, seasonEnd time.Time, err error) {
	if !opts.NeedsSeasonLookup && !opts.NeedsMatchupLookup {
		return base, time.Time{}, time.Time{}, nil
	}

	start, end, err := ft.GetSeasonDateRange()
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("get season date range: %w", err)
	}
	seasonStart = start

	// Copy rather than append in place: base may have spare capacity, in which
	// case appending would write into the caller's backing array.
	dates = make([]time.Time, len(base), len(base)+8)
	copy(dates, base)

	if opts.NeedsMatchupLookup {
		// Decided from the season range before spending a request: outside the
		// season the bounds lookup returns a zero weekStart that cannot be told
		// apart from a mid-season schedule gap, and the two need opposite
		// handling. Below this point a zero weekStart means unambiguously "in
		// season, but Fantrax published no matchup row", which stays an error.
		if opts.Today.Before(seasonStart) || opts.Today.After(end) {
			return nil, time.Time{}, time.Time{}, &OutOfSeasonError{
				Today: opts.Today, Start: seasonStart, End: end,
			}
		}

		weekStart, weekEnd, err := ft.GetMatchupWeekBounds(opts.Today, seasonStart)
		if err != nil {
			return nil, time.Time{}, time.Time{}, fmt.Errorf("get matchup week: %w", err)
		}
		if weekStart.IsZero() {
			return nil, time.Time{}, time.Time{}, fmt.Errorf("no matchup week found for today")
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
		return dates, seasonStart, end, nil
	}

	if start.Before(opts.Today) {
		start = opts.Today
	}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d)
	}
	logf("season range: %s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	return dates, seasonStart, end, nil
}
