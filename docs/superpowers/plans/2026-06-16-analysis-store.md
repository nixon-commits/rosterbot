# Analysis Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Durably retain model-performance history and make it queryable — persist backtest snapshots to S3 (fixing a migration gap), write a daily materialized **Graded Snapshot** fact as NDJSON to an **Analysis Store** in S3, and expose it via an Athena table for model-audit SQL.

**Architecture:** A new daily `grade` command reuses `internal/backtest`'s projection grading (`RunProjectionAnalysis` → `[]PlayerProjection`) and writes the rows as NDJSON to `s3://<state-bucket>/analysis/grades/dt=YYYY-MM-DD/`. A CDK-managed Glue table with partition projection on `dt` makes it queryable in Athena with no crawler. Separately, `entrypoint.sh` starts syncing `.backtest/` so the snapshots `optimize` writes actually persist on Fargate (they currently vanish), which both restores projection grading on AWS and retains the raw inputs.

**Tech Stack:** Go, `aws-sdk-go-v2` (`service/s3`), AWS CDK (Go) `awsglue`/`awsathena`, Athena/Glue, NDJSON.

**Spec/decisions (from architecture grilling, CONTEXT.md Analysis Store / Graded Snapshot):** audit-the-model use case → S3+Athena (not DB); materialized graded fact; daily `grade` command + schedule; NDJSON; CDK Glue table with partition projection. Retention: state bucket versioning already ON (retains cache overwrites); this plan adds snapshot + graded-fact retention.

**Scope:** v1 = the projection-grade fact + snapshot persistence. Claims/waiver history as a queryable fact is a separate later slice.

---

### Task 1: Persist `.backtest/` snapshots to S3 (fix the gap + retain)

**Why:** `optimize` writes `backtest.WriteSnapshot(".backtest/snapshots", …)` hourly, but `entrypoint.sh` never syncs `.backtest/`, so on Fargate the snapshot is lost when the task exits — projection grading has no data on AWS, and `grade` (Task 3) would have nothing to read. Sync it under a `backtest/` prefix.

**Files:**
- Modify: `entrypoint.sh`

- [ ] **Step 1: Add `.backtest/` to both sync directions**

In `entrypoint.sh`, `sync_down()` add (after the claims line):
```sh
  aws s3 sync "s3://$STATE_BUCKET/backtest/" ./.backtest/ --quiet || true
```
`sync_up()` add (after the claims line, before the dist publish):
```sh
  aws s3 sync ./.backtest/ "s3://$STATE_BUCKET/backtest/" --quiet || true
```

- [ ] **Step 2: Lint**

Run: `sh -n entrypoint.sh`
Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add entrypoint.sh
git commit -m "build: persist .backtest/ snapshots to S3 (was lost on Fargate)"
```

> Note (no test): this is shell glue verified end-to-end in Task 6. Per-date snapshot files don't clobber across days; same-day rewrites are last-write-wins by design (CLAUDE.md).

---

### Task 2: `internal/analysis` — Graded Snapshot rows + NDJSON + writers

**Files:**
- Create: `internal/analysis/grades.go` (GradeRow, NDJSON marshal, Writer interface, fileWriter)
- Test: `internal/analysis/grades_test.go`
- Create: `internal/analysis/s3grades/s3grades.go` (S3 writer, aws-sdk-go-v2)
- Test: `internal/analysis/s3grades/s3grades_test.go`

The `analysis` package is dependency-light (no backtest import): `cmd` maps `backtest.PlayerProjection` → `analysis.GradeRow`. The S3 writer is isolated in a sub-package so the AWS SDK stays out of `internal/analysis` (mirrors the cache/cachestore split).

- [ ] **Step 1: Write the failing test** — `internal/analysis/grades_test.go`

```go
package analysis

import (
	"strings"
	"testing"
	"time"
)

