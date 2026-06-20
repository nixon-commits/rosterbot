# Per-Run Structured Output Capture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GET /v1/runs/{id}/output` returning a typed, job-specific JSON payload so the iOS client can render per-job result views (prospects, waivers, claims, transactions, gs-check, backtest, grade).

**Architecture:** Each job, after computing its result and before writing stdout, builds a snake_case wire struct (owned by `internal/lineupapi`) and hands it to a nil-safe global `lineupapi.RecordOutput(jobType, data)` hook. A `cmd` installer (mirroring `installNotificationRecorder`) wires that hook to an `OutputStore` — S3 (`runs/<id>/output.json`) when `STATE_BUCKET` is set, else local file — keyed by the `RUN_ID` env var that `entrypoint.sh` exports. The Lambda handler gains an `Output` store and a route that serves the stored bytes (404 when absent, 501 when unwired). The run-ledger listing is hardened to ignore the new `runs/<id>/...` sub-keys.

**Tech Stack:** Go, `net/http` (stdlib mux), aws-sdk-go-v2 (S3), existing `internal/lineupapi` + `internal/lineupapi/s3lineup` packages.

---

## File Structure

**New files:**
- `internal/lineupapi/output.go` — wire Result structs (one per job), `RunOutput` envelope, `MarshalOutput`, the `OutputStore`/`OutputWriter` interfaces, the `FileOutputStore` local adapter, and the global `RecordOutput` hook.
- `internal/lineupapi/output_test.go` — marshal/envelope + FileOutputStore round-trip tests.
- `internal/lineupapi/s3lineup/output.go` — S3 `OutputStore` adapter (`runs/<id>/output.json`).
- `internal/lineupapi/s3lineup/output_test.go` — S3 adapter round-trip via a fake S3 api.
- `internal/<job>/output.go` for `prospects`, `waivers`, `claims`, `transactions`, `gscheck` — the domain→wire mapper + its unit test (`output_test.go`).
- `cmd/output.go` — `installOutputRecorder()`.

**Modified files:**
- `internal/lineupapi/handler.go` — add `Output OutputStore` to `Config`, register `GET /v1/runs/{id}/output`, add `handleRunOutput`.
- `internal/lineupapi/handler_test.go` — output-route tests (round-trip per job, 404, 501).
- `internal/lineupapi/s3lineup/runs.go` — harden `recent()` to skip non-ledger sub-keys.
- `internal/lineupapi/runs.go` — harden `FileRunStore.records()` the same way (defensive parity).
- `internal/prospects/run.go`, `internal/waivers/run.go`, `internal/claims/run.go`, `internal/transactions/transactions.go`, `internal/gscheck/gscheck.go` — call `lineupapi.RecordOutput` after the result is built.
- `cmd/backtest.go`, `cmd/grade.go` — build + record the backtest/grade wire result.
- `cmd/root.go` — call `installOutputRecorder()` next to `installNotificationRecorder()`.
- `lambda/main.go` — construct the S3 output store and pass it as `Config.Output`.
- `docs/ios-api-contract.md` — new `GET /v1/runs/{id}/output` section documenting every shape.

---

## Task 1: Wire contract types + envelope + marshal

**Files:**
- Create: `internal/lineupapi/output.go`
- Test: `internal/lineupapi/output_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/lineupapi/output_test.go
package lineupapi

import (
	"encoding/json"
	"testing"
)

func TestMarshalOutputEnvelope(t *testing.T) {
	data := WaiversResult{
		Picks: []WaiverPickOut{{
			Name: "Jane Doe", Team: "BAL", Pos: "OF", Signal: "HOT",
			ProjectedFPG: 4.2, Rank: 1,
		}},
		Total: 1,
	}
	b, err := MarshalOutput("waivers", data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != "waivers" {
		t.Fatalf("type = %q, want waivers", env.Type)
	}
	var got WaiversResult
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(got.Picks) != 1 || got.Picks[0].Name != "Jane Doe" || got.Picks[0].Signal != "HOT" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lineupapi/ -run TestMarshalOutputEnvelope -v`
Expected: FAIL — `undefined: WaiversResult` / `undefined: MarshalOutput`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/lineupapi/output.go
package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// RunOutput is the GET /v1/runs/{id}/output body: a job-type discriminator plus
// the job-specific result object. Stored verbatim at runs/<id>/output.json.
type RunOutput struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// MarshalOutput serializes a job result into the {type, data} envelope. Indented
// for curl-ability; the iOS decoder is whitespace-agnostic.
func MarshalOutput(jobType string, data any) ([]byte, error) {
	return json.MarshalIndent(RunOutput{Type: jobType, Data: data}, "", "  ")
}

// --- Per-job wire results (snake_case; decoded by the iOS client) ---

// ProspectsResult is the prospects job output. Alerts carry a `kind` the client
// partitions into call-up vs breakout views; upgrades are drop→add suggestions.
type ProspectsResult struct {
	Alerts   []ProspectAlertOut   `json:"alerts"`
	Upgrades []ProspectUpgradeOut `json:"upgrades"`
}

type ProspectAlertOut struct {
	Name     string `json:"name"`
	Team     string `json:"team"`
	Pos      string `json:"pos,omitempty"`
	Kind     string `json:"kind"`     // called-up|optioned|performance-hot|performance-cold|free-agent-buzz|upgrade-available
	Priority string `json:"priority"` // high|medium|low
	Detail   string `json:"detail"`
	Rank     int    `json:"rank,omitempty"`
}

type ProspectUpgradeOut struct {
	Source   string `json:"source"`
	Drop     string `json:"drop"`
	DropRank int    `json:"drop_rank"`
	Add      string `json:"add"`
	AddRank  int    `json:"add_rank"`
	RankGap  int    `json:"rank_gap"`
	NearTerm bool   `json:"near_term"`
}

// WaiversResult is the waivers job output.
type WaiversResult struct {
	Picks []WaiverPickOut `json:"picks"`
	Total int             `json:"total"`
}

type WaiverPickOut struct {
	Name         string  `json:"name"`
	Team         string  `json:"team"`
	Pos          string  `json:"pos"`
	IsPitcher    bool    `json:"is_pitcher"`
	Signal       string  `json:"signal,omitempty"` // BUY-LOW|HOT|BOTH
	ProjectedFPG float64 `json:"projected_pts_per_game"`
	DropName     string  `json:"drop_name,omitempty"`
	Gap          float64 `json:"gap,omitempty"`
	Xwoba        float64 `json:"xwoba,omitempty"`
	Woba         float64 `json:"woba,omitempty"`
	BarrelPct    float64 `json:"barrel_pct,omitempty"`
	HardHitPct   float64 `json:"hard_hit_pct,omitempty"`
	Era          float64 `json:"era,omitempty"`
	Xera         float64 `json:"xera,omitempty"`
	Rank         int     `json:"rank"`
}

