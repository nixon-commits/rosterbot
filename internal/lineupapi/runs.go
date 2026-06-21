package lineupapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// invTimestampBase makes RunKey's prefix sort newest-first lexically: we store
// (base - epochSeconds) zero-padded, so a more recent run yields a smaller
// number and therefore sorts earlier in ascending S3/dir listings. Good through
// the year ~2286.
const invTimestampBase = 9999999999

// RunKey is the storage key for a run ledger record: an inverted-timestamp
// prefix (newest first under ascending listing) followed by the run id. Stable
// across the start (RUNNING) and end (SUCCESS/FAILED) writes because both pass
// the same startedAt. startedAt is RFC3339; an unparseable value sorts last.
func RunKey(startedAt, id string) string {
	inv := int64(invTimestampBase)
	if t, err := time.Parse(time.RFC3339, startedAt); err == nil {
		inv = invTimestampBase - t.Unix()
	}
	return fmt.Sprintf("%010d-%s", inv, id)
}

// runKeyMatchesID reports whether a storage key (without extension) belongs to
// the given run id — i.e. ends with "-<id>".
func runKeyMatchesID(key, id string) bool {
	return strings.HasSuffix(key, "-"+id)
}

// FileRunStore is a local-filesystem run ledger: one file per run at
// <dir>/run-<key>.json. Used by `rosterbot serve` and by the run-ledger writer
// when STATE_BUCKET is unset (local dev).
type FileRunStore struct {
	dir string
}

// NewFileRunStore returns a FileRunStore rooted at dir.
func NewFileRunStore(dir string) *FileRunStore { return &FileRunStore{dir: dir} }

func (s *FileRunStore) path(key string) string {
	return filepath.Join(s.dir, "run-"+key+".json")
}

// PutRun writes (or overwrites) the ledger record for a run.
func (s *FileRunStore) PutRun(_ context.Context, rec RunDetail) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(RunKey(rec.StartedAt, rec.ID)), data, 0o644)
}

func (s *FileRunStore) records() ([]RunDetail, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		// Only ledger records (run-*.json); other files in dir are ignored.
		if !e.IsDir() && strings.HasPrefix(e.Name(), "run-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // inverted-ts prefix => ascending = newest first

	var out []RunDetail
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			continue
		}
		var rec RunDetail
		if json.Unmarshal(data, &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *FileRunStore) List(_ context.Context, limit int) ([]Run, error) {
	recs, err := s.records()
	if err != nil {
		return nil, err
	}
	runs := make([]Run, 0, limit)
	for _, r := range recs {
		if len(runs) >= limit {
			break
		}
		runs = append(runs, r.Run)
	}
	return runs, nil
}

func (s *FileRunStore) Get(_ context.Context, id string) (*RunDetail, bool, error) {
	recs, err := s.records()
	if err != nil {
		return nil, false, err
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], true, nil
		}
	}
	return nil, false, nil
}

var _ RunStore = (*FileRunStore)(nil)
