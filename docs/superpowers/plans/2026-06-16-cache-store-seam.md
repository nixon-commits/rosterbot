# Cache Store Seam Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `cache.FileCache[T]` a storage **Store** seam so the Cache can persist to S3 per-key (pure per-key `GetObject`/`PutObject`), retiring the `cache/` bulk sync in `entrypoint.sh`.

**Architecture:** Introduce a byte-level `Store` interface in `internal/cache` (zero-dep leaf) with an `fsStore` (current behaviour) and a `MemStore` (tests). `FileCache[T]` keeps all deep behaviour (TTL, envelope, stale-fallback, Notify) and delegates raw bytes to a Store. A process-wide default store (`cache.SetDefaultStore`, mirroring the existing `cache.Verbose`/`cache.Notify` package globals) means **none of the ~30 `cache.New[T](dir, ttl)` call sites change**. The S3 adapter lives in its own package `internal/cachestore/s3store` (imports `aws-sdk-go-v2`), wired in by `cmd`.

**Tech Stack:** Go generics, `aws-sdk-go-v2` (`config`, `service/s3`), stdlib.

**Spec/decisions:** CONTEXT.md (Cache, Store terms), `docs/adr/0001-s3-not-db-for-cache.md`. Grilling decisions: pure per-key S3; s3 adapter in separate pkg; cache-only scope (session/claims stay on entrypoint sync).

**Scope:** Cache only. `entrypoint.sh` keeps syncing `session/` (chromedp cookie, written by the go-fantrax lib) and `claims/` (ledger+cursor). Those are the future Analysis Store, out of scope here.

---

### Task 1: Define the `Store` interface and `fsStore`, route `FileCache` through it

**Files:**
- Modify: `internal/cache/cache.go`
- Create: `internal/cache/store.go`
- Test: `internal/cache/store_test.go`

The existing `cache_test.go` (which calls `New[string](dir, ttl)` and checks `dir/test.json` on disk) MUST keep passing — `fsStore` preserves the exact file layout, so it does.

- [ ] **Step 1: Write the failing test** — `internal/cache/store_test.go`

```go
package cache

import "testing"

func TestFSStore_RoundTripAndSuffix(t *testing.T) {
	dir := t.TempDir()
	s := fsStore{root: dir}

	if _, found, err := s.Get("missing"); err != nil || found {
		t.Fatalf("missing key: found=%v err=%v, want false,nil", found, err)
	}
	if err := s.Put("k", []byte("hi")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, found, err := s.Get("k")
	if err != nil || !found || string(got) != "hi" {
		t.Fatalf("get: %q found=%v err=%v", got, found, err)
	}
	// Layout must stay <dir>/<key>.json so existing .cache files keep working.
	if _, err := os.Stat(filepath.Join(dir, "k.json")); err != nil {
		t.Fatalf("expected k.json on disk: %v", err)
	}
	if err := s.Remove("k"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := s.Remove("k"); err != nil {
		t.Fatalf("remove missing should be nil: %v", err)
	}
}
```

Add imports `os`, `path/filepath` to this test file.

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./internal/cache/ -run TestFSStore -v`
Expected: FAIL — `undefined: fsStore`

- [ ] **Step 3: Create `internal/cache/store.go`**

```go
package cache

import (
	"errors"
	"os"
	"path/filepath"
)

// Store is the byte-level storage seam behind the Cache. FileCache[T] owns the
// TTL/envelope logic; a Store only moves opaque bytes keyed by cache key.
// found is false (with nil err) when the key is absent.
type Store interface {
	Get(key string) (data []byte, found bool, err error)
	Put(key string, data []byte) error
	Remove(key string) error
}

// fsStore stores each entry as <root>/<key>.json — the historical .cache layout.
type fsStore struct{ root string }

func (s fsStore) path(key string) string { return filepath.Join(s.root, key+".json") }

func (s fsStore) Get(key string) ([]byte, bool, error) {
	b, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s fsStore) Put(key string, data []byte) error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(key), data, 0o644)
}