// ClaimsResult is the claims job output.
type ClaimsResult struct {
	Claims []ClaimOut `json:"claims"`
}

type ClaimOut struct {
	Team      string `json:"team"`
	ClaimType string `json:"claim_type"` // FA|WW
	Added     string `json:"added"`
	AddedPos  string `json:"added_pos,omitempty"`
	Dropped   string `json:"dropped,omitempty"`
	NetValue  int    `json:"net_value"`
	Signal    string `json:"signal,omitempty"`
}

// TransactionsResult is the transactions (trade monitor) job output.
type TransactionsResult struct {
	Trades []TradeOut `json:"trades"`
}

type TradeOut struct {
	Teams       []string         `json:"teams"`
	Players     []TradePlayerOut `json:"players"`
	ProcessedAt string           `json:"processed_at"`
}

type TradePlayerOut struct {
	Name      string `json:"name"`
	FromTeam  string `json:"from_team"`
	Pos       string `json:"pos,omitempty"`
	Valuation int    `json:"valuation"`
}

// GSCheckResult is the gs-check job output.
type GSCheckResult struct {
	Period     string           `json:"period,omitempty"`
	Violations []GSViolationOut `json:"violations"`
}

type GSViolationOut struct {
	Team   string `json:"team"`
	Kind   string `json:"kind"` // over|under
	Used   int    `json:"used"`
	Limit  int    `json:"limit"`
	OverBy int    `json:"over_by,omitempty"`
}

// BacktestResult is the backtest job output.
type BacktestResult struct {
	Start    string             `json:"start"`
	End      string             `json:"end"`
	Days     []BacktestDayOut   `json:"days"`
	Accuracy *BacktestAccuracy  `json:"accuracy,omitempty"`
}

type BacktestDayOut struct {
	Date    string  `json:"date"`
	Actual  float64 `json:"actual"`
	Optimal float64 `json:"optimal"`
	Gap     float64 `json:"gap"`
}

type BacktestAccuracy struct {
	MAE        float64              `json:"mae"`
	Bias       float64              `json:"bias"`
	RMSE       float64              `json:"rmse"`
	N          int                  `json:"n"`
	ByPosition []BacktestPositionOut `json:"by_position,omitempty"`
}

type BacktestPositionOut struct {
	Bucket string  `json:"bucket"`
	N      int     `json:"n"`
	MAE    float64 `json:"mae"`
	Bias   float64 `json:"bias"`
}

// GradeResult is the grade job output (what was written to the Analysis Store).
type GradeResult struct {
	Dates       []string `json:"dates"`
	RowsWritten int      `json:"rows_written"`
}

// --- Store interfaces + local file adapter + global hook ---

// OutputStore is the read side for captured job output: fetch the stored bytes
// for a run id. ok=false means 404; err means a backend failure (502).
type OutputStore interface {
	GetOutput(ctx context.Context, runID string) ([]byte, bool, error)
}

// OutputWriter is the write side: persist the marshaled envelope for a run id.
type OutputWriter interface {
	PutOutput(ctx context.Context, runID string, data []byte) error
}

// FileOutputStore is a local-filesystem OutputStore+OutputWriter, one file per
// run at <dir>/<runID>.json. Used by `serve` and local job runs.
type FileOutputStore struct {
	dir string
}

func NewFileOutputStore(dir string) *FileOutputStore { return &FileOutputStore{dir: dir} }

func (s *FileOutputStore) path(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

func (s *FileOutputStore) GetOutput(_ context.Context, runID string) ([]byte, bool, error) {
	data, err := os.ReadFile(s.path(runID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *FileOutputStore) PutOutput(_ context.Context, runID string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(runID), data, 0o644)
}

// OutputRecorder is a nil-safe global hook (mirrors notify.Recorder). cmd sets
// it to a closure that marshals {type,data} and writes it under the RUN_ID env
// var. Jobs call RecordOutput; when the hook is unset (tests, local runs without
// a run id) the call is a no-op, so nothing else has to change.
var OutputRecorder func(jobType string, data any)

// RecordOutput hands a job's typed result to the installed recorder. Safe to
// call unconditionally; no-op when no recorder is installed.
func RecordOutput(jobType string, data any) {
	if OutputRecorder != nil {
		OutputRecorder(jobType, data)
	}
}

var (
	_ OutputStore  = (*FileOutputStore)(nil)
	_ OutputWriter = (*FileOutputStore)(nil)
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lineupapi/ -run TestMarshalOutputEnvelope -v`
Expected: PASS.

- [ ] **Step 5: Add the FileOutputStore round-trip test**

```go
// append to internal/lineupapi/output_test.go
func TestFileOutputStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileOutputStore(dir)
	if _, ok, _ := s.GetOutput(context.Background(), "run-1"); ok {
		t.Fatal("expected miss before write")
	}
	body, _ := MarshalOutput("gs-check", GSCheckResult{Violations: []GSViolationOut{{Team: "X", Kind: "over", Used: 6, Limit: 5, OverBy: 1}}})
	if err := s.PutOutput(context.Background(), "run-1", body); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetOutput(context.Background(), "run-1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if string(got) != string(body) {
		t.Fatalf("bytes mismatch")
	}
}
```

Add `"context"` to the test imports.

- [ ] **Step 6: Run + commit**

Run: `go test ./internal/lineupapi/ -run 'TestMarshalOutputEnvelope|TestFileOutputStoreRoundTrip' -v`
Expected: PASS.

```bash
git add internal/lineupapi/output.go internal/lineupapi/output_test.go
git commit -m "feat(api): run-output wire types, envelope, file store, RecordOutput hook"
```

---

## Task 2: Handler route `GET /v1/runs/{id}/output`

**Files:**
- Modify: `internal/lineupapi/handler.go`
- Test: `internal/lineupapi/handler_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// append to internal/lineupapi/handler_test.go

// fakeOutput is an in-memory OutputStore for handler tests.
type fakeOutput struct {
	data map[string][]byte
	err  error
}

func (f fakeOutput) GetOutput(_ context.Context, runID string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	d, ok := f.data[runID]
	return d, ok, nil
}

func TestRunOutputRoundTripPerJob(t *testing.T) {
	samples := map[string][]byte{}
	add := func(id, jobType string, data any) {
		b, err := MarshalOutput(jobType, data)
		if err != nil {
			t.Fatalf("marshal %s: %v", jobType, err)
		}
		samples[id] = b
	}
	add("r-prospects", "prospects", ProspectsResult{Alerts: []ProspectAlertOut{{Name: "A", Team: "BAL", Kind: "called-up", Priority: "high", Detail: "promoted"}}})
	add("r-waivers", "waivers", WaiversResult{Picks: []WaiverPickOut{{Name: "B", Team: "NYY", Pos: "OF", Signal: "HOT", ProjectedFPG: 4.1, Rank: 1}}, Total: 1})
	add("r-claims", "claims", ClaimsResult{Claims: []ClaimOut{{Team: "T", ClaimType: "FA", Added: "C", NetValue: 3}}})
	add("r-transactions", "transactions", TransactionsResult{Trades: []TradeOut{{Teams: []string{"a", "b"}, ProcessedAt: "2026-06-20T00:00:00Z"}}})
	add("r-gs-check", "gs-check", GSCheckResult{Violations: []GSViolationOut{{Team: "X", Kind: "over", Used: 6, Limit: 5, OverBy: 1}}})
	add("r-backtest", "backtest", BacktestResult{Start: "2026-06-08", End: "2026-06-14", Days: []BacktestDayOut{{Date: "2026-06-08", Actual: 40, Optimal: 42, Gap: -2}}})
	add("r-grade", "grade", GradeResult{Dates: []string{"2026-06-19"}, RowsWritten: 12})

	h := Handler(Config{Token: "t", Output: fakeOutput{data: samples}})
	for id, want := range samples {
		rec := do(h, http.MethodGet, "/v1/runs/"+id+"/output")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", id, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s: content-type = %q", id, ct)
		}
		if rec.Body.String() != string(want) {
			t.Fatalf("%s: body mismatch", id)
		}
	}
}

