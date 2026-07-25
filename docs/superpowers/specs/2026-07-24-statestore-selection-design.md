# State Store selection — collapse 10 hand-rolled STATE_BUCKET branches into one module

**Issue:** rosterbot-9s6
**Date:** 2026-07-24
**Status:** Approved (design)

## Problem

The decision "S3 when `STATE_BUCKET` is set, else a local directory" is re-implemented at
10 call sites across two packages, each returning a different concrete store type. The
branch is copy-pasted; only the store type and prefix differ. Two further variants gate
whole loops (`cmd/sync.go`) or error instead of falling back (`cmd/migrate_run_ledger.go`).

Consequences today:

- The deployment story (which prefixes exist, what the local fallback root is, that
  Fargate sets `STATE_BUCKET`) is knowledge every call site must hold rather than a fact
  one module owns.
- `internal/lineuprun` reads `os.Getenv` directly (`publish.go`), so an orchestration
  package depends on the deployment environment — one of the things that makes
  `lineuprun.Run` untestable (see rosterbot-6rv).
- Adding an eleventh durable artifact means writing the branch again and getting the
  prefix right by inspection.

Surfaced by the 2026-07-22 architecture review (candidate 1 of 7).

## The 10 sites + 2 variants

Three structurally different byte-level seams sit underneath the identical branch:

| Seam | Interface shape | Sites |
|------|-----------------|-------|
| `cache.Store` | `Get→(b,bool,err)`, `Put`, `Remove` | `cmd/root.go` (cache) |
| `ndjsonstore.Store` | `Get`, `Put`, `List` | `grade`, `team-values`, `projection-site` ×2 |
| `lineupapi` family | 5 distinct typed stores | `ledger`, `output`, `notifications`, `progress`, `publish` |

Full mapping (S3 prefix and local dir genuinely differ per artifact):

| # | Site | Return type | S3 prefix | Local dir |
|---|------|-------------|-----------|-----------|
| 1 | `cmd/root.go:95` | `cache.Store` (via `SetDefaultStore`) | `cache/` | `.cache` (default fsStore) |
| 2 | `cmd/grade.go:130` | `analysis.Writer` | `analysis/` | `.analysis` |
| 3 | `cmd/team-values.go:102` | `teamvalue.Writer` | `analysis/team-values/` | `.teamvalue` |
| 4 | `cmd/projection-site.go:44` | `analysis.Reader` | `analysis/` | `.analysis` |
| 5 | `cmd/projection-site.go:105` | `teamvalue.Reader` | `analysis/team-values/` | `.teamvalue` |
| 6 | `cmd/ledger.go:78` | `runWriter` (RunStore) | `runledger/` | `.lineup/runs` |
| 7 | `cmd/output.go:22` | `lineupapi.OutputWriter` | `runs/` | `.lineup/outputs` |
| 8 | `cmd/notifications.go:20` | `lineupapi.NotificationWriter` | `notifications/` | `.lineup/notifications` |
| 9 | `cmd/progress.go:24` | `lineupapi.ProgressWriter` | `runs/` | `.lineup/progress` |
| 10 | `internal/lineuprun/publish.go:35` | `lineupapi.Publisher` | `lineup/` | `.lineup` |

Variants (kept distinct, env read routed through the module):

- `cmd/sync.go:53,77` — bulk dir↔prefix sync gating (`statePairs`); not a per-key store.
- `cmd/migrate_run_ledger.go:42` — requires S3; errors if `STATE_BUCKET` unset.

