# Run Ledger Prefix Separation (rosterbot-432) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the run ledger its own S3 prefix (`runledger/`) so `GET /v1/runs` listing is cheap and bounded again, instead of sharing `runs/` with per-run output blobs and paginating past however many of those exist.

**Architecture:** `internal/lineupapi/s3lineup` already has the pagination + flat-key filter logic needed to list the ledger (`RunsStore.recent`); extract it into a reusable helper, then build a small `MigrateLedgerPrefix`/`ListLedgerKeys` pair on top of that same helper for a one-time copy of historical records into the new prefix. Flip both the writer (`cmd/ledger.go`) and reader (`lambda/main.go`) call sites to the new prefix, extend the Lambda's IAM read grant, and add a hidden CLI command to run + verify the migration. `OutputStore` (per-run captured output blobs) stays on `runs/<id>/output.json` — it's only ever read by exact key, never listed, so it isn't part of this change.

**Tech Stack:** Go 1.2x, aws-sdk-go-v2 (S3), Cobra CLI, AWS CDK (Go) for infra, fake-S3 test doubles already established in `internal/lineupapi/s3lineup`.

## Global Constraints

- Module: `github.com/nixon-commits/rosterbot`. Follow existing `s3lineup` package conventions: unexported `api`/`listAPI` interfaces, the `ptr(s string) *string` helper, and the `fakeS3`/`listFakeS3` test doubles already defined in `output_test.go` — reuse them, don't redefine.
- Tests require no credentials/network (CLAUDE.md) — all new tests use the existing fake S3, never a real AWS call.
- `gofmt`/`go vet` run automatically via PostToolUse hooks on every Edit/Write; still run `go vet ./...` and `go mod tidy` explicitly before considering the change done (CLAUDE.md).
- Any `cdk deploy`/`cdk diff` in `infra/` MUST pass `-c enableBuild=true` or it destroys the live CodeBuild project — never omit it.
- No new external dependencies.
- This plan covers the code change only (Phase A). The live data migration, deploy, and production verification (Phase B) are operational steps gated by explicit user confirmation and are run interactively after this plan lands — see the handoff note at the end.

---

### Task 1: Extract `listFlatKeys` and refactor `RunsStore.recent`

**Files:**
- Modify: `internal/lineupapi/s3lineup/runs.go:87-146` (the `recent` method)

