# State Store Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the 10 hand-rolled `STATE_BUCKET` store-selection branches into one `internal/statestore` module, and remove `internal/lineuprun`'s direct `os.Getenv` read.

**Architecture:** A new composition-root package `internal/statestore` owns the single `os.Getenv("STATE_BUCKET")` read, a one-place artifact→(s3Prefix, localDir) layout table, and a single generic `pick[T]` branch. It exposes one typed constructor per durable artifact. `cmd` calls these; `internal/lineuprun` instead receives its `lineupapi.Publisher` via `Options`, dropping the AWS SDK from the orchestration package.

**Tech Stack:** Go 1.x (generics), aws-sdk-go-v2 (already vendored), cobra, existing `cache`/`analysis`/`teamvalue`/`lineupapi` packages and their `s3store`/`s3ndjson`/`s3lineup` adapters.

## Global Constraints

- No behavior change: a local run and a `STATE_BUCKET` run must write to the exact same locations as before. The layout table reproduces every existing prefix/dir verbatim.
- `os.Getenv("STATE_BUCKET")` must appear **exactly once** in the codebase after this work (in `statestore.Bucket`). Out of scope: `lambda/main.go` (separate Go module) and `infra/infra.go` (CDK sets the var).
- `internal/statestore` is imported only by `cmd`. No leaf package may import it back (would risk a cycle / pull the AWS SDK into a leaf).
- `internal/lineuprun` must not read `os.Getenv` and must not import `internal/lineupapi/s3lineup` after this work.
- Follow existing patterns; run `go vet ./...` and `go mod tidy` after code changes (gofmt/vet also run via PostToolUse hooks). `lambda/`, `buildnotify/`, `infra/` are separate modules — `make build-modules` guards them.

**Exact layout table** (S3 prefix ↔ local dir), copied verbatim from the spec:

| Artifact var | s3Prefix | localDir |
|---|---|---|
| `cacheArtifact` | `cache/` | `` (default fsStore) |
| `analysisArtifact` | `analysis/` | `.analysis` |
| `teamValueArtifact` | `analysis/team-values/` | `.teamvalue` |
| `runLedgerArtifact` | `runledger/` | `.lineup/runs` |
| `runOutputArtifact` | `runs/` | `.lineup/outputs` |
| `notificationArtifact` | `notifications/` | `.lineup/notifications` |
| `progressArtifact` | `runs/` | `.lineup/progress` |
| `lineupArtifact` | `lineup/` | `.lineup` |

---

### Task 1: Create the `internal/statestore` package

**Files:**
- Create: `internal/statestore/statestore.go`
- Test: `internal/statestore/statestore_test.go`

**Interfaces:**
- Consumes (existing, verified signatures):
  - `s3store.New(ctx, bucket, prefix) (*s3store.Store, error)` → `cache.Store`
  - `s3ndjson.New(ctx, bucket, prefix) (*s3ndjson.Store, error)` → `ndjsonstore.Store`
  - `analysis.NewWriter(ndjsonstore.Store) analysis.Writer`, `analysis.NewFileWriter(dir) analysis.Writer`
  - `analysis.NewReader(ndjsonstore.Store) analysis.Reader`, `analysis.NewFileReader(dir) analysis.Reader`
  - `teamvalue.NewWriter/NewReader/NewFileWriter/NewFileReader` (same shapes)
  - `s3lineup.NewRuns(ctx, b, p) (*RunsStore, error)`, `s3lineup.NewOutput`, `s3lineup.NewNotifications`, `s3lineup.NewProgress`, `s3lineup.New` (all `(…, error)`)
  - `lineupapi.NewFileRunStore(dir) *FileRunStore`, `NewFileOutputStore(dir)`, `NewFileNotificationStore(dir)`, `NewFileProgressStore(dir)`, `NewFileStore(dir) *FileStore`
  - `lineupapi.OutputWriter`, `NotificationWriter`, `ProgressWriter`, `Publisher`, `RunDetail`
- Produces (later tasks rely on these exact names):
  - `statestore.Bucket() string`
  - `statestore.FromEnv() *Selector`, `statestore.New(bucket string) *Selector`
  - `statestore.RunWriter` interface: `PutRun(context.Context, lineupapi.RunDetail) error`
  - Methods on `*Selector`: `CacheStore() (cache.Store, error)`, `AnalysisWriter() (analysis.Writer, error)`, `AnalysisReader() (analysis.Reader, error)`, `TeamValueWriter() (teamvalue.Writer, error)`, `TeamValueReader() (teamvalue.Reader, error)`, `RunLedger() (RunWriter, error)`, `Output() (lineupapi.OutputWriter, error)`, `Notifications() (lineupapi.NotificationWriter, error)`, `Progress() (lineupapi.ProgressWriter, error)`, `LineupPublisher() (lineupapi.Publisher, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/statestore/statestore_test.go`:

