// Package dynasty writes the durable Dynasty Value Store: one row per
// (date, team, asset) — a player or an owned future draft pick — priced
// across all four StatsGuy formats, as NDJSON partitioned by date.
//
// Unlike internal/teamvalue (per-team aggregate, NoBackfill:true — HKB and
// rosters carry no history), this store is per-player grain: every future
// breakdown (by position, by format, by starter/bench) is re-derivable from
// the stored rows without re-aggregating, and a missed day is re-runnable
// (roster + StatsGuy snapshots are both re-fetchable), so it is NOT
// NoBackfill — see the layout entry added in rosterbot-5d7.
//
// The NDJSON plumbing (marshal, partition walk, storage seam) lives in
// internal/ndjsonstore, shared with internal/teamvalue and internal/analysis.
package dynasty

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// valuesFilename is the leaf object name in every partition.
const valuesFilename = "values.ndjson"

// Row is one asset's (player or pick) StatsGuy value for one team on one day.
//
// Value is carried as all four StatsGuy format leaves so the dashboard
// derives the sf_dynasty/non_sf_dynasty/sf_redraft/non_sf_redraft toggle
// client-side, without re-aggregating — the same pattern as teamvalue.Row.
type Row struct {
	Dt       string `json:"dt"`
	TeamID   string `json:"team_id"` // Sleeper roster_id
	TeamName string `json:"team_name"`

	AssetType string `json:"asset_type"` // "player" | "pick"
	AssetID   string `json:"asset_id"`   // Sleeper player_id, or "pick:<year>:<round>"
	Name      string `json:"name"`
	Position  string `json:"position"` // players only; "" for picks

	// IsStarter is meaningful for players only (false for picks): whether the
	// asset was in the roster's actual starting lineup (roster.Starters), or
	// the search_rank fallback when Starters is unusable. Never derived from
	// StatsGuy value — see Aggregate's doc comment for why that would be
	// circular.
	IsStarter bool `json:"is_starter"`

	// Estimated is meaningful for picks only: StatsGuy prices an unresolved
	// future pick generically by round ("mid" variant), not by draft slot —
	// there is no team standing yet to prefer one tier. Always false for
	// players (a player match is a direct id join, not a tier estimate).
	Estimated bool `json:"estimated"`

	ValueSFDynasty    int `json:"value_sf_dynasty"`
	ValueNonSFDynasty int `json:"value_non_sf_dynasty"`
	ValueSFRedraft    int `json:"value_sf_redraft"`
	ValueNonSFRedraft int `json:"value_non_sf_redraft"`
}

// ValueFor returns the row's value in the given StatsGuy format (one of
// "sf_dynasty"/"non_sf_dynasty"/"sf_redraft"/"non_sf_redraft" — the raw JSON
// keys, matching DYNASTY_FORMAT), or 0 for an unrecognized format. Mirrors
// statsguy.FormatValues.Get.
func (r Row) ValueFor(format string) int {
	switch format {
	case "sf_dynasty":
		return r.ValueSFDynasty
	case "non_sf_dynasty":
		return r.ValueNonSFDynasty
	case "sf_redraft":
		return r.ValueSFRedraft
	case "non_sf_redraft":
		return r.ValueNonSFRedraft
	default:
		return 0
	}
}

// Writer persists a day's per-asset rows and per-team coverage to the store.
type Writer interface {
	WriteValues(date time.Time, rows []Row) error
	// WriteCoverage persists the per-team join-coverage diagnostic alongside
	// that day's values.ndjson, as coverage.ndjson in the same partition.
	// Row's per-asset grain can't reconstruct RosteredCount on its own (an
	// unmatched player produces no Row), so the dashboard needs this to show
	// coverage counts at all.
	WriteCoverage(date time.Time, coverage []Coverage) error
}

// coverageFilename is the leaf object name for the per-team coverage
// diagnostic, written alongside valuesFilename in the same date partition.
const coverageFilename = "coverage.ndjson"

func objectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/%s", date.UTC().Format("2006-01-02"), valuesFilename)
}

func coverageObjectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/%s", date.UTC().Format("2006-01-02"), coverageFilename)
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

func (w writer) WriteCoverage(date time.Time, coverage []Coverage) error {
	return ndjsonstore.Write(w.store, coverageObjectKey(date), coverage)
}

// Reader loads rows and coverage from the Dynasty Value Store.
type Reader interface {
	ReadAll() ([]Row, error)
	// ReadAllCoverage reads every date's coverage.ndjson, chronologically.
	// Callers wanting "today's" coverage take the rows sharing the max Dt.
	ReadAllCoverage() ([]Coverage, error)
}

type reader struct{ store ndjsonstore.Store }

// NewReader returns a Reader over rows in store, partitioned as
// dt=YYYY-MM-DD/values.ndjson.
func NewReader(store ndjsonstore.Store) Reader { return reader{store: store} }

// NewFileReader returns a Reader over a local directory root.
func NewFileReader(root string) Reader { return NewReader(ndjsonstore.NewFileStore(root)) }

func (r reader) ReadAll() ([]Row, error) {
	return ndjsonstore.ReadAll[Row](r.store, "", valuesFilename, nil)
}

func (r reader) ReadAllCoverage() ([]Coverage, error) {
	return ndjsonstore.ReadAll[Coverage](r.store, "", coverageFilename, nil)
}
