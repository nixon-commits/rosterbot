// Package ndjsonstore is the shared plumbing behind this repo's durable,
// date-partitioned NDJSON stores (the Analysis Store in internal/analysis and
// the Team Value Store in internal/teamvalue).
//
// It owns marshal/unmarshal, the read-all-partitions walk, and a byte-level
// Store seam — mirroring how internal/cache.FileCache[T] sits over a Store so
// the AWS SDK stays out of the leaf. Each domain package keeps what is actually
// its own: the row type, the partition key layout, and any key-derived fields.
package ndjsonstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Store is byte-level partition storage. Keys are partition-relative (e.g.
// "grades/dt=2026-07-20/system=atc-ros/grades.ndjson"); each implementation
// joins its own root or bucket prefix.
type Store interface {
	// Put writes b at key, creating any intermediate structure.
	Put(key string, b []byte) error
	// Get reads the object at key.
	Get(key string) ([]byte, error)
	// List returns every key under prefix. A prefix with nothing under it
	// yields no keys and no error — an empty store is not an error.
	List(prefix string) ([]string, error)
}

// Marshal serializes rows as newline-delimited JSON (one row per line).
func Marshal[T any](rows []T) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// Unmarshal parses newline-delimited JSON (one row per line).
func Unmarshal[T any](b []byte) ([]T, error) {
	var rows []T
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var r T
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

// Write marshals rows and puts them at key.
func Write[T any](s Store, key string, rows []T) error {
	b, err := Marshal[T](rows)
	if err != nil {
		return err
	}
	return s.Put(key, b)
}

// ReadAll concatenates every partition under prefix whose key ends in filename,
// in key order — which is chronological, since dt=YYYY-MM-DD sorts lexically by
// date.
//
// Matching on the filename suffix rather than a fixed key depth is what lets a
// single walk pick up partition layouts that gained a dimension over time: the
// Analysis Store's legacy dt=X/ partitions and its current dt=X/system=Y/ ones
// are both found without a second pass.
//
// decorate, when non-nil, is called per partition with that partition's key and
// rows, so a store can stamp fields the key carries but the body does not.
func ReadAll[T any](s Store, prefix, filename string, decorate func(key string, rows []T)) ([]T, error) {
	keys, err := s.List(prefix)
	if err != nil {
		return nil, err
	}

	matched := keys[:0:0]
	for _, k := range keys {
		if strings.HasSuffix(k, filename) {
			matched = append(matched, k)
		}
	}
	sort.Strings(matched)

	var rows []T
	for _, k := range matched {
		b, err := s.Get(k)
		if err != nil {
			return nil, err
		}
		rs, err := Unmarshal[T](b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if decorate != nil {
			decorate(k, rs)
		}
		rows = append(rows, rs...)
	}
	return rows, nil
}