func (s fsStore) Remove(key string) error {
	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
```

- [ ] **Step 4: Refactor `FileCache` in `internal/cache/cache.go` to hold a `Store`**

Replace the struct + `New` + `path` + `load`/`loadAny`/`save`/`Invalidate` so they go through the store, keying by `key` (not by filesystem path). Apply these exact edits:

Struct:
```go
// FileCache provides TTL-based caching for any JSON-serializable type.
type FileCache[T any] struct {
	store Store
	ttl   time.Duration
}
```

`New`:
```go
// New creates a FileCache backed by the configured store (see SetDefaultStore),
// or a filesystem store rooted at dir when no default store is set. A TTL of 0
// means the cache is always bypassed (useful for --no-cache).
func New[T any](dir string, ttl time.Duration) *FileCache[T] {
	return &FileCache[T]{store: storeForDir(dir), ttl: ttl}
}
```

In `Get`, replace `path := c.path(key)` and the `c.load(path)` call:
```go
func (c *FileCache[T]) Get(key string, fetch func() (T, error)) (T, error) {
	if c.ttl > 0 {
		if data, ok := c.load(key); ok {
			if Verbose {
				fmt.Fprintf(os.Stderr, "cache hit: %s\n", key)
			}
			return data, nil
		}
		if Verbose {
			fmt.Fprintf(os.Stderr, "cache miss: %s\n", key)
		}
	}

	data, err := fetch()
	if err != nil {
		return data, err
	}
	if err := c.save(key, data); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, err)
	}
	return data, nil
}
```

In `GetWithStaleFallback`, replace `path := c.path(key)`, `c.save(path, ...)`, `c.loadAny(path)` with key-based calls:
```go
func (c *FileCache[T]) GetWithStaleFallback(key string, fetch func() (T, error)) (T, error) {
	data, err := fetch()
	if err == nil {
		if saveErr := c.save(key, data); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, saveErr)
		}
		return data, nil
	}
	if stale, ok := c.loadAny(key); ok {
		fmt.Fprintf(os.Stderr, "⚠️ stale cache: %s (%v)\n", key, err)
		if Notify != nil {
			Notify("⚠️ Stale cache", fmt.Sprintf("Serving stale %s", key))
		}
		return stale, nil
	}
	return data, err
}
```

Replace `loadAny`, `Invalidate`, `path`, `load`, `save` with key-based store calls:
```go
func (c *FileCache[T]) loadAny(key string) (T, bool) {
	var zero T
	raw, found, err := c.store.Get(key)
	if err != nil || !found {
		return zero, false
	}
	var env envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		return zero, false
	}
	return env.Data, true
}

// Invalidate removes a single cached entry.
func (c *FileCache[T]) Invalidate(key string) error {
	return c.store.Remove(key)
}

func (c *FileCache[T]) load(key string) (T, bool) {
	var zero T
	raw, found, err := c.store.Get(key)
	if err != nil || !found {
		return zero, false
	}
	var env envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintf(os.Stderr, "warning: corrupt cache entry %s: %v\n", key, err)
		return zero, false
	}
	if time.Since(env.FetchedAt) > c.ttl {
		return zero, false
	}
	return env.Data, true
}

func (c *FileCache[T]) save(key string, data T) error {
	env := envelope[T]{FetchedAt: time.Now(), Data: data}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return c.store.Put(key, b)
}
```

Remove the now-unused `"path/filepath"` import from `cache.go` if present (it moved to `store.go`). Keep `InvalidateAll(dir string)` as-is (it stays a filesystem dev tool; S3 cache is cleared with `aws s3 rm`).

- [ ] **Step 5: Add `storeForDir` + default-store plumbing** — append to `store.go`

```go
// defaultStore, when set via SetDefaultStore, backs every FileCache regardless
// of the dir passed to New. Mirrors the package-global pattern of Verbose/Notify.
var defaultStore Store

// SetDefaultStore makes every FileCache use s instead of a filesystem store.
// cmd calls this once at startup with the S3 store when STATE_BUCKET is set.
func SetDefaultStore(s Store) { defaultStore = s }

