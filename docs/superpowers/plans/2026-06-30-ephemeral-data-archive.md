# Ephemeral-Data Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture one faithful daily snapshot of each ephemeral upstream source (HKB, all four RoS FanGraphs projection systems, Baseball Savant windows, FanGraphs prospect board) into a durable, date-partitioned archive so point-in-time data that can't be re-fetched is preserved.

**Architecture:** A new leaf package `internal/archive` defines an `Artifact` value, a `Source` interface, and a `Writer` that lays blobs down under `.archive/<source>/dt=YYYY-MM-DD/`. Each source's home package (`hkb`, `projections`, `waivers`, `prospects`) gains one exported `ArchiveArtifacts(ctx, date)` that does raw HTTP GET(s) and returns the response bytes untouched. A new `archive` command wires the four sources, runs them with per-source isolation, and writes locally; `sync-up` ships `.archive/` to S3 `archive/` (append-only). An EventBridge schedule runs it daily.

**Tech Stack:** Go, Cobra (CLI), `net/http` (raw fetches), `httptest` (hermetic tests), AWS CDK in Go (`infra/`), existing `internal/statesync` S3 sync.

## Global Constraints

- Raw **upstream response bytes** are archived verbatim — no parsing, no re-serialization, no coupling to our structs.
- Partition directory format is exactly `dt=YYYY-MM-DD` using the **UTC** calendar date.
- Each source's URL knowledge stays in its home package; `internal/archive` imports **only** stdlib (leaf package).
- Per-source isolation: one source failing must not block others; the command exits non-zero **only if every source failed**.
- No credentials or live network in any test — override the existing URL `var`s with `httptest.Server` URLs.
- Run `go vet ./...` and `go mod tidy` after code changes (gofmt/vet also run via PostToolUse hooks).

---

### Task 1: `internal/archive` core (Artifact, Source, Writer)

**Files:**
- Create: `internal/archive/archive.go`
- Test: `internal/archive/archive_test.go`

**Interfaces:**
- Produces:
  - `type Artifact struct { Filename string; Bytes []byte }`
  - `type Source interface { Name() string; Fetch(ctx context.Context, date time.Time) ([]Artifact, error) }`
  - `type FuncSource struct { N string; F func(context.Context, time.Time) ([]Artifact, error) }` implementing `Source`
  - `type Writer struct { Root string }` with `func (w Writer) Write(date time.Time, source string, arts []Artifact) error`

- [ ] **Step 1: Write the failing test**

```go
package archive

import (
	"os"
	"path/filepath"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/archive/ -run TestWriter -v`
Expected: FAIL — `undefined: Writer` / `undefined: Artifact`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package archive captures faithful daily snapshots of ephemeral upstream data
// (data that only exists "as of now" and is unrecoverable once the day passes).
// It is a leaf: it imports only the standard library. Concrete Sources live in
// their home packages and are wired together by cmd/archive.go.
package archive

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Artifact is one file to archive: the upstream response bytes verbatim plus the
// filename it should land under within the source's dated partition.
type Artifact struct {
	Filename string
	Bytes    []byte
}

// Source fetches the ephemeral artifacts for one upstream (e.g. all HKB
// rankings, or all Savant CSVs) as of the given capture date.
type Source interface {
	Name() string
	Fetch(ctx context.Context, date time.Time) ([]Artifact, error)
}

// FuncSource adapts a plain fetch function into a Source, so cmd/archive.go can
// wire each package's ArchiveArtifacts without a bespoke type per source.
type FuncSource struct {
	N string
	F func(ctx context.Context, date time.Time) ([]Artifact, error)
}

func (s FuncSource) Name() string { return s.N }
func (s FuncSource) Fetch(ctx context.Context, date time.Time) ([]Artifact, error) {
	return s.F(ctx, date)
}

// Writer lays artifacts down under Root/<source>/dt=YYYY-MM-DD/<filename>.
type Writer struct{ Root string }