```go
package statestore

import (
	"context"
	"testing"
)

func TestArtifactLayout(t *testing.T) {
	cases := []struct {
		name         string
		a            artifact
		wantPrefix   string
		wantLocalDir string
	}{
		{"cache", cacheArtifact, "cache/", ""},
		{"analysis", analysisArtifact, "analysis/", ".analysis"},
		{"teamValue", teamValueArtifact, "analysis/team-values/", ".teamvalue"},
		{"runLedger", runLedgerArtifact, "runledger/", ".lineup/runs"},
		{"runOutput", runOutputArtifact, "runs/", ".lineup/outputs"},
		{"notification", notificationArtifact, "notifications/", ".lineup/notifications"},
		{"progress", progressArtifact, "runs/", ".lineup/progress"},
		{"lineup", lineupArtifact, "lineup/", ".lineup"},
	}
	for _, tc := range cases {
		if tc.a.s3Prefix != tc.wantPrefix {
			t.Errorf("%s: s3Prefix = %q, want %q", tc.name, tc.a.s3Prefix, tc.wantPrefix)
		}
		if tc.a.localDir != tc.wantLocalDir {
			t.Errorf("%s: localDir = %q, want %q", tc.name, tc.a.localDir, tc.wantLocalDir)
		}
	}
}

func TestPickRoutesToLocalWhenBucketEmpty(t *testing.T) {
	got, err := pick(New(""), artifact{"p/", "dir"},
		func(_ context.Context, b, p string) (string, error) { return "s3:" + b + "/" + p, nil },
		func(dir string) string { return "file:" + dir })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "file:dir" {
		t.Errorf("pick(bucket=\"\") = %q, want file:dir", got)
	}
}

func TestPickRoutesToS3WhenBucketSet(t *testing.T) {
	got, err := pick(New("mybucket"), artifact{"p/", "dir"},
		func(_ context.Context, b, p string) (string, error) { return "s3:" + b + "/" + p, nil },
		func(dir string) string { return "file:" + dir })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "s3:mybucket/p/" {
		t.Errorf("pick(bucket set) = %q, want s3:mybucket/p/", got)
	}
}

func TestLocalConstructors(t *testing.T) {
	s := New("")
	if cs, err := s.CacheStore(); err != nil || cs != nil {
		t.Errorf("CacheStore() local = (%v, %v), want (nil, nil)", cs, err)
	}
	if v, err := s.AnalysisWriter(); err != nil || v == nil {
		t.Errorf("AnalysisWriter() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.AnalysisReader(); err != nil || v == nil {
		t.Errorf("AnalysisReader() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.TeamValueWriter(); err != nil || v == nil {
		t.Errorf("TeamValueWriter() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.TeamValueReader(); err != nil || v == nil {
		t.Errorf("TeamValueReader() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.RunLedger(); err != nil || v == nil {
		t.Errorf("RunLedger() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.Output(); err != nil || v == nil {
		t.Errorf("Output() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.Notifications(); err != nil || v == nil {
		t.Errorf("Notifications() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.Progress(); err != nil || v == nil {
		t.Errorf("Progress() local = (%v, %v), want (non-nil, nil)", v, err)
	}
	if v, err := s.LineupPublisher(); err != nil || v == nil {
		t.Errorf("LineupPublisher() local = (%v, %v), want (non-nil, nil)", v, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/statestore/...`
Expected: FAIL — build error, `statestore.go` does not exist (undefined `artifact`, `pick`, `New`, etc.).

- [ ] **Step 3: Write the implementation**

Create `internal/statestore/statestore.go`:

