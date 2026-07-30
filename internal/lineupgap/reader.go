package lineupgap

import (
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// Reader loads rows from the Lineup Gap Store (opposite of Writer). The whole
// series is read wholesale to draw the trend.
type Reader interface {
	ReadAll() ([]Row, error)
}

type reader struct{ store ndjsonstore.Store }

// NewReader returns a Reader over rows in store, partitioned as
// dt=YYYY-MM-DD/gaps.ndjson.
func NewReader(store ndjsonstore.Store) Reader { return reader{store: store} }

// NewFileReader returns a Reader over a local directory root.
func NewFileReader(root string) Reader { return NewReader(ndjsonstore.NewFileStore(root)) }

func (r reader) ReadAll() ([]Row, error) {
	// Empty prefix: every dt= partition sits directly under the store root.
	return ndjsonstore.ReadAll[Row](r.store, "", gapsFilename, nil)
}