// Write is atomic per (source, date): it stages artifacts in a sibling temp dir,
// then swaps it into place, so a partial fetch never lands as a complete
// partition. Re-writing a date fully replaces that day's blobs (last-write-wins).
func (w Writer) Write(date time.Time, source string, arts []Artifact) error {
	dir := filepath.Join(w.Root, source, "dt="+date.UTC().Format("2006-01-02"))
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	for _, a := range arts {
		if err := os.WriteFile(filepath.Join(tmp, a.Filename), a.Bytes, 0o644); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}
```

Note: `context` is referenced by the `Source` interface signature, so the import is used — no blank-identifier trick needed.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/archive/ -run TestWriter -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/archive/archive.go internal/archive/archive_test.go
git commit -m "feat(archive): add Artifact/Source/Writer core"
```

---

### Task 2: HKB archive source

**Files:**
- Create: `internal/hkb/archive.go`
- Test: `internal/hkb/archive_test.go`

**Interfaces:**
- Consumes: `archive.Artifact` (Task 1).
- Produces: `func ArchiveArtifacts(ctx context.Context, _ time.Time) ([]archive.Artifact, error)` — returns one artifact `rankings.html` containing the raw HKB page bytes. Date is unused (HKB has no date param) but kept for `Source` conformance.

- [ ] **Step 1: Write the failing test**

```go
package hkb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestArchiveArtifactsReturnsRawPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>RAW HKB PAGE</html>"))
	}))
	defer srv.Close()
	orig := fetchURL
	fetchURL = srv.URL
	defer func() { fetchURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	if len(arts) != 1 || arts[0].Filename != "rankings.html" {
		t.Fatalf("got %+v, want one rankings.html", arts)
	}
	if string(arts[0].Bytes) != "<html>RAW HKB PAGE</html>" {
		t.Errorf("bytes = %q, want raw page verbatim", arts[0].Bytes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hkb/ -run TestArchiveArtifacts -v`
Expected: FAIL — `undefined: ArchiveArtifacts`.

- [ ] **Step 3: Write minimal implementation**

```go
package hkb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// ArchiveArtifacts fetches the HKB rankings page and returns its raw bytes for
// durable archival. HKB serves only current values, so this is the only way to
// preserve a given day's rankings. The date arg is unused (no date param
// upstream) but present for archive.Source conformance.
func ArchiveArtifacts(ctx context.Context, _ time.Time) ([]archive.Artifact, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("hkb archive fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hkb archive: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hkb archive read: %w", err)
	}
	return []archive.Artifact{{Filename: "rankings.html", Bytes: body}}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/hkb/ -run TestArchiveArtifacts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hkb/archive.go internal/hkb/archive_test.go
git commit -m "feat(archive): HKB raw-page source"
```

---

### Task 3: Projections archive source (all four RoS systems)

**Files:**
- Create: `internal/projections/archive.go`
- Test: `internal/projections/archive_test.go`

**Interfaces:**
- Consumes: `archive.Artifact` (Task 1); existing package vars `fgBaseURL`, `fgProjectionType`, and constants `ProjectionSteamerRoS`, `ProjectionDepthChartsRoS`, `ProjectionBatXRoS`, `ProjectionATCRoS`.
- Produces: `func ArchiveArtifacts(ctx context.Context, _ time.Time) ([]archive.Artifact, error)` — 8 artifacts: `<system>-bat.json` and `<system>-pit.json` for each of the four RoS systems, raw FanGraphs JSON bytes. Does **not** call `SetProjectionSystem` (that mutates package globals); builds URLs directly.

- [ ] **Step 1: Write the failing test**

```go
package projections

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestArchiveArtifactsCoversAllRoSSystems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo the type+stats so we can assert each URL was built correctly.
		w.Write([]byte(`{"type":"` + r.URL.Query().Get("type") + `","stats":"` + r.URL.Query().Get("stats") + `"}`))
	}))
	defer srv.Close()
	orig := fgBaseURL
	fgBaseURL = srv.URL + "?type=%s&stats=%s"
	defer func() { fgBaseURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	if len(arts) != 8 {
		t.Fatalf("got %d artifacts, want 8", len(arts))
	}
	var names []string
	for _, a := range arts {
		names = append(names, a.Filename)
		if len(a.Bytes) == 0 || !strings.HasPrefix(string(a.Bytes), `{"type":`) {
			t.Errorf("%s: expected raw JSON body, got %q", a.Filename, a.Bytes)
		}
	}
	sort.Strings(names)
	want := []string{
		"atc-ros-bat.json", "atc-ros-pit.json",
		"depthcharts-ros-bat.json", "depthcharts-ros-pit.json",
		"steamer-ros-bat.json", "steamer-ros-pit.json",
		"thebatx-ros-bat.json", "thebatx-ros-pit.json",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("filenames = %v, want %v", names, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/projections/ -run TestArchiveArtifacts -v`
Expected: FAIL — `undefined: ArchiveArtifacts`.

- [ ] **Step 3: Write minimal implementation**

```go
package projections

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// archivedSystems is the set of rest-of-season projection systems captured daily
// for durable archival — the full projection landscape, not just the bot's
// configured system, since FanGraphs serves only the current projection.
var archivedSystems = []string{
	ProjectionSteamerRoS,
	ProjectionDepthChartsRoS,
	ProjectionBatXRoS,
	ProjectionATCRoS,
}

// ArchiveArtifacts fetches raw FanGraphs batting+pitching JSON for every archived
// RoS system and returns them as <system>-bat.json / <system>-pit.json. It builds
// URLs directly (not via SetProjectionSystem, which mutates package globals).
func ArchiveArtifacts(ctx context.Context, _ time.Time) ([]archive.Artifact, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var arts []archive.Artifact
	for _, sys := range archivedSystems {
		apiType, ok := fgProjectionType[sys]
		if !ok {
			return nil, fmt.Errorf("archive: unknown system %q", sys)
		}
		for _, stats := range []string{"bat", "pit"} {
			url := fmt.Sprintf(fgBaseURL, apiType, stats)
			body, err := fetchRaw(ctx, client, url)
			if err != nil {
				return nil, fmt.Errorf("archive %s %s: %w", sys, stats, err)
			}
			arts = append(arts, archive.Artifact{
				Filename: fmt.Sprintf("%s-%s.json", sys, stats),
				Bytes:    body,
			})
		}
	}
	return arts, nil
}

func fetchRaw(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/projections/ -run TestArchiveArtifacts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/projections/archive.go internal/projections/archive_test.go
git commit -m "feat(archive): FanGraphs projections source (4 RoS systems)"
```

---

### Task 4: Savant archive source (five CSVs)

**Files:**
- Create: `internal/waivers/archive.go`
- Test: `internal/waivers/archive_test.go`

**Interfaces:**
- Consumes: `archive.Artifact` (Task 1); existing package vars `savantHitterExpURL`, `savantHitterExp14dURL`, `savantHitterSCURL`, `savantPitcherExpURL`, `savantPitcherExp30URL`.
- Produces: `func ArchiveArtifacts(ctx context.Context, date time.Time) ([]archive.Artifact, error)` — 5 artifacts (`hitter-exp.csv`, `hitter-statcast.csv`, `hitter-exp-14d.csv`, `pitcher-exp.csv`, `pitcher-exp-30d.csv`), raw CSV bytes. Window math mirrors `LoadSavant` exactly: `end = date.AddDate(0,0,-1)`, `start14 = end.AddDate(0,0,-13)`, `start30 = end.AddDate(0,0,-29)`, `year = date.Year()`.

- [ ] **Step 1: Write the failing test**

```go
package waivers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestArchiveArtifactsReturnsFiveCSVs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("player_id,x\n1,2\n"))
	}))
	defer srv.Close()
	// All five URL vars point at the fake server; keep their %d/%s verbs.
	save := []*string{&savantHitterExpURL, &savantHitterExp14dURL, &savantHitterSCURL, &savantPitcherExpURL, &savantPitcherExp30URL}
	orig := make([]string, len(save))
	for i, p := range save {
		orig[i] = *p
	}
	savantHitterExpURL = srv.URL + "?year=%d"
	savantHitterExp14dURL = srv.URL + "?year=%d&s=%s&e=%s"
	savantHitterSCURL = srv.URL + "?year=%d"
	savantPitcherExpURL = srv.URL + "?year=%d"
	savantPitcherExp30URL = srv.URL + "?year=%d&s=%s&e=%s"
	defer func() {
		for i, p := range save {
			*p = orig[i]
		}
	}()

	arts, err := ArchiveArtifacts(context.Background(), time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	var names []string
	for _, a := range arts {
		names = append(names, a.Filename)
		if !strings.HasPrefix(string(a.Bytes), "player_id,") {
			t.Errorf("%s: expected raw CSV, got %q", a.Filename, a.Bytes)
		}
	}
	sort.Strings(names)
	want := "hitter-exp-14d.csv,hitter-exp.csv,hitter-statcast.csv,pitcher-exp-30d.csv,pitcher-exp.csv"
	if strings.Join(names, ",") != want {
		t.Errorf("names = %v, want %v", names, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/waivers/ -run TestArchiveArtifacts -v`
Expected: FAIL — `undefined: ArchiveArtifacts`.

- [ ] **Step 3: Write minimal implementation**

```go
package waivers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// ArchiveArtifacts fetches all five Baseball Savant CSVs (raw bytes) for durable
// archival. The 14d/30d windows are rolling and roll off permanently upstream, so
// this is the only way to preserve them. Window math mirrors LoadSavant so the
// archived windows match what the waivers/claims path actually consumes.
func ArchiveArtifacts(ctx context.Context, date time.Time) ([]archive.Artifact, error) {
	year := date.Year()
	end := date.AddDate(0, 0, -1)
	start14 := end.AddDate(0, 0, -13)
	start30 := end.AddDate(0, 0, -29)
	df := func(t time.Time) string { return t.Format("2006-01-02") }

	specs := []struct {
		filename string
		url      string
	}{
		{"hitter-exp.csv", fmt.Sprintf(savantHitterExpURL, year)},
		{"hitter-statcast.csv", fmt.Sprintf(savantHitterSCURL, year)},
		{"hitter-exp-14d.csv", fmt.Sprintf(savantHitterExp14dURL, year, df(start14), df(end))},
		{"pitcher-exp.csv", fmt.Sprintf(savantPitcherExpURL, year)},
		{"pitcher-exp-30d.csv", fmt.Sprintf(savantPitcherExp30URL, year, df(start30), df(end))},
	}

	client := &http.Client{Timeout: savantHTTPTimeout}
	var arts []archive.Artifact
	for _, s := range specs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("savant archive %s: %w", s.filename, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("savant archive %s: status %d", s.filename, resp.StatusCode)
		}
		if readErr != nil {
			return nil, fmt.Errorf("savant archive %s read: %w", s.filename, readErr)
		}
		arts = append(arts, archive.Artifact{Filename: s.filename, Bytes: body})
	}
	return arts, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/waivers/ -run TestArchiveArtifacts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/waivers/archive.go internal/waivers/archive_test.go
git commit -m "feat(archive): Baseball Savant CSV source"
```

---

### Task 5: Prospects archive source (FanGraphs board)

**Files:**
- Create: `internal/prospects/archive.go`
- Test: `internal/prospects/archive_test.go`

**Interfaces:**
- Consumes: `archive.Artifact` (Task 1); existing package var `fgProspectURL` (format `...?draft=%dprospect&season=%d`).
- Produces: `func ArchiveArtifacts(ctx context.Context, date time.Time) ([]archive.Artifact, error)` — one artifact `fangraphs-board.json`, raw JSON bytes, using `season = date.Year()` for both `%d` verbs (matches `FanGraphsRankingSource.GetTopProspects`).

- [ ] **Step 1: Write the failing test**

```go
package prospects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestArchiveArtifactsReturnsBoardJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"prospect":true}]`))
	}))
	defer srv.Close()
	orig := fgProspectURL
	fgProspectURL = srv.URL + "?draft=%dprospect&season=%d"
	defer func() { fgProspectURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	if len(arts) != 1 || arts[0].Filename != "fangraphs-board.json" {
		t.Fatalf("got %+v, want one fangraphs-board.json", arts)
	}
	if !strings.HasPrefix(string(arts[0].Bytes), `[{"prospect"`) {
		t.Errorf("bytes = %q, want raw board JSON", arts[0].Bytes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prospects/ -run TestArchiveArtifacts -v`
Expected: FAIL — `undefined: ArchiveArtifacts`.

- [ ] **Step 3: Write minimal implementation**

```go
package prospects

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// ArchiveArtifacts fetches the raw FanGraphs prospect board JSON for durable
// archival (the source actually wired in run.go). season = date.Year(), matching
// FanGraphsRankingSource.GetTopProspects.
func ArchiveArtifacts(ctx context.Context, date time.Time) ([]archive.Artifact, error) {
	season := date.Year()
	url := fmt.Sprintf(fgProspectURL, season, season)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("prospects archive fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prospects archive: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("prospects archive read: %w", err)
	}
	return []archive.Artifact{{Filename: "fangraphs-board.json", Bytes: body}}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/prospects/ -run TestArchiveArtifacts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/prospects/archive.go internal/prospects/archive_test.go
git commit -m "feat(archive): FanGraphs prospect board source"
```

---

### Task 6: `archive` command

**Files:**
- Create: `cmd/archive.go`
- Test: `cmd/archive_test.go`

**Interfaces:**
- Consumes: `archive.Source`, `archive.Writer`, `archive.Artifact` (Task 1); `hkb.ArchiveArtifacts`, `projections.ArchiveArtifacts`, `waivers.ArchiveArtifacts`, `prospects.ArchiveArtifacts` (Tasks 2–5).
- Produces: a Cobra command `archive` registered on `rootCmd`; a pure helper `func runArchiveSources(ctx context.Context, sources []archive.Source, w archive.Writer, date time.Time, dryRun bool) error` that applies per-source isolation and the all-failed exit rule (tested with fakes).

- [ ] **Step 1: Write the failing test**

```go
package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

func TestRunArchiveSourcesIsolatesFailures(t *testing.T) {
	root := t.TempDir()
	good := archive.FuncSource{N: "good", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return []archive.Artifact{{Filename: "ok.json", Bytes: []byte("1")}}, nil
	}}
	bad := archive.FuncSource{N: "bad", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return nil, errors.New("boom")
	}}
	date := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	err := runArchiveSources(context.Background(), []archive.Source{good, bad}, archive.Writer{Root: root}, date, false)
	if err != nil {
		t.Fatalf("one failure should not fail the command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "good", "dt=2026-06-30", "ok.json")); err != nil {
		t.Errorf("good source should have written: %v", err)
	}
}

func TestRunArchiveSourcesAllFailedIsError(t *testing.T) {
	bad := archive.FuncSource{N: "bad", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return nil, errors.New("boom")
	}}
	err := runArchiveSources(context.Background(), []archive.Source{bad}, archive.Writer{Root: t.TempDir()},
		time.Now(), false)
	if err == nil {
		t.Fatal("all sources failing must return an error")
	}
}

func TestRunArchiveSourcesDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	good := archive.FuncSource{N: "good", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return []archive.Artifact{{Filename: "ok.json", Bytes: []byte("1")}}, nil
	}}
	if err := runArchiveSources(context.Background(), []archive.Source{good}, archive.Writer{Root: root},
		time.Now(), true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "good")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestRunArchiveSources -v`
Expected: FAIL — `undefined: runArchiveSources`.

- [ ] **Step 3: Write minimal implementation**

```go
package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/prospects"
	"github.com/nixon-commits/rosterbot/internal/waivers"
	"github.com/spf13/cobra"
)

