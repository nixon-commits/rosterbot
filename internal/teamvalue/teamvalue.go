// Package teamvalue writes the durable, append-only Team Value Store: each
// fantasy team's aggregate HKB dynasty value for one day as NDJSON, partitioned
// by date. It mirrors internal/analysis (the Graded-Snapshot store) — the S3
// adapter lives in the s3teamvalue sub-package so the AWS SDK stays out of this
// leaf.
//
// The series accumulates forward: HKB serves only current values (no history)
// and fantasy rosters are never archived, so past team compositions cannot be
// reconstructed. Each daily run appends one partition; the chart grows a point
// per day from first capture onward.
package teamvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

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
func MarshalNDJSON(rows []Row) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// UnmarshalNDJSON parses newline-delimited JSON (one Row per line).
func UnmarshalNDJSON(b []byte) ([]Row, error) {
	var rows []Row
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var r Row
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

func objectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/values.ndjson", date.UTC().Format("2006-01-02"))
}

// ObjectKey is the partition-relative key (dt=YYYY-MM-DD/values.ndjson),
// exported so the S3 writer reuses the same layout under its own prefix.
func ObjectKey(date time.Time) string { return objectKey(date) }

type fileWriter struct{ root string }

// NewFileWriter returns a Writer that persists rows to the local filesystem
// under root, partitioned as root/dt=YYYY-MM-DD/values.ndjson.
func NewFileWriter(root string) Writer { return fileWriter{root: root} }

func (w fileWriter) WriteValues(date time.Time, rows []Row) error {
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
