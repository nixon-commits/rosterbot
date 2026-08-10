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
type artifact struct{ s3Prefix, localDir string }

var (
	cacheArtifact        = artifact{layout.Cache.S3Prefix, ""} // local: default fsStore, dir unused
	analysisArtifact     = artifact{"analysis/", layout.Analysis.LocalDir}
	teamValueArtifact    = artifact{layout.TeamValues.S3Prefix, layout.TeamValues.LocalDir}
	lineupGapArtifact    = artifact{layout.LineupGaps.S3Prefix, layout.LineupGaps.LocalDir}
	runLedgerArtifact    = artifact{layout.RunLedger.S3Prefix, layout.RunLedger.LocalDir}
	runOutputArtifact    = artifact{layout.RunOutput.S3Prefix, layout.RunOutput.LocalDir}
	notificationArtifact = artifact{layout.Notification.S3Prefix, layout.Notification.LocalDir}
	progressArtifact     = artifact{layout.Progress.S3Prefix, layout.Progress.LocalDir}
	lineupArtifact       = artifact{layout.Lineup.S3Prefix, layout.Lineup.LocalDir}
	tradesArtifact       = artifact{layout.Trades.S3Prefix, layout.Trades.LocalDir}
	tradeValuesArtifact  = artifact{layout.TradeValues.S3Prefix, layout.TradeValues.LocalDir}
	tradeOfferArtifact   = artifact{layout.TradeOffers.S3Prefix, layout.TradeOffers.LocalDir}
)

// Bucket is the single os.Getenv("STATE_BUCKET") read in the codebase. Empty
// means local-filesystem mode.
func Bucket() string { return os.Getenv("STATE_BUCKET") }

// RecapLogBucket is the single os.Getenv("RECAP_LOG_BUCKET") read. It names a
// bucket outside the state bucket entirely — CloudFront writes the recap site's
// access logs there — so it sits beside Bucket() rather than in the layout
// table, which declares the STATE_BUCKET key layout only.
func RecapLogBucket() string { return os.Getenv("RECAP_LOG_BUCKET") }

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
