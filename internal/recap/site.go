package recap

import (
	"context"
	"fmt"
	"github.com/pmurley/go-fantrax/auth_client"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/schedule"
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
func RunSite(ctx context.Context, ft SiteClient, sopts SiteOptions) error {
	if sopts.OutDir == "" {
		return fmt.Errorf("OutDir is required")
	}
	if sopts.Today.IsZero() {
		sopts.Today = time.Now().UTC()
	}
	if err := os.MkdirAll(sopts.OutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", sopts.OutDir, err)
	}

	sched := schedule.NewClient()
	sched.CacheDir = sopts.Recap.CacheDir
	periods, _, _, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return fmt.Errorf("scoring periods: %w", err)
	}
	completed, err := completedMatchupWeeks(ctx, periods, sched, sopts.Today)
	if err != nil {
		return err
	}
	if len(completed) == 0 {
		return fmt.Errorf("no completed matchup weeks before %s", sopts.Today.Format("2006-01-02"))
	}

	// Build the static portion of the nav (descending — most recent first).
	nav := make([]WeekLink, 0, len(completed)+1)
	for _, w := range completed {
		nav = append(nav, WeekLink{
			WeekNumber: w.n,
			WeekLabel:  w.label,
			Filename:   weekFilename(w.n),
		})
	}
	// The year-end page rides the same picker as the weeks, last in the list.
	nav = append(nav, WeekLink{WeekLabel: "Season", Filename: seasonFilename})

	// Pass 1: build every week's recap. We can't render eagerly because each
	// page wants the season-to-date leaderboard, which requires having seen all
	// prior weeks first.
	recaps := make([]*Recap, 0, len(completed))
	weekNums := make([]int, 0, len(completed))
	for _, w := range completed {
		weekOpts := sopts.Recap
		weekOpts.WeekStart = w.start
		weekOpts.WeekEnd = w.end
		weekOpts.WeekNumber = w.n
		weekOpts.WeekLabel = w.label
		// Past weeks are immutable; default to a long TTL when caller didn't
		// override. Caller can pass 0 explicitly with --no-cache semantics.
		if weekOpts.CacheTTL == 0 {
			weekOpts.CacheTTL = fantrax.PastPeriodTTL
		}

		fmt.Fprintf(os.Stderr, "  building week %d (%s..%s)\n",
			w.n, w.start.Format("2006-01-02"), w.end.Format("2006-01-02"))

		r, err := Run(ctx, ft, weekOpts)
		if err != nil {
			return fmt.Errorf("week %d: %w", w.n, err)
		}
		recaps = append(recaps, r)
		weekNums = append(weekNums, w.n)
	}

	// Pass 2: aggregate season awards + standings history cumulatively.
	cumulative := AggregateSeasonAwards(recaps)
	standingsHistory := ComputeStandingsHistory(recaps)
	for i := range cumulative {
		if i < len(standingsHistory) {
			cumulative[i].StandingsHistory = standingsHistory[:i+1]
		}
	}

	// Pass 3: render each week with its through-week season snapshot.
	var latestRecap *Recap
	var latestSeason *SeasonAwards
	var latestNum int
	for i, r := range recaps {
		path := filepath.Join(sopts.OutDir, weekFilename(weekNums[i]))
		if err := writeRender(path, r, navWithCurrent(nav, weekNums[i]), cumulative[i]); err != nil {
			return err
		}
		if weekNums[i] > latestNum {
			latestRecap = r
			latestSeason = cumulative[i]
			latestNum = weekNums[i]
		}
	}

	// index.html = the latest week with through-latest season totals.
	if latestRecap != nil {
		path := filepath.Join(sopts.OutDir, "index.html")
		if err := writeRender(path, latestRecap, navWithCurrent(nav, latestNum), latestSeason); err != nil {
			return err
		}
	}

	// The year-end page: Fantrax's own standings, the bracket, and the season
	// awards that used to close every week page. Standings and bracket come
	// live; the awards are the final cumulative set.
	standings, err := ft.GetStandings()
	if err != nil {
		return fmt.Errorf("standings: %w", err)
	}
	bracket, err := ft.GetPlayoffBracket()
	if err != nil {
		return fmt.Errorf("playoff bracket: %w", err)
	}
	var logos map[string]string
	if latestRecap != nil {
		logos = latestRecap.LogoURLs
	}
	page := BuildSeasonPage(seasonLabel(completed), standings, bracket, latestSeason, logos, sopts.Today)
	seasonNav := make([]WeekLink, len(nav))
	copy(seasonNav, nav)
	seasonNav[len(seasonNav)-1].IsCurrent = true
	f, err := os.Create(filepath.Join(sopts.OutDir, seasonFilename))
	if err != nil {
		return fmt.Errorf("create %s: %w", seasonFilename, err)
	}
	if err := RenderSeason(f, page, seasonNav); err != nil {
		_ = f.Close()
		return fmt.Errorf("render %s: %w", seasonFilename, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", seasonFilename, err)
	}

	fmt.Fprintf(os.Stderr, "Built %d weeks → %s\n", len(completed), sopts.OutDir)
	return nil
}

// weekLabel names a period the way the site shows it: "Week N" for a
// regular-season period, Fantrax's own caption for a playoff round.
func weekLabel(p fantrax.ScoringPeriod) string {
	if p.Playoff {
		return p.Caption
	}
	return fmt.Sprintf("Week %d", p.Number)
}

// matchupWeek is one (number, label, start, end) tuple.
type matchupWeek struct {
	label      string
	n          int
	start, end time.Time
}

// dayCompletionChecker reports whether all MLB games on a date are over.
// Satisfied by *schedule.Client; narrowed to an interface so the week-cutoff
// logic is unit-testable without network.
type dayCompletionChecker interface {
	AllGamesFinalOn(ctx context.Context, date time.Time) (bool, error)
}

// SiteClient is the fantrax subset RunSite needs. It is RecapClient: the
// weeks are enumerated from the league's weekly period list, which Run
// already reads. *fantrax.Client satisfies it implicitly.
type SiteClient interface {
	RecapClient
	GetPlayoffBracket() (*auth_client.PlayoffBracket, error)
	GetStandings() ([]fantrax.StandingRow, error)
}

// completedMatchupWeeks enumerates the LEAGUE's weekly periods — regular
// season and playoff rounds alike, never the operator's own matchup weeks, so
// a round the operator was eliminated before still gets a page — and returns
// those that are over. A week is complete when its end date is
// strictly before today, OR its end date is today AND every MLB game on that
// final day has finished. The same-day case lets a Sunday-ending week render
// the same evening once its games conclude — the Fantrax weekly "points" field
// is a running in-week score and can't signal closure, but the MLB schedule
// can. On a schedule lookup error the same-day week is conservatively excluded
// (treated as still in progress). Sorted ascending.
func completedMatchupWeeks(ctx context.Context, periods []fantrax.ScoringPeriod, sched dayCompletionChecker, today time.Time) ([]matchupWeek, error) {
	todayYMD := today.Format("2006-01-02")
	var out []matchupWeek
	for _, p := range periods {
		w := matchupWeek{n: int(p.Number), label: weekLabel(p), start: p.StartDate, end: p.EndDate}
		weYMD := p.EndDate.Format("2006-01-02")
		switch {
		case weYMD < todayYMD:
			out = append(out, w)
		case weYMD == todayYMD:
			if done, err := sched.AllGamesFinalOn(ctx, p.EndDate); err == nil && done {
				out = append(out, w)
			}
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

func writeRender(path string, r *Recap, nav []WeekLink, season *SeasonAwards) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := RenderSite(f, r, nav, season); err != nil {
		_ = f.Close()
		return fmt.Errorf("render %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// seasonLabel names the season from the weeks that were rendered.
func seasonLabel(weeks []matchupWeek) string {
	if len(weeks) == 0 {
		return ""
	}
	return fmt.Sprintf("%d", weeks[0].start.Year())
}
