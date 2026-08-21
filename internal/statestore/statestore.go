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
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/archive/s3archive"
	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/backtest/s3backtest"
	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/cachestore/s3store"
	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore/s3ndjson"
	"github.com/nixon-commits/rosterbot/internal/recaplog"
	"github.com/nixon-commits/rosterbot/internal/recaplog/s3recaplog"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/nixon-commits/rosterbot/internal/tradeboard"
)

// artifact is one kind of durable state as this package needs it: an S3 key
// prefix and a local-filesystem directory.
//
// The declarations below are views onto internal/statestore/layout, which is
// the single source of the bucket layout. They are not a second copy: layout is
// a zero-dep leaf precisely so internal/lineupapi can serve the same table on
// GET /v1/infra without importing this package (which imports lineupapi, so the
// dependency can only run one way).
//
// Note analysisArtifact uses the analysis/ ROOT while layout.Analysis names the
// analysis/grades/ subtree — the writer is constructed at the root and appends
// grades/dt=.../ itself, whereas the status page lists the leaf where the
// objects actually land.
type artifact struct {
	name string
	lay  layout.Artifact
}

// of builds an artifact view from the layout table, carrying the whole
// layout.Artifact rather than a field-selective copy — so PrefixFor/LocalDirFor
// are called on the exact declaration the layout table owns, and a field this
// package doesn't yet read (Name in an error message, a future flag) can never
// silently diverge between the producer path here and the reader path in
// lambda/main.go.
func of(a layout.Artifact) artifact {
	return artifact{name: a.Name, lay: a}
}

var (
	cacheArtifact            = of(layout.Artifact{Name: layout.Cache.Name, S3Prefix: layout.Cache.S3Prefix}) // local: default fsStore, dir unused
	analysisArtifact         = of(layout.Analysis)
	teamValueArtifact        = of(layout.TeamValues)
	footballValueArtifact    = of(layout.FootballValues)
	lineupGapArtifact        = of(layout.LineupGaps)
	runLedgerArtifact        = of(layout.RunLedger)
	runOutputArtifact        = of(layout.RunOutput)
	notificationArtifact     = of(layout.Notification)
	progressArtifact         = of(layout.Progress)
	lineupArtifact           = of(layout.Lineup)
	tradesArtifact           = of(layout.Trades)
	tradeValuesArtifact      = of(layout.TradeValues)
	tradeOfferArtifact       = of(layout.TradeOffers)
	reportsArtifact          = of(layout.Reports)
	footballTradesArtifact   = of(layout.FootballTrades)
	footballTradeLogArtifact = of(layout.FootballTradeLog)
	ilStartsArtifact         = of(layout.ILStarts)
	staleCacheArtifact       = of(layout.StaleCacheAlerts)
	archiveArtifact          = of(layout.Archive)
	backtestArtifact         = of(layout.Backtest)
)

// Bucket is the single os.Getenv("STATE_BUCKET") read in the codebase. Empty
// means local-filesystem mode.
func Bucket() string { return os.Getenv("STATE_BUCKET") }

// RecapLogBucket is the single os.Getenv("RECAP_LOG_BUCKET") read. It names a
// bucket outside the state bucket entirely — CloudFront writes the recap site's
// access logs there — so it sits beside Bucket() rather than in the layout
// table, which declares the STATE_BUCKET key layout only.
func RecapLogBucket() string { return os.Getenv("RECAP_LOG_BUCKET") }

// Tenant is the single os.Getenv("ROSTERBOT_USER_ID") read. Empty means
// single-tenant mode, where per-tenant artifacts keep their original,
// un-segmented prefixes.
//
// Empty is a real mode, not a missing value: it is exactly where the
// deployment's state lives today, so an un-migrated bucket keeps working
// unchanged. The fan-out (rosterbot-crq.13) sets this per task, and the backfill
// relocates the operator's existing state under their own segment.
func Tenant() string { return os.Getenv("ROSTERBOT_USER_ID") }