var archiveDate string
var archiveDryRun bool

var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Capture a durable daily snapshot of ephemeral upstream data (HKB, projections, Savant, prospects)",
	RunE:  runArchive,
}

func init() {
	archiveCmd.Flags().StringVar(&archiveDate, "date", "", "capture date YYYY-MM-DD (default: today UTC)")
	archiveCmd.Flags().BoolVar(&archiveDryRun, "dry-run", false, "fetch and report sizes without writing")
	rootCmd.AddCommand(archiveCmd)
}

func runArchive(cmd *cobra.Command, args []string) error {
	date := time.Now().UTC()
	if archiveDate != "" {
		d, err := time.Parse("2006-01-02", archiveDate)
		if err != nil {
			return fmt.Errorf("bad --date: %w", err)
		}
		date = d
	}
	sources := []archive.Source{
		archive.FuncSource{N: "hkb", F: hkb.ArchiveArtifacts},
		archive.FuncSource{N: "projections", F: projections.ArchiveArtifacts},
		archive.FuncSource{N: "savant", F: waivers.ArchiveArtifacts},
		archive.FuncSource{N: "prospects", F: prospects.ArchiveArtifacts},
	}
	return runArchiveSources(context.Background(), sources, archive.Writer{Root: ".archive"}, date, archiveDryRun)
}

