package cmd

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/spf13/cobra"
)

var (
	gradeDates     string
	gradeWindow    int
	gradeMaxWindow int
)

var gradeCmd = &cobra.Command{
	Use:   "grade",
	Short: "Grade past projections and append Graded Snapshots to the Analysis Store",
	RunE:  runGrade,
}

func init() {
	gradeCmd.Flags().StringVar(&gradeDates, "dates", "", "explicit date or range to grade (overrides --window)")
	gradeCmd.Flags().IntVar(&gradeWindow, "window", 3, "(re)grade AT LEAST this many trailing days ending yesterday; the run reaches further back on its own to meet the newest day the Analysis Store already holds")
	gradeCmd.Flags().IntVar(&gradeMaxWindow, "max-window", 14, "ceiling on that automatic reach-back; older ungraded days are named on stdout rather than re-graded")
	rootCmd.AddCommand(gradeCmd)
}

const gradeDateFmt = "2006-01-02"

// gradeCoverage is what the Analysis Store already holds: the newest dt= day
// carrying graded rows, and whether the store could be read at all.
//
// A zero Latest and a non-nil Err are deliberately distinct states, because
// they warrant opposite reactions. An empty store is an ordinary fresh tenant
// or a fresh checkout and must NOT be read as "everything since the opener is
// missing"; an unreadable store is a fault we can say nothing from, and the
// only safe response is to REFUSE the run — see resolveGradeWindow.
type gradeCoverage struct {
	Latest time.Time
	Err    error
}

// latestGradedDay reports the newest dt= day the Analysis Store covers.
//
// The pointer is the newest day ANY projection system graded, on purpose. A
// per-system pointer would drag the window back over days a single system can
// never recover — a shadow capture that failed for one system leaves that
// system's partition permanently absent — and re-fetch a week of actuals daily
// to fill a hole nothing can fill.
//
// The seam available from cmd is Reader.ReadAll, so learning one date costs a
// walk of every grade partition. That is the same full read cmd/projection-site
// already performs on its own daily job, which is what makes it affordable
// here; the cheap version is a max-dt listing on analysis.Reader, since the
// object KEYS carry dt= and would need no GetObject at all. Worth doing if the
// Grade job's runtime ever becomes the constraint, but it is a change to a
// shared store interface and this fix does not need it.
func latestGradedDay(r analysis.Reader) gradeCoverage {
	rows, err := r.ReadAll()
	if err != nil {
		return gradeCoverage{Err: err}
	}
	var latest time.Time
	for _, row := range rows {
		d, err := time.Parse(gradeDateFmt, row.Dt)
		if err != nil {
			// A row whose dt is unparseable says nothing about coverage.
			// Skipping it can only make the window wider, never narrower, so it
			// cannot hide a hole.
			continue
		}
		if d.After(latest) {
			latest = d
		}
	}
	return gradeCoverage{Latest: latest}
}

// countDays is the inclusive day count of [start, end], zero when end precedes
// start. Counted by stepping rather than dividing a Duration so it stays right
// regardless of the zone the callers' timestamps carry.
func countDays(start, end time.Time) int {
	n := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		n++
	}
	return n
}