// Selector resolves durable-state stores against one backend choice made once.
type Selector struct {
	bucket string
	tenant string
}

// FromEnv builds a Selector from STATE_BUCKET (the deployment's signal).
func FromEnv() *Selector { return &Selector{bucket: Bucket(), tenant: Tenant()} }

// New builds a Selector for an explicit bucket ("" = local). Used by tests and
// by any caller that already knows the bucket.
func New(bucket string) *Selector { return &Selector{bucket: bucket} }

// ForTenant returns a Selector scoped to one user. Used by the job fan-out,
// which knows which tenant a task is running for, and by tests.
func ForTenant(bucket, tenant string) *Selector {
	return &Selector{bucket: bucket, tenant: tenant}
}

// pick is the sole S3-vs-local branch, and the sole place a tenant segment is
// composed. Both live here so a new store cannot accidentally opt out of
// either: there is one function to go through.
//
// On S3 it calls s3New(ctx, bucket, prefix); locally it calls fileNew(localDir).
func pick[T any](s *Selector, a artifact,
	s3New func(ctx context.Context, bucket, prefix string) (T, error),
	fileNew func(dir string) T) (T, error) {
	if s.bucket != "" {
		return s3New(context.Background(), s.bucket, s.prefixFor(a))
	}
	dir := s.dirFor(a)
	if segs := strandedTenants(a, s.tenant, dir); len(segs) > 0 {
		var zero T
		return zero, fmt.Errorf(
			"%s: no tenant is set, but %s holds tenant data (%s): this read cannot say whose "+
				"data it wants, and the un-segmented path it would fall back to is the frozen "+
				"pre-migration copy, so it would grade recent days as missing rather than fail. "+
				"Set ROSTERBOT_USER_ID (rosterbot-4b9)",
			a.name, dir, strings.Join(segs, ", "))
	}
	return fileNew(dir), nil
}

// ndjsonStore is pick specialized for the twelve stores backed by an
// ndjsonstore.Store: it builds the S3 adapter (s3ndjson.New) and wraps it with
// wrap, or hands the local directory to fileNew. Every NDJSON-backed
// constructor below is this call plus its own wrap/fileNew pair, replacing the
// identical 8-line s3ndjson.New-then-wrap closure each used to repeat.
func ndjsonStore[T any](s *Selector, a artifact, wrap func(ndjsonstore.Store) T, fileNew func(string) T) (T, error) {
	return pick(s, a, func(ctx context.Context, b, p string) (T, error) {
		st, err := s3ndjson.New(ctx, b, p)
		if err != nil {
			var zero T
			return zero, err
		}
		return wrap(st), nil
	}, fileNew)
}

// strandedTenants reports the user= segments sitting beside the un-segmented
// path a tenant-less read would resolve. Empty means there is no ambiguity to
// refuse.
//
// The guard is LOCAL-ONLY on purpose, and the asymmetry is not an oversight.
// A deployed task always declares its tenant — infra sets ROSTERBOT_USER_ID on
// the task definition from SSM and the fan-out overrides it per tenant — and a
// deployed run that somehow did not is already refused by
// resolveCredentialMode before any store is built, on the same reasoning: a
// tenancy that is real but unattributable has no safe default. Local dev has no
// user directory, so nothing refused it there, which is exactly where
// rosterbot-4b9 landed. Probing S3 instead would cost a ListObjectsV2 on every
// store construction to re-derive what the environment already states.
//
// EVIDENCE-BASED RATHER THAN UNCONDITIONAL, deliberately. Refusing every
// tenant-less local read would break existing local trees, every hermetic test
// and every fresh checkout for a mistake nobody made: a machine that never
// synced tenant data has exactly one copy, and it is the right one. Absence of
// evidence is not evidence — the same rule that stops internal/roster listing a
// player with no position data as ineligible.
//
// A missing or unreadable directory yields no segments for the same reason: it
// is the fresh-machine case, not a concealed second copy.
func strandedTenants(a artifact, tenant, dir string) []string {
	if !a.lay.PerTenant || tenant != "" || dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var segs []string
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), "user=") {
			segs = append(segs, strings.TrimPrefix(e.Name(), "user="))
		}
	}
	sort.Strings(segs)
	return segs
}

