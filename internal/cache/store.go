package cache

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Store is the byte-level storage seam behind the Cache. FileCache[T] owns the
// TTL/envelope logic; a Store only moves opaque bytes keyed by cache key.
// found is false (with nil err) when the key is absent.
type Store interface {
	Get(key string) (data []byte, found bool, err error)
	Put(key string, data []byte) error
	Remove(key string) error
}

// fsStore stores each entry as <root>/<key>.json — the historical .cache layout.
type fsStore struct{ root string }

func (s fsStore) path(key string) string { return filepath.Join(s.root, key+".json") }

func (s fsStore) Get(key string) ([]byte, bool, error) {
	b, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s fsStore) Put(key string, data []byte) error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(key), data, 0o644)
}

func (s fsStore) Remove(key string) error {
	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// defaultStore, when set via SetDefaultStore, backs every FileCache regardless
// of the dir passed to New. Mirrors the package-global pattern of Verbose/Notify.
var defaultStore Store

// SetDefaultStore makes every FileCache use s instead of a filesystem store.
func SetDefaultStore(s Store) { defaultStore = s }

// noopStore is the storage for a cache that has none: every read misses, every
// write succeeds by doing nothing. It exists so "there is no cache" is a state
// the seam can represent, rather than an fsStore rooted at "" that fails every
// Put with `mkdir : no such file or directory` (rosterbot-9uqx).
type noopStore struct{}

func (noopStore) Get(string) ([]byte, bool, error) { return nil, false, nil }
func (noopStore) Put(string, []byte) error         { return nil }
func (noopStore) Remove(string) error              { return nil }

// storeForDir resolves the backing store for a cache dir.
//
// The defaultStore check MUST stay first. SetDefaultStore backs every
// FileCache regardless of the dir passed to New, so on AWS an empty dir is an
// ordinary S3-backed cache and must not be made inert — the empty-dir guard
// below applies only to the filesystem branch it belongs to.
//
// An empty dir on the filesystem branch means there is nowhere to store
// anything, which is a legitimate state (`--no-cache`, and the several callers
// that thread a cacheDir straight through from it), not a broken path. It was
// previously an fsStore rooted at "", which read as a miss and then failed
// every write loudly enough to bury a real cache warning.
func storeForDir(dir string) Store {
	if defaultStore != nil {
		return defaultStore
	}
	if dir == "" {
		return noopStore{}
	}
	return fsStore{root: dir}
}

// NewFileStore returns the default filesystem adapter rooted at dir — the
// historical <dir>/<key>.json layout New resolves via storeForDir. Exported so
// test support (cachetest) and anything else that must address an on-disk
// cache dir through the Store seam can construct one without restating the
// layout.
func NewFileStore(dir string) Store { return fsStore{root: dir} }

// MemStore is an in-memory Store for hermetic tests in this and other packages.
type MemStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemStore() *MemStore { return &MemStore{m: map[string][]byte{}} }

func (s *MemStore) Get(key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	return b, ok, nil
}

func (s *MemStore) Put(key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	s.m[key] = cp
	return nil
}

func (s *MemStore) Remove(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