func storeForDir(dir string) Store {
	if defaultStore != nil {
		return defaultStore
	}
	return fsStore{root: dir}
}
```

- [ ] **Step 6: Run the whole cache package — existing tests + new must pass**

Run: `go test ./internal/cache/ -v`
Expected: PASS (existing `TestGet_*`, `TestInvalidate*` still green because `fsStore` preserves the `dir/key.json` layout; new `TestFSStore_RoundTripAndSuffix` passes).

- [ ] **Step 7: Build everything + vet**

Run: `go build ./... && go vet ./...`
Expected: clean (no call sites changed — `New[T](dir, ttl)` signature is unchanged).

- [ ] **Step 8: Commit**

```bash
git add internal/cache/
git commit -m "feat(cache): extract Store seam (fsStore) behind FileCache"
```

---

### Task 2: `MemStore` + verify `SetDefaultStore` routing

**Files:**
- Modify: `internal/cache/store.go`
- Test: `internal/cache/store_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/cache/store_test.go`

```go
func TestSetDefaultStore_RoutesAllCaches(t *testing.T) {
	mem := NewMemStore()
	SetDefaultStore(mem)
	t.Cleanup(func() { SetDefaultStore(nil) })

	// dir is ignored when a default store is set.
	c := New[string]("/nonexistent-dir", time.Hour)
	if _, err := c.Get("k", func() (string, error) { return "v", nil }); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, found, _ := mem.Get("k"); !found {
		t.Fatal("expected value written to the default MemStore, not the filesystem")
	}

	// Cache hit comes back from the store without calling fetch.
	got, err := c.Get("k", func() (string, error) { return "SHOULD-NOT-RUN", nil })
	if err != nil || got != "v" {
		t.Fatalf("hit: got %q err %v", got, err)
	}
}
```

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./internal/cache/ -run TestSetDefaultStore -v`
Expected: FAIL — `undefined: NewMemStore`

- [ ] **Step 3: Add `MemStore` to `store.go`**

```go
import "sync" // add to store.go's import block

// MemStore is an in-memory Store for hermetic tests in this and other packages.
type MemStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemStore() *MemStore { return &MemStore{m: map[string][]byte{}} }

func (s *MemStore) Get(key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	return b, ok, nil
}

func (s *MemStore) Put(key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	s.m[key] = cp
	return nil
}

func (s *MemStore) Remove(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
```

- [ ] **Step 4: Run, verify it passes**

Run: `go test ./internal/cache/ -run TestSetDefaultStore -v`
Expected: PASS

- [ ] **Step 5: Full package + vet, then commit**

Run: `go test ./internal/cache/ && go vet ./internal/cache/`
```bash
git add internal/cache/store.go internal/cache/store_test.go
git commit -m "feat(cache): MemStore + SetDefaultStore routing"
```

---

### Task 3: S3 store adapter

**Files:**
- Create: `internal/cachestore/s3store/s3store.go`
- Test: `internal/cachestore/s3store/s3store_test.go`
- Modify: `go.mod`, `go.sum` (via `go get`)

- [ ] **Step 1: Add the AWS SDK v2 dependencies**

Run:
```bash
go get github.com/aws/aws-sdk-go-v2/config@latest
go get github.com/aws/aws-sdk-go-v2/service/s3@latest
```
Expected: `go.mod` gains both modules.

- [ ] **Step 2: Write the failing test** — `internal/cachestore/s3store/s3store_test.go`

The adapter calls S3 through a tiny `api` interface so a fake exercises key-prefixing and not-found handling without network.

```go
package s3store

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type fakeAPI struct{ objects map[string][]byte }

type notFound struct{}

func (notFound) Error() string     { return "NoSuchKey" }
func (notFound) ErrorCode() string { return "NoSuchKey" }
func (notFound) ErrorMessage() string { return "" }
func (notFound) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func (f *fakeAPI) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objects[*in.Key]
	if !ok {
		return nil, notFound{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}
func (f *fakeAPI) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.objects[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}
func (f *fakeAPI) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, *in.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func TestS3Store_KeyPrefixAndNotFound(t *testing.T) {
	f := &fakeAPI{objects: map[string][]byte{}}
	s := &Store{client: f, bucket: "b", prefix: "cache/"}

	if _, found, err := s.Get("fangraphs-bat"); err != nil || found {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
	if err := s.Put("fangraphs-bat", []byte("xyz")); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Object key must be <prefix><key>.json so it matches the existing layout.
	if _, ok := f.objects["cache/fangraphs-bat.json"]; !ok {
		t.Fatalf("object not stored under cache/fangraphs-bat.json: keys=%v", f.objects)
	}
	got, found, err := s.Get("fangraphs-bat")
	if err != nil || !found || string(got) != "xyz" {
		t.Fatalf("get: %q found=%v err=%v", got, found, err)
	}
	if err := s.Remove("fangraphs-bat"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, found, _ := s.Get("fangraphs-bat"); found {
		t.Fatal("expected removed")
	}
}
```

