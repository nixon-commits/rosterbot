package cache

import (
	"errors"
	"os"
	"path/filepath"
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

func storeForDir(dir string) Store {
	if defaultStore != nil {
		return defaultStore
	}
	return fsStore{root: dir}
}