// resolveGradeWindow picks the [start, end] range a default (no --dates) run
// covers, and returns the account of that choice for printing.
//
// The default used to be a fixed `--window 3` trailing days, which is silently
// narrower than the outages it exists to absorb (rosterbot-n30). The
// 2026-08-01..08-03 STALE_CLIENT outage failed every Fantrax-touching job for
// three days, so the first run afterwards self-healed 08-01..08-03 and left
// 2026-07-31 a permanent hole in both the Analysis Store and the Lineup Gap
// Store — and opsalert escalates at exactly opsalert.EscalateAt = 3 consecutive
// failures, i.e. the window has already been outrun by the time anyone is told.
//
// The window is therefore DERIVED: it reaches back to the day after the newest
// day the store already holds, so it scales to the outage instead of guessing a
// constant. The bead offered two alternatives and both cost more than they buy:
//
//   - Widening the constant is still a guess, only further out. Every day of
//     slack is paid on every healthy run (one DailyFantasyPoints walk day plus
//     four partition rewrites), and the guess is wrong in precisely the case
//     that matters — an outage one day longer than whatever number was picked.
//   - Driving a targeted re-grade from the Infra tab's gap detection chases
//     EVERY hole, including ones that can never be filled: dt=2026-08-01 and
//     08-02 have no shadow capture at all and are permanently ungradeable, so
//     that design re-fetches their actuals daily, forever, to report a gap it
//     can do nothing about. A newest-day pointer is MOSTLY immune — a lost day
//     in the middle of the series sits BEHIND the pointer and is never
//     revisited, and a lost day at the tail stops being chased the moment any
//     newer day grades.
//
// The "mostly" is a real limitation and is stated rather than glossed: the
// pointer cannot see a SkipMarker. analysis.Reader.ReadAll matches only
// grades.ndjson, and SkipMarkerFilename is deliberately a DIFFERENT name so it
// never decodes as a GradeRow — so a day the store holds as "nothing to grade"
// (rosterbot-u9u) leaves the pointer where it was and reads to this function
// exactly like an ungraded hole. While the TAIL of the series is skip-marked —
// an off-season, an All-Star break, a run of days no rostered player played —
// the window keeps reaching back over those days on every run.
//
// The cost is bounded and is compute, not correctness: re-grading such a day
// re-fetches its actuals, finds nothing, and writes the same marker again
// (WriteSkip is idempotent), so nothing is corrupted and no hole is created. It
// self-clears the moment any newer day produces real rows, which in season is
// the next day. Closing it properly means teaching analysis.Reader to report
// skip-marked days — the same interface change the max-dt listing below wants,
// and out of scope here.
//
// The fixed window survives as a FLOOR rather than as the whole rule, and that
// half is load-bearing for an unrelated reason: Fantrax's YTD totals are still
// settling for the last few closed periods (fantrax.recentPeriodLookback = 3,
// which is where the 3 came from), so the trailing days must be re-graded even
// though they are already present.
//
// Two bounds keep this from widening the exposure the bead's own NOTES record —
// a broad `grade --dates` run once silently rewrote dt=2026-07-21 with one
// fewer row per system, because the local .backtest/snapshots-systems/ copy was
// an OLDER capture than production for the overlapping dates:
//
//   - The reach-back only ever covers days at or after Latest+1, i.e. days the
//     store does not hold. It cannot rewrite an existing partition; only the
//     fixed floor does that, over the same three days it always did.
//   - maxWindow caps the reach, and what it declined to reach is NAMED rather
//     than silently dropped (this repo's no-silent-caps rule, of which
//     golangci-lint's self-truncating issue count is the standing example).
//
// The account is printed, never pushed. An off-season, or a tail of days no
// shadow capture exists for, would otherwise pin an alert red with no action
// that could clear it — which is how a status signal teaches you to stop
// reading it, the same reasoning that keeps ArtifactStatus.LostGaps out of
// Health.
func resolveGradeWindow(today time.Time, window, maxWindow int, cov gradeCoverage) (start, end time.Time, notes []string, err error) {
	if window < 1 {
		window = 1
	}
	if maxWindow < window {
		// A ceiling below the floor would quietly shrink the settling-lag
		// re-grade, which is the one part of this window that is not a guess.
		maxWindow = window
	}

	end = today.AddDate(0, 0, -1)
	start = today.AddDate(0, 0, -window)
	floor := today.AddDate(0, 0, -maxWindow)

	switch {
	case cov.Err != nil:
		// Refuse rather than fall back to the floor, because the fallback is
		// not the cheaper failure it looks like.
		//
		// A floor run still GRADES and WRITES its three days, which advances
		// Latest to yesterday. Every day the reach-back would have covered then
		// sits behind the pointer and is never revisited — a recoverable hole
		// converted into a permanent one, silently, on precisely the run that
		// matters (the first after an outage).
		//
		// Refusing costs one day of grades, and the next run with a readable
		// store recovers it: the pointer never moved, so the derived window
		// reaches back over both the outage AND the refused day. One day
		// deferred beats N days destroyed, and the failure is loud — opsalert
		// escalates on the third consecutive one — where the silent version is
		// exactly the class this bead was filed against.
		return time.Time{}, time.Time{}, nil, fmt.Errorf(
			"analysis store unreadable (%w) — refusing to grade: a run on the fixed %dd floor would "+
				"advance the store's newest day past any older hole and make it permanent", cov.Err, window)
	case cov.Latest.IsZero():
		notes = append(notes, fmt.Sprintf(
			"Analysis Store holds no graded day — holding the fixed %dd floor rather than grading back to the season opener", window))
	default:
		notes = append(notes, "Analysis Store covers through "+cov.Latest.Format(gradeDateFmt))
		if next := cov.Latest.AddDate(0, 0, 1); next.Before(start) {
			start = next
		}
	}

	if start.Before(floor) {
		lastSkipped := floor.AddDate(0, 0, -1)
		notes = append(notes, fmt.Sprintf(
			"capped at --max-window %dd: %s..%s (%d day(s)) NOT re-graded — recover with `grade --dates %s:%s`. "+
				"If you run that LOCALLY, check .backtest/snapshots-systems/ covers the range and is not an OLDER capture "+
				"than production first; a deployed run reads snapshots straight from S3 and is not exposed "+
				"(rosterbot-n30: a broad local re-grade from a stale copy rewrote a partition with one fewer row per system, "+
				"recovered only because bucket versioning was on)",
			maxWindow,
			start.Format(gradeDateFmt), lastSkipped.Format(gradeDateFmt), countDays(start, lastSkipped),
			start.Format(gradeDateFmt), lastSkipped.Format(gradeDateFmt)))
		start = floor
	}
	return start, end, notes, nil
}