// prefixFor inserts user=<tenant>/ directly after the artifact prefix, for
// per-tenant artifacts only.
//
// The segment is Hive-style for the same reason the Analysis Store's system=
// is: internal/s3blob keys are relative to the prefix (joined on the way in,
// stripped on the way out), so a longer prefix is invisible to every key parser
// above it — the dt= partition walk, the ledger's inverted timestamps — and
// Athena can project it as a partition column rather than needing a crawler.
//
// A shared artifact never gains the segment even when a tenant is set. That
// asymmetry is the point: the TTL Cache and the league-wide value stores are
// deliberately common ground, and partitioning them would multiply upstream
// load by the tenant count while splitting data that describes the whole league.
func (s *Selector) prefixFor(a artifact) string {
	return a.lay.PrefixFor(s.tenant)
}

// dirFor is the local-filesystem equivalent, kept in step so `serve` and a
// deployed task disagree about nothing but the backend.
func (s *Selector) dirFor(a artifact) string {
	return a.lay.LocalDirFor(s.tenant)
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
	return ndjsonStore(s, analysisArtifact, analysis.NewWriter, analysis.NewFileWriter)
}

func (s *Selector) AnalysisReader() (analysis.Reader, error) {
	return ndjsonStore(s, analysisArtifact, analysis.NewReader, analysis.NewFileReader)
}

func (s *Selector) TeamValueWriter() (teamvalue.Writer, error) {
	return ndjsonStore(s, teamValueArtifact, teamvalue.NewWriter, teamvalue.NewFileWriter)
}

// SnapshotStore returns the Projection Snapshot store. Like the Daily Archive
// before rosterbot-s25n, this rode cmd/sync.go's bulk directory sync, so every
// task re-uploaded all ~485 snapshot objects after running and re-downloaded
// them before running. It is PerTenant, so pick composes the user= segment.
func (s *Selector) SnapshotStore() (backtest.SnapshotStore, error) {
	return pick(s, backtestArtifact,
		func(ctx context.Context, b, p string) (backtest.SnapshotStore, error) {
			return s3backtest.New(ctx, b, p)
		},
		func(dir string) backtest.SnapshotStore { return backtest.NewFileSnapshotStore(dir) })
}

// ArchiveWriter returns the Daily Archive writer. Unlike the other durable
// stores this one used to reach S3 through cmd/sync.go's bulk directory sync
// instead of a typed store, which meant every task downloaded the whole archive
// tree (679 objects / 877 MB, measured 2026-08-18) before doing any work, and
// re-uploaded it afterwards. Only cmd/archive.go ever reads or writes it, so it
// now writes per-partition here like every other artifact (rosterbot-s25n).
func (s *Selector) ArchiveWriter() (archive.Writer, error) {
	return pick(s, archiveArtifact,
		func(ctx context.Context, b, p string) (archive.Writer, error) {
			st, err := s3archive.New(ctx, b, p)
			if err != nil {
				return archive.Writer{}, err
			}
			return archive.NewWriter(st), nil
		},
		func(dir string) archive.Writer { return archive.NewFileWriter(dir) })
}

func (s *Selector) TeamValueReader() (teamvalue.Reader, error) {
	return ndjsonStore(s, teamValueArtifact, teamvalue.NewReader, teamvalue.NewFileReader)
}

func (s *Selector) FootballValueWriter() (dynasty.Writer, error) {
	return ndjsonStore(s, footballValueArtifact, dynasty.NewWriter, dynasty.NewFileWriter)
}

func (s *Selector) FootballValueReader() (dynasty.Reader, error) {
	return ndjsonStore(s, footballValueArtifact, dynasty.NewReader, dynasty.NewFileReader)
}

