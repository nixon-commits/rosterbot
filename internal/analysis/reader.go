package analysis

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Reader loads graded rows from the Analysis Store (opposite of Writer).
type Reader interface {
	ReadAll() ([]GradeRow, error)
}

type fileReader struct{ root string }

// NewFileReader returns a Reader over grades persisted under
// root/grades/dt=YYYY-MM-DD/system=SYSTEM/grades.ndjson (the FileWriter layout).
// It also reads legacy root/grades/dt=YYYY-MM-DD/grades.ndjson partitions
// (no system= segment), attributing them to LegacySystem.
func NewFileReader(root string) Reader { return fileReader{root: root} }

func (r fileReader) ReadAll() ([]GradeRow, error) {
	systemMatches, err := filepath.Glob(filepath.Join(r.root, "grades", "dt=*", "system=*", "grades.ndjson"))
	if err != nil {
		return nil, err
	}
	legacyMatches, err := filepath.Glob(filepath.Join(r.root, "grades", "dt=*", "grades.ndjson"))
	if err != nil {
		return nil, err
	}
	matches := append(systemMatches, legacyMatches...)
	sort.Strings(matches)
	var rows []GradeRow
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		rs, err := UnmarshalNDJSON(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		system := SystemFromKey(p)
		for i := range rs {
			rs[i].System = system
		}
		rows = append(rows, rs...)
	}
	return rows, nil
}
