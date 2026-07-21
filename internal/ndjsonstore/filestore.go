package ndjsonstore

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type fileStore struct{ root string }

// NewFileStore returns a Store rooted at a local directory. Keys map to paths
// under root using the OS separator.
func NewFileStore(root string) Store { return fileStore{root: root} }

func (s fileStore) path(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

func (s fileStore) Put(key string, b []byte) error {
	p := s.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func (s fileStore) Get(key string) ([]byte, error) {
	return os.ReadFile(s.path(key))
}

// List walks the tree under prefix and returns slash-separated keys relative to
// root. A missing directory yields no keys and no error, matching an empty
// bucket prefix (and the glob behavior this replaced).
func (s fileStore) List(prefix string) ([]string, error) {
	dir := s.path(prefix)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	var keys []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// MemStore is an in-memory Store for tests.
type MemStore struct{ objects map[string][]byte }

// NewMemStore returns an empty in-memory Store.
func NewMemStore() *MemStore { return &MemStore{objects: map[string][]byte{}} }

func (m *MemStore) Put(key string, b []byte) error {
	m.objects[key] = append([]byte(nil), b...)
	return nil
}

func (m *MemStore) Get(key string) ([]byte, error) {
	b, ok := m.objects[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return b, nil
}

func (m *MemStore) List(prefix string) ([]string, error) {
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
