package projections

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// openCSV opens a CSV file, strips a UTF-8 BOM if present, reads the header,
// and validates that all required columns exist. Returns the csv.Reader and
// column-index map for row iteration.
func openCSV(path string, required []string) (*os.File, *csv.Reader, map[string]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open csv: %w", err)
	}

	// Strip BOM if present.
	bom := make([]byte, 3)
	if _, err := io.ReadFull(f, bom); err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("read csv: %w", err)
	}
	if bom[0] != 0xEF || bom[1] != 0xBB || bom[2] != 0xBF {
		if _, err := f.Seek(0, 0); err != nil {
			f.Close()
			return nil, nil, nil, fmt.Errorf("seek csv: %w", err)
		}
	}

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("csv header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}

	for _, c := range required {
		if _, ok := col[c]; !ok {
			f.Close()
			return nil, nil, nil, fmt.Errorf("csv missing required column: %s", c)
		}
	}

	return f, r, col, nil
}

// csvFloat parses a float64 from the named column. Returns 0 if the column
// is missing (use for optional columns) or the value is not a valid number.
func csvFloat(record []string, col map[string]int, name string) float64 {
	idx, ok := col[name]
	if !ok {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64)
	return v
}

// csvInt parses an int from the named column. Returns 0 if the column is
// missing or the value is not a valid integer.
func csvInt(record []string, col map[string]int, name string) int {
	idx, ok := col[name]
	if !ok {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(record[idx]))
	return v
}
