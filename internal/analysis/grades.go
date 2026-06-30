// Package analysis writes the durable, append-only Analysis Store: the Graded
// Snapshot fact (projected vs actual per player per day) as NDJSON, partitioned
// by date for Athena. The S3 adapter lives in the s3grades sub-package so the
// AWS SDK stays out of this package.
package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// GradeRow is one Graded Snapshot: a (date, player) projected-vs-actual fact.
type GradeRow struct {
	Dt        string  `json:"dt"`
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

// Writer persists a day's graded rows to the Analysis Store.
type Writer interface {
	WriteGrades(date time.Time, rows []GradeRow) error
}

// MarshalNDJSON serializes rows as newline-delimited JSON (one row per line).
func MarshalNDJSON(rows []GradeRow) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func objectKey(date time.Time) string {
	return fmt.Sprintf("grades/dt=%s/grades.ndjson", date.UTC().Format("2006-01-02"))
}

// ObjectKey is exported so the S3 writer reuses the same partition layout.
func ObjectKey(date time.Time) string { return objectKey(date) }

type fileWriter struct{ root string }

// NewFileWriter returns a Writer that persists grades to the local filesystem
// under root, partitioned as grades/dt=YYYY-MM-DD/grades.ndjson.
func NewFileWriter(root string) Writer { return fileWriter{root: root} }

func (w fileWriter) WriteGrades(date time.Time, rows []GradeRow) error {
	b, err := MarshalNDJSON(rows)
	if err != nil {
		return err
	}
	p := filepath.Join(w.root, objectKey(date))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// UnmarshalNDJSON parses newline-delimited JSON (one GradeRow per line).
func UnmarshalNDJSON(b []byte) ([]GradeRow, error) {
	var rows []GradeRow
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var r GradeRow
		err := dec.Decode(&r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, nil
}