func TestRunOutputNotFound(t *testing.T) {
	h := Handler(Config{Token: "t", Output: fakeOutput{data: map[string][]byte{}}})
	if rec := do(h, http.MethodGet, "/v1/runs/missing/output"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRunOutputNotImplementedWhenNil(t *testing.T) {
	h := Handler(Config{Token: "t"}) // no Output store wired
	if rec := do(h, http.MethodGet, "/v1/runs/x/output"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/lineupapi/ -run TestRunOutput -v`
Expected: FAIL — `unknown field Output in struct literal` / route 404→ actually returns the run-detail handler.

- [ ] **Step 3: Implement — add field, route, handler**

In `internal/lineupapi/handler.go`, add to `Config`:

```go
type Config struct {
	Token         string
	Lineups       ObjectStore
	Runs          RunStore
	Jobs          JobRunner
	Notifications NotificationStore
	Output        OutputStore
}
```

Register the route in `Handler` (place it before the `{id}` route is irrelevant — stdlib mux matches the more specific pattern, but add it adjacent for clarity):

```go
	mux.HandleFunc("GET /v1/runs/{id}", cfg.handleRunDetail)
	mux.HandleFunc("GET /v1/runs/{id}/output", cfg.handleRunOutput)
```

Add the handler:

```go
func (cfg Config) handleRunOutput(w http.ResponseWriter, r *http.Request) {
	if cfg.Output == nil {
		writeErr(w, http.StatusNotImplemented, "run output not configured")
		return
	}
	id := r.PathValue("id")
	data, ok, err := cfg.Output.GetOutput(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "run output unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no output for run")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/lineupapi/ -run TestRunOutput -v`
Expected: PASS (all three).

- [ ] **Step 5: Full package + commit**

Run: `go test ./internal/lineupapi/...`
Expected: PASS.

```bash
git add internal/lineupapi/handler.go internal/lineupapi/handler_test.go
git commit -m "feat(api): GET /v1/runs/{id}/output route"
```

---

## Task 3: S3 output store + harden ledger listing

**Files:**
- Create: `internal/lineupapi/s3lineup/output.go`
- Test: `internal/lineupapi/s3lineup/output_test.go`
- Modify: `internal/lineupapi/s3lineup/runs.go` (`recent()`)
- Modify: `internal/lineupapi/runs.go` (`FileRunStore.records()`)

- [ ] **Step 1: Write the failing S3 adapter test**

Check `internal/lineupapi/s3lineup/` for an existing fake S3 `api` in `*_test.go`. If one exists, reuse it; otherwise add this minimal fake in the new test file:

```go
// internal/lineupapi/s3lineup/output_test.go
package s3lineup

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeS3 struct{ objects map[string][]byte }

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objects[*in.Key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	b, _ := io.ReadAll(in.Body)
	f.objects[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func TestOutputStoreRoundTrip(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &OutputStore{client: f, bucket: "b", prefix: "runs/"}

	if _, ok, _ := s.GetOutput(context.Background(), "abc"); ok {
		t.Fatal("expected miss")
	}
	if err := s.PutOutput(context.Background(), "abc", []byte(`{"type":"grade","data":{}}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, stored := f.objects["runs/abc/output.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", keys(f.objects))
	}
	got, ok, err := s.GetOutput(context.Background(), "abc")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if string(got) != `{"type":"grade","data":{}}` {
		t.Fatalf("bytes mismatch: %s", got)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

> NOTE during execution: if `s3lineup` tests already declare a `fakeS3`/`api` fake, delete the duplicate above and use the existing one (and matching field names on the store struct).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/lineupapi/s3lineup/ -run TestOutputStoreRoundTrip -v`
Expected: FAIL — `undefined: OutputStore`.

- [ ] **Step 3: Implement the S3 adapter**

```go
// internal/lineupapi/s3lineup/output.go
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

// OutputStore reads/writes captured job output at <prefix><runID>/output.json.
// The per-id sub-path keeps each run's output beside (but distinct from) its
// ledger record under the same runs/ prefix; the ledger listing skips these.
type OutputStore struct {
	client api
	bucket string
	prefix string
}

// NewOutput builds an OutputStore. prefix should end in "/", e.g. "runs/".
func NewOutput(ctx context.Context, bucket, prefix string) (*OutputStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &OutputStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *OutputStore) objKey(runID string) string { return s.prefix + runID + "/output.json" }

func (s *OutputStore) GetOutput(ctx context.Context, runID string) ([]byte, bool, error) {
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

func (s *OutputStore) PutOutput(ctx context.Context, runID string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey(runID)),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

var (
	_ lineupapi.OutputStore  = (*OutputStore)(nil)
	_ lineupapi.OutputWriter = (*OutputStore)(nil)
)
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/lineupapi/s3lineup/ -run TestOutputStoreRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Harden the ledger listing (write the regression test first)**

```go
// internal/lineupapi/s3lineup/output_test.go — append
func TestRunsListIgnoresOutputSubKeys(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{
		"runs/9999999999-abc.json":  []byte(`{"id":"abc","status":"SUCCESS","started_at":"2026-06-20T00:00:00Z"}`),
		"runs/abc/output.json":      []byte(`{"type":"grade","data":{}}`),
	}}
	// listAPI requires ListObjectsV2; extend the fake inline.
	lf := &listFakeS3{fakeS3: f}
	s := &RunsStore{client: lf, bucket: "b", prefix: "runs/"}
	runs, err := s.List(context.Background(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "abc" {
		t.Fatalf("want exactly the ledger run, got %+v", runs)
	}
}

type listFakeS3 struct {
	*fakeS3
}

func (f *listFakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	var contents []types.Object
	for k := range f.objects {
		k := k
		contents = append(contents, types.Object{Key: &k})
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}
```

Add `"github.com/aws/aws-sdk-go-v2/service/s3/types"` (already imported) usage. Run it — it FAILS (2 runs, one a zero-value `id:""`... actually `abc` plus an empty-id phantom):

Run: `go test ./internal/lineupapi/s3lineup/ -run TestRunsListIgnoresOutputSubKeys -v`
Expected: FAIL — phantom run from `runs/abc/output.json`.

- [ ] **Step 6: Implement the guard in `recent()`**

In `internal/lineupapi/s3lineup/runs.go`, inside `recent()`, change the key-collection loop to skip sub-keys (anything with a `/` after the prefix):

```go
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		if o.Key == nil {
			continue
		}
		// Ledger records are <prefix><invts>-<id>.json (flat). Skip per-run
		// sub-objects like <prefix><id>/output.json so they don't decode as
		// phantom zero-value runs.
		if strings.Contains(strings.TrimPrefix(*o.Key, s.prefix), "/") {
			continue
		}
		keys = append(keys, *o.Key)
	}
```

Add `"strings"` to the imports in `runs.go`.

- [ ] **Step 7: Mirror the guard in FileRunStore.records()**

In `internal/lineupapi/runs.go`, `records()` already filters to `run-*.json` in a flat dir, so file output (stored elsewhere by `FileOutputStore`) cannot collide. No change required — confirm by reading the filter. (If `FileOutputStore`'s dir were ever nested under the runs dir, the `run-` prefix filter still excludes `<id>.json`.) Leave a one-line comment noting the invariant:

```go
		// Only ledger records (run-*.json); other files in dir are ignored.
```

- [ ] **Step 8: Run + commit**

Run: `go test ./internal/lineupapi/...`
Expected: PASS.

```bash
git add internal/lineupapi/s3lineup/output.go internal/lineupapi/s3lineup/output_test.go internal/lineupapi/s3lineup/runs.go internal/lineupapi/runs.go
git commit -m "feat(api): S3 output store; harden run ledger listing against sub-keys"
```

---

## Task 4: cmd installer + Lambda wiring

**Files:**
- Create: `cmd/output.go`
- Modify: `cmd/root.go` (call installer)
- Modify: `lambda/main.go` (construct + pass store)

- [ ] **Step 1: Implement the installer**

```go
// cmd/output.go
package cmd

import (
	"context"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
)

// installOutputRecorder wires lineupapi.RecordOutput so each job persists its
// typed result under the current RUN_ID. Best-effort: a missing RUN_ID or a
// store error never affects the job. STATE_BUCKET -> S3 (runs/<id>/output.json);
// otherwise local .lineup/outputs/<id>.json. Mirrors installNotificationRecorder.
func installOutputRecorder() {
	runID := os.Getenv("RUN_ID")
	if runID == "" {
		return // no id to key on (local non-task run); leave the hook unset (no-op)
	}

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

	lineupapi.OutputRecorder = func(jobType string, data any) {
		body, err := lineupapi.MarshalOutput(jobType, data)
		if err != nil {
			return
		}
		_ = w.PutOutput(context.Background(), runID, body)
	}
}
```

- [ ] **Step 2: Call it from root**

In `cmd/root.go`, find where `installNotificationRecorder()` is called (in `initApp` or the persistent pre-run) and add `installOutputRecorder()` right after it.

Run: `grep -n "installNotificationRecorder" cmd/root.go`
Then add the sibling call at that site.

- [ ] **Step 3: Wire the Lambda read side**

In `lambda/main.go`, after the `notifs` store is built, add:

```go
	output, err := s3lineup.NewOutput(ctx, bucket, "runs/")
	if err != nil {
		log.Fatalf("init s3 output store: %v", err)
	}
```

and add `Output: output,` to the `lineupapi.Config{...}` literal.

- [ ] **Step 4: Build everything**

Run: `go build ./... && (cd lambda && go build ./...)`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/output.go cmd/root.go lambda/main.go
git commit -m "feat(api): install output recorder in cmd; wire output store into Lambda"
```

---

## Task 5: prospects output

**Files:**
- Create: `internal/prospects/output.go`, `internal/prospects/output_test.go`
- Modify: `internal/prospects/run.go`

- [ ] **Step 1: Write the failing mapper test**

```go
// internal/prospects/output_test.go
package prospects

import (
	"testing"
	"time"
)

func TestToWireResult(t *testing.T) {
	r := Report{
		Date: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC),
		Alerts: []ProspectAlert{
			{Kind: CalledUp, Priority: "high", PlayerName: "Jackson Holliday", MLBTeam: "BAL", Position: "SS", Detail: "promoted to MLB", Rank: 1},
		},
		Upgrades: []UpgradeSet{
			{Source: "FanGraphs", Candidates: []UpgradeCandidate{
				{Drop: RankedProspect{Name: "Old Guy", Rank: 80}, Add: RankedProspect{Name: "New Guy", Rank: 12, ETA: "2026"}, RankGap: 68, NearTerm: true},
			}},
		},
	}
	out := toWireResult(r)
	if len(out.Alerts) != 1 || out.Alerts[0].Name != "Jackson Holliday" || out.Alerts[0].Kind != "called-up" {
		t.Fatalf("alerts: %+v", out.Alerts)
	}
	if len(out.Upgrades) != 1 || out.Upgrades[0].Add != "New Guy" || out.Upgrades[0].RankGap != 68 || !out.Upgrades[0].NearTerm {
		t.Fatalf("upgrades: %+v", out.Upgrades)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/prospects/ -run TestToWireResult -v`
Expected: FAIL — `undefined: toWireResult`.

- [ ] **Step 3: Implement the mapper**

```go
// internal/prospects/output.go
package prospects

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult flattens the prospect Report into the iOS wire shape. Alerts keep
// their kind so the client can split call-ups from breakouts; upgrades flatten
// the drop→add pair to names+ranks.
func toWireResult(r Report) lineupapi.ProspectsResult {
	out := lineupapi.ProspectsResult{}
	for _, a := range r.Alerts {
		out.Alerts = append(out.Alerts, lineupapi.ProspectAlertOut{
			Name:     a.PlayerName,
			Team:     a.MLBTeam,
			Pos:      a.Position,
			Kind:     string(a.Kind),
			Priority: a.Priority,
			Detail:   a.Detail,
			Rank:     a.Rank,
		})
	}
	for _, set := range r.Upgrades {
		for _, u := range set.Candidates {
			out.Upgrades = append(out.Upgrades, lineupapi.ProspectUpgradeOut{
				Source:   set.Source,
				Drop:     u.Drop.Name,
				DropRank: u.Drop.Rank,
				Add:      u.Add.Name,
				AddRank:  u.Add.Rank,
				RankGap:  u.RankGap,
				NearTerm: u.NearTerm,
			})
		}
	}
	return out
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/prospects/ -run TestToWireResult -v`
Expected: PASS.

- [ ] **Step 5: Record from run.go**

In `internal/prospects/run.go`, in `RunProspectReport`, immediately after `report := Report{...}` is constructed and BEFORE `printReport(...)`:

```go
	lineupapi.RecordOutput("prospects", toWireResult(report))
```

Add `"github.com/nixon-commits/rosterbot/internal/lineupapi"` to the imports.

- [ ] **Step 6: Build, test, commit**

Run: `go build ./... && go test ./internal/prospects/...`
Expected: PASS.

```bash
git add internal/prospects/output.go internal/prospects/output_test.go internal/prospects/run.go
git commit -m "feat(api): capture prospects run output"
```

---

## Task 6: waivers output

**Files:**
- Create: `internal/waivers/output.go`, `internal/waivers/output_test.go`
- Modify: `internal/waivers/run.go`

- [ ] **Step 1: Write the failing mapper test**

```go
// internal/waivers/output_test.go
package waivers

import "testing"

func TestToWireResult(t *testing.T) {
	r := Report{
		Total: 2,
		Top: []Candidate{
			{Name: "Hitter X", MLBTeam: "BAL", Position: "OF", Signal: SignalHot, ProjectedFPG: 4.2,
				WOBA: 0.360, XwOBA: 0.400, Barrel: 14, HardHit: 48, DropName: "Bench Y", Gap: 1.1},
			{Name: "Pitcher Z", MLBTeam: "NYY", Position: "SP", IsPitcher: true, Signal: SignalBuyLow,
				ProjectedFPG: 9.5, ERA: 4.5, XERA: 3.2},
		},
	}
	out := toWireResult(r)
	if out.Total != 2 || len(out.Picks) != 2 {
		t.Fatalf("counts: %+v", out)
	}
	if out.Picks[0].Rank != 1 || out.Picks[0].Signal != "HOT" || out.Picks[0].BarrelPct != 14 {
		t.Fatalf("pick0: %+v", out.Picks[0])
	}
	if out.Picks[1].Rank != 2 || !out.Picks[1].IsPitcher || out.Picks[1].Xera != 3.2 {
		t.Fatalf("pick1: %+v", out.Picks[1])
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/waivers/ -run TestToWireResult -v`
Expected: FAIL — `undefined: toWireResult`.

- [ ] **Step 3: Implement the mapper**

```go
// internal/waivers/output.go
package waivers

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult maps the waiver Report to the iOS wire shape. Rank is the 1-based
// position in the already-sorted Top slice. Hitter/pitcher diagnostics are
// emitted as-is; omitempty drops the irrelevant set on the wire.
func toWireResult(r Report) lineupapi.WaiversResult {
	out := lineupapi.WaiversResult{Total: r.Total}
	for i, c := range r.Top {
		out.Picks = append(out.Picks, lineupapi.WaiverPickOut{
			Name:         c.Name,
			Team:         c.MLBTeam,
			Pos:          c.Position,
			IsPitcher:    c.IsPitcher,
			Signal:       c.Signal.String(),
			ProjectedFPG: c.ProjectedFPG,
			DropName:     c.DropName,
			Gap:          c.Gap,
			Xwoba:        c.XwOBA,
			Woba:         c.WOBA,
			BarrelPct:    c.Barrel,
			HardHitPct:   c.HardHit,
			Era:          c.ERA,
			Xera:         c.XERA,
			Rank:         i + 1,
		})
	}
	return out
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/waivers/ -run TestToWireResult -v`
Expected: PASS.

- [ ] **Step 5: Record from run.go**

In `internal/waivers/run.go`'s `Run`, locate where the `Report` value is assembled (the `Top`/`Total` report passed to the stdout printer / Pushover formatter). Immediately after it's built and before output:

```go
	lineupapi.RecordOutput("waivers", toWireResult(report))
```

Use the actual local variable name for the report (read `run.go` to confirm; it may be `rep` or inline — assign it to a named var if currently inlined). Add the `lineupapi` import.

- [ ] **Step 6: Build, test, commit**

Run: `go build ./... && go test ./internal/waivers/...`
Expected: PASS.

```bash
git add internal/waivers/output.go internal/waivers/output_test.go internal/waivers/run.go
git commit -m "feat(api): capture waivers run output"
```

---

## Task 7: claims output

**Files:**
- Create: `internal/claims/output.go`, `internal/claims/output_test.go`
- Modify: `internal/claims/run.go`

- [ ] **Step 1: Write the failing mapper test**

```go
// internal/claims/output_test.go
package claims

import "testing"

func TestToWireResult(t *testing.T) {
	led := Ledger{
		Date: "2026-06-20",
		Entries: []LedgerEntry{
			{Team: "Team A", ClaimType: "FA", NetValue: 3,
				Added:   LedgerPlayer{Name: "New SS", Pos: "SS", Signal: "HOT"},
				Dropped: &LedgerPlayer{Name: "Old SS", Pos: "SS"}},
			{Team: "Team B", ClaimType: "WW", NetValue: -1,
				Added: LedgerPlayer{Name: "Reliever", Pos: "RP"}},
		},
	}
	out := toWireResult(led)
	if len(out.Claims) != 2 {
		t.Fatalf("count: %+v", out)
	}
	if out.Claims[0].Added != "New SS" || out.Claims[0].Dropped != "Old SS" || out.Claims[0].Signal != "HOT" || out.Claims[0].ClaimType != "FA" {
		t.Fatalf("claim0: %+v", out.Claims[0])
	}
	if out.Claims[1].Dropped != "" || out.Claims[1].NetValue != -1 {
		t.Fatalf("claim1: %+v", out.Claims[1])
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/claims/ -run TestToWireResult -v`
Expected: FAIL — `undefined: toWireResult`.

- [ ] **Step 3: Implement the mapper**

```go
// internal/claims/output.go
package claims

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult maps the daily claims Ledger to the iOS wire shape (one row per
// added player; the first dropped player, if any, is attributed to the row).
func toWireResult(led Ledger) lineupapi.ClaimsResult {
	out := lineupapi.ClaimsResult{}
	for _, e := range led.Entries {
		c := lineupapi.ClaimOut{
			Team:      e.Team,
			ClaimType: e.ClaimType,
			Added:     e.Added.Name,
			AddedPos:  e.Added.Pos,
			NetValue:  e.NetValue,
			Signal:    e.Added.Signal,
		}
		if e.Dropped != nil {
			c.Dropped = e.Dropped.Name
		}
		out.Claims = append(out.Claims, c)
	}
	return out
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/claims/ -run TestToWireResult -v`
Expected: PASS.

- [ ] **Step 5: Record from run.go**

In `internal/claims/run.go`'s `Run`, find where the ledger is built (`BuildLedger(...)` — it already exists for the audit write). Reuse that value; right after it is created and before stdout/Pushover:

```go
	lineupapi.RecordOutput("claims", toWireResult(led))
```

If `BuildLedger` is currently called only inside the `!DryRun` write branch, hoist it so the ledger is built unconditionally (it is a pure transform of `moves`), then record. Add the `lineupapi` import.

- [ ] **Step 6: Build, test, commit**

Run: `go build ./... && go test ./internal/claims/...`
Expected: PASS.

```bash
git add internal/claims/output.go internal/claims/output_test.go internal/claims/run.go
git commit -m "feat(api): capture claims run output"
```

---

## Task 8: transactions output

**Files:**
- Create: `internal/transactions/output.go`, `internal/transactions/output_test.go`
- Modify: `internal/transactions/transactions.go`

- [ ] **Step 1: Write the failing mapper test**

```go
// internal/transactions/output_test.go
package transactions

import (
	"testing"
	"time"
)

func TestToWireResult(t *testing.T) {
	trades := []Trade{
		{
			ProcessedDate: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
			Sides: [2]TradeSide{
				{TeamName: "A", Players: []TradePlayer{{Name: "P1", Position: "OF", Value: 30}}},
				{TeamName: "B", Players: []TradePlayer{{Name: "P2", Position: "SP", Value: 25}}},
			},
		},
	}
	out := toWireResult(trades)
	if len(out.Trades) != 1 {
		t.Fatalf("count: %+v", out)
	}
	tr := out.Trades[0]
	if len(tr.Teams) != 2 || tr.Teams[0] != "A" || tr.Teams[1] != "B" {
		t.Fatalf("teams: %+v", tr.Teams)
	}
	if len(tr.Players) != 2 || tr.Players[0].FromTeam != "A" || tr.Players[1].Valuation != 25 {
		t.Fatalf("players: %+v", tr.Players)
	}
	if tr.ProcessedAt != "2026-06-20T12:00:00Z" {
		t.Fatalf("processed_at: %q", tr.ProcessedAt)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/transactions/ -run TestToWireResult -v`
Expected: FAIL — `undefined: toWireResult`.

- [ ] **Step 3: Implement the mapper**

```go
// internal/transactions/output.go
package transactions

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// toWireResult flattens grouped trades into the iOS wire shape. Each player
// carries the team they came FROM (their side); valuation is the HKB value.
func toWireResult(trades []Trade) lineupapi.TransactionsResult {
	out := lineupapi.TransactionsResult{}
	for _, tr := range trades {
		to := lineupapi.TradeOut{
			Teams:       []string{tr.Sides[0].TeamName, tr.Sides[1].TeamName},
			ProcessedAt: tr.ProcessedDate.UTC().Format(time.RFC3339),
		}
		for _, side := range tr.Sides {
			for _, p := range side.Players {
				to.Players = append(to.Players, lineupapi.TradePlayerOut{
					Name:      p.Name,
					FromTeam:  side.TeamName,
					Pos:       p.Position,
					Valuation: p.Value,
				})
			}
		}
		out.Trades = append(out.Trades, to)
	}
	return out
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/transactions/ -run TestToWireResult -v`
Expected: PASS.

- [ ] **Step 5: Record from CheckTrades**

In `internal/transactions/transactions.go`'s `CheckTrades`, after the `[]Trade` slice is fully built (the value passed to the report/Pushover formatter) and before sending/printing:

```go
	lineupapi.RecordOutput("transactions", toWireResult(trades))
```

Use the actual local variable name (read the function to confirm; it may be `trades` or similar). Add the `lineupapi` import.

- [ ] **Step 6: Build, test, commit**

Run: `go build ./... && go test ./internal/transactions/...`
Expected: PASS.

```bash
git add internal/transactions/output.go internal/transactions/output_test.go internal/transactions/transactions.go
git commit -m "feat(api): capture transactions run output"
```

---

## Task 9: gs-check output

**Files:**
- Create: `internal/gscheck/output.go`, `internal/gscheck/output_test.go`
- Modify: `internal/gscheck/gscheck.go`

- [ ] **Step 1: Write the failing mapper test**

```go
// internal/gscheck/output_test.go
package gscheck

import "testing"

func TestToWireResult(t *testing.T) {
	vs := []Violation{
		{TeamName: "Over Team", GSUsed: 7, Kind: ViolationMax},
		{TeamName: "Under Team", GSUsed: 2, Kind: ViolationMin},
	}
	out := toWireResult(vs, "Week 11", 5, 3)
	if out.Period != "Week 11" || len(out.Violations) != 2 {
		t.Fatalf("out: %+v", out)
	}
	if out.Violations[0].Kind != "over" || out.Violations[0].Limit != 5 || out.Violations[0].OverBy != 2 {
		t.Fatalf("v0: %+v", out.Violations[0])
	}
	if out.Violations[1].Kind != "under" || out.Violations[1].Limit != 3 || out.Violations[1].OverBy != 0 {
		t.Fatalf("v1: %+v", out.Violations[1])
	}
}
```

> Confirm the constant names during execution (`ViolationMax`/`ViolationMin`): `grep -n "ViolationMax\|ViolationMin\|ViolationKind" internal/gscheck/gscheck.go`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/gscheck/ -run TestToWireResult -v`
Expected: FAIL — `undefined: toWireResult`.

- [ ] **Step 3: Implement the mapper**

```go
// internal/gscheck/output.go
package gscheck

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult maps GS violations to the iOS wire shape. The limit and over_by
// are derived from the league max/min (the Violation itself carries only the
// used count and kind). For an "over" violation limit=gsMax and over_by =
// used-gsMax; for "under" limit=gsMin.
func toWireResult(vs []Violation, period string, gsMax, gsMin int) lineupapi.GSCheckResult {
	out := lineupapi.GSCheckResult{Period: period}
	for _, v := range vs {
		o := lineupapi.GSViolationOut{Team: v.TeamName, Used: v.GSUsed}
		switch v.Kind {
		case ViolationMax:
			o.Kind = "over"
			o.Limit = gsMax
			if v.GSUsed > gsMax {
				o.OverBy = v.GSUsed - gsMax
			}
		case ViolationMin:
			o.Kind = "under"
			o.Limit = gsMin
		}
		out.Violations = append(out.Violations, o)
	}
	return out
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/gscheck/ -run TestToWireResult -v`
Expected: PASS.

- [ ] **Step 5: Record from RunGSCheck**

In `internal/gscheck/gscheck.go`'s `RunGSCheck`, after the `[]Violation` slice is built and the period label + gsMax/gsMin are known (the same values handed to `BuildReport`), and before sending the notification:

```go
	lineupapi.RecordOutput("gs-check", toWireResult(violations, periodLabel, gsMax, gsMin))
```

Use the actual local variable names (read the function; the period label may be `periodLabel` and limits from `cfg`/env). Add the `lineupapi` import.

- [ ] **Step 6: Build, test, commit**

Run: `go build ./... && go test ./internal/gscheck/...`
Expected: PASS.

```bash
git add internal/gscheck/output.go internal/gscheck/output_test.go internal/gscheck/gscheck.go
git commit -m "feat(api): capture gs-check run output"
```

---

## Task 10: backtest + grade output (cmd-level)

**Files:**
- Create: `cmd/output_results.go`, `cmd/output_results_test.go`
- Modify: `cmd/backtest.go`, `cmd/grade.go`

> backtest/grade have no internal run.go — their typed results are assembled in `cmd/`. The mappers therefore live in `cmd` (pure funcs over `backtest.Report` / the grade `byDate` map), unit-tested directly.

- [ ] **Step 1: Write the failing mapper tests**

```go
// cmd/output_results_test.go
package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
)

func TestBacktestToWireResult(t *testing.T) {
	rep := backtest.Report{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		Lineup: []backtest.LineupDayResult{
			{Date: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC), ActualPts: 40, OptimalPts: 42, Gap: -2},
		},
		ProjectionSummary: &backtest.ProjectionSummary{MAE: 1.4, Bias: 0.3, RMSE: 2.1, TotalPlayerDays: 240,
			ByPosition: []backtest.PositionMAE{{Bucket: "OF", N: 50, MAE: 1.1, Bias: 0.2}}},
	}
	out := backtestToWireResult(rep)
	if out.Start != "2026-06-08" || out.End != "2026-06-14" || len(out.Days) != 1 {
		t.Fatalf("out: %+v", out)
	}
	if out.Days[0].Gap != -2 || out.Days[0].Actual != 40 {
		t.Fatalf("day0: %+v", out.Days[0])
	}
	if out.Accuracy == nil || out.Accuracy.MAE != 1.4 || out.Accuracy.N != 240 || len(out.Accuracy.ByPosition) != 1 {
		t.Fatalf("accuracy: %+v", out.Accuracy)
	}
}

func TestGradeToWireResult(t *testing.T) {
	byDate := map[string]int{"2026-06-18": 10, "2026-06-19": 12}
	out := gradeToWireResult(byDate)
	if out.RowsWritten != 22 {
		t.Fatalf("rows: %d", out.RowsWritten)
	}
	if len(out.Dates) != 2 || out.Dates[0] != "2026-06-18" || out.Dates[1] != "2026-06-19" {
		t.Fatalf("dates not sorted: %+v", out.Dates)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/ -run 'TestBacktestToWireResult|TestGradeToWireResult' -v`
Expected: FAIL — undefined funcs.

- [ ] **Step 3: Implement the mappers**

```go
// cmd/output_results.go
package cmd

import (
	"sort"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

const wireDate = "2006-01-02"

// backtestToWireResult maps a backtest.Report to the iOS wire shape: per-day
// actual/optimal/gap plus the projection-accuracy rollup when present.
func backtestToWireResult(rep backtest.Report) lineupapi.BacktestResult {
	out := lineupapi.BacktestResult{
		Start: rep.Start.UTC().Format(wireDate),
		End:   rep.End.UTC().Format(wireDate),
	}
	for _, d := range rep.Lineup {
		out.Days = append(out.Days, lineupapi.BacktestDayOut{
			Date:    d.Date.UTC().Format(wireDate),
			Actual:  d.ActualPts,
			Optimal: d.OptimalPts,
			Gap:     d.Gap,
		})
	}
	if s := rep.ProjectionSummary; s != nil {
		acc := &lineupapi.BacktestAccuracy{MAE: s.MAE, Bias: s.Bias, RMSE: s.RMSE, N: s.TotalPlayerDays}
		for _, p := range s.ByPosition {
			acc.ByPosition = append(acc.ByPosition, lineupapi.BacktestPositionOut{
				Bucket: p.Bucket, N: p.N, MAE: p.MAE, Bias: p.Bias,
			})
		}
		out.Accuracy = acc
	}
	return out
}

// gradeToWireResult summarizes what grade wrote: the sorted set of dates and the
// total graded-row count.
func gradeToWireResult(rowsByDate map[string]int) lineupapi.GradeResult {
	out := lineupapi.GradeResult{}
	for dt, n := range rowsByDate {
		out.Dates = append(out.Dates, dt)
		out.RowsWritten += n
	}
	sort.Strings(out.Dates)
	return out
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./cmd/ -run 'TestBacktestToWireResult|TestGradeToWireResult' -v`
Expected: PASS.

- [ ] **Step 5: Record from cmd/backtest.go**

Read `cmd/backtest.go` to find where the `backtest.Report` (or its pieces) is assembled for `FormatReport`. The lineup/projection results are combined into a `Report` (or assemble one from the in-scope `Lineup`/`ProjectionSummary` values). After it exists and before/after printing:

```go
	lineupapi.RecordOutput("backtest", backtestToWireResult(report))
```

If `cmd/backtest.go` doesn't currently materialize a single `backtest.Report`, construct one from the in-scope `lineupResults` + projection summary purely for this call. Add the `lineupapi` import to `cmd/backtest.go`.

- [ ] **Step 6: Record from cmd/grade.go**

In `cmd/grade.go`'s `runGrade`, the `byDate` map is `map[string][]analysis.GradeRow`. Build a count map and record after the grading loop (works for both dry-run and real paths — place it after `byDate` is fully populated, before the `if cfg.DryRun` branch):

```go
	counts := map[string]int{}
	for dt, rows := range byDate {
		counts[dt] = len(rows)
	}
	lineupapi.RecordOutput("grade", gradeToWireResult(counts))
```

Add the `lineupapi` import to `cmd/grade.go`.

- [ ] **Step 7: Build, test, commit**

Run: `go build ./... && go test ./cmd/...`
Expected: PASS.

```bash
git add cmd/output_results.go cmd/output_results_test.go cmd/backtest.go cmd/grade.go
git commit -m "feat(api): capture backtest + grade run output"
```

---

## Task 11: Documentation — ios-api-contract.md

**Files:**
- Modify: `docs/ios-api-contract.md`

- [ ] **Step 1: Add the endpoint section**

Insert a new `### GET /v1/runs/{id}/output` section after the `GET /v1/runs/{id}` section (after line ~82), documenting the envelope and every per-job `data` shape. Use the exact field names from `internal/lineupapi/output.go`. Content:

````markdown
### `GET /v1/runs/{id}/output`

Structured, typed result for a completed job — the data behind a per-job result
view. `404` for runs that produced no output (older runs, `optimize`, jobs that
finished before this existed). Distinct from `log_tail` (raw stdout), which stays
on `GET /v1/runs/{id}` for diagnostics.

```json
{ "type": "waivers", "data": { /* job-specific object, see below */ } }
```

- `type` exactly matches the job `name` from `GET /v1/jobs`.
- `data` is a job-specific object (snake_case). Decode generically, then switch on
  `type`. `optimize` and `recap-site` never produce output.

**`prospects`**
```json
{ "alerts": [ { "name": "Jackson Holliday", "team": "BAL", "pos": "SS",
    "kind": "called-up", "priority": "high", "detail": "promoted to MLB", "rank": 1 } ],
  "upgrades": [ { "source": "FanGraphs", "drop": "Old Guy", "drop_rank": 80,
    "add": "New Guy", "add_rank": 12, "rank_gap": 68, "near_term": true } ] }
```
`kind` ∈ `called-up | optioned | performance-hot | performance-cold |
free-agent-buzz | upgrade-available`; `priority` ∈ `high | medium | low`. Split
`alerts` into call-up vs breakout views by `kind`.

**`waivers`**
```json
{ "picks": [ { "name": "...", "team": "BAL", "pos": "OF", "is_pitcher": false,
    "signal": "HOT", "projected_pts_per_game": 4.2, "drop_name": "...", "gap": 1.1,
    "xwoba": 0.40, "woba": 0.36, "barrel_pct": 14, "hard_hit_pct": 48, "rank": 1 } ],
  "total": 12 }
```
`signal` ∈ `BUY-LOW | HOT | BOTH` (omitted if none). Pitcher rows carry `era`/`xera`
instead of the hitter stat fields. `rank` is 1-based. `total` is the count that
passed filters before the top-N cut.

**`claims`**
```json
{ "claims": [ { "team": "...", "claim_type": "FA", "added": "New SS",
    "added_pos": "SS", "dropped": "Old SS", "net_value": 3, "signal": "HOT" } ] }
```
`claim_type` ∈ `FA | WW`. `net_value` = added HKB value − dropped HKB value. One
row per added player.

**`transactions`**
```json
{ "trades": [ { "teams": ["A","B"], "processed_at": "2026-06-20T12:00:00Z",
    "players": [ { "name": "...", "from_team": "A", "pos": "OF", "valuation": 30 } ] } ] }
```
`from_team` is the side the player came from; `valuation` is HKB value.

**`gs-check`**
```json
{ "period": "Week 11", "violations": [ { "team": "...", "kind": "over",
    "used": 7, "limit": 5, "over_by": 2 } ] }
```
`kind` ∈ `over | under`. `over_by` present for `over` only.

**`backtest`**
```json
{ "start": "2026-06-08", "end": "2026-06-14",
  "days": [ { "date": "2026-06-08", "actual": 40.0, "optimal": 42.0, "gap": -2.0 } ],
  "accuracy": { "mae": 1.4, "bias": 0.3, "rmse": 2.1, "n": 240,
    "by_position": [ { "bucket": "OF", "n": 50, "mae": 1.1, "bias": 0.2 } ] } }
```
`gap` = actual − optimal (negative = points left on the bench). `accuracy` omitted
when projection grading didn't run (`--skip-projections`). `bucket` ∈
`C | INF | OF | UT | SP | RP`.

**`grade`**
```json
{ "dates": ["2026-06-19"], "rows_written": 12 }
```
Graded-snapshot rows written to the Analysis Store, by date.
````

- [ ] **Step 2: Cross-reference in the runs detail note**

Under `GET /v1/runs/{id}`, append a bullet: `- For a typed result (not raw logs), see GET /v1/runs/{id}/output.`

- [ ] **Step 3: Add a suggested screen**

In `## Suggested screens`, add: `5. **Run result** — after a run succeeds, GET /v1/runs/{id}/output; switch on type to render the per-job view (404 → fall back to log_tail).`

- [ ] **Step 4: Commit**

```bash
git add docs/ios-api-contract.md
git commit -m "docs: document GET /v1/runs/{id}/output contract"
```

---

## Task 12: Full verification

- [ ] **Step 1: Vet + tidy + full test**

```bash
go vet ./... && go mod tidy && go test ./... && (cd lambda && go build ./...)
```
Expected: all pass, no diffs from `go mod tidy`.

- [ ] **Step 2: Local smoke (optional but recommended)**

Run a job locally with a synthetic RUN_ID and confirm a file lands:

```bash
RUN_ID=local-test go run . prospects --dry-run
ls -la .lineup/outputs/local-test.json
```
Expected: the file exists and contains `{"type":"prospects","data":{...}}`.

- [ ] **Step 3: Final commit if anything changed in tidy**

```bash
git add -A && git commit -m "chore: go mod tidy" || true
```

---

## Self-Review

**Spec coverage:**
- Contract `GET /v1/runs/{id}/output` → Task 2. ✓
- `type` matches job name → envelope in Task 1 + per-job `RecordOutput("<name>", …)` calls (Tasks 5–10). ✓
- 404 when no output → Task 2 (`handleRunOutput`) + 501 when unwired. ✓
- Storage `runs/<id>/output.json`, s3lineup conventions → Task 3. ✓
- OutputStore alongside RunsStore → `internal/lineupapi/store.go` analog placed in `output.go`/`s3lineup/output.go` (interfaces + adapters). ✓ (Note: interfaces live in `output.go` rather than `store.go` to keep the new contract self-contained; functionally "alongside.")
- Jobs produce output after computing, before stdout, no stdout change → Tasks 5–10 each insert `RecordOutput` before the print/notify and leave stdout untouched. ✓
- Result struct per package in `lineupapi/types.go` (or per-package) → centralized in `lineupapi/output.go`. ✓
- optimize/recap-site no output → not touched; recap-site `{"url":...}` deferred (not required; documented as "never produce output"). ✓ (If a URL payload is wanted later, add a `RecapSiteResult` — out of scope here.)
- Document final shapes in `docs/ios-api-contract.md` → Task 11. ✓
- Handler test round-tripping a sample per job → Task 2 `TestRunOutputRoundTripPerJob`. ✓
- No backfill of old runs (404 forever) → no migration; covered by 404 path. ✓

**Placeholder scan:** every code step has complete code; the only execution-time lookups are explicitly flagged variable-name confirmations in run.go files (the surrounding code is shown/known). No TBD/TODO.

**Type consistency:** wire field names in Task 1 match the mapper outputs (Tasks 5–10), the handler test (Task 2), and the doc (Task 11). `BacktestAccuracy` reused only by backtest. `RecordOutput`/`OutputRecorder`/`OutputStore`/`OutputWriter`/`MarshalOutput`/`NewFileOutputStore`/`NewOutput` names are consistent across tasks.
