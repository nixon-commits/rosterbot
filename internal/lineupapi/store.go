package lineupapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
//
// A key is a FILENAME STEM, never a path: Get and Publish refuse anything that
// is not a single clean component (safeComponent). filepath.Join calls Clean,
// so a "../" inside a key does not stay a literal filename — it climbs out of
// dir, and with the empty namePrefix `serve` gives the Reports store a single
// ".." is enough (CodeQL go/path-injection #23 on Get, #24 on Publish). The S3
// twin's multi-segment keys — opsnotify's "alarm/<name>/<ts>" — are a prefix
// layout that s3blob owns; they never reach this store, and namePrefix would
// land on their first segment if they did.
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

// safeComponent reports whether s is a single clean path component — no
// separators, not "" / "." / ".." — so <dir>/<prefix><s>.json cannot name
// anything outside dir. It is the one predicate behind every file store here
// that turns an opaque token (a run id, a blob key) into a filename: those
// tokens are never paths, so a value that is not a clean component can only be
// malformed or hostile, and one shared test means the stores cannot drift on
// what "safe" means.
func safeComponent(s string) bool {
	return s != "" && s != "." && s != ".." &&
		!strings.ContainsAny(s, `/\`) && s == filepath.Clean(s)
}

func (s *FileStore) path(key string) string {
	return filepath.Join(s.dir, s.namePrefix+key+".json")
}

func (s *FileStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	if !safeComponent(key) {
		// Not found rather than an error: Publish refuses the same key, so no
		// object can exist under it, and the run stores answer the same way.
		return nil, false, nil
	}
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
	if !safeComponent(key) {
		return fmt.Errorf("invalid key %q: must be a single path component", key)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(key), data, 0o644)
}

var (
	_ ObjectStore = (*FileStore)(nil)
	_ Publisher   = (*FileStore)(nil)
)
