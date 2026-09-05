package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
)

// The Analysis Store is append-only and has no per-(day, system) delete, so a
// day written from a stale-input capture can only ever be excluded at READ
// time by a hand-maintained table (analysis.Exclusions, rosterbot-c61b). The
// filter here is the structural fix: refuse to write it at all. It lives at
// this call site rather than inside RunProjectionAnalysis, so pinning it means
// driving the call site.

func gradeResult(date string, source string, n int) backtest.ProjectionDayResult {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	players := make([]backtest.PlayerProjection, n)
	for i := range players {
		players[i] = backtest.PlayerProjection{PlayerID: "p1", Name: "P1", Source: backtest.SourceSnapshot}
	}
	return backtest.ProjectionDayResult{Date: d, Source: source, Players: players}
}

func TestGradeRowsByDate_RefusesAStaleInputCapture(t *testing.T) {
	rows, excluded := gradeRowsByDate([]backtest.ProjectionDayResult{
		gradeResult("2026-08-19", backtest.SourceInputStale, 3),
	})
	if len(rows) != 0 {
		t.Errorf("wrote %d dates from a stale-input capture, want none: %v", len(rows), rows)
	}
	if len(excluded) != 1 || excluded[0].Dt != "2026-08-19" || excluded[0].Source != backtest.SourceInputStale {
		t.Errorf("excluded = %+v, want one 2026-08-19 %s entry", excluded, backtest.SourceInputStale)
	}
}

func TestGradeRowsByDate_WritesACleanCapture(t *testing.T) {
	// The healthy half: an ordinary graded day must still be written, and must
	// not appear as an exclusion.
	rows, excluded := gradeRowsByDate([]backtest.ProjectionDayResult{
		gradeResult("2026-08-20", backtest.SourceSnapshot, 3),
	})
	if len(rows["2026-08-20"]) != 3 {
		t.Errorf("wrote %d rows for a clean capture, want 3", len(rows["2026-08-20"]))
	}
	for _, e := range excluded {
		if e.Dt == "2026-08-20" {
			t.Errorf("a graded day must not be reported as excluded: %+v", e)
		}
	}
}

func TestGradeRowsByDate_RefusesEveryNonGradeableSource(t *testing.T) {
	// The pre-existing three keep their behavior — the new source joins them
	// rather than replacing the filter.
	for _, src := range []string{backtest.SourceMissing, backtest.SourceStale, backtest.SourceNoData, backtest.SourceInputStale} {
		rows, _ := gradeRowsByDate([]backtest.ProjectionDayResult{gradeResult("2026-08-19", src, 2)})
		if len(rows) != 0 {
			t.Errorf("source %q wrote rows, want none", src)
		}
	}
}