- [ ] **Step 3: Run, verify it fails**

Run: `go test ./internal/cachestore/s3store/ -v`
Expected: FAIL — `undefined: Store`

- [ ] **Step 4: Implement `internal/cachestore/s3store/s3store.go`**

```go
// Package s3store is the S3 adapter for cache.Store. It is isolated here so the
// aws-sdk-go-v2 dependency stays out of the zero-dep internal/cache leaf.
package s3store

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// api is the slice of the S3 client this adapter needs (fakeable in tests).
type api interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// Store implements cache.Store against an S3 bucket+prefix, one object per key.
type Store struct {
	client api
	bucket string
	prefix string
}

// New builds a Store using the default AWS credential/region chain (the Fargate
// task role in prod, the dev's profile locally). prefix should end in "/", e.g. "cache/".
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Store{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *Store) objKey(key string) string { return s.prefix + key + ".json" }

func (s *Store) Get(key string) ([]byte, bool, error) {
	out, err := s.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &s.bucket, Key: ptr(s.objKey(key)),
	})
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

func (s *Store) Put(key string, data []byte) error {
	_, err := s.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &s.bucket, Key: ptr(s.objKey(key)), Body: bytes.NewReader(data),
	})
	return err
}

func (s *Store) Remove(key string) error {
	_, err := s.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: &s.bucket, Key: ptr(s.objKey(key)),
	})
	return err
}

func ptr(s string) *string { return &s }
```

> Note: the test's `notFound` fake returns a smithy `NoSuchKey`-coded error; the real not-found path uses `types.NoSuchKey`/`types.NotFound`. If `errors.As` against the smithy-coded fake doesn't classify as not-found, the test instead asserts the fake returns `notFound{}` and the real code's `errors.As` covers prod — but verify the fake exercises the not-found branch; if the smithy error doesn't match `*types.NoSuchKey`, change the fake to return `&types.NoSuchKey{}` directly. Resolve at implementation time by running the test.

- [ ] **Step 5: Run, verify it passes**

Run: `go test ./internal/cachestore/s3store/ -v`
Expected: PASS. If the not-found classification fails, switch the fake to return `&types.NoSuchKey{}` (import `.../service/s3/types`) and re-run.

- [ ] **Step 6: Tidy + commit**

Run: `go mod tidy && go build ./... && go vet ./...`
```bash
git add go.mod go.sum internal/cachestore/
git commit -m "feat(cachestore): S3 adapter for cache.Store (aws-sdk-go-v2)"
```

---

### Task 4: Wire the S3 store in `cmd`

**Files:**
- Modify: `cmd/root.go` (in `initApp`, where `SetCache`/cache is configured)

- [ ] **Step 1: Find the cache wiring point**

Run: `grep -n "SetCache\|no-cache\|noCache\|cacheDir\|\.cache" cmd/root.go`
Expected: locate where the Fantrax client cache dir (`.cache`) is set in `initApp`.

- [ ] **Step 2: Add the default-store selection**

In `cmd/root.go`, add imports `"context"`, `"os"`, and `"github.com/nixon-commits/rosterbot/internal/cache"`, `"github.com/nixon-commits/rosterbot/internal/cachestore/s3store"`. Where caching is enabled (i.e. the non-`--no-cache` branch that calls `SetCache(".cache")`), insert before it:

```go
// When STATE_BUCKET is set (on Fargate), back the Cache with S3 directly
// (per-key) instead of local files, so no bulk .cache sync is needed.
if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
	st, err := s3store.New(context.Background(), bucket, "cache/")
	if err != nil {
		return nil, nil, fmt.Errorf("init s3 cache store: %w", err)
	}
	cache.SetDefaultStore(st)
}
```

(Match the exact `initApp` return signature when adapting the `return` on error — it returns `(cfg, ft, err)`.)

- [ ] **Step 3: Build + vet**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 4: Hermetic check — no STATE_BUCKET means filesystem (unchanged)**

Run: `go test ./...`
Expected: PASS (no env set in tests → `defaultStore` stays nil → `fsStore`, identical to today).

- [ ] **Step 5: Commit**

```bash
git add cmd/root.go
git commit -m "feat(cmd): back Cache with S3 store when STATE_BUCKET is set"
```