```go
// Package statestore is the composition root's single owner of the durable-state
// backend choice: S3 when STATE_BUCKET is set (Fargate), else a local directory.
// It centralizes the one os.Getenv("STATE_BUCKET") read, the prefix/local-dir
// layout for every durable artifact, and the sole S3-vs-local branch (pick).
//
// Only cmd imports this package; no leaf imports it back, so wiring the AWS SDK
// in here (via the three s3 adapters) creates no cycle and keeps the SDK out of
// leaves like internal/lineuprun, which receives its Publisher from cmd instead.
package statestore

import (
	"context"
	"os"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/cachestore/s3store"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore/s3ndjson"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
)

// artifact is the durable-state layout for one kind of data: its S3 key prefix
// (under STATE_BUCKET) and its local-filesystem directory. This table is the
// single declaration of the prefix layout.
type artifact struct{ s3Prefix, localDir string }

var (
	cacheArtifact        = artifact{"cache/", ""} // local: default fsStore, no dir override
	analysisArtifact     = artifact{"analysis/", ".analysis"}
	teamValueArtifact    = artifact{"analysis/team-values/", ".teamvalue"}
	runLedgerArtifact    = artifact{"runledger/", ".lineup/runs"}
	runOutputArtifact    = artifact{"runs/", ".lineup/outputs"}
	notificationArtifact = artifact{"notifications/", ".lineup/notifications"}
	progressArtifact     = artifact{"runs/", ".lineup/progress"}
	lineupArtifact       = artifact{"lineup/", ".lineup"}
)

// Bucket is the single os.Getenv("STATE_BUCKET") read in the codebase. Empty
// means local-filesystem mode.
func Bucket() string { return os.Getenv("STATE_BUCKET") }

// Selector resolves durable-state stores against one backend choice made once.
type Selector struct{ bucket string }

// FromEnv builds a Selector from STATE_BUCKET (the deployment's signal).
func FromEnv() *Selector { return &Selector{bucket: Bucket()} }

// New builds a Selector for an explicit bucket ("" = local). Used by tests and
// by any caller that already knows the bucket.
func New(bucket string) *Selector { return &Selector{bucket: bucket} }

// pick is the sole S3-vs-local branch. On S3 it calls s3New(ctx, bucket, prefix);
// locally it calls fileNew(localDir).
func pick[T any](s *Selector, a artifact,
	s3New func(ctx context.Context, bucket, prefix string) (T, error),
	fileNew func(dir string) T) (T, error) {
	if s.bucket != "" {
		return s3New(context.Background(), s.bucket, a.s3Prefix)
	}
	return fileNew(a.localDir), nil
}

// RunWriter is the write side of the run ledger, satisfied by both the S3 and
// local-file run stores. It lives here so RunLedger returns one type for either
// backend (replacing cmd/ledger.go's local runWriter interface).
type RunWriter interface {
	PutRun(context.Context, lineupapi.RunDetail) error
}

// CacheStore returns the S3-backed cache.Store on Fargate, or nil locally (the
// caller keeps the default fsStore — SetDefaultStore is skipped).
func (s *Selector) CacheStore() (cache.Store, error) {
	return pick(s, cacheArtifact,
		func(ctx context.Context, b, p string) (cache.Store, error) { return s3store.New(ctx, b, p) },
		func(string) cache.Store { return nil })
}

func (s *Selector) AnalysisWriter() (analysis.Writer, error) {
	return pick(s, analysisArtifact,
		func(ctx context.Context, b, p string) (analysis.Writer, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return analysis.NewWriter(st), nil
		},
		func(dir string) analysis.Writer { return analysis.NewFileWriter(dir) })
}

func (s *Selector) AnalysisReader() (analysis.Reader, error) {
	return pick(s, analysisArtifact,
		func(ctx context.Context, b, p string) (analysis.Reader, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return analysis.NewReader(st), nil
		},
		func(dir string) analysis.Reader { return analysis.NewFileReader(dir) })
}

func (s *Selector) TeamValueWriter() (teamvalue.Writer, error) {
	return pick(s, teamValueArtifact,
		func(ctx context.Context, b, p string) (teamvalue.Writer, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return teamvalue.NewWriter(st), nil
		},
		func(dir string) teamvalue.Writer { return teamvalue.NewFileWriter(dir) })
}

func (s *Selector) TeamValueReader() (teamvalue.Reader, error) {
	return pick(s, teamValueArtifact,
		func(ctx context.Context, b, p string) (teamvalue.Reader, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return teamvalue.NewReader(st), nil
		},
		func(dir string) teamvalue.Reader { return teamvalue.NewFileReader(dir) })
}

func (s *Selector) RunLedger() (RunWriter, error) {
	return pick(s, runLedgerArtifact,
		func(ctx context.Context, b, p string) (RunWriter, error) { return s3lineup.NewRuns(ctx, b, p) },
		func(dir string) RunWriter { return lineupapi.NewFileRunStore(dir) })
}

func (s *Selector) Output() (lineupapi.OutputWriter, error) {
	return pick(s, runOutputArtifact,
		func(ctx context.Context, b, p string) (lineupapi.OutputWriter, error) { return s3lineup.NewOutput(ctx, b, p) },
		func(dir string) lineupapi.OutputWriter { return lineupapi.NewFileOutputStore(dir) })
}

func (s *Selector) Notifications() (lineupapi.NotificationWriter, error) {
	return pick(s, notificationArtifact,
		func(ctx context.Context, b, p string) (lineupapi.NotificationWriter, error) { return s3lineup.NewNotifications(ctx, b, p) },
		func(dir string) lineupapi.NotificationWriter { return lineupapi.NewFileNotificationStore(dir) })
}

func (s *Selector) Progress() (lineupapi.ProgressWriter, error) {
	return pick(s, progressArtifact,
		func(ctx context.Context, b, p string) (lineupapi.ProgressWriter, error) { return s3lineup.NewProgress(ctx, b, p) },
		func(dir string) lineupapi.ProgressWriter { return lineupapi.NewFileProgressStore(dir) })
}

func (s *Selector) LineupPublisher() (lineupapi.Publisher, error) {
	return pick(s, lineupArtifact,
		func(ctx context.Context, b, p string) (lineupapi.Publisher, error) { return s3lineup.New(ctx, b, p) },
		func(dir string) lineupapi.Publisher { return lineupapi.NewFileStore(dir) })
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/statestore/... && go vet ./internal/statestore/...`
Expected: PASS (all sub-tests), no vet errors.

- [ ] **Step 5: Commit**

```bash
git add internal/statestore/statestore.go internal/statestore/statestore_test.go
git commit -m "feat: add internal/statestore state-backend selector (rosterbot-9s6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018mY8YoKun45RH29QZHFtbW"
```

---

### Task 2: Rewire the cache + ndjson call sites

