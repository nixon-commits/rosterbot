# Projection Accuracy Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a daily-updating, single-page interactive HTML dashboard that visualizes how well the production projection system predicts actual fantasy points over time, reading the existing Graded Snapshots and deploying to its own S3+CloudFront like the recap site.

**Architecture:** A new `analysis.Reader` (mirror of the existing `analysis.Writer`) loads `GradeRow` NDJSON from the local `.analysis/` dir or S3. A new pure `internal/report` package aggregates those rows into a compact `Model` of precomputed views (one per timeframe×role) plus rolling trends and rule-based insights, then renders a self-contained HTML page that embeds the `Model` as JSON and draws charts client-side with Chart.js. A new `cmd/projection-site` wires it; CDK adds a dedicated bucket+distribution and a daily EventBridge schedule.

**Tech Stack:** Go (Cobra CLI, `html/template`, `embed`), aws-sdk-go-v2 (S3, isolated in `s3grades`), AWS CDK (Go) for infra, Chart.js v4 via CDN for client-side charts.

## Global Constraints

- Module path: `github.com/nixon-commits/rosterbot`.
- `gofmt` and `go vet` run automatically via PostToolUse hooks on every Edit/Write; still run `go vet ./...` and `go mod tidy` after code changes.
- The AWS SDK must stay out of the `internal/analysis` leaf — all S3 code lives in `internal/analysis/s3grades` (mirrors `cachestore/s3store`).
- `internal/report` is pure: aggregation and insight functions do no I/O.
- Sign convention (already established in the grades store): `Diff = actual - projected`; positive bias ⇒ under-projecting.
- Position buckets: hitters `C`/`INF`/`OF`/`UT`, pitchers `SP`/`RP`.
- Charts use Chart.js pinned to an exact version from jsDelivr with Subresource Integrity (`integrity="sha384-…" crossorigin="anonymous"`) — never an unpinned/un-hashed CDN tag (CDN-compromise protection).
- Any CDK deploy MUST pass `-c enableBuild=true` (omitting it destroys the CodeBuild project). This plan does not deploy; deployment is a manual follow-up step.
- Standard windows: `[7, 14, 30, 0]` where `0` = season-to-date. Standard roles: `["all", "hitters", "pitchers"]`.

---

### Task 1: `analysis.Reader` + `FileReader` + NDJSON decode

**Files:**
- Create: `internal/analysis/reader.go`
- Modify: `internal/analysis/grades.go` (add `UnmarshalNDJSON`)
- Test: `internal/analysis/reader_test.go`

**Interfaces:**
- Consumes: `analysis.GradeRow`, `analysis.MarshalNDJSON`, `analysis.NewFileWriter` (existing).
- Produces:
  - `analysis.UnmarshalNDJSON(b []byte) ([]GradeRow, error)`
  - `analysis.Reader` interface: `ReadAll() ([]GradeRow, error)`
  - `analysis.NewFileReader(root string) Reader`

- [ ] **Step 1: Write the failing test**

```go
// internal/analysis/reader_test.go
package analysis

import (
	"testing"
	"time"
)

func TestUnmarshalNDJSON_RoundTrip(t *testing.T) {
	in := []GradeRow{
		{Dt: "2026-06-15", PlayerID: "1", Name: "A", Projected: 5, Actual: 7, Diff: 2, Bucket: "OF"},
		{Dt: "2026-06-15", PlayerID: "2", Name: "B", Projected: 3, Actual: 1, Diff: -2, Bucket: "SP", IsPitcher: true},
	}
	b, err := MarshalNDJSON(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := UnmarshalNDJSON(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 || out[0].PlayerID != "1" || !out[1].IsPitcher || out[1].Diff != -2 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestFileReader_ReadAll(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	d1 := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGrades(d1, []GradeRow{{Dt: "2026-06-14", PlayerID: "1"}}); err != nil {
		t.Fatalf("write d1: %v", err)
	}
	if err := w.WriteGrades(d2, []GradeRow{{Dt: "2026-06-15", PlayerID: "2"}, {Dt: "2026-06-15", PlayerID: "3"}}); err != nil {
		t.Fatalf("write d2: %v", err)
	}
	rows, err := NewFileReader(dir).ReadAll()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows across 2 days, got %d", len(rows))
	}
	// Glob is sorted, so the 2026-06-14 partition comes first.
	if rows[0].Dt != "2026-06-14" {
		t.Fatalf("want first row from 2026-06-14, got %q", rows[0].Dt)
	}
}

func TestFileReader_EmptyDir(t *testing.T) {
	rows, err := NewFileReader(t.TempDir()).ReadAll()
	if err != nil {
		t.Fatalf("readall empty: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows, got %d", len(rows))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/analysis/ -run 'Unmarshal|FileReader' -v`
Expected: FAIL — `undefined: UnmarshalNDJSON`, `undefined: NewFileReader`.

- [ ] **Step 3: Add `UnmarshalNDJSON` to `grades.go`**

Add these imports to `internal/analysis/grades.go` (it already imports `bytes`, `encoding/json`, `fmt`, `os`, `path/filepath`, `time`): add `"io"`.

Append to `internal/analysis/grades.go`:

```go
// UnmarshalNDJSON parses newline-delimited JSON (one GradeRow per line).
func UnmarshalNDJSON(b []byte) ([]GradeRow, error) {
	var rows []GradeRow
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var r GradeRow
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
```

- [ ] **Step 4: Create `internal/analysis/reader.go`**

```go
package analysis

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Reader loads graded rows from the Analysis Store (opposite of Writer).
type Reader interface {
	ReadAll() ([]GradeRow, error)
}

type fileReader struct{ root string }

// NewFileReader returns a Reader over grades persisted under
// root/grades/dt=YYYY-MM-DD/grades.ndjson (the FileWriter layout).
func NewFileReader(root string) Reader { return fileReader{root: root} }

func (r fileReader) ReadAll() ([]GradeRow, error) {
	matches, err := filepath.Glob(filepath.Join(r.root, "grades", "dt=*", "grades.ndjson"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var rows []GradeRow
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		rs, err := UnmarshalNDJSON(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		rows = append(rows, rs...)
	}
	return rows, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/analysis/ -v`
Expected: PASS (all, including the pre-existing writer tests).

- [ ] **Step 6: Commit**

```bash
git add internal/analysis/reader.go internal/analysis/grades.go internal/analysis/reader_test.go
git commit -m "feat(analysis): add Reader + FileReader for the grades store

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: S3 reader in `s3grades`

**Files:**
- Modify: `internal/analysis/s3grades/s3grades.go`
- Test: `internal/analysis/s3grades/s3grades_test.go`

**Interfaces:**
- Consumes: `analysis.UnmarshalNDJSON`, `analysis.Reader` (Task 1).
- Produces:
  - `s3grades.NewReader(ctx context.Context, bucket, prefix string) (*Reader, error)`
  - `(*s3grades.Reader).ReadAll() ([]analysis.GradeRow, error)` (satisfies `analysis.Reader`)

- [ ] **Step 1: Write the failing test**

Append to `internal/analysis/s3grades/s3grades_test.go`:

```go
import (
	"bytes"
	"io"
	"strings"
	// (context, testing, time, s3, analysis already imported by the file)
)

type fakeReadAPI struct{ objs map[string][]byte }