// explicitDatesNotes annotates a manual --dates range.
//
// A wide manual range is exactly the operation the bead's NOTES record going
// wrong, and unlike the derived window it CAN rewrite partitions the store
// already holds with worse ones. It stays permitted — a deliberate backfill has
// to be possible, and refusing it would only push the operator to a script with
// no warning at all — so the guard is a printed caution rather than a refusal.
func explicitDatesNotes(start, end time.Time, maxWindow int) []string {
	n := countDays(start, end)
	notes := []string{fmt.Sprintf("explicit --dates (%d day(s)); the store's own coverage is not consulted", n)}
	if n > maxWindow {
		notes = append(notes,
			"this range rewrites partitions the store already holds. Running LOCALLY, confirm "+
				".backtest/snapshots-systems/ covers the range and is not an OLDER capture than production "+
				"(rosterbot-n30: a broad local re-grade from a stale copy rewrote dt=2026-07-21 with one fewer row "+
				"per system). A deployed run reads snapshots from S3 via statestore.SnapshotStore and has no local "+
				"copy to be stale — .backtest/ has not been in the entrypoint sync since rosterbot-iqso")
	}
	return notes
}

// ungradeableDays picks out the days on which no rostered player appeared in a
// game. Those days produce no graded rows, so no dt= partition is written, and
// the Infra page counts the absence as a hole (rosterbot-u9u).
//
// The discriminator is the day's own roster snapshot rather than the MLB
// schedule, for two reasons. The 2026 All-Star break spans three dates and the
// middle one DID have a game — the All-Star Game, an exhibition Fantrax scores
// nothing for — so a schedule lookup would call that day gradeable and leave
// the false gap in place. And the answer is already in hand here, so asking
// statsapi would add a network dependency to a judgement that does not need one.
//
// Only dates the fetch actually returned are considered, and only when the
// snapshot held players. An absent or empty day is not evidence that no
// baseball was played — treating it as such would suppress precisely the real
// gap this has to keep reporting (2026-07-01: games were played, the shadow
// capture missed the day, the partition is genuinely and recoverably absent).
func ungradeableDays(days []fantrax.DayRoster, graded map[string]int) []analysis.SkipMarker {
	var out []analysis.SkipMarker
	for _, d := range days {
		if len(d.Players) == 0 {
			continue
		}
		dt := d.Date.UTC().Format("2006-01-02")
		if graded[dt] > 0 {
			continue
		}
		played := 0
		for _, p := range d.Players {
			if p.HadGame || p.FPts != 0 {
				played++
			}
		}
		if played > 0 {
			continue
		}
		out = append(out, analysis.SkipMarker{
			Dt:               dt,
			Reason:           "no rostered player appeared in a game",
			RosterPlayers:    len(d.Players),
			PlayersWithGames: 0,
			WrittenAt:        time.Now().UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dt < out[j].Dt })
	return out
}

func runGrade(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	// Default: a trailing window ending yesterday, whose START is derived from
	// what the Analysis Store already covers rather than fixed at --window days
	// (rosterbot-n30 — see resolveGradeWindow for why derivation beats a bigger
	// constant, and what bounds it). Grading is idempotent per date (each dt
	// partition is overwritten) and missing/stale days are skipped, so a failed
	// night self-heals on the next run however long the outage ran.
	var (
		start, end time.Time
		windowLog  []string
	)
	if gradeDates != "" {
		ds, err := parseDates(gradeDates, today)
		if err != nil {
			return err
		}
		start, end = ds[0], ds[len(ds)-1]
		windowLog = explicitDatesNotes(start, end, gradeMaxWindow)
	} else {
		// An unreadable store is FATAL here, not soft. See resolveGradeWindow:
		// grading on the floor would advance the store's newest day past any
		// older hole, so the degraded run is the one that loses data while the
		// refusal only defers a day.
		cov := gradeCoverage{}
		reader, readerErr := statestore.FromEnv().AnalysisReader()
		if readerErr != nil {
			cov.Err = readerErr
		} else {
			cov = latestGradedDay(reader)
		}
		var werr error
		start, end, windowLog, werr = resolveGradeWindow(today, gradeWindow, gradeMaxWindow, cov)
		if werr != nil {
			return werr
		}
	}

	// Printed unconditionally, healthy case included — the same rule as the
	// `il-start check:` and `mlb recency coverage:` lines. A window that has
	// silently stopped covering what it claims to cover is indistinguishable
	// from a quiet one unless the run states its own reach every time.
	fmt.Printf("grade window: %s..%s (%d day(s))\n",
		start.Format(gradeDateFmt), end.Format(gradeDateFmt), countDays(start, end))
	for _, note := range windowLog {
		fmt.Printf("  %s\n", note)
	}

	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		return fmt.Errorf("get season start: %w", err)
	}

	snapTTL := cacheTTL(fantrax.PastPeriodTTL)
	// DailyFantasyPoints resolves the MLB-statsapi backfill internally (soft-fail),
	// so the returned rows never carry placeholder zeros.
	days, err := ft.DailyFantasyPoints(cfg.TeamID, start, end, seasonStart, cacheDir, snapTTL)
	if err != nil {
		return fmt.Errorf("daily fpts: %w", err)
	}

	// Grade every projection system the shadow command captured. Actuals
	// (days) are fetched once above and reused — only the projection side
	// differs per system. Each system's rows land in its own Hive partition
	// (grades/dt=X/system=Y/...). The depthcharts-ros slice keeps feeding the
	// existing detailed dashboard; the others power the comparison panel.
	snapStore, err := statestore.FromEnv().SnapshotStore()
	if err != nil {
		return fmt.Errorf("snapshot store: %w", err)
	}

	bySystemDate := map[string]map[string][]analysis.GradeRow{}
	for _, sys := range shadowSystems {
		dir := systemSnapshotDir(shadowSnapshotRoot, sys)
		results := backtest.RunProjectionAnalysis(days, snapStore, dir)
		byDate := map[string][]analysis.GradeRow{}
		for _, d := range results {
			if d.Source == "missing" || d.Source == "stale" || d.Source == "no-data" {
				// Forward-only: before the shadow command has captured a day,
				// its snapshot is absent and the day is skipped, not graded.
				// "no-data" means the system had a real outage that day (see
				// Snapshot.HittersNoData/PitchersNoData) — same treatment.
				continue
			}
			dt := d.Date.UTC().Format("2006-01-02")
			for _, p := range d.Players {
				byDate[dt] = append(byDate[dt], analysis.GradeRow{
					Dt:        dt,
					PlayerID:  p.PlayerID,
					Name:      p.Name,
					MLBTeam:   p.MLBTeam,
					Projected: p.Projected,
					Actual:    p.Actual,
					Diff:      p.Diff,
					Bucket:    p.Bucket,
					IsPitcher: p.IsPitcher,
					Source:    p.Source,
				})
			}
		}
		if len(byDate) > 0 {
			bySystemDate[sys] = byDate
		}
	}

	// Aggregate per-date row counts across all systems for the health signal.
	counts := map[string]int{}
	for _, byDate := range bySystemDate {
		for dt, rows := range byDate {
			counts[dt] += len(rows)
		}
	}

	// Days with no fantasy-relevant baseball at all. These produce no graded
	// rows and so no dt= partition, which the Infra page then counts as a hole
	// — three fabricated gaps across the 2026 All-Star break, sitting in the
	// same "Re-runnable" list as one real one (rosterbot-u9u). A date that did
	// grade rows is never marked, so the two signals can't contradict.
	skips := ungradeableDays(days, counts)
	lineupapi.RecordOutput("grade", gradeToWireResult(counts, windowLog))

	if cfg.DryRun {
		for _, sys := range shadowSystems {
			for dt, rows := range bySystemDate[sys] {
				fmt.Printf("[dry-run] %s %s: %d graded rows\n", sys, dt, len(rows))
			}
		}
		for _, m := range skips {
			fmt.Printf("[dry-run] %s: nothing to grade (%s)\n", m.Dt, m.Reason)
		}
		return nil
	}

	w, err := statestore.FromEnv().AnalysisWriter()
	if err != nil {
		return fmt.Errorf("init analysis store: %w", err)
	}

	for _, sys := range shadowSystems {
		for dt, rows := range bySystemDate[sys] {
			date, _ := time.Parse("2006-01-02", dt)
			if err := w.WriteGrades(date, sys, rows); err != nil {
				return fmt.Errorf("write grades %s %s: %w", sys, dt, err)
			}
			fmt.Printf("wrote %d graded rows for %s %s\n", len(rows), sys, dt)
		}
	}

	// Written after the grades so a partial run can never leave a day marked
	// "nothing to grade" when grading was in fact attempted and failed.
	for _, m := range skips {
		date, _ := time.Parse("2006-01-02", m.Dt)
		if err := w.WriteSkip(date, m); err != nil {
			return fmt.Errorf("write skip marker %s: %w", m.Dt, err)
		}
		fmt.Printf("marked %s ungradeable: %s\n", m.Dt, m.Reason)
	}

	// The Lineup Gap Store: how the lineup we actually applied scored against
	// the hindsight-optimal one. Computed here because this command already
	// holds `days` and a live client — the read-side (projection-site) has
	// neither.
	//
	// Soft-fail: grades are irreplaceable, and a gap day is recoverable with
	// `grade --dates`, so a gap hiccup must never fail the run.
	if err := recordLineupGaps(ft, days); err != nil {
		fmt.Fprintf(os.Stderr, "warning: lineup gaps not written: %v\n", err)
	}
	return nil
}