// runArchiveSources runs each source independently. A single source failure is
// logged and skipped; the command errors only when every source failed.
func runArchiveSources(ctx context.Context, sources []archive.Source, w archive.Writer, date time.Time, dryRun bool) error {
	var failed int
	for _, s := range sources {
		arts, err := s.Fetch(ctx, date)
		if err != nil {
			warn("archive %s: %v", s.Name(), err)
			failed++
			continue
		}
		if dryRun {
			var total int
			for _, a := range arts {
				total += len(a.Bytes)
			}
			fmt.Printf("archive %s (dry-run): %d artifact(s), %d bytes\n", s.Name(), len(arts), total)
			continue
		}
		if err := w.Write(date, s.Name(), arts); err != nil {
			warn("archive write %s: %v", s.Name(), err)
			failed++
		}
	}
	if failed == len(sources) {
		return fmt.Errorf("archive: all %d sources failed", len(sources))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/ -run TestRunArchiveSources -v`
Expected: PASS (all three).

- [ ] **Step 5: Verify the whole tree builds and vets**

Run: `go build ./... && go vet ./... && go test ./internal/... ./cmd/... 2>&1 | tail -20`
Expected: build clean, vet clean, tests PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/archive.go cmd/archive_test.go
git commit -m "feat(archive): archive command wiring the four sources"
```

---

### Task 7: S3 sync + `make run-all` wiring

**Files:**
- Modify: `cmd/sync.go` (the `statePairs` slice, around lines 22–29)
- Modify: `Makefile` (the `run-all` recipe)

**Interfaces:**
- Consumes: `statePairs` (Task context); the `archive` command (Task 6).
- Produces: `.archive/` ↔ S3 `archive/` sync (append-only, `del=false` — already how `Up` is called for `statePairs`).

- [ ] **Step 1: Add the archive dir↔prefix pair**

In `cmd/sync.go`, add to the `statePairs` slice:

```go
	{".backtest/", "backtest/"},
	{".archive/", "archive/"},
}
```

- [ ] **Step 2: Confirm append-only semantics**

Read `runSyncUp` in `cmd/sync.go` and confirm the `statePairs` loop calls `s.Up(ctx, bucket, p.prefix, p.dir, false)` — the `false` (no `--delete`) is what makes `archive/` keep-forever. No code change needed; this step is a verification.

Run: `go build ./cmd/...`
Expected: builds clean.

- [ ] **Step 3: Add archive to the smoke test**

In the `Makefile` `run-all` recipe, add a line alongside the other dry-run commands:

```make
	time go run . archive --dry-run
```

- [ ] **Step 4: Run the smoke target**

Run: `make run-all 2>&1 | grep -A2 archive`
Expected: the `archive --dry-run` step runs and prints per-source `(dry-run)` artifact/byte lines (real network fetch; if an upstream is flaky the per-source warn is acceptable as long as not all four fail).

- [ ] **Step 5: Commit**

```bash
git add cmd/sync.go Makefile
git commit -m "feat(archive): sync .archive/ to S3 archive/ prefix + run-all"
```

---

### Task 8: EventBridge schedule (CDK)

**Files:**
- Modify: the CDK stack under `infra/` that defines the other EventBridge schedules (grep for an existing schedule such as `grade` or `ProjectionSite` to find the file and pattern).

**Interfaces:**
- Consumes: the same Fargate task-definition / schedule pattern the existing commands use; the `schedulesEnabled` gate.
- Produces: a daily rule that runs the container with command `archive`.

- [ ] **Step 1: Locate the schedule pattern**

Run: `grep -rn "ProjectionSite\|cron(0 15\|schedulesEnabled\|EventBridge\|ScheduleExpression" infra/ | head -20`
Expected: identifies the file and the helper that registers one command on a cron.

- [ ] **Step 2: Add the archive schedule**

Following the exact pattern of the neighboring schedule (same task def, subnet, `schedulesEnabled` gate), register a new rule:
- Command: `["archive"]`
- Cron: `cron(0 14 * * ? *)` (14:00 UTC daily — after FanGraphs/Savant post their once-daily refresh)
- Name/description: `Archive` / "Daily ephemeral-data archive (HKB, projections, Savant, prospects)"

Copy the neighboring schedule block verbatim and change only the name, command, cron, and description. Do not invent new construct types.

- [ ] **Step 3: Synth to verify**

Run (from `infra/`): `cdk synth 2>&1 | grep -iA3 "archive"` (or `go build ./...` in `infra/` if synth needs credentials)
Expected: the new rule appears in the synthesized template (or the stack compiles).

- [ ] **Step 4: Commit**

```bash
git add infra/
git commit -m "feat(archive): daily EventBridge schedule for archive command"
```

Note: deploy is out of scope for this plan and is done separately with `cdk deploy -c enableBuild=true` (see memory: infra-enablebuild-deploy-flag).

---

### Task 9: Docs (README + CLAUDE.md)

**Files:**
- Modify: `README.md` (commands section)
- Modify: `CLAUDE.md` (Commands block + a short architecture note; the caching section's mutability discussion)

**Interfaces:** none (documentation only).

- [ ] **Step 1: Document the command in README**

Add to the README commands list:

```
go run . archive --dry-run           # capture today's ephemeral data (HKB, projections, Savant, prospects); --dry-run prints sizes
go run . archive --date 2026-06-30   # capture a specific date
```

- [ ] **Step 2: Document in CLAUDE.md**

- Add the `archive` lines to the `## Commands` code block.
- Add a short bullet under architecture describing `internal/archive` (leaf package; `Artifact`/`Source`/`Writer`; raw upstream bytes; `.archive/<source>/dt=YYYY-MM-DD/`; synced to S3 `archive/` append-only; distinct from the TTL cache which overwrites, and additive to the projection snapshot store).
- Note the new S3 `archive/` prefix alongside the `cache/`/`session/`/`claims/`/`backtest/` list.

- [ ] **Step 3: Verify no stale references**

Run: `grep -rn "archive" README.md CLAUDE.md | head`
Expected: new entries present, consistent with the command's actual flags.

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs(archive): document archive command, package, and S3 prefix"
```

---

## Self-Review

**Spec coverage:**
- HKB, full projections (4 RoS systems), Savant, prospects → Tasks 2, 3, 4, 5. ✓
- Raw blobs, `dt=YYYY-MM-DD` layout → Task 1 Writer. ✓
- Dedicated command + `--date`/`--dry-run` → Task 6. ✓
- Per-source isolation + all-failed exit → Task 6 helper + tests. ✓
- `.archive/` → S3 `archive/`, append-only, keep-forever → Task 7. ✓
- EventBridge daily schedule (~14:00 UTC), `schedulesEnabled` gate → Task 8. ✓
- `make run-all` line → Task 7. ✓
- README + CLAUDE.md → Task 9. ✓
- Hermetic tests via URL-var override → every source task. ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code. Task 8 references neighboring CDK constructs by grep rather than inventing types (correct — the infra pattern must be followed, not guessed). ✓

**Type consistency:** `ArchiveArtifacts(ctx, date)` signature is identical across Tasks 2–5 and matches `archive.FuncSource.F`'s `func(context.Context, time.Time) ([]archive.Artifact, error)` and the `Source.Fetch` interface in Task 1. `Writer.Write(date, source, arts)` and `Artifact{Filename, Bytes}` are used consistently in Tasks 1, 6, 7. ✓