**Files:**
- Modify: `cmd/root.go` (cache selection, ~lines 91-103)
- Modify: `cmd/grade.go` (~lines 129-138)
- Modify: `cmd/team-values.go` (~lines 99-110)
- Modify: `cmd/projection-site.go` (~lines 43-52 and ~lines 104-113)

**Interfaces:**
- Consumes: `statestore.FromEnv().CacheStore()`, `.AnalysisWriter()`, `.AnalysisReader()`, `.TeamValueWriter()`, `.TeamValueReader()` (from Task 1).
- Produces: nothing new; these are leaf call sites.

- [ ] **Step 1: Rewire `cmd/root.go` cache selection**

Replace the block (currently `cmd/root.go:92-101`):

```go
		// On Fargate, back the Cache with S3 directly (per-key) instead of
		// local files, so no bulk .cache sync is needed. STATE_BUCKET is the
		// task's state bucket; cache entries live under the cache/ prefix.
		if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
			st, err := s3store.New(context.Background(), bucket, "cache/")
			if err != nil {
				return nil, nil, fmt.Errorf("init s3 cache store: %w", err)
			}
			cache.SetDefaultStore(st)
		}
```

with:

```go
		// On Fargate, back the Cache with S3 directly (per-key) instead of
		// local files, so no bulk .cache sync is needed. statestore owns the
		// STATE_BUCKET decision + the cache/ prefix; nil means local (keep the
		// default fsStore).
		st, err := statestore.FromEnv().CacheStore()
		if err != nil {
			return nil, nil, fmt.Errorf("init cache store: %w", err)
		}
		if st != nil {
			cache.SetDefaultStore(st)
		}
```

Then fix imports in `cmd/root.go`: add `"github.com/nixon-commits/rosterbot/internal/statestore"`; remove `"github.com/nixon-commits/rosterbot/internal/cachestore/s3store"` **if now unused** and remove `"context"` **only if now unused** (verify with the next step — root.go may use `context`/`os` elsewhere; keep whatever is still referenced). Keep `internal/cache` (still used for `SetDefaultStore` and `Notify`).

- [ ] **Step 2: Rewire `cmd/grade.go`**

Replace (currently `cmd/grade.go:129-138`):

```go
	var w analysis.Writer
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		store, err := s3ndjson.New(context.Background(), bucket, "analysis/")
		if err != nil {
			return fmt.Errorf("init analysis store: %w", err)
		}
		w = analysis.NewWriter(store)
	} else {
		w = analysis.NewFileWriter(".analysis")
	}
```

with:

```go
	w, err := statestore.FromEnv().AnalysisWriter()
	if err != nil {
		return fmt.Errorf("init analysis store: %w", err)
	}
```

Fix imports: add `statestore`; drop `s3ndjson` and `context`/`os` **if now unused** in grade.go (verify). Keep `analysis` (still referenced for `analysis.GradeRow`, etc.).

- [ ] **Step 3: Rewire `cmd/team-values.go`**

Replace the body of `teamValueWriter` (currently `cmd/team-values.go:101-110`):

```go
func teamValueWriter(ctx context.Context) (teamvalue.Writer, string, error) {
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		store, err := s3ndjson.New(ctx, bucket, teamValuePrefix)
		if err != nil {
			return nil, "", fmt.Errorf("init s3 team-value writer: %w", err)
		}
		return teamvalue.NewWriter(store), "s3://" + bucket + "/" + teamValuePrefix, nil
	}
	return teamvalue.NewFileWriter(teamValueLocalDir), teamValueLocalDir, nil
}
```

with:

```go
func teamValueWriter(ctx context.Context) (teamvalue.Writer, string, error) {
	w, err := statestore.FromEnv().TeamValueWriter()
	if err != nil {
		return nil, "", fmt.Errorf("init team-value writer: %w", err)
	}
	dest := teamValueLocalDir
	if b := statestore.Bucket(); b != "" {
		dest = "s3://" + b + "/" + teamValuePrefix
	}
	return w, dest, nil
}
```

Note: `dest` is a human-readable log string ("Wrote N team rows to %s"), not a store path, so it may legitimately reference `statestore.Bucket()`/`teamValuePrefix` for the message. `ctx` is now unused in this function — drop the parameter and update the one caller (search `teamValueWriter(` in team-values.go and remove the `ctx` argument), or keep the parameter and add `_ = ctx`; prefer dropping it. Fix imports: add `statestore`; drop `s3ndjson` if unused; keep `teamvalue`, `teamValuePrefix`, `teamValueLocalDir`.

- [ ] **Step 4: Rewire `cmd/projection-site.go` (both readers)**

Replace the analysis-reader block (currently `cmd/projection-site.go:43-52`):

```go
	var reader analysis.Reader
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		store, err := s3ndjson.New(context.Background(), bucket, "analysis/")
		if err != nil {
			return fmt.Errorf("init analysis reader: %w", err)
		}
		reader = analysis.NewReader(store)
	} else {
		reader = analysis.NewFileReader(".analysis")
	}
```

with:

```go
	reader, err := statestore.FromEnv().AnalysisReader()
	if err != nil {
		return fmt.Errorf("init analysis reader: %w", err)
	}
```