// LineupGapWriter returns the write side of the Lineup Gap Store — S3 when
// STATE_BUCKET is set, else the local .lineupgap directory.
func (s *Selector) LineupGapWriter() (lineupgap.Writer, error) {
	return ndjsonStore(s, lineupGapArtifact, lineupgap.NewWriter, lineupgap.NewFileWriter)
}

// LineupGapReader returns the read side of the Lineup Gap Store.
func (s *Selector) LineupGapReader() (lineupgap.Reader, error) {
	return ndjsonStore(s, lineupGapArtifact, lineupgap.NewReader, lineupgap.NewFileReader)
}

func (s *Selector) RunLedger() (RunWriter, error) {
	return pick(s, runLedgerArtifact,
		func(ctx context.Context, b, p string) (RunWriter, error) { return s3lineup.NewRuns(ctx, b, p) },
		func(dir string) RunWriter { return lineupapi.NewFileRunStore(dir) })
}

func (s *Selector) Output() (lineupapi.OutputWriter, error) {
	return pick(s, runOutputArtifact,
		func(ctx context.Context, b, p string) (lineupapi.OutputWriter, error) {
			return s3lineup.NewOutput(ctx, b, p)
		},
		func(dir string) lineupapi.OutputWriter { return lineupapi.NewFileOutputStore(dir) })
}

func (s *Selector) Notifications() (lineupapi.NotificationWriter, error) {
	return pick(s, notificationArtifact,
		func(ctx context.Context, b, p string) (lineupapi.NotificationWriter, error) {
			return s3lineup.NewNotifications(ctx, b, p)
		},
		func(dir string) lineupapi.NotificationWriter { return lineupapi.NewFileNotificationStore(dir) })
}

func (s *Selector) Progress() (lineupapi.ProgressWriter, error) {
	return pick(s, progressArtifact,
		func(ctx context.Context, b, p string) (lineupapi.ProgressWriter, error) {
			return s3lineup.NewProgress(ctx, b, p)
		},
		func(dir string) lineupapi.ProgressWriter { return lineupapi.NewFileProgressStore(dir) })
}

// RecapLogReader returns a read-only store over the recap site's CloudFront
// access logs, or (nil, nil) when RECAP_LOG_BUCKET is unset.
//
// It deliberately does NOT go through pick[T]. Every other artifact here has a
// local-directory equivalent because the bot itself writes it; these logs are
// written by CloudFront and exist only in S3, so there is nothing to fall back
// to locally. A nil reader means "no readership data available here" and the
// caller skips that output rather than failing — which is exactly what a local
// dev run should do.
func (s *Selector) RecapLogReader() (recaplog.Store, error) {
	bucket := RecapLogBucket()
	if bucket == "" {
		return nil, nil
	}
	return s3recaplog.New(context.Background(), bucket, recaplog.Prefix)
}

func (s *Selector) LineupPublisher() (lineupapi.Publisher, error) {
	return pick(s, lineupArtifact,
		func(ctx context.Context, b, p string) (lineupapi.Publisher, error) { return s3lineup.New(ctx, b, p) },
		func(dir string) lineupapi.Publisher { return lineupapi.NewFileStore(dir) })
}

// blobStore wires an artifact to s3lineup's generic <prefix><key>.json layout
// on S3, or to a filename-prefixed local file. s3lineup.Store is not
// lineup-specific despite its package name — the prefix is a constructor
// argument — so the Trades artifacts need no new S3 adapter.
func blobStore(s *Selector, a artifact, namePrefix string) (lineupapi.BlobStore, error) {
	return pick(s, a,
		func(ctx context.Context, b, p string) (lineupapi.BlobStore, error) { return s3lineup.New(ctx, b, p) },
		func(dir string) lineupapi.BlobStore { return lineupapi.NewFileBlobStore(dir, namePrefix) })
}

// TradesStore is the pending-offer snapshot (trades/current.json), rewritten
// hourly by the Lineup job.
func (s *Selector) TradesStore() (lineupapi.BlobStore, error) {
	return blobStore(s, tradesArtifact, "trades-")
}