// recordLineupGaps grades each day's applied lineup against hindsight and
// persists the result. Slot lists are stableTTL-cached (7d), so this costs one
// cold fetch per week on top of the actuals runGrade already holds.
func recordLineupGaps(ft *fantrax.Client, days []fantrax.DayRoster) error {
	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		return fmt.Errorf("get hitter slots: %w", err)
	}
	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		return fmt.Errorf("get pitcher slots: %w", err)
	}

	results := backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)
	if len(results) == 0 {
		return nil
	}

	w, err := statestore.FromEnv().LineupGapWriter()
	if err != nil {
		return fmt.Errorf("init lineup gap store: %w", err)
	}
	return writeLineupGaps(w, results)
}

// writeLineupGaps maps lineup-grade results to store rows, one partition per
// date. Split from recordLineupGaps so the mapping is testable without a
// Fantrax client.
func writeLineupGaps(w lineupgap.Writer, results []backtest.LineupDayResult) error {
	for _, d := range results {
		dt := d.Date.UTC().Format("2006-01-02")
		row := lineupgap.Row{
			Dt:         dt,
			ActualPts:  d.ActualPts,
			OptimalPts: d.OptimalPts,
			Gap:        d.Gap,
			StartedN:   len(d.Started),
			BenchedN:   len(d.Benched),
		}
		if err := w.WriteGaps(d.Date, []lineupgap.Row{row}); err != nil {
			return fmt.Errorf("write lineup gap %s: %w", dt, err)
		}
		fmt.Printf("wrote lineup gap for %s (actual %.1f, optimal %.1f, gap %.1f)\n",
			dt, d.ActualPts, d.OptimalPts, d.Gap)
	}
	return nil
}