func (f *fakeReadAPI) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out := &s3.ListObjectsV2Output{}
	for k := range f.objs {
		if strings.HasPrefix(k, *in.Prefix) {
			key := k
			out.Contents = append(out.Contents, s3types.Object{Key: &key})
		}
	}
	return out, nil
}

func (f *fakeReadAPI) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, io.EOF
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func TestS3Reader_ReadAll(t *testing.T) {
	d1, _ := analysis.MarshalNDJSON([]analysis.GradeRow{{Dt: "2026-06-14", PlayerID: "1"}})
	d2, _ := analysis.MarshalNDJSON([]analysis.GradeRow{{Dt: "2026-06-15", PlayerID: "2"}, {Dt: "2026-06-15", PlayerID: "3"}})
	f := &fakeReadAPI{objs: map[string][]byte{
		"analysis/grades/dt=2026-06-14/grades.ndjson": d1,
		"analysis/grades/dt=2026-06-15/grades.ndjson": d2,
		"analysis/other/ignore.json":                  []byte("{}"),
	}}
	r := &Reader{client: f, bucket: "b", prefix: "analysis/"}
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Dt != "2026-06-14" {
		t.Fatalf("want sorted-by-key first row 2026-06-14, got %q", rows[0].Dt)
	}
}
```

Add the import for `s3types` at the top of the test file:

```go
s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/analysis/s3grades/ -run S3Reader -v`
Expected: FAIL — `undefined: Reader`.

- [ ] **Step 3: Add the reader to `s3grades.go`**

Add imports to `internal/analysis/s3grades/s3grades.go`: `"io"`, `"sort"`, `"strings"`. Append:

```go
type readAPI interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// Reader implements analysis.Reader against S3 (prefix should end in "/").
type Reader struct {
	client readAPI
	bucket string
	prefix string
}

