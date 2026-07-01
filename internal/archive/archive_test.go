package archive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriterWritesDatePartition(t *testing.T) {
	root := t.TempDir()
	w := Writer{Root: root}
	date := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	err := w.Write(date, "hkb", []Artifact{{Filename: "rankings.html", Bytes: []byte("hello")}})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "hkb", "dt=2026-06-30", "rankings.html"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("bytes = %q, want %q", got, "hello")
	}
}

func TestWriterLastWriteWinsAndNoTempLeft(t *testing.T) {
	root := t.TempDir()
	w := Writer{Root: root}
	date := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	if err := w.Write(date, "savant", []Artifact{{Filename: "a.csv", Bytes: []byte("v1")}, {Filename: "b.csv", Bytes: []byte("x")}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Re-run with a smaller set + changed bytes: old dir must be fully replaced.
	if err := w.Write(date, "savant", []Artifact{{Filename: "a.csv", Bytes: []byte("v2")}}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	dir := filepath.Join(root, "savant", "dt=2026-06-30")
	got, _ := os.ReadFile(filepath.Join(dir, "a.csv"))
	if string(got) != "v2" {
		t.Errorf("a.csv = %q, want v2", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.csv")); !os.IsNotExist(err) {
		t.Errorf("stale b.csv should be gone after last-write-wins")
	}
	// No leftover temp dir beside the final one.
	entries, _ := os.ReadDir(filepath.Join(root, "savant"))
	for _, e := range entries {
		if e.Name() != "dt=2026-06-30" {
			t.Errorf("unexpected leftover entry %q", e.Name())
		}
	}
}

func TestGetReturnsRawBytesAndErrorsOnNon200(t *testing.T) {
	// Use zero backoff so the 500 sub-test doesn't sleep during CI.
	orig := getRetryBackoff
	getRetryBackoff = []time.Duration{0, 0}
	defer func() { getRetryBackoff = orig }()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("RAW"))
	}))
	defer ok.Close()
	got, err := Get(context.Background(), ok.URL)
	if err != nil || string(got) != "RAW" {
		t.Fatalf("Get ok = %q, %v; want RAW, nil", got, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := Get(context.Background(), bad.URL); err == nil {
		t.Error("Get on 500 should error")
	}
}

func TestGetRetriesThenSucceeds(t *testing.T) {
	orig := getRetryBackoff
	getRetryBackoff = []time.Duration{0, 0}
	defer func() { getRetryBackoff = orig }()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("OK"))
	}))
	defer srv.Close()

	got, err := Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	if string(got) != "OK" {
		t.Fatalf("body = %q, want OK", got)
	}
}

func TestGetExhaustsRetriesOn500(t *testing.T) {
	orig := getRetryBackoff
	getRetryBackoff = []time.Duration{0, 0}
	defer func() { getRetryBackoff = orig }()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	wantCalls := int32(len(getRetryBackoff) + 1)
	if n := calls.Load(); n != wantCalls {
		t.Errorf("handler called %d times, want %d", n, wantCalls)
	}
}

func TestGetDoesNotRetry4xx(t *testing.T) {
	orig := getRetryBackoff
	getRetryBackoff = []time.Duration{0, 0}
	defer func() { getRetryBackoff = orig }()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("handler called %d times, want 1 (no retry on 4xx)", n)
	}
}
