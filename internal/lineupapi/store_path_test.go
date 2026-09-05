package lineupapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The go/path-injection findings on FileStore (CodeQL #23 on Get, #24 on
// Publish). path() built filepath.Join(dir, prefix+key+".json") straight from
// the key, and Join calls Clean — so a "../" inside a key does not stay a
// literal filename, it climbs.
//
// Two store shapes are exercised because the prefix changes the arithmetic:
// "lineup-.." is a literal directory name, so the lineup store needs three
// ".." to climb out of base/store, while the Reports store `serve` wires with
// an EMPTY prefix escapes on one — the same single ".." the sibling run stores
// were fixed for.
var traversalCases = []struct{ name, prefix, key string }{
	{"prefixed lineup store", "lineup-", "../../../"},
	{"empty-prefix reports store", "", "../"},
}

// TestFileStore_ReadTraversalEscape plants a sentinel at the escape target
// (base/secret.json, sibling of the store dir) and asserts Get does not return
// it. The discriminating assertion is data == nil, not merely err == nil: an
// unguarded read returns the sentinel bytes with a nil error.
func TestFileStore_ReadTraversalEscape(t *testing.T) {
	for _, tc := range traversalCases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			storeDir := filepath.Join(base, "store")
			sentinelPath := filepath.Join(base, "secret.json")
			if err := os.WriteFile(sentinelPath, []byte("SENTINEL"), 0o644); err != nil {
				t.Fatalf("write sentinel: %v", err)
			}

			s := NewFileBlobStore(storeDir, tc.prefix)
			key := tc.key + "secret"
			data, ok, err := s.Get(context.Background(), key)
			if err != nil {
				t.Fatalf("Get(%q): unexpected error %v", key, err)
			}
			if ok || data != nil {
				t.Fatalf("Get(%q) = %q, ok=%v; want nil, false — guard should have blocked escape to %s", key, data, ok, sentinelPath)
			}
		})
	}
}

// TestFileStore_WriteTraversalEscape targets an escape directory (base) that
// already exists, so an unguarded write succeeds with no coincidental ENOENT
// to mask the missing guard. Both assertions matter: a non-nil error AND the
// escape target's absence, which is what actually proves nothing escaped.
func TestFileStore_WriteTraversalEscape(t *testing.T) {
	for _, tc := range traversalCases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			storeDir := filepath.Join(base, "store")
			if err := os.MkdirAll(storeDir, 0o755); err != nil {
				t.Fatalf("mkdir store: %v", err)
			}

			s := NewFileBlobStore(storeDir, tc.prefix)
			key := tc.key + "evil"
			escapeTarget := filepath.Join(base, "evil.json")
			if err := s.Publish(key, []byte("data")); err == nil {
				t.Fatalf("Publish(%q) = nil error, want non-nil — guard should have blocked escape to %s", key, escapeTarget)
			}
			if _, err := os.Stat(escapeTarget); !os.IsNotExist(err) {
				t.Fatalf("Publish(%q) wrote escape target %s (stat err=%v); traversal write succeeded", key, escapeTarget, err)
			}
		})
	}
}

// TestFileStore_RejectsEveryNonComponentKey pins the whole rejected set, not
// only the ".." that escapes: a separator anywhere makes prefix+key a path
// rather than the filename stem this store's layout promises, and "" / "." are
// not names at all. Each must be refused on write, and the store directory must
// hold nothing afterwards.
func TestFileStore_RejectsEveryNonComponentKey(t *testing.T) {
	dir := t.TempDir()
	s := NewFileBlobStore(dir, "trades-")
	for _, key := range []string{"", ".", "..", "a/b", `a\b`, "./current", "alarm/x/2026-09-02T00:00:00Z"} {
		if err := s.Publish(key, []byte("data")); err == nil {
			t.Errorf("Publish(%q) = nil error, want a rejection", key)
		}
		if _, ok, err := s.Get(context.Background(), key); err != nil || ok {
			t.Errorf("Get(%q) = ok=%v err=%v, want not found with a nil error", key, ok, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected keys still left files behind: %v", entries)
	}
}

// TestFileStore_LiveKeyShapesStillRoundTrip guards against fixing traversal
// by breaking the store: one key of every shape a real caller writes today
// must still publish and read back byte-for-byte.
func TestFileStore_LiveKeyShapesStillRoundTrip(t *testing.T) {
	keys := []string{
		TodayKey, PreviewKey, TradesCurrentKey, TradeValuesKey, AvailablePoolKey,
		ReportModelKey, ReportGapKey, ReportViewsKey,
		"2026-09-02",                          // the dated lineup lineuprun publishes
		"05p5r-2026-09-02",                    // ilStartMarkerKey: (player id, start date)
		"2026-p22-d2",                         // gsFloorMarkerKey: (season, weekly period, days left)
		"stale-fangraphs-bat-depthcharts-ros", // staleMarkerKey over a cache key
		"1136419849512345678",                 // a Sleeper transaction id (football trade markers)
		"tm4x9",                               // a Fantrax team id (roster values)
	}
	s := NewFileBlobStore(t.TempDir(), "")
	for _, key := range keys {
		want := []byte(`{"key":"` + key + `"}`)
		if err := s.Publish(key, want); err != nil {
			t.Fatalf("Publish(%q): %v", key, err)
		}
		got, ok, err := s.Get(context.Background(), key)
		if err != nil || !ok || string(got) != string(want) {
			t.Fatalf("Get(%q) = %q ok=%v err=%v; want %q", key, got, ok, err, want)
		}
	}
}
