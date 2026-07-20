# Dashboard v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the dashboard's iframe report embeds with native JSON-fed SPA views, restyle the whole dashboard into one design system, and add live phased run-progress so triggered jobs are watchable in real time.

**Architecture:** `projection-site` emits the already-computed report/value view models as static JSON sidecars the SPA renders natively (Chart.js vendored, no CDN). `internal/progress` gains a `Recorder` hook (mirroring the existing `OutputRecorder`) that persists optimize's phase transitions to `runs/<id>/progress.json`, exposed via a new `GET /v1/runs/{id}/progress`; the SPA background-polls the run ledger and that endpoint to drive a "Now Running" hero. Run *status* stays owned by the ledger; progress only carries phase detail.

**Tech Stack:** Go 1.22+ (net/http `mux.HandleFunc("GET /path/{id}")` routing, aws-sdk-go-v2 for S3), vanilla ES-module SPA, vendored Chart.js UMD.

## Global Constraints

- Go: after any Go change run `go build ./...`, `go vet ./...`, `go mod tidy`. `gofmt`/`vet` also run on save via hooks.
- Tests are hermetic — no credentials, all network mocked. Never add a test that needs real Fantrax/AWS.
- The SPA is dependency-free vanilla ES modules except the one vendored Chart.js file; no bundler, no npm. Every `import` is a relative path.
- All `/v1/*` fetches are same-origin relative paths (CloudFront routes `/v1/*` to the Lambda; `serve --web` mirrors it). No CORS code.
- Run *status* is authoritative from the ledger (`RUNNING|SUCCESS|FAILED`); `progress.json` is phase detail only. A RUNNING entry older than `maxJobDuration` (2h) is stale — never poll it forever.
- No CDK/infra change in this plan: progress rides the existing `runs/` S3 prefix the task role already read/writes.
- Match existing file conventions: snake_case JSON tags, `writeErr`/`writeJSON` helpers, table-driven Go tests.

---

## Phase A — Reports data pipeline (Go)

### Task 1: projection-site emits JSON sidecars; delete HTML render

**Files:**
- Modify: `cmd/projection-site.go` (both `report.Render` and `valuereport.Render` call sites)
- Delete: `internal/report/render.go`, `internal/report/template.html`, `internal/report/render_test.go`
- Delete: `internal/valuereport/render.go`, `internal/valuereport/template.html`
- Modify: `internal/valuereport/valuereport_test.go` (drop the render-exercising test case(s), keep `BuildModel` tests)

**Interfaces:**
- Consumes: `report.Aggregate(rows, generatedAt, seasonStart) *report.Model`; `valuereport.BuildModel(rows) *valuereport.Model` (both unchanged).
- Produces: files `report/model.json` (a `*report.Model`) and `report/value.json` (a `*valuereport.Model`) under the `--out` dir.

- [ ] **Step 1: Find the two render call sites**

Run: `grep -n "report.Render\|valuereport.Render\|\.html" cmd/projection-site.go`
Expected: the `runProjectionSite` writer block (around the `report.Render(f, m)` seen at the end of the file) and `renderValueSite`'s `valuereport.Render(...)`.

- [ ] **Step 2: Replace the projection-accuracy writer with JSON**

In `runProjectionSite`, replace the `index.html` create + `report.Render(f, m)` block with:

```go
	outPath := filepath.Join(projSiteOut, "model.json")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("encode model: %w", err)
	}
```

Add `"encoding/json"` to the imports; remove the now-unused `report` import ONLY if nothing else references it (it still references `report.Aggregate`, so keep it).

- [ ] **Step 3: Replace the value writer with JSON**

In `renderValueSite`, replace its `value.html` create + `valuereport.Render(...)` block with the same pattern writing `value.json`:

```go
	outPath := filepath.Join(projSiteOut, "value.json")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(vm); err != nil {
		return fmt.Errorf("encode value model: %w", err)
	}
```

(Use whatever the local model variable is named — read the function; it builds a `*valuereport.Model`.)

- [ ] **Step 4: Delete the render code + templates**

```bash
git rm internal/report/render.go internal/report/template.html internal/report/render_test.go
git rm internal/valuereport/render.go internal/valuereport/template.html
```

- [ ] **Step 5: Prune the valuereport render test**

Open `internal/valuereport/valuereport_test.go`. Remove any test function that calls `Render` (e.g. `TestRender*`) and any now-unused imports (`bytes`, `strings` if only render used them). Keep all `BuildModel` tests.

- [ ] **Step 6: Build + vet + tidy**

Run: `go build ./... && go vet ./... && go mod tidy`
Expected: clean. If `render_test.go` referenced helpers now unused elsewhere, fix compile errors.

- [ ] **Step 7: Smoke the JSON output**

