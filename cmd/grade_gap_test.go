package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
)

type fakeGapWriter struct {
	rows []lineupgap.Row
	err  error
}

func (f *fakeGapWriter) WriteGaps(date time.Time, rows []lineupgap.Row) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, rows...)
	return nil
}

func TestWriteLineupGaps_MapsResultsToRows(t *testing.T) {
	w := &fakeGapWriter{}
	results := []backtest.LineupDayResult{{
		Date:       time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		ActualPts:  90,
		OptimalPts: 100,
		Gap:        -10,
		Started:    []backtest.PlayerPts{{PlayerID: "a"}, {PlayerID: "b"}},
		Benched:    []backtest.PlayerPts{{PlayerID: "c"}},
	}}

	if err := writeLineupGaps(w, results); err != nil {
		t.Fatalf("writeLineupGaps: %v", err)
	}
	if len(w.rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(w.rows))
	}
	got := w.rows[0]
	if got.Dt != "2026-07-20" || got.ActualPts != 90 || got.OptimalPts != 100 || got.Gap != -10 {
		t.Errorf("row mapped wrong: %+v", got)
	}
	if got.StartedN != 2 || got.BenchedN != 1 {
		t.Errorf("counts wrong: StartedN=%d BenchedN=%d, want 2/1", got.StartedN, got.BenchedN)
	}
}

// Grades are the irreplaceable artifact; the gap is recomputable. A gap-write
// failure must never take the grade run down with it.
func TestWriteLineupGaps_ReturnsErrorForCallerToSoftFail(t *testing.T) {
	w := &fakeGapWriter{err: errors.New("s3 exploded")}
	results := []backtest.LineupDayResult{{Date: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)}}
	if err := writeLineupGaps(w, results); err == nil {
		t.Error("want an error the caller can log-and-continue on, got nil")
	}
}

// The coverage line prints on every run, healthy case included — the same
// rule as `il-start check:` and `mlb recency coverage:`. A gap write that has
// quietly stopped covering a day is indistinguishable from a quiet one unless
// the run states its own reach every time.
func TestLineupGapCoverageLines_HealthyWindowStillPrints(t *testing.T) {
	got := lineupGapCoverageLines(3, nil)
	if len(got) != 1 || got[0] != "lineup gaps: 3 scoring day(s), 0 excluded" {
		t.Errorf("got %q", got)
	}
}

func TestLineupGapCoverageLines_NamesEachExcludedDay(t *testing.T) {
	got := lineupGapCoverageLines(1, []backtest.ExcludedDay{{
		Date:   time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC),
		Reason: "no scoring matchup for this team in Playoffs - Round 2 (2026-09-14 to 2026-09-20): bye or eliminated",
	}})
	want := []string{
		"lineup gaps: 1 scoring day(s), 1 excluded",
		"  2026-09-14 withheld: no scoring matchup for this team in Playoffs - Round 2 (2026-09-14 to 2026-09-20): bye or eliminated",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
