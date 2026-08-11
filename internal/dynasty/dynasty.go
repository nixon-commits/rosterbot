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

// Writer persists a day's per-asset rows to the store.
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

// Reader loads rows from the Dynasty Value Store.
type Reader interface {
	ReadAll() ([]Row, error)
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
