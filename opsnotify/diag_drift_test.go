//go:build diag

package main

import (
	"context"
	"os"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// TestDiagBuildDrift runs the real drift check against live GitHub and live
// CodeBuild, printing what it reads and what it would push. Never in CI.
//
//	DIAG_BUILD_PROJECT=Build45A36621-KN5IpAFXWtlG AWS_REGION=us-west-1 \
//	  go test -tags diag -run TestDiagBuildDrift ./ -v
//
// It exists because the two things most likely to break this check are the two
// things unit tests cannot see: a GitHub response shape that stops carrying the
// fields headOfMain reads, and a CodeBuild build that omits
// resolvedSourceVersion (measured 2026-08-20: STOPPED builds do exactly that).
//
// It sends nothing and writes no marker — send is captured and markers is left
// nil, which the marker store treats as "no dedup" rather than as an error.
func TestDiagBuildDrift(t *testing.T) {
	project := os.Getenv("DIAG_BUILD_PROJECT")
	if project == "" {
		t.Skip("set DIAG_BUILD_PROJECT to run")
	}
	repo := os.Getenv("DIAG_GITHUB_REPO")
	if repo == "" {
		repo = "nixon-commits/rosterbot"
	}

	ctx := context.Background()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	builds = codebuild.NewFromConfig(cfg)

	head, at, err := headOfMain(ctx, repo)
	if err != nil {
		t.Fatalf("head of main: %v", err)
	}
	t.Logf("github  head=%s at=%s", opsalert.ShortSHA(head), at.Format(time.RFC3339))

	built, err := newestSuccessfulBuild(ctx, project)
	if err != nil {
		t.Fatalf("newest successful build: %v", err)
	}
	t.Logf("codebuild newest SUCCEEDED=%s", opsalert.ShortSHA(built))

	d, drifted := opsalert.Check(
		opsalert.BuildState{HeadSHA: head, HeadTime: at, BuiltSHA: built},
		driftGrace, time.Now())
	if !drifted {
		t.Logf("VERDICT current — nothing to alert")
		return
	}
	title, body := opsalert.FormatDrift(d)
	t.Logf("VERDICT DRIFT — would push: %s | %s", title, body)
	t.Logf("marker key=%s token=%s", d.MarkerKey(), d.AlertToken())
}
