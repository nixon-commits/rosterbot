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
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore/s3ndjson"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
)

// artifact is the durable-state layout for one kind of data: its S3 key prefix
// (under STATE_BUCKET) and its local-filesystem directory. This table is the
// single declaration of the prefix layout.
type artifact struct{ s3Prefix, localDir string }

var (
	cacheArtifact        = artifact{"cache/", ""} // local: default fsStore, no dir override
	analysisArtifact     = artifact{"analysis/", ".analysis"}
	teamValueArtifact    = artifact{"analysis/team-values/", ".teamvalue"}
	runLedgerArtifact    = artifact{"runledger/", ".lineup/runs"}
	runOutputArtifact    = artifact{"runs/", ".lineup/outputs"}
	notificationArtifact = artifact{"notifications/", ".lineup/notifications"}
	progressArtifact     = artifact{"runs/", ".lineup/progress"}
	lineupArtifact       = artifact{"lineup/", ".lineup"}
)

// Bucket is the single os.Getenv("STATE_BUCKET") read in the codebase. Empty
// means local-filesystem mode.
func Bucket() string { return os.Getenv("STATE_BUCKET") }

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

func (s *Selector) LineupPublisher() (lineupapi.Publisher, error) {
	return pick(s, lineupArtifact,
		func(ctx context.Context, b, p string) (lineupapi.Publisher, error) { return s3lineup.New(ctx, b, p) },
		func(dir string) lineupapi.Publisher { return lineupapi.NewFileStore(dir) })
}