Replace the team-value-reader block (currently `cmd/projection-site.go:104-113`):

```go
	var reader teamvalue.Reader
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		store, err := s3ndjson.New(context.Background(), bucket, teamValuePrefix)
		if err != nil {
			return fmt.Errorf("init team-value reader: %w", err)
		}
		reader = teamvalue.NewReader(store)
	} else {
		reader = teamvalue.NewFileReader(teamValueLocalDir)
	}
```

with:

```go
	reader, err := statestore.FromEnv().TeamValueReader()
	if err != nil {
		return fmt.Errorf("init team-value reader: %w", err)
	}
```

Note: the second block is inside `renderValueSite`, which already has an `err` in scope later (`rows, err := reader.ReadAll()`). Using `reader, err :=` introduces `err`; the subsequent `rows, err :=` must then become `rows, err =` if `reader` and `rows` share scope — verify and adjust `:=`/`=` so it compiles (Go requires at least one new var on the left of `:=`). Fix imports: add `statestore`; drop `s3ndjson` if now unused; keep `analysis`, `teamvalue`, `report`.

- [ ] **Step 5: Build and run existing tests**

Run: `go build ./... && go vet ./... && go test ./cmd/... ./internal/...`
Expected: builds clean, vet clean, all existing tests PASS. If any import is reported unused, remove it; if `os`/`context` are still used elsewhere in a file, keep them.

- [ ] **Step 6: Commit**

```bash
git add cmd/root.go cmd/grade.go cmd/team-values.go cmd/projection-site.go
git commit -m "refactor: route cache + ndjson stores through statestore (rosterbot-9s6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018mY8YoKun45RH29QZHFtbW"
```

---

### Task 3: Rewire the lineupapi recorder + ledger call sites

**Files:**
- Modify: `cmd/ledger.go` (~lines 14-17 remove local `runWriter`; ~lines 77-86 selection)
- Modify: `cmd/output.go` (~lines 21-30)
- Modify: `cmd/notifications.go` (~lines 19-28)
- Modify: `cmd/progress.go` (~lines 23-32)

**Interfaces:**
- Consumes: `statestore.FromEnv().RunLedger()`, `.Output()`, `.Notifications()`, `.Progress()`; type `statestore.RunWriter` (from Task 1).
- Produces: nothing new.

- [ ] **Step 1: Rewire `cmd/ledger.go`**

Remove the local interface declaration (currently `cmd/ledger.go:13-17`):

```go
// runWriter is the write side of the run ledger, satisfied by both the S3 and
// local-file run stores.
type runWriter interface {
	PutRun(context.Context, lineupapi.RunDetail) error
}
```

Replace the selection block (currently `cmd/ledger.go:77-86`):

```go
	var w runWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewRuns(context.Background(), bucket, "runledger/")
		if err != nil {
			return err
		}
		w = s
	} else {
		w = lineupapi.NewFileRunStore(".lineup/runs")
	}
	return w.PutRun(context.Background(), rec)
```

with:

```go
	w, err := statestore.FromEnv().RunLedger()
	if err != nil {
		return err
	}
	return w.PutRun(context.Background(), rec)
```

Fix imports: add `statestore`; drop `s3lineup` if now unused; keep `lineupapi` only if still referenced elsewhere in ledger.go (e.g. `lineupapi.Run`, `lineupapi.RunDetail`) — verify and keep/drop accordingly; keep `context`, `os` only if still used.

- [ ] **Step 2: Rewire `cmd/output.go`**

Replace (currently `cmd/output.go:21-30`):

```go
	var w lineupapi.OutputWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewOutput(context.Background(), bucket, "runs/")
		if err != nil {
			return
		}
		w = s
	} else {
		w = lineupapi.NewFileOutputStore(".lineup/outputs")
	}
```

with:

```go
	w, err := statestore.FromEnv().Output()
	if err != nil {
		return
	}
```

Fix imports: add `statestore`; drop `s3lineup` if unused; keep `lineupapi` (still used for `RecordOutput`, `OutputRecorder`, `MarshalOutput`); keep `os` (still reads `RUN_ID`); drop `context` if unused (note `w.PutOutput(context.Background(), …)` still uses it — keep `context`).

- [ ] **Step 3: Rewire `cmd/notifications.go`**

Replace (currently `cmd/notifications.go:19-28`):

```go
	var w lineupapi.NotificationWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewNotifications(context.Background(), bucket, "notifications/")
		if err != nil {
			return
		}
		w = s
	} else {
		w = lineupapi.NewFileNotificationStore(".lineup/notifications")
	}
```

with:

```go
	w, err := statestore.FromEnv().Notifications()
	if err != nil {
		return
	}
```

Fix imports: add `statestore`; drop `s3lineup` if unused; keep `lineupapi`, `notify`, `fmt`, `time`, `os` (all still used), and `context` (used in `PutNotification(context.Background(), …)`).

- [ ] **Step 4: Rewire `cmd/progress.go`**

Replace (currently `cmd/progress.go:23-32`):

```go
	var w lineupapi.ProgressWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewProgress(context.Background(), bucket, "runs/")
		if err != nil {
			return
		}
		w = s
	} else {
		w = lineupapi.NewFileProgressStore(".lineup/progress")
	}
```