func TestMarshalNDJSON(t *testing.T) {
	rows := []GradeRow{
		{Dt: "2026-06-15", PlayerID: "1", Name: "A", Projected: 5, Actual: 7, Diff: 2, Bucket: "OF"},
		{Dt: "2026-06-15", PlayerID: "2", Name: "B", Projected: 3, Actual: 1, Diff: -2, Bucket: "SP", IsPitcher: true},
	}
	b, err := MarshalNDJSON(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 NDJSON lines, got %d: %q", len(lines), b)
	}
	if !strings.Contains(lines[0], `"player_id":"1"`) || !strings.Contains(lines[1], `"is_pitcher":true`) {
		t.Fatalf("unexpected NDJSON: %q", b)
	}
}

func TestFileWriter_PathLayout(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	date := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGrades(date, []GradeRow{{Dt: "2026-06-15", PlayerID: "1"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Must land at <dir>/grades/dt=2026-06-15/grades.ndjson (partition layout).
	if _, err := readFile(dir + "/grades/dt=2026-06-15/grades.ndjson"); err != nil {
		t.Fatalf("expected partitioned file: %v", err)
	}
}
```

Add a tiny `readFile` test helper at the bottom of the test file:
```go
func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
```
with imports `os` (and keep `strings`, `testing`, `time`).

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/analysis/ -v`
Expected: FAIL — `undefined: GradeRow` / `MarshalNDJSON` / `NewFileWriter`.

- [ ] **Step 3: Implement `internal/analysis/grades.go`**

```go
// Package analysis writes the durable, append-only Analysis Store: the Graded
// Snapshot fact (projected vs actual per player per day) as NDJSON, partitioned
// by date for Athena. The S3 adapter lives in the s3grades sub-package so the
// AWS SDK stays out of this package.
package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GradeRow is one Graded Snapshot: a (date, player) projected-vs-actual fact.
// Mirrors backtest.PlayerProjection plus the dt partition key. cmd maps from
// PlayerProjection so this package needn't import internal/backtest.
type GradeRow struct {
	Dt        string  `json:"dt"`
	PlayerID  string  `json:"player_id"`
	Name      string  `json:"name"`
	MLBTeam   string  `json:"mlb_team"`
	Projected float64 `json:"projected"`
	Actual    float64 `json:"actual"`
	Diff      float64 `json:"diff"`
	Bucket    string  `json:"bucket"`
	IsPitcher bool    `json:"is_pitcher"`
	Source    string  `json:"source"`
}

// Writer persists a day's graded rows to the Analysis Store.
type Writer interface {
	WriteGrades(date time.Time, rows []GradeRow) error
}

// MarshalNDJSON serializes rows as newline-delimited JSON (one row per line),
// the format Athena's JSON SerDe reads.
func MarshalNDJSON(rows []GradeRow) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil { // Encode appends "\n"
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// objectKey is the partition-projection path for a date's grades, shared by the
// file and S3 writers: grades/dt=YYYY-MM-DD/grades.ndjson.
func objectKey(date time.Time) string {
	return fmt.Sprintf("grades/dt=%s/grades.ndjson", date.UTC().Format("2006-01-02"))
}

// fileWriter writes to a local root (dev / non-S3).
type fileWriter struct{ root string }

func NewFileWriter(root string) Writer { return fileWriter{root: root} }

func (w fileWriter) WriteGrades(date time.Time, rows []GradeRow) error {
	b, err := MarshalNDJSON(rows)
	if err != nil {
		return err
	}
	p := filepath.Join(w.root, objectKey(date))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// ObjectKey is exported so the S3 writer reuses the same partition layout.
func ObjectKey(date time.Time) string { return objectKey(date) }
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/analysis/ -v`
Expected: PASS.

- [ ] **Step 5: Write the S3 writer failing test** — `internal/analysis/s3grades/s3grades_test.go`

```go
package s3grades

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/nixon-commits/rosterbot/internal/analysis"
)

type fakeAPI struct{ puts map[string][]byte }

func (f *fakeAPI) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.puts[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func TestS3Writer_KeyAndBody(t *testing.T) {
	f := &fakeAPI{puts: map[string][]byte{}}
	w := &Writer{client: f, bucket: "b", prefix: "analysis/"}
	date := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGrades(date, []analysis.GradeRow{{Dt: "2026-06-15", PlayerID: "1"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	key := "analysis/grades/dt=2026-06-15/grades.ndjson"
	if _, ok := f.puts[key]; !ok {
		t.Fatalf("object not at %s; keys=%v", key, f.puts)
	}
}
```

- [ ] **Step 6: Run, verify fail**

Run: `go test ./internal/analysis/s3grades/ -v`
Expected: FAIL — `undefined: Writer`.

- [ ] **Step 7: Implement `internal/analysis/s3grades/s3grades.go`**

```go
// Package s3grades is the S3 adapter for analysis.Writer (the Analysis Store).
package s3grades

import (
	"bytes"
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/nixon-commits/rosterbot/internal/analysis"
)

type api interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// Writer implements analysis.Writer against S3 (prefix should end in "/", e.g. "analysis/").
type Writer struct {
	client api
	bucket string
	prefix string
}

func New(ctx context.Context, bucket, prefix string) (*Writer, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Writer{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (w *Writer) WriteGrades(date time.Time, rows []analysis.GradeRow) error {
	b, err := analysis.MarshalNDJSON(rows)
	if err != nil {
		return err
	}
	key := w.prefix + analysis.ObjectKey(date)
	_, err = w.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &w.bucket, Key: &key, Body: bytes.NewReader(b),
	})
	return err
}

var _ analysis.Writer = (*Writer)(nil)
```

- [ ] **Step 8: Run, verify pass; build; vet; commit**

Run: `go test ./internal/analysis/... && go build ./... && go vet ./...`
Expected: PASS, clean.
```bash
git add internal/analysis/
git commit -m "feat(analysis): Graded Snapshot NDJSON writer (file + S3)"
```

---

### Task 3: `grade` command

**Files:**
- Create: `cmd/grade.go`
- Modify: `Makefile` (add a `grade` line to `run-all`)

- [ ] **Step 1: Implement `cmd/grade.go`**

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/analysis/s3grades"
	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/spf13/cobra"
)

var gradeDates string

var gradeCmd = &cobra.Command{
	Use:   "grade",
	Short: "Grade past projections and append Graded Snapshots to the Analysis Store",
	RunE:  runGrade,
}

func init() {
	gradeCmd.Flags().StringVar(&gradeDates, "dates", "", "date or range to grade (default: yesterday)")
	rootCmd.AddCommand(gradeCmd)
}

func runGrade(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	start, end := today.AddDate(0, 0, -1), today.AddDate(0, 0, -1)
	if gradeDates != "" {
		ds, err := parseDates(gradeDates, today)
		if err != nil {
			return err
		}
		start, end = ds[0], ds[len(ds)-1]
	}

	seasonStart, err := ft.SeasonStart()
	if err != nil {
		return fmt.Errorf("season start: %w", err)
	}
	snapTTL := 30 * 24 * time.Hour
	if noCache {
		snapTTL = 0
	}
	days, err := ft.DailyFantasyPoints(cfg.TeamID, start, end, seasonStart, cacheDir, snapTTL)
	if err != nil {
		return fmt.Errorf("daily fpts: %w", err)
	}
	if err := ft.BackfillDailyFPts(days); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: MLB backfill: %v\n", err)
	}

	results := backtest.RunProjectionAnalysis(days, ".backtest/snapshots")

	// Flatten to GradeRows, skipping days with no usable snapshot.
	byDate := map[string][]analysis.GradeRow{}
	for _, d := range results {
		if d.Source == "missing" || d.Source == "stale" {
			fmt.Fprintf(os.Stderr, "skip %s: source=%s\n", d.Date.Format("2006-01-02"), d.Source)
			continue
		}
		dt := d.Date.UTC().Format("2006-01-02")
		for _, p := range d.Players {
			byDate[dt] = append(byDate[dt], analysis.GradeRow{
				Dt: dt, PlayerID: p.PlayerID, Name: p.Name, MLBTeam: p.MLBTeam,
				Projected: p.Projected, Actual: p.Actual, Diff: p.Diff,
				Bucket: p.Bucket, IsPitcher: p.IsPitcher, Source: p.Source,
			})
		}
	}

	if cfg.DryRun {
		for dt, rows := range byDate {
			fmt.Printf("[dry-run] %s: %d graded rows\n", dt, len(rows))
		}
		return nil
	}

	var w analysis.Writer
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		sw, err := s3grades.New(context.Background(), bucket, "analysis/")
		if err != nil {
			return fmt.Errorf("init analysis store: %w", err)
		}
		w = sw
	} else {
		w = analysis.NewFileWriter(".analysis")
	}

	for dt, rows := range byDate {
		date, _ := time.Parse("2006-01-02", dt)
		if err := w.WriteGrades(date, rows); err != nil {
			return fmt.Errorf("write grades %s: %w", dt, err)
		}
		fmt.Printf("wrote %d graded rows for %s\n", len(rows), dt)
	}
	return nil
}
```

> Verify at implementation time that `ft.SeasonStart()`, `cacheDir`, `noCache`, `parseDates`, and `todayET()` exist with these names (they are used by `cmd/backtest.go`). If `SeasonStart()` has a different name, grep `cmd/backtest.go` for how it obtains `seasonStart` and match it.

- [ ] **Step 2: Build + vet**

Run: `go build ./... && go vet ./...`
Expected: clean. Fix any signature mismatches against the real fantrax/cmd helpers (grep `cmd/backtest.go`).

- [ ] **Step 3: Local dry-run smoke (hermetic; no creds needed if it no-ops)**

Run: `go run . grade --dry-run --dates 2026-06-14 2>&1 | tail -5`
Expected: either `[dry-run] …: N graded rows` or a `skip …: source=missing` line (no snapshot locally is fine) — and a clean exit, no panic.

- [ ] **Step 4: Add to `Makefile` `run-all`** — append a line near the other read-only commands:
```make
	@echo "== grade ==" && time go run . grade --dry-run