// TradeValuesStore is the league values table (tradevalues/values.json),
// rewritten daily by the TeamValues job.
func (s *Selector) TradeValuesStore() (lineupapi.BlobStore, error) {
	return blobStore(s, tradeValuesArtifact, "tradevalues-")
}

// ReportsStore holds the three private dashboard reports (reports/model.json,
// reports/gap.json, reports/views.json), rewritten daily by the ProjectionSite
// job and served only through the passkey-gated GET /v1/reports/{name}.
//
// No namePrefix: the reports/ prefix (.reports/ locally) holds nothing else, so
// the key alone names the file and the local layout matches the S3 one
// one-for-one — which is what makes a local `serve` read exactly the bytes a
// deployed Lambda would.
func (s *Selector) ReportsStore() (lineupapi.BlobStore, error) {
	return blobStore(s, reportsArtifact, "")
}

// FootballTradeMarkers is one dedup marker object per Sleeper trade
// transaction_id, keyed with no namePrefix -- the transaction_id alone is the
// key. Get is the "already alerted?" check; Publish is the mark, called only
// after a confirmed send (check -> send -> mark, rosterbot-chs).
//
// The football/trades/ prefix is no longer this store's alone: the durable
// trade log nests under football/trades/log/ (see FootballTradeLogWriter). That
// is safe precisely because a BlobStore never lists -- it Gets and Publishes an
// exact key -- so a marker key and a log partition cannot collide, and neither
// store can enumerate the other's objects.
func (s *Selector) FootballTradeMarkers() (lineupapi.BlobStore, error) {
	return blobStore(s, footballTradesArtifact, "")
}

// FootballTradeLogWriter returns the write side of the Football Trade Log -- S3
// when STATE_BUCKET is set, else the local .football/trades/log directory.
//
// A separate store from FootballValueWriter, not a third method on it: that
// writer is rooted at analysis/football-values/ and this log lives under
// football/trades/log/, so one interface would mean two instances on which half
// the methods must never be called.
func (s *Selector) FootballTradeLogWriter() (dynasty.TradeLogWriter, error) {
	return ndjsonStore(s, footballTradeLogArtifact, dynasty.NewTradeLogWriter, dynasty.NewFileTradeLogWriter)
}

// FootballTradeLogReader returns the read side of the Football Trade Log.
func (s *Selector) FootballTradeLogReader() (dynasty.TradeLogReader, error) {
	return ndjsonStore(s, footballTradeLogArtifact, dynasty.NewTradeLogReader, dynasty.NewFileTradeLogReader)
}

// ILStartMarkers is one dedup marker per (player, start date) for the IL-start
// alert. The date is part of the key on purpose: a player stranded on the IL
// across two separate starts is two distinct things worth being told about,
// where a player-only key would alert once and then go quiet for the season.
func (s *Selector) ILStartMarkers() (lineupapi.BlobStore, error) {
	return blobStore(s, ilStartsArtifact, "")
}

// StaleCacheMarkers is one dedup marker per cache key for the stale-cache
// alert. It is deliberately NOT tenant-scoped: the cache it guards is shared,
// so the outage it reports is shared too, and one marker is the correct number.
func (s *Selector) StaleCacheMarkers() (lineupapi.BlobStore, error) {
	return blobStore(s, staleCacheArtifact, "")
}

// TradeOfferWriter is the write side of the durable Trade Offer Log.
func (s *Selector) TradeOfferWriter() (tradeboard.Writer, error) {
	return ndjsonStore(s, tradeOfferArtifact, tradeboard.NewWriter, tradeboard.NewFileWriter)
}

// TradeOfferReader is the read side of the durable Trade Offer Log.
func (s *Selector) TradeOfferReader() (tradeboard.Reader, error) {
	return ndjsonStore(s, tradeOfferArtifact, tradeboard.NewReader, tradeboard.NewFileReader)
}
