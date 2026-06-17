package lineupapi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Publisher is the write side: producers Publish the marshaled lineup under a
// key. Implemented by FileStore (local dev) and the S3 adapter (Fargate).
type Publisher interface {
	Publish(key string, data []byte) error
}

// FileStore is a local-filesystem ObjectStore + Publisher. It writes one file
// per key at <dir>/lineup-<key>.json, used by `rosterbot serve` and by
// `optimize --publish-lineup` for local curl testing before deploy.
type FileStore struct {
	dir string
}

// NewFileStore returns a FileStore rooted at dir (created lazily on Publish).
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

func (s *FileStore) path(key string) string {
	return filepath.Join(s.dir, "lineup-"+key+".json")
}

func (s *FileStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	data, err := os.ReadFile(s.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *FileStore) Publish(key string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(key), data, 0o644)
}

var (
	_ ObjectStore = (*FileStore)(nil)
	_ Publisher   = (*FileStore)(nil)
)