```

- [ ] **Step 5: Commit**

```bash
git add cmd/grade.go Makefile
git commit -m "feat(grade): daily command writing Graded Snapshots to the Analysis Store"
```

---

### Task 4: CDK — daily `grade` schedule + Glue table + Athena workgroup

**Files:**
- Modify: `infra/infra.go`

- [ ] **Step 1: Add the daily `grade` schedule** — in the `jobs` table in `infra/infra.go`, add:
```go
		{"Grade", "cron(0 13 * * ? *)", jsii.Strings("grade")},
```
(13:00 UTC = after the prior day's MLB games are final and actuals settle.)

- [ ] **Step 2: Add Glue database + table with partition projection** — add imports `awsglue` and after the buckets, using `stateBucket`:

```go
glueDB := awsglue.NewCfnDatabase(stack, jsii.String("AnalysisDB"), &awsglue.CfnDatabaseProps{
	CatalogId: stack.Account(),
	DatabaseInput: &awsglue.CfnDatabase_DatabaseInputProperty{Name: jsii.String("rosterbot_analysis")},
})

cols := func(name, typ string) *awsglue.CfnTable_ColumnProperty {
	return &awsglue.CfnTable_ColumnProperty{Name: jsii.String(name), Type: jsii.String(typ)}
}
gradesLoc := awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("s3://"), stateBucket.BucketName(), jsii.String("/analysis/grades/")})

