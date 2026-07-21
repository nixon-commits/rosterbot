package analysis

import (
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// Reader loads graded rows from the Analysis Store (opposite of Writer).
type Reader interface {
	ReadAll() ([]GradeRow, error)
}

type reader struct{ store ndjsonstore.Store }

// NewReader returns a Reader over grades in store. It reads both the current
// grades/dt=X/system=Y/ layout and legacy grades/dt=X/ partitions (no system=
// segment), attributing the latter to LegacySystem.
func NewReader(store ndjsonstore.Store) Reader { return reader{store: store} }

// NewFileReader returns a Reader over a local directory root.
func NewFileReader(root string) Reader { return NewReader(ndjsonstore.NewFileStore(root)) }

func (r reader) ReadAll() ([]GradeRow, error) {
	return ndjsonstore.ReadAll[GradeRow](r.store, gradesPrefix, gradesFilename, func(key string, rows []GradeRow) {
		system := SystemFromKey(key)
		for i := range rows {
			rows[i].System = system
		}
	})
}
