// Package lineupgap writes the durable Lineup Gap Store: how each day's
// applied lineup scored against the hindsight-optimal one, as NDJSON
// partitioned by date.
//
// This is the only metric on the dashboard denominated in points the league
// actually awards. Projection accuracy (internal/report) says how well a system
// ranks players; this says what that ranking cost or saved in the standings.
//
// There is deliberately NO system dimension. Exactly one lineup was applied
// each day, and the hindsight-optimal total does not depend on which projection
// system was consulted — so the partition key is dt= alone and the entity name
// lives in the storage prefix, exactly as in internal/teamvalue. The
// consequence worth remembering: this store cannot discriminate between
// projection systems. Rank skill answers "which system"; this answers "what is
// the whole pipeline costing us".
//
// Unlike the Team Value Store, this series IS backfillable: past-period roster
// snapshots are immutable and cached, so a missed day is recoverable with
// `grade --dates`.
package lineupgap

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// gapsFilename is the leaf object name in every partition.
const gapsFilename = "gaps.ndjson"

// Row is one day's lineup decision graded against hindsight.
//
// Gap is signed as actual - optimal, so it is zero at best and negative when
// points were left on the bench. That sign convention matches
// backtest.LineupDayResult.Gap, which is where these numbers come from.
type Row struct {
	Dt         string  `json:"dt"`
	ActualPts  float64 `json:"actual_pts"`
	OptimalPts float64 `json:"optimal_pts"`
	Gap        float64 `json:"gap"` // actual - optimal; negative = left on bench
	StartedN   int     `json:"started_n"`
	BenchedN   int     `json:"benched_n"` // scored but not started
}

// Efficiency is the share of the hindsight-optimal total actually captured.
// Zero when there was no optimal total to capture (e.g. an off day).
func (r Row) Efficiency() float64 {
	if r.OptimalPts == 0 {
		return 0
	}
	return r.ActualPts / r.OptimalPts
}

// Writer persists a day's gap rows to the store.
type Writer interface {
	WriteGaps(date time.Time, rows []Row) error
}

func objectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/%s", date.UTC().Format("2006-01-02"), gapsFilename)
}

// ObjectKey is the store-relative partition key (dt=YYYY-MM-DD/gaps.ndjson).
func ObjectKey(date time.Time) string { return objectKey(date) }

type writer struct{ store ndjsonstore.Store }

// NewWriter returns a Writer persisting rows to store, partitioned as
// dt=YYYY-MM-DD/gaps.ndjson.
func NewWriter(store ndjsonstore.Store) Writer { return writer{store: store} }

// NewFileWriter returns a Writer over a local directory root.
func NewFileWriter(root string) Writer { return NewWriter(ndjsonstore.NewFileStore(root)) }

func (w writer) WriteGaps(date time.Time, rows []Row) error {
	return ndjsonstore.Write(w.store, objectKey(date), rows)
}