gradesTable := awsglue.NewCfnTable(stack, jsii.String("GradesTable"), &awsglue.CfnTableProps{
	CatalogId:    stack.Account(),
	DatabaseName: jsii.String("rosterbot_analysis"),
	TableInput: &awsglue.CfnTable_TableInputProperty{
		Name:      jsii.String("grades"),
		TableType: jsii.String("EXTERNAL_TABLE"),
		PartitionKeys: &[]interface{}{cols("dt", "string")},
		Parameters: &map[string]*string{
			"classification":               jsii.String("json"),
			"projection.enabled":           jsii.String("true"),
			"projection.dt.type":           jsii.String("date"),
			"projection.dt.format":         jsii.String("yyyy-MM-dd"),
			"projection.dt.range":          jsii.String("2026-01-01,NOW"),
			"projection.dt.interval":       jsii.String("1"),
			"projection.dt.interval.unit":  jsii.String("DAYS"),
			"storage.location.template":    awscdk.Fn_Join(jsii.String(""), &[]*string{gradesLoc, jsii.String("dt=${dt}/")}),
		},
		StorageDescriptor: &awsglue.CfnTable_StorageDescriptorProperty{
			Location:     gradesLoc,
			InputFormat:  jsii.String("org.apache.hadoop.mapred.TextInputFormat"),
			OutputFormat: jsii.String("org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat"),
			SerdeInfo: &awsglue.CfnTable_SerdeInfoProperty{
				SerializationLibrary: jsii.String("org.openx.data.jsonserde.JsonSerDe"),
			},
			Columns: &[]interface{}{
				cols("player_id", "string"), cols("name", "string"), cols("mlb_team", "string"),
				cols("projected", "double"), cols("actual", "double"), cols("diff", "double"),
				cols("bucket", "string"), cols("is_pitcher", "boolean"), cols("source", "string"),
			},
		},
	},
})
gradesTable.AddDependency(glueDB)
```

- [ ] **Step 3: Add an Athena workgroup with a results location** — import `awsathena`:
```go
awsathena.NewCfnWorkGroup(stack, jsii.String("AnalysisWG"), &awsathena.CfnWorkGroupProps{
	Name: jsii.String("rosterbot"),
	WorkGroupConfiguration: &awsathena.CfnWorkGroup_WorkGroupConfigurationProperty{
		ResultConfiguration: &awsathena.CfnWorkGroup_ResultConfigurationProperty{
			OutputLocation: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("s3://"), stateBucket.BucketName(), jsii.String("/athena-results/")}),
		},
	},
})
```

- [ ] **Step 4: Synth (validate CDK Go)**

Run: `cd infra && JSII_SILENCE_WARNING_UNTESTED_NODE_VERSION=1 cdk synth 2>&1 | grep -E "AWS::Glue::Table|AWS::Glue::Database|AWS::Athena::WorkGroup|Events::Rule" | sort | uniq -c`
Expected: 1 Glue Database, 1 Glue Table, 1 Athena WorkGroup, 9 Events::Rule (8 + Grade). Fix any CDK Go prop-name errors against `cdk synth` output (the source of truth).

- [ ] **Step 5: Commit**

```bash
git add infra/infra.go
git commit -m "infra: daily grade schedule + Glue table (partition projection) + Athena workgroup"
```

---

### Task 5: Docs + retention notes

**Files:**
- Modify: `CLAUDE.md`, `README.md`, `docs/aws-deployment.md`, `Makefile`

- [ ] **Step 1: `CLAUDE.md`** — add an `internal/analysis` paragraph near `internal/backtest`: the daily `grade` command reuses `RunProjectionAnalysis` to materialize Graded Snapshots as NDJSON in `analysis/grades/dt=…/`, queryable via the CDK-managed Athena `rosterbot_analysis.grades` table (partition projection on `dt`). Note `entrypoint.sh` now syncs `.backtest/` (under the `backtest/` prefix) so snapshots persist on Fargate.

- [ ] **Step 2: `README.md`** — under Automation, add the `grade` job row and a short "Model auditing" note pointing at the Athena table with one sample query:
```sql
SELECT bucket, count(*) n, avg(abs(diff)) mae, avg(diff) bias
FROM rosterbot_analysis.grades
WHERE dt >= '2026-06-01'
GROUP BY bucket ORDER BY mae DESC;
```

- [ ] **Step 3: `docs/aws-deployment.md`** — add `analysis/` and `backtest/` to the S3 prefix list; document the Athena workgroup `rosterbot` and the `grades` table; add a **Retention** note: state bucket versioning is ON (cache overwrites retained as noncurrent versions); `analysis/` and `backtest/` are append-only and never expired.

- [ ] **Step 4: `Makefile`** — confirm the `grade` line added in Task 3 Step 4 is present in `run-all`.

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md README.md docs/aws-deployment.md Makefile
git commit -m "docs: Analysis Store, grade command, model-audit queries, retention"
```

