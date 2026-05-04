package recap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// SiteOptions configures a multi-week site build.
type SiteOptions struct {
	OutDir string
	// Today is the cutoff: matchup weeks whose end date is strictly before
	// Today (in YYYY-MM-DD lexical order) are considered completed and
	// included in the output. The in-progress week is skipped.
	Today time.Time
	// Recap is the per-week base options (CacheDir, CacheTTL, TopPlayers,
	// Concurrency). WeekStart/WeekEnd/WeekNumber/WeekLabel are overwritten
	// per week.
	Recap Options
}

// RunSite renders every completed matchup week into OutDir as
// `week-NN.html`, plus duplicates the latest week as `index.html` so the
// site root serves the most recent recap. Each rendered page includes a
// dropdown navigation linking to all other completed weeks.
func RunSite(ft *fantrax.Client, sopts SiteOptions) error {
	if sopts.OutDir == "" {
		return fmt.Errorf("OutDir is required")
	}
	if sopts.Today.IsZero() {
		sopts.Today = time.Now().UTC()
	}
	if err := os.MkdirAll(sopts.OutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", sopts.OutDir, err)
	}

	completed, err := completedMatchupWeeks(ft, sopts.Today)
	if err != nil {
		return err
	}
	if len(completed) == 0 {
		return fmt.Errorf("no completed matchup weeks before %s", sopts.Today.Format("2006-01-02"))
	}

	// Build the static portion of the nav (descending — most recent first).
	nav := make([]WeekLink, len(completed))
	for i, w := range completed {
		nav[i] = WeekLink{
			WeekNumber: w.n,
			WeekLabel:  fmt.Sprintf("Week %d", w.n),
			Filename:   weekFilename(w.n),
		}
	}

	var latestRecap *Recap
	var latestNum int
	for _, w := range completed {
		weekOpts := sopts.Recap
		weekOpts.WeekStart = w.start
		weekOpts.WeekEnd = w.end
		weekOpts.WeekNumber = w.n
		// Past weeks are immutable; default to a long TTL when caller didn't
		// override. Caller can pass 0 explicitly with --no-cache semantics.
		if weekOpts.CacheTTL == 0 {
			weekOpts.CacheTTL = 30 * 24 * time.Hour
		}

		fmt.Fprintf(os.Stderr, "  rendering week %d (%s..%s)\n",
			w.n, w.start.Format("2006-01-02"), w.end.Format("2006-01-02"))

		r, err := Run(ft, weekOpts)
		if err != nil {
			return fmt.Errorf("week %d: %w", w.n, err)
		}

		path := filepath.Join(sopts.OutDir, weekFilename(w.n))
		if err := writeRender(path, r, navWithCurrent(nav, w.n)); err != nil {
			return err
		}

		if w.n > latestNum {
			latestRecap = r
			latestNum = w.n
		}
	}

	// index.html = the latest week.
	if latestRecap != nil {
		path := filepath.Join(sopts.OutDir, "index.html")
		if err := writeRender(path, latestRecap, navWithCurrent(nav, latestNum)); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "Built %d weeks → %s\n", len(completed), sopts.OutDir)
	return nil
}

// matchupWeek is one (number, start, end) tuple.
type matchupWeek struct {
	n          int
	start, end time.Time
}

// completedMatchupWeeks enumerates weeks 1..N for the configured team and
// returns only those whose end date is strictly before today (lexical YMD
// comparison so timezone arithmetic doesn't bite us). Sorted ascending.
func completedMatchupWeeks(ft *fantrax.Client, today time.Time) ([]matchupWeek, error) {
	todayYMD := today.Format("2006-01-02")
	var out []matchupWeek
	for n := 1; ; n++ {
		ws, we, err := ft.GetMatchupWeekByNumber(n)
		if err != nil {
			return nil, fmt.Errorf("week %d bounds: %w", n, err)
		}
		if ws.IsZero() {
			break
		}
		if we.Format("2006-01-02") < todayYMD {
			out = append(out, matchupWeek{n: n, start: ws, end: we})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	return out, nil
}

// navWithCurrent returns a copy of nav with IsCurrent set on the entry whose
// WeekNumber matches current. Order is preserved.
func navWithCurrent(nav []WeekLink, current int) []WeekLink {
	out := make([]WeekLink, len(nav))
	for i, link := range nav {
		link.IsCurrent = link.WeekNumber == current
		out[i] = link
	}
	return out
}

func weekFilename(n int) string {
	return fmt.Sprintf("week-%02d.html", n)
}

func writeRender(path string, r *Recap, nav []WeekLink) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := RenderSite(f, r, nav); err != nil {
		_ = f.Close()
		return fmt.Errorf("render %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
