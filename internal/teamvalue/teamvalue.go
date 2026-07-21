// Package teamvalue writes the durable, append-only Team Value Store: each
// fantasy team's aggregate HKB dynasty value for one day as NDJSON, partitioned
// by date.
//
// The NDJSON plumbing (marshal, partition walk, storage seam) lives in
// internal/ndjsonstore, shared with internal/analysis (the Graded-Snapshot
// store). This store is single-entity, so its partition key is just dt= — the
// entity name lives in the storage prefix rather than a second dimension.
//
// The series accumulates forward: HKB serves only current values (no history)
// and fantasy rosters are never archived, so past team compositions cannot be
// reconstructed. Each daily run appends one partition; the chart grows a point
// per day from first capture onward.
package teamvalue

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// valuesFilename is the leaf object name in every partition.
const valuesFilename = "values.ndjson"

// Row is one team's aggregate HKB value for one day.
//
// Value is stored as four leaf cells — hitter/pitcher × MLB/minors — so the
// renderer derives every toggle (Total / MLB / Minors / Hitter / Pitcher)
// client-side without re-aggregating. TeamName and LogoURL are denormalized
// from Fantrax at write time so the read+render path (projection-site) needs no
// Fantrax call.
//
// Dt is carried in the NDJSON body for convenience but is also the partition
// (dt=YYYY-MM-DD); on read it is populated from the row itself.
type Row struct {
	Dt       string `json:"dt"`
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	LogoURL  string `json:"logo_url"`

	// Value leaves (sum of HKB Value over rostered players in each bucket).
	HitterMLBValue     int `json:"hitter_mlb_value"`
	HitterMinorsValue  int `json:"hitter_minors_value"`
	PitcherMLBValue    int `json:"pitcher_mlb_value"`
	PitcherMinorsValue int `json:"pitcher_minors_value"`

	// Player counts per leaf (matched to HKB — a rostered player with no HKB
	// match contributes to RosteredCount but not to any value/count leaf).
	HitterMLBCount     int `json:"hitter_mlb_count"`
	HitterMinorsCount  int `json:"hitter_minors_count"`
	PitcherMLBCount    int `json:"pitcher_mlb_count"`
	PitcherMinorsCount int `json:"pitcher_minors_count"`

	// Join-coverage transparency: total rostered players on the team vs how
	// many joined to an HKB value. MatchedCount < RosteredCount means the value
	// totals undercount by the unmatched players (surfaced on the page).
	RosteredCount int `json:"rostered_count"`
	MatchedCount  int `json:"matched_count"`
}

// TotalValue is the team's whole-roster HKB value (all four leaves).
func (r Row) TotalValue() int {
	return r.HitterMLBValue + r.HitterMinorsValue + r.PitcherMLBValue + r.PitcherMinorsValue
}

// MLBValue is the value of MLB-roster (non-minors-eligible) players.
func (r Row) MLBValue() int { return r.HitterMLBValue + r.PitcherMLBValue }

// MinorsValue is the value of minors-eligible (farm) players.
func (r Row) MinorsValue() int { return r.HitterMinorsValue + r.PitcherMinorsValue }

// HitterValue is the value of all hitters (MLB + minors).
func (r Row) HitterValue() int { return r.HitterMLBValue + r.HitterMinorsValue }

// PitcherValue is the value of all pitchers (MLB + minors).
func (r Row) PitcherValue() int { return r.PitcherMLBValue + r.PitcherMinorsValue }

// Writer persists a day's per-team rows to the store.
type Writer interface {
	WriteValues(date time.Time, rows []Row) error
}

// MarshalNDJSON serializes rows as newline-delimited JSON (one row per line).
func MarshalNDJSON(rows []Row) ([]byte, error) { return ndjsonstore.Marshal(rows) }

// UnmarshalNDJSON parses newline-delimited JSON (one Row per line).
func UnmarshalNDJSON(b []byte) ([]Row, error) { return ndjsonstore.Unmarshal[Row](b) }

func objectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/%s", date.UTC().Format("2006-01-02"), valuesFilename)
}

// ObjectKey is the store-relative partition key (dt=YYYY-MM-DD/values.ndjson).
func ObjectKey(date time.Time) string { return objectKey(date) }

type writer struct{ store ndjsonstore.Store }

// NewWriter returns a Writer persisting rows to store, partitioned as
// dt=YYYY-MM-DD/values.ndjson.
func NewWriter(store ndjsonstore.Store) Writer { return writer{store: store} }

// NewFileWriter returns a Writer over a local directory root.
func NewFileWriter(root string) Writer { return NewWriter(ndjsonstore.NewFileStore(root)) }

func (w writer) WriteValues(date time.Time, rows []Row) error {
	return ndjsonstore.Write(w.store, objectKey(date), rows)
}