with:

```go
	w, err := statestore.FromEnv().Progress()
	if err != nil {
		return
	}
```

Fix imports: add `statestore`; drop `s3lineup` if unused; keep `lineupapi`, `progress`, `encoding/json`, `os`, and `context` (used in `PutProgress`).

- [ ] **Step 5: Build and run existing tests**

Run: `go build ./... && go vet ./... && go test ./cmd/... ./internal/...`
Expected: builds clean, vet clean, tests PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/ledger.go cmd/output.go cmd/notifications.go cmd/progress.go
git commit -m "refactor: route ledger + recorder stores through statestore (rosterbot-9s6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018mY8YoKun45RH29QZHFtbW"
```

---

### Task 4: Inject the lineup Publisher into lineuprun (remove its os.Getenv)

**Files:**
- Modify: `internal/lineuprun/lineuprun.go` (Options struct ~lines 84-94; publish call site ~lines 954-964)
- Modify: `internal/lineuprun/publish.go` (whole file)
- Modify: `cmd/optimize.go` (Options build ~lines 92-104)
- Test: `internal/lineuprun/publish_test.go` (new)

**Interfaces:**
- Consumes: `statestore.FromEnv().LineupPublisher()` (Task 1); `lineupapi.Publisher` (`Publish(key string, data []byte) error`).
- Produces: `lineuprun.Options.Publisher lineupapi.Publisher`; `publishLineup(dr dateResult, cfg *config.Config, hitterSlots, pitcherSlots []fantrax.Slot, pub lineupapi.Publisher) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/lineuprun/publish_test.go`:

```go
package lineuprun

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// memPub is an in-memory lineupapi.Publisher capturing published payloads.
type memPub struct{ m map[string][]byte }

func (p *memPub) Publish(key string, data []byte) error {
	if p.m == nil {
		p.m = map[string][]byte{}
	}
	p.m[key] = data
	return nil
}

func TestPublishLineupWritesTodayAndDateKeys(t *testing.T) {
	pub := &memPub{}
	dr := dateResult{
		date:         time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
		benchedToday: map[string]bool{},
	}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}

	if err := publishLineup(dr, cfg, nil, nil, pub); err != nil {
		t.Fatalf("publishLineup: %v", err)
	}

	if _, ok := pub.m[lineupapi.TodayKey]; !ok {
		t.Errorf("missing %q key; got keys %v", lineupapi.TodayKey, keysOf(pub.m))
	}
	if _, ok := pub.m["2026-07-24"]; !ok {
		t.Errorf("missing date key; got keys %v", keysOf(pub.m))
	}
}

func TestPublishLineupNilPublisherIsNoOp(t *testing.T) {
	dr := dateResult{date: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC), benchedToday: map[string]bool{}}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}
	if err := publishLineup(dr, cfg, nil, nil, nil); err != nil {
		t.Fatalf("nil publisher should be a no-op, got: %v", err)
	}
}