**Interfaces:**
- Produces: `listFlatKeys(ctx context.Context, client listAPI, bucket, prefix string, limit int) ([]string, error)` — lists every flat (non sub-object) key under `prefix`, paginating via `NextContinuationToken`. `limit > 0` stops once at least `limit` keys are collected and trims to exactly `limit`; `limit <= 0` collects every flat key under the prefix (needed by Task 2's migration, which has no limit).
- Consumes: nothing new — this is a pure extraction of logic already in `recent`.

This is a behavior-preserving refactor (the only existing caller, `recent`, always passes `limit > 0`, so its observable behavior is unchanged). The safety net is the existing test suite, not a new failing test — there's no new behavior yet, so there's nothing meaningful to red/green here. Steps below verify green-before and green-after instead.

- [ ] **Step 1: Confirm the existing tests pass before touching anything**

Run: `go test ./internal/lineupapi/s3lineup/... -run TestRuns -v`
Expected: `TestRunsListIgnoresOutputSubKeys` and `TestRunsListPaginatesPastOutputSubKeys` both PASS.

- [ ] **Step 2: Extract `listFlatKeys` and rewrite `recent` to call it**

Replace the body of `internal/lineupapi/s3lineup/runs.go` from the `recent` function (currently lines 87-146) with:

```go
// listFlatKeys lists every flat ledger key under prefix -- i.e. every key
// immediately under prefix with no further "/" in the remainder, which
// excludes per-run output sub-objects like <prefix><id>/output.json.
// Pagination follows NextContinuationToken. If limit > 0, listing stops once
// at least limit flat keys have been collected and the result is trimmed to
// exactly limit; limit <= 0 collects every flat key under the prefix.
func listFlatKeys(ctx context.Context, client listAPI, bucket, prefix string, limit int) ([]string, error) {
	var keys []string
	var token *string
	for {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            &bucket,
			Prefix:            &prefix,
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			if o.Key == nil {
				continue
			}
			// Ledger records are <prefix><invts>-<id>.json (flat). Skip
			// per-run sub-objects like <prefix><id>/output.json so they
			// don't decode as phantom zero-value runs.
			if strings.Contains(strings.TrimPrefix(*o.Key, prefix), "/") {
				continue
			}
			keys = append(keys, *o.Key)
		}
		if limit > 0 && len(keys) >= limit {
			break
		}
		if out.IsTruncated == nil || !*out.IsTruncated || out.NextContinuationToken == nil {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys) // defensive: ensure newest-first ordering
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

// recent lists the newest `limit` ledger objects and reads each. Ledger keys
// sort newest-first (inverted-timestamp prefix) among themselves, but they
// share the runs/ prefix with per-run sub-objects (runs/<hex-id>/output.json)
// whose hex ids can sort anywhere relative to the ledger block - including
// entirely before it. A single-page list can therefore turn up zero ledger
// keys even though many exist, so listFlatKeys paginates (following
// NextContinuationToken) until it has collected `limit` ledger keys or pages
// are exhausted.
func (s *RunsStore) recent(ctx context.Context, limit int) ([]lineupapi.RunDetail, error) {
	keys, err := listFlatKeys(ctx, s.client, s.bucket, s.prefix, limit)
	if err != nil {
		return nil, err
	}

	var recs []lineupapi.RunDetail
	for _, k := range keys {
		obj, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
		if err != nil {
			continue
		}
		data, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
			continue
		}
		var rec lineupapi.RunDetail
		if json.Unmarshal(data, &rec) == nil {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}
```

Leave everything else in the file (imports, `RunsStore`, `NewRuns`, `objKey`, `PutRun`, `List`, `Get`, the trailing `var _ lineupapi.RunStore = ...`) untouched. No import changes are needed — `sort` and `strings` are already imported.

- [ ] **Step 3: Re-run the same tests to confirm no regression**

Run: `go test ./internal/lineupapi/s3lineup/... -run TestRuns -v`
Expected: both tests still PASS, byte-identical behavior to Step 1.

- [ ] **Step 4: Commit**

```bash
git add internal/lineupapi/s3lineup/runs.go
git commit -m "refactor(s3lineup): extract listFlatKeys helper from RunsStore.recent"
```

---

### Task 2: Add `MigrateLedgerPrefix` and `ListLedgerKeys`

**Files:**
- Create: `internal/lineupapi/s3lineup/migrate.go`
- Create: `internal/lineupapi/s3lineup/migrate_test.go`

**Interfaces:**
- Consumes: `listFlatKeys(ctx, client listAPI, bucket, prefix string, limit int) ([]string, error)` (Task 1); `ptr(s string) *string` (already in `s3lineup.go`); the `fakeS3`/`listFakeS3` test doubles and `keys(map[string][]byte) []string` helper (already in `output_test.go`, same package).
- Produces: `ListLedgerKeys(ctx context.Context, client listAPI, bucket, prefix string) ([]string, error)` and `MigrateLedgerPrefix(ctx context.Context, client listAPI, bucket, srcPrefix, dstPrefix string) ([]string, error)` — both exported, consumed by Task 4's CLI command.

- [ ] **Step 1: Write the failing tests**

Create `internal/lineupapi/s3lineup/migrate_test.go`:

```go
package s3lineup

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestMigrateLedgerPrefixCopiesFlatKeysOnly(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{
		"runs/9999999999-abc.json": []byte(`{"id":"abc","status":"SUCCESS","started_at":"2026-06-20T00:00:00Z"}`),
		"runs/8888888888-def.json": []byte(`{"id":"def","status":"SUCCESS","started_at":"2026-06-21T00:00:00Z"}`),
		"runs/abc/output.json":     []byte(`{"type":"grade","data":{}}`),
	}}
	lf := &listFakeS3{fakeS3: f}

	copied, err := MigrateLedgerPrefix(context.Background(), lf, "b", "runs/", "runledger/")
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("want 2 keys copied, got %d: %v", len(copied), copied)
	}

	if _, ok := f.objects["runledger/9999999999-abc.json"]; !ok {
		t.Fatalf("expected runledger/9999999999-abc.json to exist; got keys %v", keys(f.objects))
	}
	if _, ok := f.objects["runledger/8888888888-def.json"]; !ok {
		t.Fatalf("expected runledger/8888888888-def.json to exist; got keys %v", keys(f.objects))
	}
	if _, ok := f.objects["runledger/abc/output.json"]; ok {
		t.Fatal("output sub-object should not have been migrated")
	}
	if _, ok := f.objects["runs/9999999999-abc.json"]; !ok {
		t.Fatal("source ledger object should still exist after copy (this is a copy, not a move)")
	}
}

func TestMigrateLedgerPrefixPreservesBytesExactly(t *testing.T) {
	body := []byte(`{"id":"xyz","status":"FAILED","started_at":"2026-06-22T00:00:00Z","log_tail":"boom\nline2"}`)
	f := &fakeS3{objects: map[string][]byte{"runs/7777777777-xyz.json": body}}
	lf := &listFakeS3{fakeS3: f}

	if _, err := MigrateLedgerPrefix(context.Background(), lf, "b", "runs/", "runledger/"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, ok := f.objects["runledger/7777777777-xyz.json"]
	if !ok {
		t.Fatal("migrated object missing")
	}
	if string(got) != string(body) {
		t.Fatalf("bytes mismatch: got %s want %s", got, body)
	}
}

func TestMigrateLedgerPrefixNoLimitCollectsAllPages(t *testing.T) {
	objects := map[string][]byte{}
	// 1500 ledger records -- more than one S3 ListObjectsV2 page (1000 max),
	// so this only passes if listFlatKeys(..., limit=0) actually paginates
	// through to the end instead of stopping after the first page.
	for i := 0; i < 1500; i++ {
		key := fmt.Sprintf("runs/%010d-r%04d.json", 9999999999-i, i)
		objects[key] = []byte(fmt.Sprintf(`{"id":"r%04d","status":"SUCCESS","started_at":"2026-06-01T00:00:00Z"}`, i))
	}
	f := &fakeS3{objects: objects}
	lf := &listFakeS3{fakeS3: f}

	copied, err := MigrateLedgerPrefix(context.Background(), lf, "b", "runs/", "runledger/")
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(copied) != 1500 {
		t.Fatalf("want 1500 keys copied, got %d", len(copied))
	}
	dstCount := 0
	for k := range f.objects {
		if strings.HasPrefix(k, "runledger/") {
			dstCount++
		}
	}
	if dstCount != 1500 {
		t.Fatalf("want 1500 objects under runledger/, got %d", dstCount)
	}
}

func TestListLedgerKeysSkipsOutputSubKeys(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{
		"runledger/9999999999-abc.json": []byte(`{"id":"abc"}`),
		"runs/abc/output.json":          []byte(`{"type":"grade","data":{}}`),
	}}
	lf := &listFakeS3{fakeS3: f}

	got, err := ListLedgerKeys(context.Background(), lf, "b", "runledger/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0] != "runledger/9999999999-abc.json" {
		t.Fatalf("want exactly the one ledger key, got %v", got)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/lineupapi/s3lineup/... -run 'TestMigrateLedgerPrefix|TestListLedgerKeys' -v`
Expected: FAIL — `undefined: MigrateLedgerPrefix` / `undefined: ListLedgerKeys` (neither function exists yet).

- [ ] **Step 3: Implement `migrate.go`**

Create `internal/lineupapi/s3lineup/migrate.go`:

```go
package s3lineup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ListLedgerKeys lists every flat ledger key under prefix (skipping per-run
// output sub-keys such as <prefix><id>/output.json), following pagination
// until every match is collected. Used for the run-ledger migration's
// dry-run preview and post-copy verification (cmd/migrate_run_ledger.go).
func ListLedgerKeys(ctx context.Context, client listAPI, bucket, prefix string) ([]string, error) {
	return listFlatKeys(ctx, client, bucket, prefix, 0)
}

// MigrateLedgerPrefix copies every flat ledger key from srcPrefix to
// dstPrefix within the same bucket, byte-for-byte, skipping per-run output
// sub-keys. It is idempotent -- rerunning it overwrites each destination
// object with the current source bytes. Returns the full source keys it
// copied (still under srcPrefix) so the caller can print a count and spot-
// check IDs.
func MigrateLedgerPrefix(ctx context.Context, client listAPI, bucket, srcPrefix, dstPrefix string) ([]string, error) {
	keys, err := listFlatKeys(ctx, client, bucket, srcPrefix, 0)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", srcPrefix, err)
	}
	for _, k := range keys {
		obj, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &k})
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", k, err)
		}
		data, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", k, err)
		}
		dstKey := dstPrefix + strings.TrimPrefix(k, srcPrefix)
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      &bucket,
			Key:         &dstKey,
			Body:        bytes.NewReader(data),
			ContentType: ptr("application/json"),
		}); err != nil {
			return nil, fmt.Errorf("put %s: %w", dstKey, err)
		}
	}
	return keys, nil
}
```

- [ ] **Step 4: Run the tests again to verify they pass**

Run: `go test ./internal/lineupapi/s3lineup/... -run 'TestMigrateLedgerPrefix|TestListLedgerKeys' -v`
Expected: all four PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lineupapi/s3lineup/migrate.go internal/lineupapi/s3lineup/migrate_test.go
git commit -m "feat(s3lineup): add MigrateLedgerPrefix + ListLedgerKeys for rosterbot-432"
```

---

### Task 3: Switch the ledger prefix to `runledger/`

**Files:**
- Modify: `cmd/ledger.go:79`
- Modify: `lambda/main.go:42`
- Modify: `internal/lineupapi/s3lineup/output_test.go:123,158` (test prefix literals)

**Interfaces:**
- Consumes: `s3lineup.NewRuns(ctx, bucket, prefix string) (*RunsStore, error)` (unchanged signature — only the literal argument changes).

- [ ] **Step 1: Update the writer**

In `cmd/ledger.go`, change:

```go
		s, err := s3lineup.NewRuns(context.Background(), bucket, "runs/")
```

to:

```go
		s, err := s3lineup.NewRuns(context.Background(), bucket, "runledger/")
```

- [ ] **Step 2: Update the reader**

In `lambda/main.go`, change:

```go
	runs, err := s3lineup.NewRuns(ctx, bucket, "runs/")
```

to:

```go
	runs, err := s3lineup.NewRuns(ctx, bucket, "runledger/")
```

Leave the next line (`output, err := s3lineup.NewOutput(ctx, bucket, "runs/")`) untouched — output blobs stay on `runs/`.

- [ ] **Step 3: Update the two existing RunsStore tests to use the real production prefix**

In `internal/lineupapi/s3lineup/output_test.go`, `TestRunsListIgnoresOutputSubKeys` currently has:

```go
	f := &fakeS3{objects: map[string][]byte{
		"runs/9999999999-abc.json": []byte(`{"id":"abc","status":"SUCCESS","started_at":"2026-06-20T00:00:00Z"}`),
		"runs/abc/output.json":     []byte(`{"type":"grade","data":{}}`),
	}}
	lf := &listFakeS3{fakeS3: f}
	s := &RunsStore{client: lf, bucket: "b", prefix: "runs/"}
```

Change it to:

```go
	f := &fakeS3{objects: map[string][]byte{
		"runledger/9999999999-abc.json": []byte(`{"id":"abc","status":"SUCCESS","started_at":"2026-06-20T00:00:00Z"}`),
		"runs/abc/output.json":          []byte(`{"type":"grade","data":{}}`),
	}}
	lf := &listFakeS3{fakeS3: f}
	s := &RunsStore{client: lf, bucket: "b", prefix: "runledger/"}
```

(The output sub-key deliberately stays under `runs/` — the point of this test is that `RunsStore` on the `runledger/` prefix never even sees the unrelated `runs/` output objects, which is a stronger statement than the old test made.)

In the same file, `TestRunsListPaginatesPastOutputSubKeys` currently builds:

```go
	objects := map[string][]byte{}
	// 32 output sub-objects with hex ids "00".."1f" - all start with a digit
	// below '8', so they sort before every ledger key below and would fully
	// occupy a small page on their own.
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("%02x", i)
		objects["runs/"+id+"/output.json"] = []byte(`{"type":"grade","data":{}}`)
	}
	// 3 ledger records, newest first by inverted timestamp, sorting after all
	// of the above.
	objects["runs/8214999999-newest.json"] = []byte(`{"id":"newest","status":"SUCCESS","started_at":"2026-07-03T00:00:00Z"}`)
	objects["runs/8215999999-middle.json"] = []byte(`{"id":"middle","status":"SUCCESS","started_at":"2026-07-02T00:00:00Z"}`)
	objects["runs/8216999999-oldest.json"] = []byte(`{"id":"oldest","status":"SUCCESS","started_at":"2026-07-01T00:00:00Z"}`)

	f := &fakeS3{objects: objects}
	lf := &listFakeS3{fakeS3: f}
	s := &RunsStore{client: lf, bucket: "b", prefix: "runs/"}