Out of scope: `lambda/main.go` (separate Go module, the read-side API handler) and
`infra/infra.go` (CDK sets the env var — the value's source, not a consumer).

## Design decisions

Two open questions from the issue were resolved during brainstorming:

1. **Typed constructors, not a unified byte-level store.** The three byte seams are
   structurally different; collapsing the three S3 adapters into one is the explicit,
   *downstream* job of rosterbot-oye (which this issue blocks). Doing it here would merge
   oye into 9s6 and enlarge the blast radius. A generic `pick[T]` helper still makes the
   S3-vs-local branch appear literally once.

2. **The two variants route their env read through the module** (`statestore.Bucket()`)
   so `os.Getenv("STATE_BUCKET")` appears exactly once, but keep their distinct behavior,
   documented as intentionally not store-constructors.

**`internal/lineuprun` does not depend on `statestore`.** The fix for its `os.Getenv` is
to hand `publish.go` a `lineupapi.Publisher` via `Options`; lineuprun then depends only on
the `lineupapi` interface leaf, and the AWS SDK leaves the orchestration package entirely.
The composition root (`cmd`) does the selecting.

## The module

`internal/statestore` — a composition-root hub, imported only by `cmd`. No leaf imports it
back (none of `cache`/`analysis`/`teamvalue`/`lineupapi`/the s3 adapters import `cmd`), so
there is no cycle.

### Layout table (criterion 3 — one place)

```go
type artifact struct{ s3Prefix, localDir string }

var (
    cacheArtifact        = artifact{"cache/",                ""}          // local: default fsStore
    analysisArtifact     = artifact{"analysis/",             ".analysis"}
    teamValueArtifact    = artifact{"analysis/team-values/", ".teamvalue"}
    runLedgerArtifact    = artifact{"runledger/",            ".lineup/runs"}
    runOutputArtifact    = artifact{"runs/",                 ".lineup/outputs"}
    notificationArtifact = artifact{"notifications/",        ".lineup/notifications"}
    progressArtifact     = artifact{"runs/",                 ".lineup/progress"}
    lineupArtifact       = artifact{"lineup/",               ".lineup"}
)
```

### Selector + the sole branch

```go
type Selector struct{ bucket string }

// Bucket is the ONE os.Getenv("STATE_BUCKET") literal in the tree.
func Bucket() string { return os.Getenv("STATE_BUCKET") }

func FromEnv() *Selector   { return &Selector{bucket: Bucket()} }
func New(bucket string) *Selector { return &Selector{bucket: bucket} } // tests / explicit

// pick is the sole S3-vs-local branch.
func pick[T any](s *Selector, a artifact,
    s3New func(ctx context.Context, bucket, prefix string) (T, error),
    fileNew func(dir string) T) (T, error) {
    if s.bucket != "" {
        return s3New(context.Background(), s.bucket, a.s3Prefix)
    }
    return fileNew(a.localDir), nil
}
```

### Typed methods (9)

Each is three lines wrapping `pick`:

- `CacheStore() (cache.Store, error)` — S3 via `s3store.New`; local returns `nil` (caller
  keeps the default fsStore, so `SetDefaultStore` is skipped).
- `AnalysisWriter() (analysis.Writer, error)`, `AnalysisReader() (analysis.Reader, error)`
  — S3 via `s3ndjson.New` wrapped in `analysis.NewWriter/NewReader`; local via
  `analysis.NewFileWriter/NewFileReader`.
- `TeamValueWriter() (teamvalue.Writer, error)`, `TeamValueReader() (teamvalue.Reader, error)`
  — same, `teamvalue` wrappers, `teamValueArtifact`.
- `RunLedger() (RunWriter, error)` — S3 via `s3lineup.NewRuns`; local via
  `lineupapi.NewFileRunStore`. `RunWriter` is a minimal interface defined in `statestore`
  (`PutRun(context.Context, lineupapi.RunDetail) error`), replacing `cmd/ledger.go`'s
  local `runWriter`.
- `Output() (lineupapi.OutputWriter, error)` — `s3lineup.NewOutput` / `NewFileOutputStore`.
- `Notifications() (lineupapi.NotificationWriter, error)` — `s3lineup.NewNotifications` /
  `NewFileNotificationStore`.
- `Progress() (lineupapi.ProgressWriter, error)` — `s3lineup.NewProgress` /
  `NewFileProgressStore`.
- `LineupPublisher() (lineupapi.Publisher, error)` — `s3lineup.New` / `lineupapi.NewFileStore`.

The `CacheStore` `nil`-on-local case is expressed by a `fileNew` that returns `nil`, so it
still routes through `pick` and adds no second branch.

## Call-site rewrites

Each of sites 2–9 collapses to (example, grade):

```go
w, err := statestore.FromEnv().AnalysisWriter()
if err != nil { return fmt.Errorf("init analysis store: %w", err) }
```

`cmd/root.go` (site 1) becomes:

```go
if st, err := statestore.FromEnv().CacheStore(); err != nil {
    return nil, nil, fmt.Errorf("init cache store: %w", err)
} else if st != nil {
    cache.SetDefaultStore(st)
}
```

`SetDefaultStore` stays in `cmd`; only the *selection* moves into `statestore`.

`cmd/ledger.go` drops its local `runWriter` interface in favor of `statestore.RunWriter`.

## lineuprun (criterion 2)

- `Options` gains `Publisher lineupapi.Publisher`.
- `cmd/optimize.go` builds it (`statestore.FromEnv().LineupPublisher()`) and sets
  `opts.Publisher`. It is only needed when a publish will actually happen
  (`!DryRun || --publish-lineup`); building it unconditionally is fine (cheap, and the S3
  client construction is lazy/no-op locally), so `optimize` always sets it.
- `internal/lineuprun/publish.go`:
  - `publishLineup` takes the publisher as a parameter (or reads `opts.Publisher`).
  - Nil-guard: if `opts.Publisher == nil`, skip publishing (matches "don't publish" — e.g.
    a caller that never wants the API JSON). `shadow` never publishes, so it leaves it nil.
  - Drops the `os` and `internal/lineupapi/s3lineup` imports.

Result: `go list -deps ./internal/lineuprun` no longer includes `aws-sdk-go-v2`.

## The two variants

- `cmd/sync.go`: `runSyncDown`/`runSyncUp` call `statestore.Bucket()` instead of
  `os.Getenv("STATE_BUCKET")`; the sync-loop gating logic is unchanged, with a comment
  noting it is a bulk dir sync, not a per-key store.
- `cmd/migrate_run_ledger.go`: calls `statestore.Bucket()`; keeps
  `if bucket == "" { return err }` (migration only makes sense against S3), commented as
  intentionally require-S3.

## Testing (criterion 4)

- `internal/statestore` table-driven unit tests via `New(bucket)`: for each typed method,
  assert `bucket=""` yields the local store rooted at the artifact's `localDir`, and a
  non-empty bucket yields the S3-backed concrete type with no error. `LoadDefaultConfig`
  does not require credentials (they resolve lazily on first S3 call), so both branches
  construct cleanly in a hermetic test — no network, no stubbed AWS config needed. Assert
  the local branch's directory rooting where the concrete file store exposes it, and the
  S3 branch by concrete return type / non-nil, non-error.
- Previously-untested call path with an in-memory-ish store: exercise `publishLineup`
  through `Options.Publisher` using `lineupapi.NewFileStore(t.TempDir())`, asserting the
  published JSON lands under both `today` and the date key. This path had no test because
  the selection was inline in `publish.go`.

## Acceptance criteria mapping

1. `os.Getenv("STATE_BUCKET")` appears exactly once → `statestore.Bucket()`; grep confirms.
   The `sync.go`/`migrate` variants route through it and are documented as different.
2. `internal/lineuprun` no longer reads `os.Getenv` → `publish.go` receives its publisher
   from the caller via `Options`.
3. Prefix layout for every durable artifact declared in one place → the `artifact` table.
4. Unit tests exercise a previously-untested call path with an in-memory store → the
   `publishLineup`-via-`Options.Publisher` test.
5. No behavior change → local and `STATE_BUCKET` runs write to the same locations; the
   layout table reproduces every existing prefix/dir exactly.
6. `go vet ./...`, `make build-modules`, `make test` pass; `make run-all` completes clean.

## Non-goals / staged follow-ups

- Unifying the three S3 blob adapters into one (`rosterbot-oye`) — this design deliberately
  leaves them intact and is compatible with that later collapse.
- Splitting `lineuprun.Run` into phases (`rosterbot-6rv`) — removing lineuprun's `os.Getenv`
  is a prerequisite this issue satisfies.
- Compatible with ADR-0001: this changes where the choice is made, not what backs the Cache
  Store. S3-for-key-to-blob stands.
