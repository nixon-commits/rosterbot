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
// root/grades/dt=YYYY-MM-DD/grades.ndjson (the FileWriter layout).
func NewFileReader(root string) Reader { return fileReader{root: root} }

func (r fileReader) ReadAll() ([]GradeRow, error) {
	matches, err := filepath.Glob(filepath.Join(r.root, "grades", "dt=*", "grades.ndjson"))
	if err != nil {
		return nil, err
	}
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
		rows = append(rows, rs...)
	}
	return rows, nil
}