```

Change the ledger keys and the store's prefix to `runledger/`, keeping the sub-object keys on `runs/` (they represent the OTHER prefix's objects and existing in the same bucket is exactly why the two used to interleave — but since the store below now only lists `runledger/`, they won't appear regardless of key content; keep them anyway as a sanity check that a differently-prefixed key is never returned):

```go
	objects := map[string][]byte{}
	// 32 unrelated output sub-objects under the OTHER prefix (runs/), left in
	// the same bucket to confirm RunsStore scoped to runledger/ never lists
	// them regardless of how their keys sort.
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("%02x", i)
		objects["runs/"+id+"/output.json"] = []byte(`{"type":"grade","data":{}}`)
	}
	// 3 ledger records, newest first by inverted timestamp.
	objects["runledger/8214999999-newest.json"] = []byte(`{"id":"newest","status":"SUCCESS","started_at":"2026-07-03T00:00:00Z"}`)
	objects["runledger/8215999999-middle.json"] = []byte(`{"id":"middle","status":"SUCCESS","started_at":"2026-07-02T00:00:00Z"}`)
	objects["runledger/8216999999-oldest.json"] = []byte(`{"id":"oldest","status":"SUCCESS","started_at":"2026-07-01T00:00:00Z"}`)

	f := &fakeS3{objects: objects}
	lf := &listFakeS3{fakeS3: f}
	s := &RunsStore{client: lf, bucket: "b", prefix: "runledger/"}