// NewReader constructs a Reader using the default AWS config.
func NewReader(ctx context.Context, bucket, prefix string) (*Reader, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Reader{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

// ReadAll lists every <prefix>grades/dt=*/grades.ndjson object and returns the
// concatenated rows, ordered by object key (date) ascending.
func (r *Reader) ReadAll() ([]analysis.GradeRow, error) {
	ctx := context.Background()
	gradesPrefix := r.prefix + "grades/"
	var keys []string
	var token *string
	for {
		out, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &r.bucket, Prefix: &gradesPrefix, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			if strings.HasSuffix(*o.Key, "grades.ndjson") {
				keys = append(keys, *o.Key)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys)
	var rows []analysis.GradeRow
	for _, k := range keys {
		obj, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &r.bucket, Key: &k})
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
			return nil, err
		}
		rs, err := analysis.UnmarshalNDJSON(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		rows = append(rows, rs...)
	}
	return rows, nil
}

var _ analysis.Reader = (*Reader)(nil)
```

Add `"fmt"` to the imports if not already present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/analysis/s3grades/ -v`
Expected: PASS (both writer and reader tests).

- [ ] **Step 5: Commit**

```bash
git add internal/analysis/s3grades/
git commit -m "feat(s3grades): add S3 Reader for the grades store

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: `internal/report` metrics + window/position/calibration/misses helpers

**Files:**
- Create: `internal/report/aggregate.go`
- Test: `internal/report/aggregate_test.go`

**Interfaces:**
- Consumes: `analysis.GradeRow`.
- Produces (used by Task 4):
  - Types `Metrics`, `PositionRow`, `CalibPoint`, `Miss`.
  - `computeMetrics(rows []analysis.GradeRow) Metrics`
  - `filterRole(rows []analysis.GradeRow, role string) []analysis.GradeRow`
  - `windowRows(rows []analysis.GradeRow, latest time.Time, window int) []analysis.GradeRow`
  - `priorWindowRows(rows []analysis.GradeRow, latest time.Time, window int) []analysis.GradeRow`
  - `byPosition(rows []analysis.GradeRow) []PositionRow`
  - `calibration(rows []analysis.GradeRow) []CalibPoint`
  - `worstMisses(rows []analysis.GradeRow, n int) []Miss`

- [ ] **Step 1: Write the failing test**

```go
// internal/report/aggregate_test.go
package report

import (
	"math"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputeMetrics(t *testing.T) {
	rows := []analysis.GradeRow{
		{Diff: 2}, {Diff: -2}, {Diff: 4},
	}
	m := computeMetrics(rows)
	if m.N != 3 || !approx(m.MAE, (2+2+4)/3.0) || !approx(m.Bias, (2-2+4)/3.0) {
		t.Fatalf("metrics: %+v", m)
	}
	if !approx(m.RMSE, math.Sqrt((4+4+16)/3.0)) {
		t.Fatalf("rmse: %v", m.RMSE)
	}
	if z := computeMetrics(nil); z.N != 0 || z.MAE != 0 {
		t.Fatalf("empty metrics not zero: %+v", z)
	}
}

func TestFilterRole(t *testing.T) {
	rows := []analysis.GradeRow{{PlayerID: "h", IsPitcher: false}, {PlayerID: "p", IsPitcher: true}}
	if got := filterRole(rows, "all"); len(got) != 2 {
		t.Fatalf("all: %d", len(got))
	}
	if got := filterRole(rows, "hitters"); len(got) != 1 || got[0].PlayerID != "h" {
		t.Fatalf("hitters: %+v", got)
	}
	if got := filterRole(rows, "pitchers"); len(got) != 1 || got[0].PlayerID != "p" {
		t.Fatalf("pitchers: %+v", got)
	}
}

func TestWindowRows(t *testing.T) {
	latest := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		{Dt: "2026-06-10"}, {Dt: "2026-06-13"}, {Dt: "2026-06-14"}, {Dt: "2026-06-15"},
	}
	// window 3 = [06-13, 06-15]
	if got := windowRows(rows, latest, 3); len(got) != 3 {
		t.Fatalf("window 3: %d", len(got))
	}
	// season (0) = all
	if got := windowRows(rows, latest, 0); len(got) != 4 {
		t.Fatalf("season: %d", len(got))
	}
	// prior of window 3 = [06-10, 06-12] -> only 06-10
	if got := priorWindowRows(rows, latest, 3); len(got) != 1 || got[0].Dt != "2026-06-10" {
		t.Fatalf("prior: %+v", got)
	}
	if got := priorWindowRows(rows, latest, 0); got != nil {
		t.Fatalf("season has no prior: %+v", got)
	}
}

func TestByPosition_OrderAndMetrics(t *testing.T) {
	rows := []analysis.GradeRow{
		{Bucket: "OF", Diff: 2}, {Bucket: "C", Diff: -4}, {Bucket: "SP", Diff: 1, IsPitcher: true},
	}
	got := byPosition(rows)
	// order is C, INF, OF, UT, SP, RP — present buckets only -> C, OF, SP
	if len(got) != 3 || got[0].Bucket != "C" || got[1].Bucket != "OF" || got[2].Bucket != "SP" {
		t.Fatalf("order: %+v", got)
	}
	if !approx(got[0].MAE, 4) || !approx(got[0].Bias, -4) {
		t.Fatalf("C metrics: %+v", got[0])
	}
}

func TestCalibration_Bins(t *testing.T) {
	rows := []analysis.GradeRow{
		{Projected: 1, Actual: 1}, {Projected: 1.5, Actual: 3}, // bin [0,2)
		{Projected: 21, Actual: 25},                            // bin [20, inf)
	}
	pts := calibration(rows)
	if len(pts) != 2 {
		t.Fatalf("want 2 non-empty bins, got %d: %+v", len(pts), pts)
	}
	if !approx(pts[0].Proj, 1.25) || !approx(pts[0].Actual, 2) || pts[0].N != 2 {
		t.Fatalf("bin0: %+v", pts[0])
	}
}

func TestWorstMisses_SortedByAbsDiff(t *testing.T) {
	rows := []analysis.GradeRow{
		{PlayerID: "a", Diff: 1}, {PlayerID: "b", Diff: -9}, {PlayerID: "c", Diff: 5},
	}
	got := worstMisses(rows, 2)
	if len(got) != 2 || got[0].PlayerID != "b" || got[1].PlayerID != "c" {
		t.Fatalf("misses: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -v`
Expected: FAIL — package/symbols undefined.

- [ ] **Step 3: Create `internal/report/aggregate.go`**

```go
// Package report aggregates the durable Graded Snapshots (analysis.GradeRow)
// into a compact Model of precomputed views (per timeframe x role) for the
// projection-accuracy dashboard. Pure: no I/O.
package report

import (
	"math"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// Metrics is the accuracy summary for a set of graded rows.
type Metrics struct {
	MAE  float64 `json:"mae"`
	Bias float64 `json:"bias"` // mean(actual - projected); positive = under-projecting
	RMSE float64 `json:"rmse"`
	N    int     `json:"n"`
}

// PositionRow is per-bucket accuracy.
type PositionRow struct {
	Bucket string  `json:"bucket"`
	MAE    float64 `json:"mae"`
	Bias   float64 `json:"bias"`
	N      int     `json:"n"`
}

// CalibPoint is a calibration bin: mean projected vs mean actual.
type CalibPoint struct {
	Proj   float64 `json:"proj"`
	Actual float64 `json:"actual"`
	N      int     `json:"n"`
}

// Miss is one large projection error (player-day).
type Miss struct {
	Date      string  `json:"date"`
	PlayerID  string  `json:"playerID"`
	Name      string  `json:"name"`
	MLBTeam   string  `json:"mlbTeam"`
	Bucket    string  `json:"bucket"`
	IsPitcher bool    `json:"isPitcher"`
	Projected float64 `json:"projected"`
	Actual    float64 `json:"actual"`
	Diff      float64 `json:"diff"`
}

var (
	hitterBuckets  = []string{"C", "INF", "OF", "UT"}
	pitcherBuckets = []string{"SP", "RP"}
	calibEdges     = []float64{0, 2, 4, 6, 8, 10, 12, 15, 20} // last bin = [20, +inf)
)

func computeMetrics(rows []analysis.GradeRow) Metrics {
	if len(rows) == 0 {
		return Metrics{}
	}
	var sumAbs, sumSigned, sumSq float64
	for _, r := range rows {
		d := r.Diff
		sumAbs += math.Abs(d)
		sumSigned += d
		sumSq += d * d
	}
	n := float64(len(rows))
	return Metrics{MAE: sumAbs / n, Bias: sumSigned / n, RMSE: math.Sqrt(sumSq / n), N: len(rows)}
}

func filterRole(rows []analysis.GradeRow, role string) []analysis.GradeRow {
	if role == "all" {
		return rows
	}
	want := role == "pitchers"
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if r.IsPitcher == want {
			out = append(out, r)
		}
	}
	return out
}

// windowRows returns rows in the last `window` days ending at latest (inclusive).
// window <= 0 returns all rows (season). ISO date strings sort lexicographically,
// so string comparison is correct.
func windowRows(rows []analysis.GradeRow, latest time.Time, window int) []analysis.GradeRow {
	if window <= 0 {
		return rows
	}
	cutoff := latest.AddDate(0, 0, -(window - 1)).Format("2006-01-02")
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if r.Dt >= cutoff {
			out = append(out, r)
		}
	}
	return out
}

// priorWindowRows returns the equal-length window immediately before the current
// one. Returns nil for the season window (no prior).
func priorWindowRows(rows []analysis.GradeRow, latest time.Time, window int) []analysis.GradeRow {
	if window <= 0 {
		return nil
	}
	hi := latest.AddDate(0, 0, -window).Format("2006-01-02")
	lo := latest.AddDate(0, 0, -(2*window - 1)).Format("2006-01-02")
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if r.Dt >= lo && r.Dt <= hi {
			out = append(out, r)
		}
	}
	return out
}

func byPosition(rows []analysis.GradeRow) []PositionRow {
	groups := map[string][]analysis.GradeRow{}
	for _, r := range rows {
		groups[r.Bucket] = append(groups[r.Bucket], r)
	}
	order := append(append([]string{}, hitterBuckets...), pitcherBuckets...)
	var out []PositionRow
	for _, b := range order {
		g, ok := groups[b]
		if !ok {
			continue
		}
		m := computeMetrics(g)
		out = append(out, PositionRow{Bucket: b, MAE: m.MAE, Bias: m.Bias, N: m.N})
	}
	return out
}

func calibBinIndex(p float64) int {
	for i := len(calibEdges) - 1; i >= 0; i-- {
		if p >= calibEdges[i] {
			return i
		}
	}
	return 0
}

func calibration(rows []analysis.GradeRow) []CalibPoint {
	type acc struct {
		sumP, sumA float64
		n          int
	}
	bins := make([]acc, len(calibEdges))
	for _, r := range rows {
		i := calibBinIndex(r.Projected)
		bins[i].sumP += r.Projected
		bins[i].sumA += r.Actual
		bins[i].n++
	}
	var out []CalibPoint
	for _, b := range bins {
		if b.n == 0 {
			continue
		}
		out = append(out, CalibPoint{Proj: b.sumP / float64(b.n), Actual: b.sumA / float64(b.n), N: b.n})
	}
	return out
}

func worstMisses(rows []analysis.GradeRow, n int) []Miss {
	sorted := make([]analysis.GradeRow, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool {
		ai, aj := math.Abs(sorted[i].Diff), math.Abs(sorted[j].Diff)
		if ai != aj {
			return ai > aj
		}
		return sorted[i].PlayerID < sorted[j].PlayerID // stable tiebreak
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	out := make([]Miss, 0, len(sorted))
	for _, r := range sorted {
		out = append(out, Miss{
			Date: r.Dt, PlayerID: r.PlayerID, Name: r.Name, MLBTeam: r.MLBTeam,
			Bucket: r.Bucket, IsPitcher: r.IsPitcher,
			Projected: r.Projected, Actual: r.Actual, Diff: r.Diff,
		})
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/report/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/report/aggregate.go internal/report/aggregate_test.go
git commit -m "feat(report): metrics + window/position/calibration/misses helpers

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: `internal/report` trend, insights, and `Aggregate`

**Files:**
- Create: `internal/report/insights.go`
- Create: `internal/report/model.go`
- Test: `internal/report/model_test.go`

**Interfaces:**
- Consumes: everything from Task 3.
- Produces (used by Task 5 + Task 6):
  - Types `Insight`, `TrendPoint`, `Scorecard`, `View`, `Model`.
  - `rollingTrend(rows []analysis.GradeRow, roll int) []TrendPoint`
  - `generateInsights(cur, prior Metrics, byPos []PositionRow, windowLabel string) []Insight`
  - `Aggregate(rows []analysis.GradeRow, generatedAt, seasonStart time.Time) *Model`

- [ ] **Step 1: Write the failing test**

```go
// internal/report/model_test.go
package report

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func TestRollingTrend(t *testing.T) {
	rows := []analysis.GradeRow{
		{Dt: "2026-06-14", Diff: 2}, {Dt: "2026-06-15", Diff: -4},
	}
	tp := rollingTrend(rows, 7)
	if len(tp) != 2 || tp[0].Date != "2026-06-14" || tp[1].Date != "2026-06-15" {
		t.Fatalf("trend dates: %+v", tp)
	}
	// 7-day rolling on 06-15 includes both rows -> MAE = (2+4)/2 = 3
	if tp[1].MAE != 3 {
		t.Fatalf("rolling MAE: %+v", tp[1])
	}
}

func TestGenerateInsights_BiasAndImprovement(t *testing.T) {
	cur := Metrics{MAE: 3, Bias: 1.5, N: 500}
	prior := Metrics{MAE: 4, Bias: 0, N: 500}
	ins := generateInsights(cur, prior, nil, "14d")
	var sawBias, sawImprove bool
	for _, i := range ins {
		if contains(i.Text, "under-projecting") {
			sawBias = true
		}
		if contains(i.Text, "improved") {
			sawImprove = true
		}
	}
	if !sawBias || !sawImprove {
		t.Fatalf("expected bias + improvement insights, got %+v", ins)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestAggregate_KeysAndShape(t *testing.T) {
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		{Dt: "2026-06-14", PlayerID: "1", Bucket: "OF", Projected: 5, Actual: 7, Diff: 2},
		{Dt: "2026-06-15", PlayerID: "2", Bucket: "SP", IsPitcher: true, Projected: 8, Actual: 4, Diff: -4},
	}
	m := Aggregate(rows, gen, seasonStart)
	if m.LatestDate != "2026-06-15" {
		t.Fatalf("latest: %q", m.LatestDate)
	}
	if len(m.Windows) != 4 || len(m.Roles) != 3 {
		t.Fatalf("windows/roles: %+v %+v", m.Windows, m.Roles)
	}
	// 4 windows x 3 roles = 12 views
	if len(m.Views) != 12 {
		t.Fatalf("want 12 views, got %d", len(m.Views))
	}
	if _, ok := m.Views["0|all"]; !ok {
		t.Fatalf("missing season|all view; keys=%v", m.Views)
	}
	if _, ok := m.Trends["pitchers"]; !ok {
		t.Fatalf("missing pitchers trend")
	}
	// season|all should see both rows
	if m.Views["0|all"].Scorecard.Cur.N != 2 {
		t.Fatalf("season|all N: %+v", m.Views["0|all"].Scorecard.Cur)
	}
}

func TestAggregate_Empty(t *testing.T) {
	m := Aggregate(nil, time.Now(), time.Now())
	if len(m.Views) != 12 {
		t.Fatalf("want 12 (empty) views, got %d", len(m.Views))
	}
	if m.Views["7|all"].Scorecard.Cur.N != 0 {
		t.Fatalf("empty view should have N=0")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -run 'Trend|Insights|Aggregate' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Create `internal/report/insights.go`**

```go
package report

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// Insight is one auto-generated plain-language callout.
type Insight struct {
	Severity string `json:"severity"` // "info" | "warn"
	Text     string `json:"text"`
}

// TrendPoint is a rolling-window accuracy reading for one date.
type TrendPoint struct {
	Date string  `json:"date"`
	MAE  float64 `json:"mae"`
	Bias float64 `json:"bias"`
}

// rollingTrend computes a trailing roll-day MAE/bias for each date that has
// grades, ascending. O(D*R) — fine for a season of small data.
func rollingTrend(rows []analysis.GradeRow, roll int) []TrendPoint {
	dates := map[string]bool{}
	for _, r := range rows {
		dates[r.Dt] = true
	}
	ds := make([]string, 0, len(dates))
	for d := range dates {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	var out []TrendPoint
	for _, d := range ds {
		dt, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		lo := dt.AddDate(0, 0, -(roll - 1)).Format("2006-01-02")
		var win []analysis.GradeRow
		for _, r := range rows {
			if r.Dt >= lo && r.Dt <= d {
				win = append(win, r)
			}
		}
		m := computeMetrics(win)
		out = append(out, TrendPoint{Date: d, MAE: m.MAE, Bias: m.Bias})
	}
	return out
}

// generateInsights derives plain-language callouts from a window's aggregates.
// Thresholds are intentionally simple and centralized here for easy tuning.
func generateInsights(cur, prior Metrics, byPos []PositionRow, windowLabel string) []Insight {
	var out []Insight

	if math.Abs(cur.Bias) >= 0.5 {
		sev := "info"
		if math.Abs(cur.Bias) >= 1.0 {
			sev = "warn"
		}
		dir := "over-projecting"
		if cur.Bias > 0 {
			dir = "under-projecting"
		}
		out = append(out, Insight{sev, fmt.Sprintf("Overall bias %+.1f over %s — systematically %s.", cur.Bias, windowLabel, dir)})
	}

	var weak, strong *PositionRow
	for i := range byPos {
		p := &byPos[i]
		if p.N < 20 {
			continue
		}
		if weak == nil || p.MAE > weak.MAE {
			weak = p
		}
		if strong == nil || p.MAE < strong.MAE {
			strong = p
		}
	}
	if weak != nil {
		out = append(out, Insight{"info", fmt.Sprintf("Weakest position: %s (MAE %.1f).", weak.Bucket, weak.MAE)})
	}
	if strong != nil && (weak == nil || strong.Bucket != weak.Bucket) {
		out = append(out, Insight{"info", fmt.Sprintf("Best-calibrated: %s (MAE %.1f).", strong.Bucket, strong.MAE)})
	}

	for _, p := range byPos {
		if p.N >= 30 && math.Abs(p.Bias) >= 1.0 {
			dir := "over"
			if p.Bias > 0 {
				dir = "under"
			}
			out = append(out, Insight{"warn", fmt.Sprintf("%s bias %+.1f — %s-projecting %s.", p.Bucket, p.Bias, dir, p.Bucket)})
		}
	}

	if prior.N > 0 && prior.MAE > 0 {
		change := (cur.MAE - prior.MAE) / prior.MAE
		if change <= -0.05 {
			out = append(out, Insight{"info", fmt.Sprintf("Accuracy improved %.0f%% vs the prior %s.", -change*100, windowLabel)})
		} else if change >= 0.05 {
			out = append(out, Insight{"warn", fmt.Sprintf("Accuracy degraded %.0f%% vs the prior %s.", change*100, windowLabel)})
		}
	}

	if cur.N > 0 && cur.N < 200 {
		out = append(out, Insight{"info", fmt.Sprintf("Thin sample (%d player-days) — interpret with caution.", cur.N)})
	}
	return out
}
```

- [ ] **Step 4: Create `internal/report/model.go`**

```go
package report

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// Scorecard holds the current window's metrics plus the equal-length prior
// window for delta display.
type Scorecard struct {
	Cur   Metrics `json:"cur"`
	Prior Metrics `json:"prior"`
}

// View is the fully precomputed dashboard for one (window, role) pair.
type View struct {
	Window    int           `json:"window"`
	Role      string        `json:"role"`
	Scorecard Scorecard     `json:"scorecard"`
	ByPos     []PositionRow `json:"byPos"`
	Calib     []CalibPoint  `json:"calib"`
	Misses    []Miss        `json:"misses"`
	Insights  []Insight     `json:"insights"`
}

// Model is the complete payload embedded into the dashboard HTML.
type Model struct {
	GeneratedAt string                  `json:"generatedAt"`
	SeasonStart string                  `json:"seasonStart"`
	LatestDate  string                  `json:"latestDate"`
	Windows     []int                   `json:"windows"` // [7,14,30,0]; 0 = season
	Roles       []string                `json:"roles"`   // ["all","hitters","pitchers"]
	Trends      map[string][]TrendPoint `json:"trends"`  // keyed by role
	Views       map[string]View         `json:"views"`   // keyed "window|role"
}

var (
	stdWindows = []int{7, 14, 30, 0}
	stdRoles   = []string{"all", "hitters", "pitchers"}
)

func windowLabel(w int) string {
	if w <= 0 {
		return "season"
	}
	return fmt.Sprintf("%dd", w)
}

func viewKey(window int, role string) string { return fmt.Sprintf("%d|%s", window, role) }

// Aggregate builds the full embedded Model from graded rows. generatedAt stamps
// the render time; seasonStart is a display floor. Pure: no I/O.
func Aggregate(rows []analysis.GradeRow, generatedAt, seasonStart time.Time) *Model {
	latest := seasonStart
	for _, r := range rows {
		if d, err := time.Parse("2006-01-02", r.Dt); err == nil && d.After(latest) {
			latest = d
		}
	}
	m := &Model{
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		SeasonStart: seasonStart.Format("2006-01-02"),
		LatestDate:  latest.Format("2006-01-02"),
		Windows:     stdWindows,
		Roles:       stdRoles,
		Trends:      map[string][]TrendPoint{},
		Views:       map[string]View{},
	}
	for _, role := range stdRoles {
		rr := filterRole(rows, role)
		m.Trends[role] = rollingTrend(rr, 7)
		for _, w := range stdWindows {
			cur := windowRows(rr, latest, w)
			prior := priorWindowRows(rr, latest, w)
			curM := computeMetrics(cur)
			priorM := computeMetrics(prior)
			bp := byPosition(cur)
			m.Views[viewKey(w, role)] = View{
				Window:    w,
				Role:      role,
				Scorecard: Scorecard{Cur: curM, Prior: priorM},
				ByPos:     bp,
				Calib:     calibration(cur),
				Misses:    worstMisses(cur, 25),
				Insights:  generateInsights(curM, priorM, bp, windowLabel(w)),
			}
		}
	}
	return m
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/report/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/report/insights.go internal/report/model.go internal/report/model_test.go
git commit -m "feat(report): trend, insights, and Aggregate model assembly

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: `internal/report` render + `template.html`

**Files:**
- Create: `internal/report/render.go`
- Create: `internal/report/template.html`
- Test: `internal/report/render_test.go`

**Interfaces:**
- Consumes: `Model` (Task 4).
- Produces: `Render(w io.Writer, m *Model) error`.

- [ ] **Step 1: Write the failing test**

```go
// internal/report/render_test.go
package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func TestRender_EmbedsModelAndPanels(t *testing.T) {
	rows := []analysis.GradeRow{
		{Dt: "2026-06-15", PlayerID: "1", Name: "Tester", Bucket: "OF", Projected: 5, Actual: 7, Diff: 2},
	}
	m := Aggregate(rows, time.Now().UTC(), time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC))
	var buf bytes.Buffer
	if err := Render(&buf, m); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"const MODEL =", "chart.js", "id=\"scorecard\"", "id=\"calib\"", "id=\"insights\"", "id=\"misses\""} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered HTML missing %q", want)
		}
	}
	// The embedded JSON must be valid: extract between the markers and parse.
	start := strings.Index(html, "const MODEL = ") + len("const MODEL = ")
	end := strings.Index(html[start:], ";\n")
	if end < 0 {
		t.Fatalf("could not locate embedded JSON terminator")
	}
	var got Model
	if err := json.Unmarshal([]byte(html[start:start+end]), &got); err != nil {
		t.Fatalf("embedded JSON invalid: %v", err)
	}
	if got.LatestDate != "2026-06-15" || len(got.Views) != 12 {
		t.Fatalf("round-tripped model wrong: %+v", got.LatestDate)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -run Render -v`
Expected: FAIL — `undefined: Render` (and missing embed file).

- [ ] **Step 3: Create `internal/report/render.go`**

```go
package report

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
)

//go:embed template.html
var templateHTML string

var tmpl = template.Must(template.New("report").Parse(templateHTML))

// Render writes the self-contained dashboard HTML, embedding the Model as JSON
// that the page's client-side JS consumes.
func Render(w io.Writer, m *Model) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, map[string]any{"Data": template.JS(data)})
}
```

- [ ] **Step 4: Create `internal/report/template.html`**

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Projection Accuracy</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.7/dist/chart.umd.min.js" integrity="SRI_PLACEHOLDER" crossorigin="anonymous"></script>
<style>
  :root { --bg:#0f1115; --card:#181b22; --ink:#e6e8eb; --muted:#9aa0a8; --accent:#4f9dff; --warn:#ffb454; --good:#5fd38d; --bad:#ff6b6b; }
  * { box-sizing: border-box; }
  body { margin:0; background:var(--bg); color:var(--ink); font:15px/1.5 -apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif; }
  header { padding:20px 24px; border-bottom:1px solid #23262e; }
  h1 { margin:0 0 4px; font-size:20px; }
  .sub { color:var(--muted); font-size:13px; }
  main { max-width:1100px; margin:0 auto; padding:20px 24px 60px; }
  .controls { display:flex; gap:24px; flex-wrap:wrap; margin:16px 0 24px; }
  .grp { display:flex; gap:6px; align-items:center; }
  .grp .lbl { color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.05em; margin-right:4px; }
  button.seg { background:var(--card); color:var(--ink); border:1px solid #2a2e37; padding:6px 12px; border-radius:6px; cursor:pointer; font-size:13px; }
  button.seg.active { background:var(--accent); border-color:var(--accent); color:#06101f; font-weight:600; }
  .cards { display:grid; grid-template-columns:repeat(4,1fr); gap:12px; }
  .card { background:var(--card); border:1px solid #23262e; border-radius:10px; padding:14px 16px; }
  .card .k { color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.05em; }
  .card .v { font-size:26px; font-weight:700; margin-top:4px; }
  .card .d { font-size:12px; margin-top:2px; }
  .d.good { color:var(--good); } .d.bad { color:var(--bad); } .d.flat { color:var(--muted); }
  section.panel { background:var(--card); border:1px solid #23262e; border-radius:10px; padding:16px; margin-top:20px; }
  section.panel h2 { margin:0 0 12px; font-size:15px; }
  table { width:100%; border-collapse:collapse; font-size:13px; }
  th,td { text-align:left; padding:6px 8px; border-bottom:1px solid #23262e; }
  th { color:var(--muted); font-weight:600; }
  td.num { text-align:right; font-variant-numeric:tabular-nums; }
  .over { color:var(--bad); } .under { color:var(--good); }
  ul.insights { list-style:none; margin:0; padding:0; }
  ul.insights li { padding:8px 12px; border-radius:8px; margin-bottom:8px; background:#1d2129; border-left:3px solid var(--accent); }
  ul.insights li.warn { border-left-color:var(--warn); }
  .empty { color:var(--muted); padding:24px; text-align:center; }
  .chartwrap { position:relative; height:280px; }
</style>
</head>
<body>
<header>
  <h1>Projection Accuracy</h1>
  <div class="sub" id="meta"></div>
</header>
<main>
  <div class="controls">
    <div class="grp"><span class="lbl">Window</span><div id="winseg"></div></div>
    <div class="grp"><span class="lbl">Players</span><div id="roleseg"></div></div>
  </div>

  <div id="scorecard" class="cards"></div>

  <section class="panel"><h2>Rolling accuracy (7-day) over the season</h2><div class="chartwrap"><canvas id="trendChart"></canvas></div></section>
  <section class="panel"><h2>Accuracy by position</h2><div class="chartwrap"><canvas id="posChart"></canvas></div></section>
  <section class="panel" id="calib"><h2>Calibration — projected vs actual</h2><div class="chartwrap"><canvas id="calibChart"></canvas></div></section>
  <section class="panel" id="insights"><h2>Insights</h2><ul class="insights" id="insightList"></ul></section>
  <section class="panel" id="misses"><h2>Worst misses</h2><div id="missTable"></div></section>
</main>

<script>
const MODEL = {{.Data}};

let state = { window: 7, role: "all" };
let charts = {};

const fmt = (x, d=1) => (x===0?0:x).toFixed(d);
const winLabel = w => w<=0 ? "Season" : w+"d";

function seg(containerId, items, key) {
  const c = document.getElementById(containerId);
  c.innerHTML = "";
  items.forEach(it => {
    const b = document.createElement("button");
    b.className = "seg" + (state[key]==it.val ? " active" : "");
    b.textContent = it.label;
    b.onclick = () => { state[key] = it.val; render(); };
    c.appendChild(b);
  });
}

function deltaCell(cur, prior, lowerBetter=true) {
  if (!prior || prior===0) return '<div class="d flat">—</div>';
  const diff = cur - prior;
  const improved = lowerBetter ? diff < 0 : diff > 0;
  const cls = Math.abs(diff) < 1e-9 ? "flat" : (improved ? "good" : "bad");
  const sign = diff > 0 ? "+" : "";
  return '<div class="d '+cls+'">'+sign+fmt(diff,2)+' vs prior</div>';
}

function renderScorecard(v) {
  const s = v.scorecard, c = s.cur, p = s.prior;
  const el = document.getElementById("scorecard");
  if (!c || c.n===0) { el.innerHTML = '<div class="empty" style="grid-column:1/-1">No graded data in this window yet.</div>'; return; }
  el.innerHTML = `
    <div class="card"><div class="k">MAE</div><div class="v">${fmt(c.mae)}</div>${deltaCell(c.mae,p.mae,true)}</div>
    <div class="card"><div class="k">Bias</div><div class="v">${fmt(c.bias)}</div>${deltaCell(Math.abs(c.bias),Math.abs(p.bias),true)}</div>
    <div class="card"><div class="k">RMSE</div><div class="v">${fmt(c.rmse)}</div>${deltaCell(c.rmse,p.rmse,true)}</div>
    <div class="card"><div class="k">Sample (player-days)</div><div class="v">${c.n.toLocaleString()}</div><div class="d flat">prior ${p.n.toLocaleString()}</div></div>`;
}

function destroy(id){ if (charts[id]) { charts[id].destroy(); delete charts[id]; } }

function renderTrend() {
  const tp = MODEL.trends[state.role] || [];
  destroy("trendChart");
  charts.trendChart = new Chart(document.getElementById("trendChart"), {
    type: "line",
    data: { labels: tp.map(p=>p.date), datasets: [
      { label:"MAE", data: tp.map(p=>p.mae), borderColor:"#4f9dff", backgroundColor:"#4f9dff", pointRadius:0, tension:.25 },
      { label:"Bias", data: tp.map(p=>p.bias), borderColor:"#ffb454", backgroundColor:"#ffb454", pointRadius:0, tension:.25 },
    ]},
    options: { maintainAspectRatio:false, scales:{ x:{ ticks:{ color:"#9aa0a8", maxTicksLimit:10 } }, y:{ ticks:{ color:"#9aa0a8" } } }, plugins:{ legend:{ labels:{ color:"#e6e8eb" } } } }
  });
}

function renderPos(v) {
  destroy("posChart");
  charts.posChart = new Chart(document.getElementById("posChart"), {
    type: "bar",
    data: { labels: v.byPos.map(p=>p.bucket), datasets: [
      { label:"MAE", data: v.byPos.map(p=>p.mae), backgroundColor:"#4f9dff" },
      { label:"Bias", data: v.byPos.map(p=>p.bias), backgroundColor:"#ffb454" },
    ]},
    options: { maintainAspectRatio:false, scales:{ x:{ ticks:{ color:"#9aa0a8" } }, y:{ ticks:{ color:"#9aa0a8" } } }, plugins:{ legend:{ labels:{ color:"#e6e8eb" } } } }
  });
}

function renderCalib(v) {
  destroy("calibChart");
  const pts = v.calib.map(p=>({x:p.proj, y:p.actual}));
  let max = 1;
  pts.forEach(p=>{ max = Math.max(max, p.x, p.y); });
  charts.calibChart = new Chart(document.getElementById("calibChart"), {
    type: "scatter",
    data: { datasets: [
      { label:"bins", data: pts, backgroundColor:"#5fd38d", pointRadius:5 },
      { label:"perfect (y=x)", type:"line", data:[{x:0,y:0},{x:max,y:max}], borderColor:"#9aa0a8", borderDash:[5,5], pointRadius:0 },
    ]},
    options: { maintainAspectRatio:false, scales:{ x:{ title:{display:true,text:"projected",color:"#9aa0a8"}, ticks:{ color:"#9aa0a8" } }, y:{ title:{display:true,text:"actual",color:"#9aa0a8"}, ticks:{ color:"#9aa0a8" } } }, plugins:{ legend:{ labels:{ color:"#e6e8eb" } } } }
  });
}

function renderInsights(v) {
  const ul = document.getElementById("insightList");
  if (!v.insights || v.insights.length===0) { ul.innerHTML = '<li class="flat">No notable signals in this window.</li>'; return; }
  ul.innerHTML = v.insights.map(i => '<li class="'+(i.severity==="warn"?"warn":"")+'">'+escapeHtml(i.text)+'</li>').join("");
}

function renderMisses(v) {
  const el = document.getElementById("missTable");
  if (!v.misses || v.misses.length===0) { el.innerHTML = '<div class="empty">No data.</div>'; return; }
  const rows = v.misses.map(m => {
    const cls = m.diff < 0 ? "over" : "under";
    return '<tr><td>'+escapeHtml(m.name)+'</td><td>'+escapeHtml(m.bucket)+'</td><td>'+m.date+'</td>'+
      '<td class="num">'+fmt(m.projected)+'</td><td class="num">'+fmt(m.actual)+'</td>'+
      '<td class="num '+cls+'">'+(m.diff>0?"+":"")+fmt(m.diff)+'</td></tr>';
  }).join("");
  el.innerHTML = '<table><thead><tr><th>Player</th><th>Pos</th><th>Date</th><th class="num">Proj</th><th class="num">Actual</th><th class="num">Diff</th></tr></thead><tbody>'+rows+'</tbody></table>';
}

function escapeHtml(s){ return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }

function render() {
  seg("winseg", MODEL.windows.map(w=>({val:w,label:winLabel(w)})), "window");
  seg("roleseg", MODEL.roles.map(r=>({val:r,label:r[0].toUpperCase()+r.slice(1)})), "role");
  const v = MODEL.views[state.window+"|"+state.role];
  renderScorecard(v);
  renderTrend();
  renderPos(v);
  renderCalib(v);
  renderInsights(v);
  renderMisses(v);
}

document.getElementById("meta").textContent =
  "Latest graded: " + MODEL.latestDate + " · season since " + MODEL.seasonStart + " · generated " + MODEL.generatedAt;
render();
</script>
</body>
</html>
```

- [ ] **Step 5: Compute the Chart.js SRI hash and substitute it**

Fetch the exact pinned file, compute its sha384 SRI, and replace the placeholder
(the template hard-fails the browser if the hash is wrong, so it must match the
file the `src` points at):

```bash
HASH=$(curl -fsSL https://cdn.jsdelivr.net/npm/chart.js@4.4.7/dist/chart.umd.min.js \
  | openssl dgst -sha384 -binary | openssl base64 -A)
# macOS/BSD sed in-place:
sed -i '' "s|SRI_PLACEHOLDER|sha384-$HASH|" internal/report/template.html
grep -q 'integrity="sha384-' internal/report/template.html && echo "SRI set"
```

Expected: `SRI set`, and no remaining `SRI_PLACEHOLDER` in the file.

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/report/ -run Render -v`
Expected: PASS.

- [ ] **Step 7: Run the full package test**

Run: `go test ./internal/report/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/report/render.go internal/report/template.html internal/report/render_test.go
git commit -m "feat(report): self-contained HTML render with Chart.js dashboard

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: `cmd/projection-site` command

**Files:**
- Create: `cmd/projection-site.go`
- Modify: `Makefile` (add a `run-all` line)
- Test: manual run against a local `.analysis/` fixture

**Interfaces:**
- Consumes: `analysis.Reader`, `analysis.NewFileReader`, `s3grades.NewReader`, `report.Aggregate`, `report.Render`, and the existing cmd helpers `todayET()` and `openInBrowser()`.
- Produces: the `projection-site` Cobra subcommand writing `<out>/index.html`.

- [ ] **Step 1: Create `cmd/projection-site.go`**

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/analysis/s3grades"
	"github.com/nixon-commits/rosterbot/internal/report"
	"github.com/spf13/cobra"
)

var (
	projSiteOut  string
	projSiteOpen bool
)

var projectionSiteCmd = &cobra.Command{
	Use:   "projection-site",
	Short: "Render the projection-accuracy dashboard from the Analysis Store",
	Long: `Reads the Graded Snapshots written by the grade command (analysis/grades/
on S3 when STATE_BUCKET is set, else local .analysis/) and renders a single
self-contained HTML dashboard to <out>/index.html. Intended for daily
deployment to its own S3+CloudFront, mirroring the recap site.`,
	RunE: runProjectionSite,
}

func init() {
	projectionSiteCmd.Flags().StringVar(&projSiteOut, "out", "report", "output directory for the rendered dashboard")
	projectionSiteCmd.Flags().BoolVar(&projSiteOpen, "open", false, "open the rendered index.html in the default browser")
	rootCmd.AddCommand(projectionSiteCmd)
}

func runProjectionSite(cmd *cobra.Command, args []string) error {
	today := todayET()

	var reader analysis.Reader
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		r, err := s3grades.NewReader(context.Background(), bucket, "analysis/")
		if err != nil {
			return fmt.Errorf("init analysis reader: %w", err)
		}
		reader = r
	} else {
		reader = analysis.NewFileReader(".analysis")
	}

	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read grades: %w", err)
	}

	// Earliest graded date is a safe season-start floor with no Fantrax call.
	seasonStart := today
	for _, r := range rows {
		if d, err := time.Parse("2006-01-02", r.Dt); err == nil && d.Before(seasonStart) {
			seasonStart = d
		}
	}

	m := report.Aggregate(rows, time.Now().UTC(), seasonStart)

	if err := os.MkdirAll(projSiteOut, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", projSiteOut, err)
	}
	outPath := filepath.Join(projSiteOut, "index.html")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if err := report.Render(f, m); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d graded rows, latest %s)\n", outPath, len(rows), m.LatestDate)

	if projSiteOpen {
		if err := openInBrowser(outPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}
```

Note: an empty `.analysis/` is not an error — `Aggregate` returns 12 empty views and the page shows an empty-state. This keeps the daily run and `make run-all` green before any grades exist.

- [ ] **Step 2: Build and verify the command is registered**

Run: `go build -o /tmp/rosterbot . && /tmp/rosterbot projection-site --help`
Expected: help text for `projection-site` with `--out` and `--open` flags.

- [ ] **Step 3: Smoke-test against a local fixture**

```bash
mkdir -p .analysis/grades/dt=2026-06-15
printf '%s\n' '{"dt":"2026-06-15","player_id":"1","name":"Tester","mlb_team":"NYY","projected":5,"actual":8,"diff":3,"bucket":"OF","is_pitcher":false,"source":"snapshot"}' > .analysis/grades/dt=2026-06-15/grades.ndjson
/tmp/rosterbot projection-site --out /tmp/proj-report
test -f /tmp/proj-report/index.html && echo OK
rm -rf .analysis/grades/dt=2026-06-15
```

Expected: `Wrote /tmp/proj-report/index.html (1 graded rows, latest 2026-06-15)` then `OK`.

- [ ] **Step 4: Add the `run-all` smoke line**

In the `Makefile`, inside the `run-all` recipe, add a line after the `recap-site` invocation (read-only; tolerate the no-grades case locally):

```make
	go run . projection-site --out /tmp/rosterbot-proj-report || true
```

- [ ] **Step 5: Tidy + vet**

Run: `go mod tidy && go vet ./...`
Expected: no output (clean).

- [ ] **Step 6: Commit**

```bash
git add cmd/projection-site.go Makefile
git commit -m "feat(cmd): projection-site renders the accuracy dashboard

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: Infra — dedicated bucket + CloudFront + daily schedule + entrypoint sync

**Files:**
- Modify: `infra/infra.go`
- Modify: `entrypoint.sh`
- Test: `cd infra && go build ./...`

**Interfaces:**
- Consumes: existing CDK objects in `infra.go` (`stack`, `taskDef`, `cluster`, the `jobs` slice loop).
- Produces: a `ReportBucket` + `ReportCdn` distribution, a `REPORT_BUCKET` container env, and a `ProjectionSite` daily EventBridge rule.

- [ ] **Step 1: Add the report bucket (after the `siteBucket` block, ~line 54)**

```go
	// Projection-accuracy dashboard bucket (private; served via its own CDN).
	reportBucket := awss3.NewBucket(stack, jsii.String("ReportBucket"), &awss3.BucketProps{
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
		AutoDeleteObjects: jsii.Bool(true),
	})
```

- [ ] **Step 2: Output the bucket name (after the `SiteBucketName` output, ~line 64)**

```go
	awscdk.NewCfnOutput(stack, jsii.String("ReportBucketName"), &awscdk.CfnOutputProps{Value: reportBucket.BucketName()})
```

- [ ] **Step 3: Grant the task role write + add the env var**

After `siteBucket.GrantReadWrite(taskDef.TaskRole(), nil)` (~line 84):

```go
	reportBucket.GrantReadWrite(taskDef.TaskRole(), nil)
```

In the container `Environment` map (~line 102-106), add the entry alongside `SITE_BUCKET`:

```go
			"REPORT_BUCKET":      reportBucket.BucketName(),
```

(The task role already has read on the `analysis/` grades via the existing `stateBucket.GrantReadWrite`, so no extra read grant is needed.)

- [ ] **Step 4: Add the second CloudFront distribution (after the `SiteCdn` block + its output, ~line 135)**

```go
	reportDist := awscloudfront.NewDistribution(stack, jsii.String("ReportCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(reportBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
		},
	})
	awscdk.NewCfnOutput(stack, jsii.String("ReportUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), reportDist.DistributionDomainName()}),
	})
```

- [ ] **Step 5: Add the daily schedule (in the `jobs` slice, after the `Grade` entry, ~line 304)**

```go
		{"ProjectionSite", "cron(0 15 * * ? *)", jsii.Strings("projection-site", "--out", "report")},
```

- [ ] **Step 6: Add the entrypoint sync (in `sync_up`, after the recap `dist` line, ~line 24)**

```sh
  # Publish the projection dashboard when present (projection-site writes ./report).
  [ -d ./report ] && [ -n "${REPORT_BUCKET:-}" ] && aws s3 sync ./report/ "s3://$REPORT_BUCKET/" --delete --quiet || true
```

- [ ] **Step 7: Build infra to verify it compiles**

Run: `cd infra && go build ./... && cd ..`
Expected: clean build (no output).

- [ ] **Step 8: Commit**

```bash
git add infra/infra.go entrypoint.sh
git commit -m "feat(infra): projection dashboard bucket, CDN, and daily schedule

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

**Deployment note (manual, not part of this task):** after merge, deploy with
`cd infra && cdk deploy -c enableBuild=true` — omitting the flag destroys the
CodeBuild project. The new `ReportUrl` output is the dashboard's public URL.

---

### Task 8: Documentation + v2 follow-on issue

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `docs/aws-deployment.md`
- Create: `.scratch/projection-comparison-v2/issue.md`

- [ ] **Step 1: README — document the command**

Add `projection-site` to the commands section of `README.md` (near `recap-site`), describing: renders a daily-updating projection-accuracy dashboard from the grades store to `<out>/index.html`; flags `--out` (default `report`) and `--open`; reads S3 when `STATE_BUCKET` is set, else `.analysis/`. Mention the live URL is the CDK `ReportUrl` output.

- [ ] **Step 2: CLAUDE.md — document the architecture**

In `CLAUDE.md`:
- Under the `internal/analysis` paragraph, note the new `analysis.Reader` / `FileReader` (mirror of `Writer`) and the `s3grades.Reader` S3 adapter, plus `UnmarshalNDJSON`.
- Add a new `**internal/report**` paragraph describing: pure aggregation of `analysis.GradeRow` into a `Model` of precomputed views (per timeframe×role) + rolling trends + rule-based insights, rendered to a self-contained HTML dashboard (embedded JSON + Chart.js via CDN). Note the daily `projection-site` command and its own S3+CloudFront (`REPORT_BUCKET` / `ReportCdn`), distinct from the recap's `SITE_BUCKET`.
- Note the new `ProjectionSite` EventBridge schedule (`cron(0 15 * * ? *)`, ~90 min after `grade`) in the AWS/GHA architecture references.

- [ ] **Step 3: docs/aws-deployment.md — schedule + distribution**

Add the `ProjectionSite` schedule to the EventBridge schedule mapping, and document the second CloudFront distribution (`ReportCdn` / `ReportUrl`) + the `REPORT_BUCKET` sync in `entrypoint.sh` alongside the existing recap-site notes.

- [ ] **Step 4: File the v2 follow-on issue**

```bash
mkdir -p .scratch/projection-comparison-v2
```

Create `.scratch/projection-comparison-v2/issue.md`:

```markdown
# Projection system comparison (v2)

Status: ready-for-human

## Summary
Extend the projection-accuracy dashboard to compare MULTIPLE projection systems
and blending weights head-to-head (Steamer / DepthCharts / TheBatX + weighting
variants), not just the production blend.

## Dependency (blocking)
The grades store records ONE projected value per player-day (the production
blend). v2 requires a daily MULTI-SYSTEM capture pipeline: for each player-day,
snapshot each candidate system's projection, grade each against actual FPts.
This must accumulate weeks of data before comparisons are statistically
meaningful — so the capture should start well before the comparison UI ships.

## Scope sketch
- Capture: extend the snapshot writer (or a new sidecar) to record per-system
  projected pts/game per player per day; grade each into the Analysis Store with
  a `system` dimension on GradeRow.
- UI: overlay systems in the existing calibration + by-position panels (v1 was
  designed so a per-system series slots in without restructuring), plus a
  systems-vs-MAE leaderboard.

## Notes
v1 design: docs/superpowers/specs/2026-06-29-projection-accuracy-dashboard-design.md
```

- [ ] **Step 5: Run the doc-drift check**

Run: `go vet ./... && go test ./internal/analysis/... ./internal/report/...`
Expected: PASS — confirms docs didn't accompany a broken build.

- [ ] **Step 6: Commit**

```bash
git add README.md CLAUDE.md docs/aws-deployment.md .scratch/projection-comparison-v2/
git commit -m "docs: projection dashboard + v2 comparison follow-on

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- Reads grades from S3 directly (no Athena) → Tasks 1, 2, 6. ✓
- Daily-updating single-page interactive HTML → Tasks 5 (template), 6 (cmd), 7 (schedule). ✓
- Embedded JSON + client-side timeframe/role toggling → Task 4 (`Model` of precomputed views keyed `window|role`), Task 5 (JS). ✓
- Four core panels (scorecard+trend, by-position, calibration, worst-misses) + auto-insights → Tasks 3, 4, 5. ✓
- Separate bucket+distribution, daily schedule ~after grade, entrypoint sync → Task 7. ✓
- v2 follow-on filed → Task 8. ✓
- `analysis.Reader` mirrors `Writer`; AWS SDK isolated in `s3grades` → Tasks 1, 2. ✓
- `make run-all` line for the new top-level command → Task 6. ✓
- README + CLAUDE.md kept in sync → Task 8. ✓

**Placeholder scan:** No TBD/TODO; insight rules are concrete code with real thresholds; full `template.html` provided; every test has assertions. ✓

**Type consistency:** `analysis.Reader.ReadAll()`/`UnmarshalNDJSON` consistent across Tasks 1/2/6. `report` types (`Metrics`, `PositionRow`, `CalibPoint`, `Miss`, `Insight`, `TrendPoint`, `Scorecard`, `View`, `Model`) defined in Tasks 3/4 and consumed unchanged in Tasks 5/6. `viewKey` format `"window|role"` matches the JS lookup `state.window+"|"+state.role`. `Render(w, m)` signature consistent across Task 5 and Task 6. ✓