---

### Task 5: Drop the `cache/` bulk sync from the entrypoint; update docs

**Files:**
- Modify: `entrypoint.sh`
- Modify: `docs/aws-deployment.md`, `CLAUDE.md` (caching note), `Makefile` (clean-cache note)

- [ ] **Step 1: Remove the `cache/` sync lines from `entrypoint.sh`**

In `entrypoint.sh`, delete the two `cache/` lines (the bot now reads/writes S3 per-key). Keep `session/` and `claims/`:

`sync_down` becomes:
```sh
sync_down() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync "s3://$STATE_BUCKET/session/" ./.fantrax-cache/ --quiet || true
  aws s3 sync "s3://$STATE_BUCKET/claims/"  ./.waivers/       --quiet || true
}
```
`sync_up` becomes (keep the dist publish line):
```sh
sync_up() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync ./.fantrax-cache/ "s3://$STATE_BUCKET/session/" --quiet || true
  aws s3 sync ./.waivers/       "s3://$STATE_BUCKET/claims/"  --quiet || true
  [ -d ./dist ] && [ -n "${SITE_BUCKET:-}" ] && aws s3 sync ./dist/ "s3://$SITE_BUCKET/" --delete --quiet || true
}
```

- [ ] **Step 2: Lint the script**

Run: `sh -n entrypoint.sh`
Expected: no output.

- [ ] **Step 3: Update docs**

- `docs/aws-deployment.md`: in the S3 layout, note `cache/` is now written **per-key by the bot's Store (live S3)**, not bulk-synced; clearing the cache is `aws s3 rm s3://<state-bucket>/cache/ --recursive`.
- `CLAUDE.md` (the caching section): note `internal/cache` has a `Store` seam; on AWS `cmd` calls `cache.SetDefaultStore(s3store…)` keyed under `cache/` when `STATE_BUCKET` is set; the entrypoint no longer syncs `cache/`.
- `Makefile`: next to `clean-cache`, add a comment that it only clears local `.cache/`; the S3 cache is cleared with `aws s3 rm`.

- [ ] **Step 4: Commit**

```bash
git add entrypoint.sh docs/aws-deployment.md CLAUDE.md Makefile
git commit -m "build: entrypoint drops cache/ bulk sync (cache now per-key S3); docs"
```

---

### Task 6: End-to-end verification on AWS

**Files:** none (operational)

- [ ] **Step 1: Build + push a new image** (CodeBuild, via push to main after merge — or manual). The new binary contains the S3-backed Cache.

- [ ] **Step 2: Run an ad-hoc task and confirm per-key S3 writes**

```bash
# after the image with this change is :latest
aws ecs run-task --region us-west-1 --cluster InfraStack-ClusterEB0386A7-gW887MG6jGtU \
  --task-definition InfraStackTaskA0548DCD --launch-type FARGATE \
  --network-configuration 'awsvpcConfiguration={subnets=[subnet-058c996ad3d4776fd],securityGroups=[sg-0660b03e9e7fe25c4],assignPublicIp=ENABLED}' \
  --overrides '{"containerOverrides":[{"name":"bot","command":["waivers","--dry-run"]}]}'
```
Then: `aws s3 ls s3://<state-bucket>/cache/ --recursive | tail` — expect fresh `cache/*.json` objects with current timestamps (written live by the bot, not the entrypoint).

- [ ] **Step 3: Confirm idempotent warm read** — run the same task again; CloudWatch logs should show `cache hit:` lines (run with the command including `--verbose` if available) and no errors. Task exits 0.

---

## Notes for the executor

- **No call-site churn:** the `New[T](dir, ttl)` signature is unchanged; the default-store global does the swap. Do not refactor the ~30 call sites.
- **Layout compatibility:** both `fsStore` and `s3store` key as `<...>/<key>.json`, so the existing `cache/*.json` objects already in S3 (from the old bulk sync) are reused, not orphaned.
- **`InvalidateAll(dir)`** stays filesystem-only by design (a dev tool); S3 cache clearing is `aws s3 rm`.
- **`--no-cache`** still works: it sets ttl 0 (bypass) and/or skips `SetCache`; with no default store and no dir caching, behaviour is unchanged.
- **smithy not-found classification** (Task 3 Step 4 note) is the one spot to verify against a real run of the unit test.
```
