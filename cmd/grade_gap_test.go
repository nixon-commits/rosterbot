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
