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
	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
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
	s3Prefix, localDir string
	perTenant          bool
}

// of builds an artifact view from the layout table, so PerTenant is read from
// the single declaration rather than restated here — a second copy is exactly
// what layout exists to prevent.
func of(a layout.Artifact) artifact {
	return artifact{a.S3Prefix, a.LocalDir, a.PerTenant}
}

var (
	cacheArtifact = artifact{layout.Cache.S3Prefix, "", false} // local: default fsStore, dir unused
	// analysisArtifact uses the analysis/ ROOT while layout.Analysis names the
	// analysis/grades/ subtree, so it cannot come from of(): the writer appends
	// grades/dt=.../ itself. PerTenant is carried across by hand for that reason.
	analysisArtifact       = artifact{"analysis/", layout.Analysis.LocalDir, layout.Analysis.PerTenant}
	teamValueArtifact      = of(layout.TeamValues)
	footballValueArtifact  = of(layout.FootballValues)
	lineupGapArtifact      = of(layout.LineupGaps)
	runLedgerArtifact      = of(layout.RunLedger)
	runOutputArtifact      = of(layout.RunOutput)
	notificationArtifact   = of(layout.Notification)
	progressArtifact       = of(layout.Progress)
	lineupArtifact         = of(layout.Lineup)
	tradesArtifact         = of(layout.Trades)
	tradeValuesArtifact    = of(layout.TradeValues)
	tradeOfferArtifact     = of(layout.TradeOffers)
	reportsArtifact        = of(layout.Reports)
	footballTradesArtifact = of(layout.FootballTrades)
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
	return fileNew(s.dirFor(a)), nil
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
	return layout.Artifact{S3Prefix: a.s3Prefix, PerTenant: a.perTenant}.PrefixFor(s.tenant)
}

// dirFor is the local-filesystem equivalent, kept in step so `serve` and a
// deployed task disagree about nothing but the backend.
func (s *Selector) dirFor(a artifact) string {
	return layout.Artifact{LocalDir: a.localDir, PerTenant: a.perTenant}.LocalDirFor(s.tenant)
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

func (s *Selector) FootballValueWriter() (dynasty.Writer, error) {
	return pick(s, footballValueArtifact,
		func(ctx context.Context, b, p string) (dynasty.Writer, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return dynasty.NewWriter(st), nil
		},
		func(dir string) dynasty.Writer { return dynasty.NewFileWriter(dir) })
}

func (s *Selector) FootballValueReader() (dynasty.Reader, error) {
	return pick(s, footballValueArtifact,
		func(ctx context.Context, b, p string) (dynasty.Reader, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return dynasty.NewReader(st), nil
		},
		func(dir string) dynasty.Reader { return dynasty.NewFileReader(dir) })
}

// LineupGapWriter returns the write side of the Lineup Gap Store — S3 when
// STATE_BUCKET is set, else the local .lineupgap directory.
func (s *Selector) LineupGapWriter() (lineupgap.Writer, error) {
	return pick(s, lineupGapArtifact,
		func(ctx context.Context, b, p string) (lineupgap.Writer, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return lineupgap.NewWriter(st), nil
		},
		func(dir string) lineupgap.Writer { return lineupgap.NewFileWriter(dir) })
}

// LineupGapReader returns the read side of the Lineup Gap Store.
func (s *Selector) LineupGapReader() (lineupgap.Reader, error) {
	return pick(s, lineupGapArtifact,
		func(ctx context.Context, b, p string) (lineupgap.Reader, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return lineupgap.NewReader(st), nil
		},
		func(dir string) lineupgap.Reader { return lineupgap.NewFileReader(dir) })
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
// key, since the football/trades/ prefix (.football/trades/ locally) holds
// nothing else. Get is the "already alerted?" check; Publish is the mark,
// called only after a confirmed send (check -> send -> mark, rosterbot-chs).
func (s *Selector) FootballTradeMarkers() (lineupapi.BlobStore, error) {
	return blobStore(s, footballTradesArtifact, "")
}

// TradeOfferWriter is the write side of the durable Trade Offer Log.
func (s *Selector) TradeOfferWriter() (tradeboard.Writer, error) {
	return pick(s, tradeOfferArtifact,
		func(ctx context.Context, b, p string) (tradeboard.Writer, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return tradeboard.NewWriter(st), nil
		},
		func(dir string) tradeboard.Writer { return tradeboard.NewFileWriter(dir) })
}

// TradeOfferReader is the read side of the durable Trade Offer Log.
func (s *Selector) TradeOfferReader() (tradeboard.Reader, error) {
	return pick(s, tradeOfferArtifact,
		func(ctx context.Context, b, p string) (tradeboard.Reader, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return tradeboard.NewReader(st), nil
		},
		func(dir string) tradeboard.Reader { return tradeboard.NewFileReader(dir) })
}