```

The rest of both test functions (the `s.List(...)` call and assertions) is unchanged.

- [ ] **Step 4: Run the full s3lineup + cmd + lambda-adjacent test suite**

Run: `go build ./... && go test ./internal/lineupapi/... ./cmd/... -v -run TestRuns`
Expected: everything builds; `TestRunsListIgnoresOutputSubKeys` and `TestRunsListPaginatesPastOutputSubKeys` PASS with the new prefix.

- [ ] **Step 5: Commit**

```bash
git add cmd/ledger.go lambda/main.go internal/lineupapi/s3lineup/output_test.go
git commit -m "fix(lineupapi): point run ledger writer+reader at runledger/ prefix (rosterbot-432)"
```

---

### Task 4: Add the `migrate-run-ledger` CLI command

**Files:**
- Create: `cmd/migrate_run_ledger.go`
- Create: `cmd/migrate_run_ledger_test.go`

**Interfaces:**
- Consumes: `s3lineup.ListLedgerKeys`, `s3lineup.MigrateLedgerPrefix` (Task 2); `rootCmd` (package-level `*cobra.Command` already defined in `cmd/root.go`, same pattern as `ledgerCmd` in `cmd/ledger.go`).
- Produces: `diffLedgerKeySuffixes(srcKeys []string, srcPrefix string, dstKeys []string, dstPrefix string) []string` — pure, unit-tested without AWS.

Note: `runMigrateRunLedger` itself (like `runLedger` in `cmd/ledger.go`) builds a real AWS config/client and isn't unit-tested — there's no fake-able seam at that layer in this codebase's existing pattern. Only the pure `diffLedgerKeySuffixes` helper gets tests here; the command itself is exercised live in Phase B under `--dry-run` first.

- [ ] **Step 1: Write the failing tests for the pure diff helper**

Create `cmd/migrate_run_ledger_test.go`:

```go
package cmd

