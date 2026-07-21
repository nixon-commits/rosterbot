// Package analysis writes the durable, append-only Analysis Store: the Graded
// Snapshot fact (projected vs actual per player per day) as NDJSON, partitioned
// by date and projection system for Athena.
//
// The NDJSON plumbing (marshal, partition walk, storage seam) lives in
// internal/ndjsonstore, shared with the Team Value Store. What stays here is
// what is genuinely this store's own: the row, the partition layout, and the
// system dimension the key carries.
package analysis

import (
	"fmt"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// LegacySystem is the projection system attributed to grade rows written
// before the system partition existed (the bot ran depth-charts RoS then).
// Legacy partitions (grades/dt=X/grades.ndjson, no system= segment) are read
// back under this system so the detailed dashboard keeps its pre-migration
// history across cutover.
const LegacySystem = "depthcharts-ros"

const (
	// gradesPrefix is the store root within its bucket prefix or local dir.
	gradesPrefix = "grades/"
	// gradesFilename is the leaf object name in every partition.
	gradesFilename = "grades.ndjson"
)

// GradeRow is one Graded Snapshot: a (date, player) projected-vs-actual fact.
//
// System is owned by the partition path (grades/dt=X/system=Y/...), not the
// NDJSON body, so it never collides with the Athena `system` partition column.
// Writers take it as an argument; Readers populate it from the object key.
type GradeRow struct {
	Dt        string  `json:"dt"`
	System    string  `json:"-"`
	PlayerID  string  `json:"player_id"`
	Name      string  `json:"name"`
	MLBTeam   string  `json:"mlb_team"`
	Projected float64 `json:"projected"`
	Actual    float64 `json:"actual"`
	Diff      float64 `json:"diff"`
	Bucket    string  `json:"bucket"`
	IsPitcher bool    `json:"is_pitcher"`
	Source    string  `json:"source"`
}

// Writer persists a day's graded rows for one projection system to the store.
type Writer interface {
	WriteGrades(date time.Time, system string, rows []GradeRow) error
}

// MarshalNDJSON serializes rows as newline-delimited JSON (one row per line).
func MarshalNDJSON(rows []GradeRow) ([]byte, error) { return ndjsonstore.Marshal(rows) }

// UnmarshalNDJSON parses newline-delimited JSON (one GradeRow per line).
func UnmarshalNDJSON(b []byte) ([]GradeRow, error) { return ndjsonstore.Unmarshal[GradeRow](b) }

func objectKey(date time.Time, system string) string {
	return fmt.Sprintf("%sdt=%s/system=%s/%s", gradesPrefix, date.UTC().Format("2006-01-02"), system, gradesFilename)
}

// ObjectKey is the store-relative partition key for a date and system.
func ObjectKey(date time.Time, system string) string { return objectKey(date, system) }

// SystemFromKey extracts the projection system from a grades object key. Keys
// carrying a `system=Y` segment return Y; legacy keys without one return
// LegacySystem so pre-migration partitions read back as depth-charts RoS.
func SystemFromKey(key string) string {
	for _, seg := range strings.Split(key, "/") {
		if v, ok := strings.CutPrefix(seg, "system="); ok {
			return v
		}
	}
	return LegacySystem
}

type writer struct{ store ndjsonstore.Store }

// NewWriter returns a Writer persisting grades to store, partitioned as
// grades/dt=YYYY-MM-DD/system=SYSTEM/grades.ndjson.
func NewWriter(store ndjsonstore.Store) Writer { return writer{store: store} }

// NewFileWriter returns a Writer over a local directory root.
func NewFileWriter(root string) Writer { return NewWriter(ndjsonstore.NewFileStore(root)) }

func (w writer) WriteGrades(date time.Time, system string, rows []GradeRow) error {
	return ndjsonstore.Write(w.store, objectKey(date, system), rows)
}