---

### Task 6: AWS end-to-end verification

**Files:** none (operational; after merge → CodeBuild builds the image)

- [ ] **Step 1: Confirm snapshots now persist** — after a post-merge `optimize` run (hourly or ad-hoc), check:
```bash
aws s3 ls s3://<state-bucket>/backtest/snapshots/ | tail
```
Expected: per-date snapshot JSON objects (proves Task 1 fixed the gap).

- [ ] **Step 2: Run `grade` ad-hoc and confirm NDJSON lands**
```bash
aws ecs run-task --region us-west-1 --cluster InfraStack-ClusterEB0386A7-gW887MG6jGtU \
  --task-definition InfraStackTaskA0548DCD --launch-type FARGATE \
  --network-configuration 'awsvpcConfiguration={subnets=[subnet-058c996ad3d4776fd],securityGroups=[sg-0660b03e9e7fe25c4],assignPublicIp=ENABLED}' \
  --overrides '{"containerOverrides":[{"name":"bot","command":["grade","--dates","<a-date-with-a-snapshot>"]}]}'
aws s3 ls s3://<state-bucket>/analysis/grades/ --recursive
```
Expected: `analysis/grades/dt=<date>/grades.ndjson` present.

- [ ] **Step 3: Query it in Athena** (console or CLI, workgroup `rosterbot`):
```sql
SELECT count(*) FROM rosterbot_analysis.grades WHERE dt = '<date>';
SELECT bucket, avg(abs(diff)) mae FROM rosterbot_analysis.grades WHERE dt='<date>' GROUP BY bucket;
```
Expected: row counts > 0; per-bucket MAE returned — the model-audit query path works end to end.

---

## Notes for the executor

- **Reuse, don't reimplement:** `grade` calls `backtest.RunProjectionAnalysis(days, ".backtest/snapshots")` and maps `PlayerProjection` → `analysis.GradeRow`. Do not re-derive actuals or MAE.
- **Snapshot dependency:** `grade` reads `.backtest/snapshots/<date>.json`; on Fargate those arrive via the Task 1 entrypoint sync. Grading a date with no snapshot is a no-op (`source=missing`, skipped) — not an error.
- **Partition projection** means no Glue crawler and no `ALTER TABLE ADD PARTITION`; Athena infers `dt` from the path. Verify the `storage.location.template` matches the actual write path `analysis/grades/dt=YYYY-MM-DD/`.
- **CDK Go prop drift:** `awsglue`/`awsathena` are L1 (Cfn*) constructs; field names map to CloudFormation. Validate with `cdk synth` and adjust against its errors.
- **Retention:** nothing here deletes data. Cache history is retained by bucket versioning; `analysis/` and `backtest/` are append-only. A future cost-control lifecycle rule (expire noncurrent `cache/` versions after N days) is out of scope.
```
