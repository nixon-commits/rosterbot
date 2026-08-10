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

// BlobStore is both sides of a keyed object store. Producers Publish, the API
// Gets. The S3 adapter and FileStore each satisfy it, which is what lets
// statestore hand one value to a writer and a reader alike.
type BlobStore interface {
	ObjectStore
	Publisher
}

// FileStore is a local-filesystem ObjectStore + Publisher. It writes one file
// per key at <dir>/<namePrefix><key>.json, used by `rosterbot serve` and by
// `optimize --publish-lineup` for local curl testing before deploy.
type FileStore struct {
	dir string
	// namePrefix disambiguates artifacts that share a directory. It mirrors
	// the S3 side, where the key prefix ("lineup/", "trades/") does the same
	// job — there the separator is a path segment, here a filename stem.
	namePrefix string
}

// NewFileStore returns a FileStore for published lineups, rooted at dir
// (created lazily on Publish).
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir, namePrefix: "lineup-"} }

// NewFileBlobStore returns a FileStore for an arbitrary artifact, writing
// <dir>/<namePrefix><key>.json.
func NewFileBlobStore(dir, namePrefix string) *FileStore {
	return &FileStore{dir: dir, namePrefix: namePrefix}
}

func (s *FileStore) path(key string) string {
	return filepath.Join(s.dir, s.namePrefix+key+".json")
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