func keysOf(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
```

Note: verify `config.Config` has exported `LeagueID`/`TeamID` string fields (it does — used throughout cmd). If the struct requires more, set only what `lineupapi.Build` reads (`Date`, `LeagueID`, `TeamID`, slices, `BenchedToday`, `DataWarnings`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lineuprun/ -run TestPublishLineup -v`
Expected: FAIL — compile error: `publishLineup` takes 4 args, not 5 (`pub` param does not exist yet).

- [ ] **Step 3: Rewrite `internal/lineuprun/publish.go`**

Replace the whole file with:

```go
package lineuprun

import (
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// publishLineup serializes today's optimized lineup into the read-only API's
// wire shape and writes it via pub — the caller supplies the destination
// (S3 or local), selected in cmd through internal/statestore. A nil pub is a
// no-op (the caller chose not to publish). It publishes under both "today"
// (the alias the endpoint serves) and the date string.
func publishLineup(dr dateResult, cfg *config.Config, hitterSlots, pitcherSlots []fantrax.Slot, pub lineupapi.Publisher) error {
	if pub == nil {
		return nil
	}
	resp := lineupapi.Build(lineupapi.Inputs{
		Date:         dr.date.Format("2006-01-02"),
		LeagueID:     cfg.LeagueID,
		TeamID:       cfg.TeamID,
		HitterSlots:  hitterSlots,
		PitcherSlots: pitcherSlots,
		Hitters:      dr.hitterResult.Scored,
		Pitchers:     dr.pitcherResult.Scored,
		BenchedToday: dr.benchedToday,
		DataWarnings: dr.warnings,
	})
	data, err := lineupapi.Marshal(resp)
	if err != nil {
		return err
	}
	if err := pub.Publish(lineupapi.TodayKey, data); err != nil {
		return err
	}
	return pub.Publish(dr.date.Format("2006-01-02"), data)
}
```

This drops the `context`, `os`, and `s3lineup` imports — the AWS SDK leaves `internal/lineuprun`.

- [ ] **Step 4: Add `Publisher` to `Options` and thread it at the call site**

In `internal/lineuprun/lineuprun.go`, add to the `Options` struct (after the `PublishLineupFlag` field, ~line 86):

```go
	// Publisher is the destination for the read-only API lineup JSON, selected
	// by the caller (cmd, via internal/statestore) so this package no longer
	// reads STATE_BUCKET. Nil means "do not publish" (the shadow command).
	Publisher lineupapi.Publisher
```

Confirm `lineuprun.go` already imports `internal/lineupapi` (it does, for `lineupapi.Build`). Then update the publish call site (currently `internal/lineuprun/lineuprun.go:959`):

```go
			if err := publishLineup(dr, cfg, hitterSlots, pitcherSlots); err != nil {
```

to:

```go
			if err := publishLineup(dr, cfg, hitterSlots, pitcherSlots, opts.Publisher); err != nil {
```

- [ ] **Step 5: Set `opts.Publisher` in `cmd/optimize.go`**

In `cmd/optimize.go`, before building `opts` (before `opts := lineuprun.Options{` at ~line 92), add:

```go
	pub, err := statestore.FromEnv().LineupPublisher()
	if err != nil {
		return fmt.Errorf("init lineup publisher: %w", err)
	}
```

Then add `Publisher: pub,` to the `lineuprun.Options{…}` literal. Add the `statestore` import. Note: `shadow` (`cmd/shadow.go`) leaves `Publisher` unset (nil) — it never publishes (always dry-run, `PublishLineupFlag` false), so no change needed there; the nil-guard in `publishLineup` covers it.

Guard against a redeclared `err`: `cmd/optimize.go` already has `err` in scope (e.g. `_, err = lineuprun.Run(...)`). Use `pub, err := …` only if `pub` is new (it is) — that is valid. Ensure the earlier `err` usages still compile (they use `=` where appropriate); verify build.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/lineuprun/ -run TestPublishLineup -v && go build ./... && go vet ./...`
Expected: both `TestPublishLineup*` PASS; build + vet clean.

- [ ] **Step 7: Verify the AWS SDK left lineuprun**

Run: `go list -deps ./internal/lineuprun | grep -c 'aws-sdk-go-v2/service/s3'`
Expected: `0` (no S3 SDK in lineuprun's dependency graph). Also run `go test ./internal/lineuprun/...` and expect all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/lineuprun/publish.go internal/lineuprun/lineuprun.go internal/lineuprun/publish_test.go cmd/optimize.go
git commit -m "refactor: inject lineup Publisher into lineuprun via Options (rosterbot-9s6)

Removes internal/lineuprun's os.Getenv(STATE_BUCKET) read and its
s3lineup import; cmd now selects the publisher through statestore and
passes it in. Adds the first unit test for the publish path (in-memory
Publisher).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018mY8YoKun45RH29QZHFtbW"
```

---

### Task 5: Route the two variant sites through `statestore.Bucket()`

**Files:**
- Modify: `cmd/sync.go` (~lines 53, 77)
- Modify: `cmd/migrate_run_ledger.go` (~line 42)

**Interfaces:**
- Consumes: `statestore.Bucket()` (Task 1).
- Produces: nothing new.

- [ ] **Step 1: Rewire `cmd/sync.go`**

In `runSyncDown`, replace (currently `cmd/sync.go:53`):

```go
	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		return nil
	}
```

with:

```go
	// Bulk dir<->prefix sync (statePairs), not a per-key store — intentionally
	// distinct from statestore's typed constructors, but the env read is shared.
	bucket := statestore.Bucket()
	if bucket == "" {
		return nil
	}
```

In `runSyncUp`, replace (currently `cmd/sync.go:77`):

```go
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
```

with:

```go
	if bucket := statestore.Bucket(); bucket != "" {
```

Fix imports: add `statestore`; keep `os` (still used for `os.Getenv("SITE_BUCKET")`, `os.Getenv("DASHBOARD_BUCKET")`, `os.Stat`, `os.Getenv("SITE_CF_DIST_ID")`, `os.Getenv("DASHBOARD_CF_DIST_ID_PARAM")`).

- [ ] **Step 2: Rewire `cmd/migrate_run_ledger.go`**

Replace (currently `cmd/migrate_run_ledger.go:42-45`):

```go
	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		return fmt.Errorf("migrate-run-ledger: STATE_BUCKET must be set")
	}
```

with:

```go
	// One-time S3-only migration: require the bucket (no local fallback), so
	// this intentionally differs from statestore's typed constructors while
	// sharing the single env read.
	bucket := statestore.Bucket()
	if bucket == "" {
		return fmt.Errorf("migrate-run-ledger: STATE_BUCKET must be set")
	}
```

Fix imports: add `statestore`; drop `os` **if now unused** in migrate_run_ledger.go (verify — it may not use `os` elsewhere).

- [ ] **Step 3: Build + vet**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/sync.go cmd/migrate_run_ledger.go
git commit -m "refactor: route sync + migrate env read through statestore.Bucket (rosterbot-9s6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018mY8YoKun45RH29QZHFtbW"
```

---

### Task 6: Docs + full acceptance verification

**Files:**
- Modify: `CLAUDE.md` (Architecture section — add the `internal/statestore` entry)
- Verify only: whole tree

**Interfaces:** none (documentation + verification).

- [ ] **Step 1: Assert the single env read (criterion 1)**

Run: `grep -rn 'os.Getenv("STATE_BUCKET")' --include="*.go" . | grep -v '/lambda/' | grep -v '_test.go'`
Expected: exactly **one** line — `internal/statestore/statestore.go` inside `func Bucket()`. (The `lambda/` module is a separate binary and is out of scope; `infra/` sets the var, it doesn't read it via `os.Getenv` at runtime — confirm the grep shows only the statestore line.)

If any other line appears, it was missed — go back and route it through `statestore`.

- [ ] **Step 2: Assert lineuprun no longer reads env or imports the S3 SDK (criterion 2)**

Run:
```bash
grep -rn 'os.Getenv' internal/lineuprun/ ; echo "---" ; go list -deps ./internal/lineuprun | grep -c 'aws-sdk-go-v2/service/s3'
```
Expected: no `os.Getenv` in `internal/lineuprun/`; the SDK count is `0`.

- [ ] **Step 3: Add the CLAUDE.md architecture entry**

In `CLAUDE.md`, in the Architecture section (near the `internal/cache` / Cache Store paragraphs), add a paragraph:

```markdown
**`internal/statestore`** — the composition root's single owner of the durable-state backend choice: S3 when `STATE_BUCKET` is set (Fargate), else a local directory. It centralizes the one `os.Getenv("STATE_BUCKET")` read (`statestore.Bucket`), the prefix/local-dir layout table for every durable artifact (`cache/`, `analysis/`, `analysis/team-values/`, `runledger/`, `runs/`, `notifications/`, `lineup/` ↔ their `.cache`/`.analysis`/`.teamvalue`/`.lineup/*` local roots), and the sole S3-vs-local branch (`pick[T]`). It exposes one typed constructor per artifact (`CacheStore`, `AnalysisWriter/Reader`, `TeamValueWriter/Reader`, `RunLedger`, `Output`, `Notifications`, `Progress`, `LineupPublisher`). **Only `cmd` imports it** — no leaf imports it back, so the AWS SDK it pulls in (via the three s3 adapters) never reaches leaves like `internal/lineuprun`, which receives its `lineupapi.Publisher` from `cmd` via `Options.Publisher` instead of reading the environment itself. The `cmd/sync.go` bulk dir-sync gating and `cmd/migrate_run_ledger.go` (require-S3) share the one env read via `statestore.Bucket()` but keep their distinct, non-store-constructor behavior. Compatible with ADR-0001 and with the still-pending three-adapter unification (rosterbot-oye).
```

- [ ] **Step 4: Full build + module + test sweep**

Run: `go vet ./... && go mod tidy && make build-modules && make test`
Expected: vet clean; `go mod tidy` produces no unexpected diff; `make build-modules` builds `lambda/`, `buildnotify/`, `infra/` clean; `make test` all PASS.

- [ ] **Step 5: End-to-end smoke (criterion 6)**

Run: `make clean-cache && make run-all`
Expected: every CLI command completes clean in dry-run/read-only mode; no errors; final `.cache/` size printed. (This exercises the real local-branch selection through `statestore` for cache, grade, team-values, projection-site, ledger/output/notifications/progress recorders, and the optimize publish path.)

- [ ] **Step 6: Commit**

```bash
git add CLAUDE.md go.mod go.sum
git commit -m "docs: document internal/statestore + verify single STATE_BUCKET read (rosterbot-9s6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018mY8YoKun45RH29QZHFtbW"
```

---

## Self-Review

**Spec coverage:**
- Layout table in one place → Task 1 (`artifact` vars) + Task 6 grep. ✓
- Single env read → Task 1 (`Bucket()`), Tasks 2–5 route through it, Task 6 Step 1 asserts. ✓
- Typed constructors + `pick[T]` → Task 1. ✓
- 10 call sites rewired → Tasks 2 (5 sites), 3 (4 sites), 4 (publish site). ✓
- lineuprun no `os.Getenv`, no AWS SDK → Task 4 + Task 6 Step 2. ✓
- Two variants routed + documented distinct → Task 5. ✓
- Previously-untested path with in-memory store → Task 4 (`memPub` publish test). ✓
- No behavior change → layout table verbatim; `make run-all` (Task 6 Step 5). ✓
- Build/module/test/run-all green → Task 6 Step 4–5. ✓
- CLAUDE.md updated → Task 6 Step 3. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code; import-cleanup steps say exactly which imports to add and which to drop "if unused" (with the reason each surviving import stays). ✓

**Type consistency:** `Selector` method names and return types in Task 1 match their call sites in Tasks 2–4 (`CacheStore`, `AnalysisWriter`, `AnalysisReader`, `TeamValueWriter`, `TeamValueReader`, `RunLedger`→`RunWriter`, `Output`, `Notifications`, `Progress`, `LineupPublisher`). `publishLineup`'s new 5th param `pub lineupapi.Publisher` matches the call site update in Task 4 Step 4 and the test in Step 1. `statestore.Bucket()` matches Task 5 usage. ✓