import (
	"reflect"
	"testing"
)

func TestDiffLedgerKeySuffixesAllPresent(t *testing.T) {
	src := []string{"runs/9999999999-abc.json", "runs/8888888888-def.json"}
	dst := []string{"runledger/9999999999-abc.json", "runledger/8888888888-def.json"}
	missing := diffLedgerKeySuffixes(src, "runs/", dst, "runledger/")
	if len(missing) != 0 {
		t.Fatalf("want no missing keys, got %v", missing)
	}
}

func TestDiffLedgerKeySuffixesReportsMissing(t *testing.T) {
	src := []string{"runs/9999999999-abc.json", "runs/8888888888-def.json"}
	dst := []string{"runledger/9999999999-abc.json"}
	missing := diffLedgerKeySuffixes(src, "runs/", dst, "runledger/")
	want := []string{"8888888888-def.json"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("got %v, want %v", missing, want)
	}
}

func TestDiffLedgerKeySuffixesEmptySource(t *testing.T) {
	missing := diffLedgerKeySuffixes(nil, "runs/", nil, "runledger/")
	if len(missing) != 0 {
		t.Fatalf("want no missing keys for empty source, got %v", missing)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/... -run TestDiffLedgerKeySuffixes -v`
Expected: FAIL — `undefined: diffLedgerKeySuffixes`.

- [ ] **Step 3: Implement the command**

Create `cmd/migrate_run_ledger.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
)

const (
	oldRunLedgerPrefix = "runs/"
	newRunLedgerPrefix = "runledger/"
)

var migrateRunLedgerDryRun bool

// migrate-run-ledger is a one-time internal command (rosterbot-432) that
// copies existing run ledger records from the old shared runs/ prefix to
// their own runledger/ prefix, then verifies the copy by re-listing the
// destination and diffing against the source key set. Copying is
// idempotent (the same source bytes overwrite the same destination key), so
// it's safe to rerun.
var migrateRunLedgerCmd = &cobra.Command{
	Use:    "migrate-run-ledger",
	Short:  "Internal: one-time copy of run ledger records from runs/ to runledger/ (rosterbot-432)",
	Hidden: true,
	RunE:   runMigrateRunLedger,
}

func init() {
	migrateRunLedgerCmd.Flags().BoolVar(&migrateRunLedgerDryRun, "dry-run", false, "list and count ledger records without copying")
	rootCmd.AddCommand(migrateRunLedgerCmd)
}

func runMigrateRunLedger(cmd *cobra.Command, args []string) error {
	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		return fmt.Errorf("migrate-run-ledger: STATE_BUCKET must be set")
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(cfg)

	if migrateRunLedgerDryRun {
		srcKeys, err := s3lineup.ListLedgerKeys(ctx, client, bucket, oldRunLedgerPrefix)
		if err != nil {
			return fmt.Errorf("list %s: %w", oldRunLedgerPrefix, err)
		}
		fmt.Printf("dry-run: would migrate %d ledger record(s) from %s to %s\n", len(srcKeys), oldRunLedgerPrefix, newRunLedgerPrefix)
		return nil
	}

	copied, err := s3lineup.MigrateLedgerPrefix(ctx, client, bucket, oldRunLedgerPrefix, newRunLedgerPrefix)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	fmt.Printf("copied %d ledger record(s) from %s to %s\n", len(copied), oldRunLedgerPrefix, newRunLedgerPrefix)

	dstKeys, err := s3lineup.ListLedgerKeys(ctx, client, bucket, newRunLedgerPrefix)
	if err != nil {
		return fmt.Errorf("verify: list %s: %w", newRunLedgerPrefix, err)
	}
	missing := diffLedgerKeySuffixes(copied, oldRunLedgerPrefix, dstKeys, newRunLedgerPrefix)
	if len(missing) > 0 {
		return fmt.Errorf("verify: %d record(s) missing from %s after migration: %v", len(missing), newRunLedgerPrefix, missing)
	}
	fmt.Printf("verify: %d record(s) present under %s, matches source count\n", len(dstKeys), newRunLedgerPrefix)
	return nil
}

// diffLedgerKeySuffixes returns the suffixes (the part after each prefix,
// e.g. "9999999999-abc.json") present in srcKeys but missing from dstKeys.
// An empty result means every source record was found at the destination.
func diffLedgerKeySuffixes(srcKeys []string, srcPrefix string, dstKeys []string, dstPrefix string) []string {
	dstSet := make(map[string]bool, len(dstKeys))
	for _, k := range dstKeys {
		dstSet[strings.TrimPrefix(k, dstPrefix)] = true
	}
	var missing []string
	for _, k := range srcKeys {
		suffix := strings.TrimPrefix(k, srcPrefix)
		if !dstSet[suffix] {
			missing = append(missing, suffix)
		}
	}
	return missing
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/... -run TestDiffLedgerKeySuffixes -v`
Expected: all three PASS.

- [ ] **Step 5: Build the whole module to confirm the new command wires in cleanly**

Run: `go build ./...`
Expected: clean build. Optionally sanity-check registration: `go run . --help | grep migrate-run-ledger` should print nothing (it's `Hidden: true`, by design — same as `run-ledger`), but `go run . migrate-run-ledger --help` should show the `--dry-run` flag.

- [ ] **Step 6: Commit**

```bash
git add cmd/migrate_run_ledger.go cmd/migrate_run_ledger_test.go
git commit -m "feat(cmd): add hidden migrate-run-ledger command for rosterbot-432"
```

---

### Task 5: Update infra IAM grant + stale comment

**Files:**
- Modify: `infra/infra.go:174-178` (comment above the Lambda construct)
- Modify: `infra/infra.go:207-210` (IAM read grants)

**Interfaces:**
- None (CDK construct wiring, no Go interfaces involved).

- [ ] **Step 1: Update the comment above the Lambda construct**

In `infra/infra.go`, change:

```go
	// --- Lineup + control API: Go Lambda behind a Function URL ---
	// Serves GET /v1/lineup/today from the precomputed JSON the hourly optimize
	// run publishes (lineup/ prefix), GET /v1/runs from the run ledger (runs/
	// prefix written by entrypoint.sh), and POST /v1/jobs/{name} which launches
	// the existing Fargate task. No Chrome/Fantrax on the request path.
```

to:

```go
	// --- Lineup + control API: Go Lambda behind a Function URL ---
	// Serves GET /v1/lineup/today from the precomputed JSON the hourly optimize
	// run publishes (lineup/ prefix), GET /v1/runs from the run ledger
	// (runledger/ prefix written by entrypoint.sh) plus captured output blobs
	// (runs/<id>/output.json), and POST /v1/jobs/{name} which launches the
	// existing Fargate task. No Chrome/Fantrax on the request path.
```

- [ ] **Step 2: Add the `runledger/*` read grant, keeping the existing `runs/*` grant**

Change:

```go
	// Least privilege: read lineup/ + runs/ objects and the one token param.
	stateBucket.GrantRead(apiFn, jsii.String("lineup/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runs/*"))
	stateBucket.GrantRead(apiFn, jsii.String("notifications/*"))
```

to:

```go
	// Least privilege: read lineup/ + the run ledger/output objects + the one
	// token param. runledger/ is the ledger (rosterbot-432); runs/ is still
	// read for per-run captured output blobs (runs/<id>/output.json).
	stateBucket.GrantRead(apiFn, jsii.String("lineup/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runledger/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runs/*"))
	stateBucket.GrantRead(apiFn, jsii.String("notifications/*"))
```

- [ ] **Step 3: Synthesize the stack and confirm the new grant appears**

Run: `cd infra && npx cdk synth -c enableBuild=true > /tmp/synth.json 2>&1; grep -c runledger /tmp/synth.json`
Expected: exits with the synthesized template written, and the grep count is `>= 1` (the new `runledger/*` resource ARN pattern appears in the `LineupApi`'s IAM policy). Also run `go build ./...` from `infra/` implicitly via `cdk synth` (it compiles the Go CDK app) — a non-zero `cdk synth` exit code means a compile error, investigate before proceeding.

- [ ] **Step 4: Commit**

```bash
git add infra/infra.go
git commit -m "infra: grant LineupApi read access to runledger/* (rosterbot-432)"
```

---

### Task 6: Update docs

**Files:**
- Modify: `README.md:195-196`
- Modify: `docs/aws-deployment.md:16-17`
- Modify: `docs/aws-architecture.md` (mermaid diagram block + arrows)

**Interfaces:** None (documentation only).

- [ ] **Step 1: README.md**

Change:

```markdown
The run ledger is written by `entrypoint.sh` (one S3 object per run under the
`runs/` prefix, via the internal `run-ledger` command) so it covers both
```

to:

```markdown
The run ledger is written by `entrypoint.sh` (one S3 object per run under the
`runledger/` prefix, via the internal `run-ledger` command) so it covers both
```

- [ ] **Step 2: docs/aws-deployment.md — Lineup + control API bullet**

Change:

```markdown
- **Lineup + control API** — a Go Lambda (`LineupApi`) behind a **Function URL** (output `LineupApiUrl`). Routes: `GET /v1/lineup/today` (from `lineup/today.json`), `GET /v1/runs` + `GET /v1/runs/{id}` (the run ledger under `runs/`), and `POST /v1/jobs/{name}` (launches the existing Fargate task via `ecs:RunTask`, command overridden, `RUN_TRIGGER=manual`). Auth is a Bearer token in SSM (`/rosterbot/ROSTERBOT_API_TOKEN`), enforced in the function (Function URL auth type `NONE`). IAM is least-privilege: read `lineup/*`+`runs/*`, `ssm:GetParameter` on the token, `ecs:RunTask` on the task def, `iam:PassRole` on the task/execution roles. Tasks it launches use a dedicated egress-only SG (`TaskSg`) in the default VPC's public subnets. See the README "Lineup HTTP API" section for the contract.
```

to:

```markdown
- **Lineup + control API** — a Go Lambda (`LineupApi`) behind a **Function URL** (output `LineupApiUrl`). Routes: `GET /v1/lineup/today` (from `lineup/today.json`), `GET /v1/runs` + `GET /v1/runs/{id}` (the run ledger under `runledger/`, split out from `runs/` in rosterbot-432 so listing no longer pages past per-run output blobs), and `POST /v1/jobs/{name}` (launches the existing Fargate task via `ecs:RunTask`, command overridden, `RUN_TRIGGER=manual`). Auth is a Bearer token in SSM (`/rosterbot/ROSTERBOT_API_TOKEN`), enforced in the function (Function URL auth type `NONE`). IAM is least-privilege: read `lineup/*`+`runledger/*`+`runs/*` (the last for per-run captured output), `ssm:GetParameter` on the token, `ecs:RunTask` on the task def, `iam:PassRole` on the task/execution roles. Tasks it launches use a dedicated egress-only SG (`TaskSg`) in the default VPC's public subnets. See the README "Lineup HTTP API" section for the contract.
```

- [ ] **Step 3: docs/aws-deployment.md — Run ledger bullet**

Change:

```markdown
- **Run ledger** — `entrypoint.sh` writes one JSON object per run to `runs/<invTs>-<taskId>.json` (start = `RUNNING`, end = `SUCCESS`/`FAILED` with exit code + a log tail on failure) via the internal `rosterbot run-ledger` command. The inverted-timestamp key prefix sorts newest-first, so `GET /v1/runs` is a single `MaxKeys` list. Covers scheduled and API-triggered runs alike (`RUN_TRIGGER` distinguishes `schedule` vs `manual`).
```

to:

```markdown
- **Run ledger** — `entrypoint.sh` writes one JSON object per run to `runledger/<invTs>-<taskId>.json` (start = `RUNNING`, end = `SUCCESS`/`FAILED` with exit code + a log tail on failure) via the internal `rosterbot run-ledger` command. Until rosterbot-432 this lived under `runs/`, shared with per-run output blobs (`runs/<id>/output.json`), so listing had to page past however many of those existed; the ledger now has its own prefix. The inverted-timestamp key prefix sorts newest-first, so `GET /v1/runs` is a cheap, bounded list scoped to `runledger/` alone. Covers scheduled and API-triggered runs alike (`RUN_TRIGGER` distinguishes `schedule` vs `manual`).
```

- [ ] **Step 4: docs/aws-architecture.md — mermaid diagram prefixes**

Change:

```
        P_lineup["lineup/ — precomputed lineup JSON"]
        P_runs["runs/ — run ledger + captured output"]
        P_notif["notifications/ — activity feed events"]
```

to:

```
        P_lineup["lineup/ — precomputed lineup JSON"]
        P_runledger["runledger/ — run ledger records"]
        P_runs["runs/ — captured run output (output.json)"]
        P_notif["notifications/ — activity feed events"]
```

- [ ] **Step 5: docs/aws-architecture.md — mermaid diagram TASK write arrows**

Change:

```
    TASK -->|optimize publishes| P_lineup
    TASK -->|run-ledger writes| P_runs
    TASK -->|notify events| P_notif
```

to:

```
    TASK -->|optimize publishes| P_lineup
    TASK -->|run-ledger writes| P_runledger
    TASK -->|job output writes| P_runs
    TASK -->|notify events| P_notif
```

- [ ] **Step 6: docs/aws-architecture.md — mermaid diagram LAMBDA read arrows**

Change:

```
    P_lineup --> LAMBDA
    P_runs --> LAMBDA
    P_notif --> LAMBDA
```

to:

```
    P_lineup --> LAMBDA
    P_runledger --> LAMBDA
    P_runs --> LAMBDA
    P_notif --> LAMBDA
```

- [ ] **Step 7: Commit**

```bash
git add README.md docs/aws-deployment.md docs/aws-architecture.md
git commit -m "docs: reflect runledger/ prefix split for the run ledger (rosterbot-432)"
```

---

### Task 7: Final verification sweep

**Files:** none (verification only).

- [ ] **Step 1: Vet and tidy**

Run: `go vet ./... && go mod tidy && git status --short`
Expected: `go vet` clean; `go mod tidy` makes no changes (no new deps were added) — if `git status --short` shows `go.mod`/`go.sum` modified, inspect why before proceeding.

- [ ] **Step 2: Full build**

Run: `go build ./...`
Expected: clean build, no errors.

- [ ] **Step 3: Full test suite**

Run: `go test ./...`
Expected: every package `ok`, none `FAIL`. In particular `internal/lineupapi/s3lineup` and `cmd` should show all-new tests passing alongside the untouched rest of the suite.

- [ ] **Step 4: Smoke-test dry-run commands still work**

Run: `make run-all`
Expected: same as any other dry-run sweep — every existing command still exits cleanly. `migrate-run-ledger` is intentionally NOT added to this target (like `run-ledger`, it's an internal one-off tool gated on `STATE_BUCKET` and flags, not a general dry-run-able command).

- [ ] **Step 5: Final commit if anything is outstanding**

```bash
git status --short
```

If clean (everything was committed per-task above), there's nothing to do here. If `go mod tidy` or the smoke test touched anything, commit it:

```bash
git add -A
git commit -m "chore: post-implementation verification cleanup for rosterbot-432"
```

---

## Handoff to Phase B (live migration + deploy — NOT part of this plan's task-by-task execution)

Once all 7 tasks above are done and `go test ./...` is green on this branch, the remaining work is operational and touches production S3 data and a live Lambda, so it is **not** pre-scripted here — it's driven interactively with explicit user confirmation at each risky step, per the issue's own instructions:

1. Confirm with the user before running `migrate-run-ledger` against the live `STATE_BUCKET` (bucket `infrastack-statebucket446e0578-rm3scvdxnnyg`, account `476646938644`).
2. Run `go run . migrate-run-ledger --dry-run` first (read-only preview + count), show the user, then run it for real.
3. Spot-check a handful of migrated IDs (`aws s3 cp` old vs new, or `aws s3api list-objects-v2 --prefix runledger/` vs `--prefix runs/`) in addition to the command's own built-in count verification.
4. Decide with the user how the Lambda + Fargate image roll out: the CDK `GoFunction` compiles `lambda/main.go` directly from source at `cdk deploy` time (independent of the ECR image), while `cmd/ledger.go`'s writer only takes effect once a new container image is built (CodeBuild, triggered by a push to `main`) and the Fargate task picks up `:latest`. Deploying the Lambda before the new image ships would leave a window where new runs still write to the old `runs/` prefix while the reader looks at `runledger/` — flag this explicitly and get sign-off on sequencing (e.g., merge to `main` first so CodeBuild's post-build `cdk deploy -c enableBuild=true` ships both together, vs. manually deploying the Lambda now and accepting a gap until merge).
5. Deploy via `cd infra && npx cdk deploy -c enableBuild=true` (always with the flag) if deploying manually, or merge to `main` and confirm the CodeBuild pipeline's build + auto-deploy succeeded.
6. Verify `GET /v1/runs` and `GET /v1/runs/{id}` live (Function URL + Bearer token from SSM `/rosterbot/ROSTERBOT_API_TOKEN`), confirm newest-first ordering matches pre-migration output.
7. `bd close rosterbot-432`, then the standard session-close protocol (commit, push, verify `git status` clean against origin).