Run: `go run . projection-site --out /tmp/rv && ls -la /tmp/rv && jq 'keys' /tmp/rv/model.json && jq 'keys' /tmp/rv/value.json`
Expected: `model.json` keys include `generatedAt,windows,roles,systems,views,compare,...`; `value.json` keys include `dates,teams,series,latest` (or `{"empty":true}` if the local store is empty — also valid).

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(projection-site): emit model.json/value.json, drop HTML render"
```

---

## Phase B — Live progress backend (Go)

### Task 2: internal/progress Recorder hook + Snapshot

**Files:**
- Modify: `internal/progress/progress.go`
- Test: `internal/progress/progress_test.go`

**Interfaces:**
- Produces:
  - `type PhaseState struct { Name string \`json:"name"\`; State string \`json:"state"\` }` (`state` ∈ `pending|active|done|warn`)
  - `type Snapshot struct { Phase string \`json:"phase"\`; Pct int \`json:"pct"\`; Phases []PhaseState \`json:"phases"\`; Status string \`json:"status"\`; UpdatedAt string \`json:"updated_at"\` }`
  - `var Recorder func(Snapshot)` — nil-safe global hook.
  - `var PipelinePhases = []string{"Roster","Projections","Recent stats","Pitcher info","Handedness","GS budget","Optimize"}` — the ordered phase list emit iterates for `Phases`.
- Consumes: nothing new. Existing `Start/Done/Warn` gain recorder emission.

- [ ] **Step 1: Write the failing test**

Add to `internal/progress/progress_test.go`:

```go
func TestRecorder_EmitsPhasesNonInteractive(t *testing.T) {
	var got []Snapshot
	old := Recorder
	Recorder = func(s Snapshot) { got = append(got, s) }
	defer func() { Recorder = old }()

	// Non-interactive, discard terminal output — recorder must still fire.
	p := New(false, io.Discard)
	p.Start("Roster")
	p.Done("Roster", "ok")
	p.Start("Projections")
	p.Warn("Projections", "degraded")

	if len(got) != 4 {
		t.Fatalf("want 4 emissions, got %d", len(got))
	}
	// After Start("Roster"): Roster active, current phase Roster.
	if got[0].Phase != "Roster" || phaseState(got[0], "Roster") != "active" {
		t.Errorf("emission 0 = %+v", got[0])
	}
	// After Done("Roster"): Roster done, pct = 10.
	if phaseState(got[1], "Roster") != "done" || got[1].Pct != 10 {
		t.Errorf("emission 1 = %+v", got[1])
	}
	// After Warn("Projections"): Projections warn.
	if phaseState(got[3], "Projections") != "warn" {
		t.Errorf("emission 3 = %+v", got[3])
	}
	// Unreached phase stays pending.
	if phaseState(got[3], "GS budget") != "pending" {
		t.Errorf("GS budget should be pending: %+v", got[3])
	}
}

func phaseState(s Snapshot, name string) string {
	for _, ph := range s.Phases {
		if ph.Name == name {
			return ph.State
		}
	}
	return "MISSING"
}
```

Ensure the test file imports `"io"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/progress/ -run TestRecorder -v`
Expected: FAIL — `Snapshot`, `Recorder`, `PhaseState`, `PipelinePhases` undefined.

- [ ] **Step 3: Add the types + hook + emit**

At the top of `internal/progress/progress.go` (after imports), add:

```go
// PhaseState is one pipeline phase's status in a Snapshot.
type PhaseState struct {
	Name  string `json:"name"`
	State string `json:"state"` // pending | active | done | warn
}

// Snapshot is the persisted live-progress record (runs/<id>/progress.json).
type Snapshot struct {
	Phase     string       `json:"phase"`
	Pct       int          `json:"pct"`
	Phases    []PhaseState `json:"phases"`
	Status    string       `json:"status"` // running
	UpdatedAt string       `json:"updated_at"`
}

// PipelinePhases is the ordered phase list emit reports. Matches the optimize
// pipeline's Start/Done sequence and the phaseWeight keys.
var PipelinePhases = []string{"Roster", "Projections", "Recent stats", "Pitcher info", "Handedness", "GS budget", "Optimize"}

// Recorder is a nil-safe global hook (mirrors lineupapi.OutputRecorder). cmd
// installs it to write runs/<RUN_ID>/progress.json. Unset => no-op.
var Recorder func(Snapshot)
```

Add `"time"` to the import block if not present.

Add state fields to the `Progress` struct:

```go
type Progress struct {
	interactive bool
	verbose     bool
	w           io.Writer
	pct         int
	state       map[string]string // phase name -> pending/active/done/warn
	current     string
}
```

Initialize `state` in both `New` and `NewVerbose`:

```go
func New(interactive bool, w io.Writer) *Progress {
	return &Progress{interactive: interactive, w: w, state: map[string]string{}}
}

func NewVerbose() *Progress {
	return &Progress{verbose: true, state: map[string]string{}}
}
```

Add the emit helper:

```go
// emit builds the current snapshot and hands it to Recorder (if installed).
// Fires in every mode — terminal drawing is gated on interactive, emission is not.
func (p *Progress) emit() {
	if Recorder == nil {
		return
	}
	phases := make([]PhaseState, len(PipelinePhases))
	for i, name := range PipelinePhases {
		st := p.state[name]
		if st == "" {
			st = "pending"
		}
		phases[i] = PhaseState{Name: name, State: st}
	}
	Recorder(Snapshot{
		Phase:     p.current,
		Pct:       p.pct,
		Phases:    phases,
		Status:    "running",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
}
```

- [ ] **Step 4: Wire emit into Start/Done/Warn (fires before the mode gates)**

Rewrite the three methods so state update + emit happen unconditionally, then the existing terminal drawing stays gated:

```go
func (p *Progress) Start(phase string) {
	p.current = phase
	p.state[phase] = "active"
	p.emit()
	if p.verbose || !p.interactive {
		return
	}
	p.startInteractive(phase)
}

func (p *Progress) Done(phase string, detail string) {
	p.state[phase] = "done"
	if w, ok := phaseWeight[phase]; ok {
		p.pct = w
	}
	p.current = ""
	p.emit()
	if p.verbose {
		return
	}
	if p.interactive {
		p.doneInteractive(phase, detail)
		return
	}
	log.Printf("%s: %s", phase, detail)
}

func (p *Progress) Warn(phase string, detail string) {
	p.state[phase] = "warn"
	if w, ok := phaseWeight[phase]; ok {
		p.pct = w
	}
	p.emit()
	if p.verbose {
		return
	}
	if p.interactive {
		p.warnInteractive(phase, detail)
		return
	}
	log.Printf("WARNING: %s: %s", phase, detail)
}
```

Note: `doneInteractive`/`warnInteractive` currently also set `p.pct` from `phaseWeight` — that's now done in the outer method. Remove the duplicate `if w, ok := phaseWeight[phase]; ok { p.pct = w }` lines inside `doneInteractive` and `warnInteractive` to avoid double-setting (harmless but redundant).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/progress/ -run TestRecorder -v`
Expected: PASS.

- [ ] **Step 6: Full package test + vet**

Run: `go test ./internal/progress/ && go vet ./internal/progress/`
Expected: PASS (existing terminal tests unaffected — emit is a no-op when `Recorder` is nil, which it is in those tests).

- [ ] **Step 7: Commit**

```bash
git add internal/progress/
git commit -m "feat(progress): add Recorder hook + Snapshot for live run progress"
```

---

### Task 3: lineupapi progress store seam + endpoint

**Files:**
- Create: `internal/lineupapi/progress.go`
- Modify: `internal/lineupapi/types.go` (add `ProgressSnapshot` wire type)
- Modify: `internal/lineupapi/handler.go` (Config field + route + handler)
- Test: `internal/lineupapi/handler_test.go` (endpoint test), `internal/lineupapi/progress_test.go` (FileProgressStore round-trip)

**Interfaces:**
- Produces:
  - `type ProgressStore interface { GetProgress(ctx, runID string) ([]byte, bool, error) }`
  - `type ProgressWriter interface { PutProgress(ctx, runID string, data []byte) error }`
  - `type FileProgressStore struct{...}`; `func NewFileProgressStore(dir string) *FileProgressStore`
  - `Config.Progress ProgressStore` field
  - route `GET /v1/runs/{id}/progress` → `handleRunProgress`
- Consumes: the `writeErr` helper and Go-1.22 mux routing already in `handler.go`.

- [ ] **Step 1: Write the failing FileProgressStore test**

Create `internal/lineupapi/progress_test.go`:

```go
package lineupapi

import (
	"context"
	"testing"
)

func TestFileProgressStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileProgressStore(dir)
	ctx := context.Background()

	if _, ok, err := s.GetProgress(ctx, "r1"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	want := []byte(`{"phase":"Roster","pct":10}`)
	if err := s.PutProgress(ctx, "r1", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetProgress(ctx, "r1")
	if err != nil || !ok || string(got) != string(want) {
		t.Fatalf("get: got=%q ok=%v err=%v", got, ok, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/lineupapi/ -run TestFileProgressStore -v`
Expected: FAIL — `NewFileProgressStore` undefined.

- [ ] **Step 3: Implement the store seam**

Create `internal/lineupapi/progress.go` (mirrors `output.go`'s FileOutputStore, one file per run at `<dir>/<runID>.json`):

```go
package lineupapi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ProgressStore is the read side for live run progress (GET /v1/runs/{id}/progress).
type ProgressStore interface {
	GetProgress(ctx context.Context, runID string) ([]byte, bool, error)
}

// ProgressWriter is the write side: persist the snapshot bytes for a run id.
type ProgressWriter interface {
	PutProgress(ctx context.Context, runID string, data []byte) error
}

// FileProgressStore is a local-filesystem ProgressStore+Writer, one file per run
// at <dir>/<runID>.json. Used by `serve` and local job runs.
type FileProgressStore struct{ dir string }

func NewFileProgressStore(dir string) *FileProgressStore { return &FileProgressStore{dir: dir} }

func (s *FileProgressStore) path(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

func (s *FileProgressStore) GetProgress(_ context.Context, runID string) ([]byte, bool, error) {
	data, err := os.ReadFile(s.path(runID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *FileProgressStore) PutProgress(_ context.Context, runID string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(runID), data, 0o644)
}

var (
	_ ProgressStore  = (*FileProgressStore)(nil)
	_ ProgressWriter = (*FileProgressStore)(nil)
)
```

- [ ] **Step 4: Add the wire type to types.go**

Append to `internal/lineupapi/types.go`:

```go
// ProgressSnapshot is the GET /v1/runs/{id}/progress body — live phase progress
// for a run. Mirrors internal/progress.Snapshot. Phase detail only; the run's
// authoritative status comes from the ledger (GET /v1/runs).
type ProgressSnapshot struct {
	Phase     string          `json:"phase"`
	Pct       int             `json:"pct"`
	Phases    []ProgressPhase `json:"phases"`
	Status    string          `json:"status"`
	UpdatedAt string          `json:"updated_at"`
}

type ProgressPhase struct {
	Name  string `json:"name"`
	State string `json:"state"`
}
```

- [ ] **Step 5: Add Config field + route + handler**

In `handler.go`, add to the `Config` struct (after `Output OutputStore`):

```go
	Progress      ProgressStore
```

Register the route in `Handler(...)` right after the `/v1/runs/{id}/output` line:

```go
	mux.HandleFunc("GET /v1/runs/{id}/progress", cfg.handleRunProgress)
```

Add the handler after `handleRunOutput` (byte-for-byte mirror, distinct 404 message):

```go
func (cfg Config) handleRunProgress(w http.ResponseWriter, r *http.Request) {
	if cfg.Progress == nil {
		writeErr(w, http.StatusNotImplemented, "run progress not configured")
		return
	}
	id := r.PathValue("id")
	data, ok, err := cfg.Progress.GetProgress(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "run progress unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no progress for run")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
```

- [ ] **Step 6: Write the endpoint test**

Add to `internal/lineupapi/handler_test.go` (follow the file's existing test-config construction pattern — grep for how `handleRunOutput` is tested and copy its Config setup, substituting a `FileProgressStore` seeded via `PutProgress`). Minimum three cases:

```go
func TestHandleRunProgress(t *testing.T) {
	dir := t.TempDir()
	ps := NewFileProgressStore(dir)
	_ = ps.PutProgress(context.Background(), "run123", []byte(`{"phase":"Roster","pct":10,"status":"running"}`))

	h := Handler(Config{Token: "tok", Lineups: stubLineups{}, Progress: ps})

	// present
	rec := doAuthedGET(t, h, "/v1/runs/run123/progress", "tok")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"phase":"Roster"`) {
		t.Fatalf("present: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// missing => 404
	rec = doAuthedGET(t, h, "/v1/runs/nope/progress", "tok")
	if rec.Code != 404 {
		t.Fatalf("missing: code=%d", rec.Code)
	}
	// not configured => 501
	h2 := Handler(Config{Token: "tok", Lineups: stubLineups{}})
	rec = doAuthedGET(t, h2, "/v1/runs/x/progress", "tok")
	if rec.Code != 501 {
		t.Fatalf("nil store: code=%d", rec.Code)
	}
}
```

If `stubLineups` / `doAuthedGET` don't exist under those names, use whatever the file's existing tests use for a minimal authed GET (grep `handler_test.go` for the auth-header helper and the lineup stub; reuse them verbatim). Ensure imports include `"context"` and `"strings"`.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/lineupapi/ -run 'TestFileProgressStore|TestHandleRunProgress' -v`
Expected: PASS.

- [ ] **Step 8: Build + vet + commit**

```bash
go build ./... && go vet ./...
git add internal/lineupapi/
git commit -m "feat(lineupapi): GET /v1/runs/{id}/progress + progress store seam"
```

---

### Task 4: s3lineup progress adapter

**Files:**
- Create: `internal/lineupapi/s3lineup/progress.go`
- Test: `internal/lineupapi/s3lineup/progress_test.go` (mirror `output_test.go`)

**Interfaces:**
- Produces: `func NewProgress(ctx, bucket, prefix string) (*ProgressStore, error)` storing at `<prefix><runID>/progress.json`; implements `lineupapi.ProgressStore` + `lineupapi.ProgressWriter`.
- Consumes: the package-internal `api` client interface, `ptr` helper (already in `s3lineup.go`).

- [ ] **Step 1: Copy output.go to progress.go, rename**

Create `internal/lineupapi/s3lineup/progress.go` — an exact copy of `output.go` with: type `OutputStore`→`ProgressStore`, `NewOutput`→`NewProgress`, `GetOutput`→`GetProgress`, `PutOutput`→`PutProgress`, `objKey` returns `s.prefix + runID + "/progress.json"`, and the interface assertions target `lineupapi.ProgressStore`/`ProgressWriter`:

```go
package s3lineup

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// ProgressStore reads/writes live run progress at <prefix><runID>/progress.json,
// beside the run's ledger + output under the same runs/ prefix.
type ProgressStore struct {
	client api
	bucket string
	prefix string
}

// NewProgress builds a ProgressStore. prefix should end in "/", e.g. "runs/".
func NewProgress(ctx context.Context, bucket, prefix string) (*ProgressStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &ProgressStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *ProgressStore) objKey(runID string) string { return s.prefix + runID + "/progress.json" }

func (s *ProgressStore) GetProgress(ctx context.Context, runID string) ([]byte, bool, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: ptr(s.objKey(runID))})
	if err != nil {
		var nsk *types.NoSuchKey
		var nf *types.NotFound
		if errors.As(err, &nsk) || errors.As(err, &nf) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *ProgressStore) PutProgress(ctx context.Context, runID string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey(runID)),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

var (
	_ lineupapi.ProgressStore  = (*ProgressStore)(nil)
	_ lineupapi.ProgressWriter = (*ProgressStore)(nil)
)
```

- [ ] **Step 2: Copy the output test, rename**

Open `internal/lineupapi/s3lineup/output_test.go`. Create `progress_test.go` as a copy with the same substitutions (it uses a fake `api` client — reuse whatever mock the output test uses; grep `output_test.go` for the fake S3 client type and reuse it). Assert `objKey` ends in `/progress.json` and the round-trip / NoSuchKey→(false,nil) behavior.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/lineupapi/s3lineup/ -run Progress -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
go build ./... && go vet ./...
git add internal/lineupapi/s3lineup/
git commit -m "feat(s3lineup): S3 progress adapter (runs/<id>/progress.json)"
```

---

### Task 5: Install the progress recorder + wire Config.Progress

**Files:**
- Create: `cmd/progress.go` (mirrors `cmd/output.go`)
- Modify: `cmd/root.go` (call `installProgressRecorder()` next to `installOutputRecorder()`)
- Modify: `cmd/serve.go` (add `Progress:` to the Config literal)
- Modify: `lambda/main.go` (init an S3 progress store + add `Progress:` to the Config literal)

**Interfaces:**
- Consumes: `progress.Recorder` (Task 2), `lineupapi.NewFileProgressStore` / `s3lineup.NewProgress` (Tasks 3–4).
- Produces: a nil-safe global installer `installProgressRecorder()`.

- [ ] **Step 1: Create the installer**

Create `cmd/progress.go`:

```go
package cmd

import (
	"context"
	"encoding/json"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/progress"
)

// installProgressRecorder wires progress.Recorder so a run's phase transitions
// persist under the current RUN_ID (runs/<id>/progress.json). Best-effort:
// missing RUN_ID or a store error never affects the job. STATE_BUCKET -> S3;
// otherwise local .lineup/progress/<id>.json. Mirrors installOutputRecorder.
func installProgressRecorder() {
	runID := os.Getenv("RUN_ID")
	if runID == "" {
		return
	}

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

	progress.Recorder = func(s progress.Snapshot) {
		body, err := json.Marshal(s)
		if err != nil {
			return
		}
		_ = w.PutProgress(context.Background(), runID, body)
	}
}
```

- [ ] **Step 2: Call it from initApp**

In `cmd/root.go`, right after the `installOutputRecorder()` call (~line 120):

```go
	// Persist optimize's phase transitions under RUN_ID so the app can show a
	// live progress bar (GET /v1/runs/{id}/progress).
	installProgressRecorder()
```

- [ ] **Step 3: Wire serve's Config**

In `cmd/serve.go`, in the `lineupapi.Config{...}` literal, after the `Output:` line:

```go
		Progress:      lineupapi.NewFileProgressStore(lineupDir + "/progress"),
```

- [ ] **Step 4: Wire the Lambda's Config**

In `lambda/main.go`, after the `output, err := s3lineup.NewOutput(...)` block:

```go
	progressStore, err := s3lineup.NewProgress(ctx, bucket, "runs/")
	if err != nil {
		log.Fatalf("init s3 progress store: %v", err)
	}
```

And in the `lineupapi.Config{...}` literal, after `Output: output,`:

```go
		Progress:      progressStore,
```

- [ ] **Step 5: Build + vet + tidy**

Run: `go build ./... && go vet ./... && go mod tidy`
Expected: clean.

- [ ] **Step 6: End-to-end local smoke (progress persists + serves)**

```bash
rm -rf /tmp/lu && RUN_ID=testrun go run . optimize --dry-run --dates 2026-04-01 --no-cache 2>/dev/null; \
cat .lineup/progress/testrun.json | jq '.phases[] | "\(.name): \(.state)"'
```
Expected: a JSON snapshot whose phases show `Roster: done`, `Projections: done`, etc. (final write = last phase reached). (If Fantrax creds are absent locally the optimize may bail early — that's fine; you still get an early-phase snapshot proving the recorder fires.)

- [ ] **Step 7: Commit**

```bash
git add cmd/progress.go cmd/root.go cmd/serve.go lambda/main.go
git commit -m "feat(cmd): install progress recorder + wire Config.Progress"
```

---

## Phase C — SPA reports views (vanilla JS; verified manually in-browser)

> No JS test harness exists in this repo; Phase C–E steps verify by loading the SPA via `serve --web` against real JSON. Each task ends with a browser check + commit.

### Task 6: Vendor Chart.js + chart wrapper + api.js endpoints

**Files:**
- Create: `web/dashboard/vendor/chart.min.js` (Chart.js UMD build, pinned version)
- Create: `web/dashboard/chart.js`
- Modify: `web/dashboard/api.js`
- Modify: `web/dashboard/index.html` (load the vendor script)

**Interfaces:**
- Produces:
  - `api.reportModel()` → GET `/report/model.json`; `api.reportValue()` → GET `/report/value.json`; `api.runProgress(id)` → GET `/v1/runs/{id}/progress`.
  - `chart.js` exports `lineChart(canvas, cfg)`, `scatterChart(canvas, cfg)`, `barChart(canvas, cfg)`, `themeColors()` — thin wrappers applying theme-aware defaults over the global `Chart`.

- [ ] **Step 1: Vendor Chart.js**

Download a pinned UMD build into the repo (no CDN at runtime):

```bash
mkdir -p web/dashboard/vendor
curl -fsSL https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js -o web/dashboard/vendor/chart.min.js
test -s web/dashboard/vendor/chart.min.js && head -c 60 web/dashboard/vendor/chart.min.js
```
Expected: file exists, non-empty, begins with the Chart.js UMD banner. (If `report/template.html` referenced a specific Chart.js version before deletion, match that major version.)

- [ ] **Step 2: Load it before the module script in index.html**

In `web/dashboard/index.html`, immediately before `<script type="module" src="app.js"></script>`:

```html
<script src="vendor/chart.min.js"></script>
```

This puts the global `Chart` on `window` before any module runs.

- [ ] **Step 3: Add API methods**

In `web/dashboard/api.js`, add inside the `api` object (report JSON is served from the CloudFront root, not `/v1`, so use absolute `/report/...` paths):

```js
  reportModel: () => request("GET", "/report/model.json"),
  reportValue: () => request("GET", "/report/value.json"),
  runProgress: (id) => request("GET", `/v1/runs/${encodeURIComponent(id)}/progress`),
```

- [ ] **Step 4: Write the chart wrapper**

Create `web/dashboard/chart.js`:

```js
// chart.js — thin wrappers over the vendored global Chart with theme-aware
// defaults read from CSS custom properties, so charts recolor with light/dark.
function css(name, fallback) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

export function themeColors() {
  return {
    fg: css("--fg", "#1a1a1a"),
    muted: css("--muted", "#6b7280"),
    border: css("--border", "#e5e7eb"),
    accent: css("--accent", "#2563eb"),
    palette: [
      css("--c1", "#4e79a7"), css("--c2", "#f28e2b"), css("--c3", "#59a14f"),
      css("--c4", "#e15759"), css("--c5", "#76b7b2"), css("--c6", "#edc948"),
      css("--c7", "#b07aa1"), css("--c8", "#ff9da7"),
    ],
  };
}

function base(t) {
  return {
    responsive: true,
    maintainAspectRatio: false,
    plugins: { legend: { labels: { color: t.fg } } },
    scales: {
      x: { ticks: { color: t.muted }, grid: { color: t.border } },
      y: { ticks: { color: t.muted }, grid: { color: t.border } },
    },
  };
}

function make(type, canvas, cfg) {
  const t = themeColors();
  const b = base(t);
  return new Chart(canvas, {
    type,
    data: cfg.data,
    options: { ...b, ...(cfg.options || {}), plugins: { ...b.plugins, ...(cfg.options?.plugins || {}) }, scales: { ...b.scales, ...(cfg.options?.scales || {}) } },
  });
}

export const lineChart = (c, cfg) => make("line", c, cfg);
export const scatterChart = (c, cfg) => make("scatter", c, cfg);
export const barChart = (c, cfg) => make("bar", c, cfg);
```

- [ ] **Step 5: Browser sanity**

```bash
ROSTERBOT_API_TOKEN=test ROSTERBOT_SESSION_SECRET=test-secret go run . serve &
# in another shell: go run . projection-site --out web/dashboard/report
```
Open `http://localhost:8080`, open DevTools console, run `Chart` — expected: the Chart constructor function is defined (vendored file loaded). Stop `serve` when done.

- [ ] **Step 6: Commit**

```bash
git add web/dashboard/vendor/chart.min.js web/dashboard/chart.js web/dashboard/api.js web/dashboard/index.html
git commit -m "feat(dashboard): vendor Chart.js + chart wrapper + report/progress API"
```

---

### Task 7: Native Projections view

**Files:**
- Create: `web/dashboard/projections.js`
- (The deleted `internal/report/template.html` from Task 1 is the port reference — recover its inline JS from git history: `git show HEAD~N:internal/report/template.html`.)

**Interfaces:**
- Consumes: `api.reportModel()` (Task 6), `lineChart`/`scatterChart`/`barChart` (Task 6), the `report.Model` JSON shape (keys: `windows,roles,systems,detailSystem,views,trends,compare,compareTrends,generatedAt,latestDate` — see `internal/report/model.go`; `views` keyed `"system|window|role"`, each a `View{scorecard,byPos,calib,misses,insights}`).
- Produces: `export function renderProjections(root)`.

- [ ] **Step 1: Recover the port reference**

```bash
git log --oneline -- internal/report/template.html | head -1   # find the last commit that had it
git show <that-sha>:internal/report/template.html > /tmp/report-template.html
```
Read `/tmp/report-template.html`'s `<script>` block: it holds the exact toggle + Chart.js render logic against the same `Model` JSON. Port that logic into the module below (the data shape is identical — only the DOM/host and chart construction change to use `chart.js` + design-system primitives).

- [ ] **Step 2: Write the module skeleton + state**

Create `web/dashboard/projections.js`:

```js
// projections.js — native projection-accuracy view. Fetches the precomputed
// report.Model JSON and renders scorecard, by-position, calibration, misses,
// and the system-comparison panel with client-side window/role/system toggles.
import { api } from "./api.js";
import { lineChart, scatterChart, barChart } from "./chart.js";

const WINDOW_LABELS = { 7: "7d", 14: "14d", 30: "30d", 0: "Season" };
const ROLE_LABELS = { all: "All", hitters: "Hitters", pitchers: "Pitchers" };

export async function renderProjections(root) {
  root.innerHTML = `<p class="muted">Loading projections…</p>`;
  let model;
  try {
    model = await api.reportModel();
  } catch (err) {
    root.innerHTML = `<div class="card"><p class="muted">No projection data yet. The daily grade + projection-site run publishes it.</p></div>`;
    return;
  }
  const state = { window: 30, role: "all", system: model.detailSystem };
  const el = buildLayout(root, model, state);
  const rerender = () => paint(el, model, state);
  wireToggles(el, model, state, rerender);
  rerender();
}
```

- [ ] **Step 3: Implement buildLayout / wireToggles / paint**

Port the rest from `/tmp/report-template.html`, mapping its pieces to design-system primitives:
- **Toggles** — window (from `model.windows`), role (`model.roles`), system (`model.systems`) as `.toggle-group` button rows; `state` holds the selection; `wireToggles` attaches click handlers calling `rerender`.
- **Scorecard** — read `view = model.views[state.system + "|" + state.window + "|" + state.role]`. Render four `.stat-tile`s (MAE/Bias/RMSE/N) from `view.scorecard.cur` with the prior-window delta from `view.scorecard.prior`.
- **By-position** — `barChart` over `view.byPos` (bucket → MAE), plus a small table.
- **Calibration** — `scatterChart` of `view.calib` (projected vs actual points).
- **Misses** — a table of `view.misses` (top signed errors).
- **Trend** — `lineChart` of `model.trends[state.system + "|" + state.window + "|" + state.role]`.
- **Comparison panel** — table from `model.compare[state.window + "|" + state.role]` (systems ranked by MAE, `best` flagged) + an overlaid multi-line `lineChart` from `model.compareTrends[...]` (one dataset per system).

`paint(el, model, state)` destroys any prior Chart instances (keep references on `el`) and rebuilds them for the current selection. Use `<canvas>` elements sized by CSS (the wrapper sets `maintainAspectRatio:false`, so give each canvas a fixed-height `.chart-box` parent).

Keep the exact metric math from the template — do not recompute; the Model already carries computed values.

- [ ] **Step 4: Route it in (temporary manual wire for testing)**

Temporarily, to test before Task 9, import and call `renderProjections` from the console, or wait for Task 9. Preferred: do Task 9 immediately after so `#projections` routes here.

- [ ] **Step 5: Browser check**

With `serve` running and `projection-site --out web/dashboard/report` done, navigate to `#projections`. Expected: tiles + charts render; window/role/system toggles re-render without a reload; light/dark both legible. Verify the missing-data path by temporarily renaming `web/dashboard/report/model.json` → reload → friendly empty card.

- [ ] **Step 6: Commit**

```bash
git add web/dashboard/projections.js
git commit -m "feat(dashboard): native Projections view (ports report template)"
```

---

### Task 8: Native Value view

**Files:**
- Create: `web/dashboard/value.js`
- Port reference: the deleted `internal/valuereport/template.html` (recover from git as in Task 7).

**Interfaces:**
- Consumes: `api.reportValue()`, `lineChart` (Task 6), the `valuereport.Model` shape (`empty,dates,teams[{id,name,logo,color}],series[{team,dt,h_mlb,h_min,p_mlb,p_min}],latest[...]` — see `internal/valuereport/model.go`).
- Produces: `export function renderValue(root)`.

- [ ] **Step 1: Recover the port reference**

```bash
git show <last-sha-with-file>:internal/valuereport/template.html > /tmp/value-template.html
```

- [ ] **Step 2: Write the module**

Create `web/dashboard/value.js`:

```js
// value.js — native team-value view. Fetches the valuereport.Model and renders
// the multi-team time series with a client-side metric selector + a standings
// table. Metrics derive from the four value leaves shipped per point.
import { api } from "./api.js";
import { lineChart } from "./chart.js";

const METRICS = {
  total: (r) => r.h_mlb + r.h_min + r.p_mlb + r.p_min,
  mlb: (r) => r.h_mlb + r.p_mlb,
  minors: (r) => r.h_min + r.p_min,
  hitter: (r) => r.h_mlb + r.h_min,
  pitcher: (r) => r.p_mlb + r.p_min,
};

export async function renderValue(root) {
  root.innerHTML = `<p class="muted">Loading team value…</p>`;
  let model;
  try {
    model = await api.reportValue();
  } catch (err) {
    root.innerHTML = `<div class="card"><p class="muted">No team-value data yet.</p></div>`;
    return;
  }
  if (model.empty) {
    root.innerHTML = `<div class="card"><p class="muted">Collecting team value — check back after the next daily run.</p></div>`;
    return;
  }
  const state = { metric: "total", hidden: new Set() };
  // buildLayout: metric selector (.toggle-group over Object.keys(METRICS)),
  // a .chart-box canvas, a legend with per-team toggles (+All/None), and the
  // standings table from model.latest. Port the exact series-assembly + legend
  // logic from /tmp/value-template.html, replacing its Chart(...) call with
  // lineChart(canvas, { data, options }).
  // ... (port here)
}
```

Port the series assembly (group `model.series` by team → one dataset per team using `model.teams[i].color`, x-axis `model.dates`, y = `METRICS[state.metric](row)`), the legend toggle (add/remove team id in `state.hidden`, rebuild chart), the All/None buttons, and the standings table (from `model.latest`, with logo + `matched`/`rostered` join coverage).

- [ ] **Step 3: Browser check**

Navigate to `#value` (after Task 9). Expected: multi-team line chart; metric selector switches Total/MLB/Minors/Hitter/Pitcher; legend toggles hide/show teams; standings table matches. Empty path: an empty `value.json` (`{"empty":true}`) shows the collecting note.

- [ ] **Step 4: Commit**

```bash
git add web/dashboard/value.js
git commit -m "feat(dashboard): native Value view (ports valuereport template)"
```

---

### Task 9: Route native views, remove iframe wrapper

**Files:**
- Modify: `web/dashboard/app.js`
- Delete: `web/dashboard/reportview.js`

- [ ] **Step 1: Swap imports + routes**

In `app.js`, replace:

```js
import { renderProjections, renderValue } from "./reportview.js";
```
with:
```js
import { renderProjections } from "./projections.js";
import { renderValue } from "./value.js";
```

The `ROUTES` entries (`"#projections": renderProjections`, `"#value": renderValue`) stay unchanged — only the source modules differ. Note `route()` calls `render(root)` synchronously; the new renderers are `async` and manage their own loading state, which is fine (the returned promise is ignored, matching how an async view should self-manage).

- [ ] **Step 2: Delete the iframe wrapper**

```bash
git rm web/dashboard/reportview.js
```

- [ ] **Step 3: Full browser check**

Reload the SPA. Click Projections and Value tabs. Expected: both render natively (no iframe, shared header/theme). Switch tabs back and forth — no leaked Chart instances / console errors.

- [ ] **Step 4: Commit**

```bash
git add web/dashboard/app.js
git commit -m "feat(dashboard): route native Projections/Value, drop iframe wrapper"
```

---

## Phase D — SPA live run UX

### Task 10: Live "Now Running" hero + poller + toasts

**Files:**
- Create: `web/dashboard/live.js`
- Modify: `web/dashboard/app.js` (start the live controller after `showShell`)
- Modify: `web/dashboard/index.html` (add a `<div id="live-hero"></div>` and `<div id="toasts"></div>` inside `#shell`, above `<main>`)

**Interfaces:**
- Consumes: `api.runs()`, `api.runProgress(id)` (Task 6), `ApiError`.
- Produces: `export function startLive()` (begins background polling); `export function watchRun(id)` (jump the hero to a specific run immediately after a trigger); `export function toast(msg, kind)` (kind ∈ `ok|fail`).

- [ ] **Step 1: Add hero + toast hosts to index.html**

Inside `#shell`, between `</header>` and `<main id="view-root">`:

```html
  <div id="live-hero"></div>
```
And just before `</div>` closing `#shell` (or at end of body inside shell):
```html
  <div id="toasts" aria-live="polite"></div>
```

- [ ] **Step 2: Write live.js**

```js
// live.js — background poller for in-flight runs. Shows a "Now Running" hero
// with a phased progress bar (or an indeterminate bar for jobs that emit no
// progress.json), fires a toast when a run finishes, and badges the Runs nav.
import { api, ApiError } from "./api.js";

const RUNS_POLL_MS = 5000;
const PROG_POLL_MS = 2000;
const MAX_RUN_MS = 2 * 60 * 60 * 1000; // mirrors backend maxJobDuration (2h)

let runsTimer = null;
let progTimer = null;
let watchedId = null;
const lastStatus = new Map(); // run id -> last seen status, for completion toasts

const heroEl = () => document.getElementById("live-hero");
const badgeEl = () => document.querySelector('nav a[href="#runs"]');

function isLive(run) {
  if (run.status !== "RUNNING") return false;
  const started = Date.parse(run.started_at);
  return !Number.isNaN(started) && Date.now() - started < MAX_RUN_MS;
}

export function toast(msg, kind = "ok") {
  const host = document.getElementById("toasts");
  if (!host) return;
  const t = document.createElement("div");
  t.className = `toast toast-${kind}`;
  t.textContent = msg;
  host.appendChild(t);
  setTimeout(() => t.remove(), 5000);
}

async function pollRuns() {
  let runs = [];
  try {
    const res = await api.runs(25);
    runs = res.runs || [];
  } catch {
    schedule();
    return;
  }
  // Completion detection: any id that was RUNNING last tick and is now terminal.
  for (const r of runs) {
    const prev = lastStatus.get(r.id);
    if (prev === "RUNNING" && r.status !== "RUNNING") {
      toast(`${r.command.split(" ")[0]} ${r.status === "SUCCESS" ? "finished" : "failed"}`, r.status === "SUCCESS" ? "ok" : "fail");
    }
    lastStatus.set(r.id, r.status);
  }
  const live = runs.filter(isLive);
  const badge = badgeEl();
  if (badge) badge.classList.toggle("has-live", live.length > 0);

  const target = live.find((r) => r.id === watchedId) || live[0];
  if (target) {
    renderHero(target);
    pollProgress(target.id);
  } else {
    clearHero();
  }
  schedule();
}

function schedule() {
  clearTimeout(runsTimer);
  runsTimer = setTimeout(pollRuns, RUNS_POLL_MS);
}

async function pollProgress(id) {
  clearTimeout(progTimer);
  let snap = null;
  try {
    snap = await api.runProgress(id);
  } catch (err) {
    // 404 => job emits no phases; leave hero indeterminate.
    if (!(err instanceof ApiError) || err.status !== 404) { /* transient */ }
  }
  updateHeroProgress(id, snap);
  progTimer = setTimeout(() => pollProgress(id), PROG_POLL_MS);
}

function renderHero(run) {
  const host = heroEl();
  if (!host) return;
  if (host.dataset.runId === run.id) return; // already showing; progress updates in place
  host.dataset.runId = run.id;
  host.innerHTML = `
    <div class="hero card">
      <div class="hero-head"><span class="badge badge-running">RUNNING</span>
        <strong>${run.command}</strong><span class="muted hero-elapsed"></span></div>
      <div class="progress"><div class="progress-fill" style="width:0%"></div></div>
      <ol class="phases"></ol>
    </div>`;
  startElapsed(host, run.started_at);
}

function updateHeroProgress(id, snap) {
  const host = heroEl();
  if (!host || host.dataset.runId !== id) return;
  const fill = host.querySelector(".progress-fill");
  const phases = host.querySelector(".phases");
  if (!snap) { // indeterminate
    host.querySelector(".progress").classList.add("indeterminate");
    return;
  }
  host.querySelector(".progress").classList.remove("indeterminate");
  if (fill) fill.style.width = `${snap.pct}%`;
  if (phases) {
    phases.innerHTML = (snap.phases || [])
      .map((p) => `<li class="phase phase-${p.state}">${p.name}</li>`)
      .join("");
  }
}

let elapsedTimer = null;
function startElapsed(host, startedAt) {
  clearInterval(elapsedTimer);
  const started = Date.parse(startedAt);
  const tick = () => {
    const s = Math.max(0, Math.floor((Date.now() - started) / 1000));
    const el = host.querySelector(".hero-elapsed");
    if (el) el.textContent = `  ${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
  };
  tick();
  elapsedTimer = setInterval(tick, 1000);
}

function clearHero() {
  const host = heroEl();
  if (host) { host.innerHTML = ""; delete host.dataset.runId; }
  clearTimeout(progTimer);
  clearInterval(elapsedTimer);
  watchedId = null;
}

export function watchRun(id) { watchedId = id; pollRuns(); }
export function startLive() { pollRuns(); }
```

- [ ] **Step 3: Start the controller once the shell shows**

In `app.js`, import and start it in `showShell()`:

```js
import { startLive } from "./live.js";
// ... inside showShell(), after route():
  startLive();
```

- [ ] **Step 4: Browser check**

With `serve` running, trigger a job (Jobs tab, e.g. Backtest dry-run). Expected: the hero appears with RUNNING + elapsed timer; for optimize the phased bar advances; on completion a toast fires and the hero clears; the Runs nav shows the live dot while running. (Local `serve` has no ECS `JobRunner`, so triggering returns 501 — to exercise the hero locally, seed a RUNNING ledger record + a `progress.json` by hand in `.lineup/runs`/`.lineup/progress`, or verify against a real deployed environment. Note this limitation in the commit.)

- [ ] **Step 5: Commit**

```bash
git add web/dashboard/live.js web/dashboard/app.js web/dashboard/index.html
git commit -m "feat(dashboard): live Now-Running hero + progress poller + toasts"
```

---

### Task 11: Trigger→watch flow + runs list polish

**Files:**
- Modify: `web/dashboard/jobs.js` (on 202, call `watchRun(id)` and navigate to a live context)
- Modify: `web/dashboard/runs.js` (status chips, durations, relative time — adopt the new primitives)

- [ ] **Step 1: Wire trigger→watch in jobs.js**

Find where `jobs.js` handles the `triggerJob` success (the 202 `{id, command, status}`). After a successful trigger, call:

```js
import { watchRun } from "./live.js";
// ... in the submit handler, after a successful api.triggerJob(...):
watchRun(res.id);
window.location.hash = "#runs";
```

So triggering drops the user onto the Runs view with the hero already tracking their run. Keep any existing success messaging.

- [ ] **Step 2: Polish runs.js**

In `runs.js`, render each run's status as a `.badge` (`badge-success`/`badge-failed`/`badge-running` — classes already exist in `style.css`), show a duration (`ended_at - started_at` when terminal, else live elapsed), and a relative "x min ago" for `started_at`. Keep the existing detail/output drill-in.

- [ ] **Step 3: Browser check**

Trigger a job → lands on Runs with the hero tracking it. Runs list shows status chips + durations. (Same local 501 caveat as Task 10 for the trigger itself.)

- [ ] **Step 4: Commit**

```bash
git add web/dashboard/jobs.js web/dashboard/runs.js
git commit -m "feat(dashboard): trigger->watch flow + runs list status chips/durations"
```

---

## Phase E — Glow-up

### Task 12: Design-system tokens + primitives + live components

**Files:**
- Modify: `web/dashboard/style.css`

- [ ] **Step 1: Extend the token set**

In `:root` (and the dark `@media` block) add, alongside the existing color vars: a spacing scale (`--s1:.25rem … --s5:2rem`), `--radius`, `--shadow`, and the chart palette `--c1..--c8` (copy the 8 hex values from the deleted valuereport `palette` — e.g. `#4e79a7,#f28e2b,#59a14f,#e15759,#76b7b2,#edc948,#b07aa1,#ff9da7`) so `chart.js:themeColors()` reads them. In dark mode, keep the same palette (it's colorblind-conscious and reads on both).

- [ ] **Step 2: Add primitives**

Add rules for: `.stat-tile` (big number + label, used by scorecard/standings), `.toggle-group` + `.toggle-group button[aria-pressed=true]` (the window/role/system/metric toggles), `.chart-box` (fixed-height canvas wrapper, e.g. `height:220px; position:relative`), and refine `.card`/`table`/`.badge` to use the new radius/shadow/spacing tokens. Refine `nav a.active` for a clearer active-tab treatment.

- [ ] **Step 3: Add live components**

```css
.hero { display: flex; flex-direction: column; gap: var(--s2); }
.hero-head { display: flex; align-items: center; gap: var(--s2); }
.progress { height: 8px; background: var(--border); border-radius: 999px; overflow: hidden; }
.progress-fill { height: 100%; background: var(--accent); transition: width .3s ease; }
.progress.indeterminate .progress-fill { width: 40% !important; animation: indet 1.2s ease-in-out infinite; }
@keyframes indet { 0% { margin-left: -40%; } 100% { margin-left: 100%; } }
.phases { list-style: none; display: flex; flex-wrap: wrap; gap: var(--s2); padding: 0; margin: 0; }
.phase { font-size: .8rem; color: var(--muted); }
.phase-active { color: var(--accent); font-weight: 600; }
.phase-done { color: var(--success); }
.phase-warn { color: var(--danger); }
#toasts { position: fixed; bottom: 1rem; right: 1rem; display: flex; flex-direction: column; gap: .5rem; z-index: 50; }
.toast { padding: .5rem .9rem; border-radius: var(--radius); box-shadow: var(--shadow); background: var(--bg); border: 1px solid var(--border); }
.toast-ok { border-left: 3px solid var(--success); }
.toast-fail { border-left: 3px solid var(--danger); }
nav a.has-live::after { content: "●"; color: var(--accent); font-size: .6rem; margin-left: .25rem; vertical-align: middle; }
```

- [ ] **Step 4: Cross-tab visual pass**

Reload the SPA. Walk every tab (Lineup, Jobs, Runs, Projections, Value, Passkeys) in both light and dark (toggle OS theme). Expected: consistent cards/tiles/badges/spacing; charts recolor with theme; hero + toasts styled. Fix any tab that looks off by nudging shared primitives (not per-tab overrides).

- [ ] **Step 5: Commit**

```bash
git add web/dashboard/style.css
git commit -m "feat(dashboard): design-system glow-up + live progress/toast styles"
```

> **Aesthetic direction:** before this task, load the `frontend-design` skill for palette/typography/spacing guidance so the glow-up reads as intentional, not default. Keep it a personal-tool aesthetic (clean, dense, legible) — this plan fixes structure; the skill informs taste.

---

## Phase F — Docs

### Task 13: Update README, CLAUDE.md, aws-deployment.md

**Files:**
- Modify: `README.md`, `CLAUDE.md`, `docs/aws-deployment.md`

- [ ] **Step 1: README**

Update the local-dev note: `projection-site --out web/dashboard/report` now writes `model.json` + `value.json` (not HTML) that the native `#projections`/`#value` tabs fetch. Document the new `GET /v1/runs/{id}/progress` endpoint and the live-progress hero. Remove any "iframe" phrasing.

- [ ] **Step 2: CLAUDE.md**

In the `internal/report` / `internal/valuereport` sections: they now produce Models serialized to JSON sidecars consumed by the SPA (not self-contained HTML); the SPA is the render path. Add to the `internal/progress` / lineupapi description: the `progress.Recorder` hook → `runs/<id>/progress.json` (S3 via `s3lineup.NewProgress`, local via `FileProgressStore`), surfaced by `GET /v1/runs/{id}/progress`; run status stays ledger-owned; only `optimize` emits phases (other jobs → indeterminate hero).

- [ ] **Step 3: aws-deployment.md**

Adjust the report-publish bullet if it names `index.html`/`value.html` — the `report/` prefix now carries `model.json`/`value.json`. No infra change otherwise (progress reuses the `runs/` prefix; no new IAM).

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md docs/aws-deployment.md
git commit -m "docs: dashboard v2 (native reports JSON, live progress endpoint)"
```

---

## Final verification (after all tasks)

- [ ] `go build ./... && go vet ./... && go mod tidy` — clean.
- [ ] `go test ./...` — all pass (progress, lineupapi, s3lineup, report, valuereport).
- [ ] `make run-all` — completes; `projection-site` writes JSON; cache behavior unchanged.
- [ ] Local SPA pass via `serve --web` + `projection-site --out web/dashboard/report`: Projections + Value render natively with working toggles; missing/empty states friendly; live hero exercised (seeded or against deployed env); light/dark cohesive across all tabs.
- [ ] `bd close rosterbot-ku4` after merge.

## Self-review notes (coverage vs spec)

- Spec A (reports JSON pipeline) → Task 1. B (SPA report views) → Tasks 6–9. C (live progress backend) → Tasks 2–5. D (live UX + glow-up) → Tasks 10–12. Docs → Task 13.
- Spec decision 6 (status ledger-owned, progress is detail-only) → enforced in live.js (status from `api.runs`, phase from `runProgress`) and the `Global Constraints`.
- Spec "other 8 jobs coarse" → realized as 404-progress → indeterminate hero (Task 10), no per-command changes.
- No infra/IAM change: progress reuses `runs/` prefix (Tasks 4–5), stated in constraints + Task 13.
