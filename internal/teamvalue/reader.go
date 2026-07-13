package teamvalue

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Reader loads rows from the Team Value Store (opposite of Writer). The whole
// series is read wholesale to draw the time plot.
type Reader interface {
	ReadAll() ([]Row, error)
}

type fileReader struct{ root string }

// NewFileReader returns a Reader over rows persisted under
// root/dt=YYYY-MM-DD/values.ndjson (the FileWriter layout).
func NewFileReader(root string) Reader { return fileReader{root: root} }

func (r fileReader) ReadAll() ([]Row, error) {
	matches, err := filepath.Glob(filepath.Join(r.root, "dt=*", "values.ndjson"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches) // chronological: dt= partitions sort lexically by date
	var rows []Row
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
