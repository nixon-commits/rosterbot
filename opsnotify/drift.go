package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// driftDetailType is the detail-type the CDK's scheduled rule puts on its
// constant input. Like heartbeatDetailType it is not an AWS event type, so it
// carries the "Rosterbot" prefix to mark it as ours beside the two real sources.
const driftDetailType = "Rosterbot Build Drift"

// driftGrace is how young a commit may be before an absent build is treated as
// news rather than as a build still in flight.
//
// A full build measured ~3.5-7 minutes (docker build, two pushes, cdk deploy),
// so 30 minutes clears it several times over. The asymmetry is deliberate: too
// low fires a false alert on ordinary deploys and teaches the operator to ignore
// the channel, while too high only delays a report that the 6-hourly tick would
// bound anyway.
const driftGrace = 30 * time.Minute

// Environment carrying the two coordinates the check needs. Both are set by the
// CDK from the SAME literals that declare the webhook source and the project, so
// the checker cannot disagree with the thing it is checking — the JOB_SCHEDULES
// principle applied one rule over.
const (
	repoEnv    = "GITHUB_REPO"   // "owner/name"
	projectEnv = "BUILD_PROJECT" // CodeBuild project name
)

// builds is the CodeBuild reader, nil until main wires it. Nil disables the
// drift check rather than failing the Lambda: the failure and crash paths are
// the load-bearing ones and must not go down with this.
var builds codeBuildAPI

// codeBuildAPI is the slice of the CodeBuild client this needs, named so tests
// substitute a double instead of reaching AWS.
type codeBuildAPI interface {
	ListBuildsForProject(ctx context.Context, in *codebuild.ListBuildsForProjectInput, opts ...func(*codebuild.Options)) (*codebuild.ListBuildsForProjectOutput, error)
	BatchGetBuilds(ctx context.Context, in *codebuild.BatchGetBuildsInput, opts ...func(*codebuild.Options)) (*codebuild.BatchGetBuildsOutput, error)
}

// httpGet is the HTTP seam, replaced in tests so no test reaches github.com.
var httpGet = func(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rosterbot-opsnotify")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// headOfMain reads the newest commit on main from the GitHub REST API.
//
// Unauthenticated: rosterbot is a public repository, so this needs no token and
// therefore no secret to rotate, and the 60-requests-per-hour anonymous limit is
// untroubled by four calls a day. It is also the point of the check that this
// source is INDEPENDENT of AWS — asking AWS what AWS built would answer a
// different, useless question.
//
// A zero time on an unparseable date is deliberate and is handled by
// opsalert.Check as "cannot judge" rather than as the epoch.
func headOfMain(ctx context.Context, repo string) (sha string, at time.Time, err error) {
	url := "https://api.github.com/repos/" + repo + "/commits/main"
	body, err := httpGet(ctx, url)
	if err != nil {
		return "", time.Time{}, err
	}
	var out struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("parse github response: %w", err)
	}
	if out.SHA == "" {
		return "", time.Time{}, fmt.Errorf("github response carried no sha")
	}
	t, perr := time.Parse(time.RFC3339, out.Commit.Committer.Date)
	if perr != nil {
		log.Printf("drift: head %s has unparseable date %q: %v",
			opsalert.ShortSHA(out.SHA), out.Commit.Committer.Date, perr)
		return out.SHA, time.Time{}, nil
	}
	return out.SHA, t, nil
}

// driftBuildPage bounds how far back the check looks for a successful build.
//
// ListBuildsForProject returns ids newest-first, so one page of 100 covers the
// recent history that can possibly matter; a project whose last 100 builds all
// failed is drifting by any definition, and Check reports the empty baseline as
// a finding rather than hiding it.
const driftBuildPage = 100

// newestSuccessfulBuild returns the commit of the most recent SUCCEEDED build.
//
// resolvedSourceVersion, not sourceVersion: the latter is the ref the build was
// asked for ("main"), the former is the commit it actually resolved to, which is
// the only one comparable with a head sha.
//
// An empty return with a nil error means "no successful build in the window",
// which is a genuine answer. Errors are reserved for not being able to look.
func newestSuccessfulBuild(ctx context.Context, project string) (string, error) {
	if builds == nil {
		return "", fmt.Errorf("codebuild client not configured")
	}
	ids, err := builds.ListBuildsForProject(ctx, &codebuild.ListBuildsForProjectInput{
		ProjectName: &project,
		SortOrder:   cbtypes.SortOrderTypeDescending,
	})
	if err != nil {
		return "", fmt.Errorf("list builds: %w", err)
	}
	if len(ids.Ids) == 0 {
		return "", nil
	}
	if len(ids.Ids) > driftBuildPage {
		ids.Ids = ids.Ids[:driftBuildPage]
	}
	// BatchGetBuilds caps at 100 ids, which driftBuildPage already respects.
	got, err := builds.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: ids.Ids})
	if err != nil {
		return "", fmt.Errorf("get builds: %w", err)
	}
	// BatchGetBuilds does not promise the request's order, so pick by start
	// time rather than trusting position — relying on the order would silently
	// return an OLDER successful build and under-report the drift.
	var bestSHA string
	var bestAt time.Time
	for _, b := range got.Builds {
		if b.BuildStatus != cbtypes.StatusTypeSucceeded || b.ResolvedSourceVersion == nil {
			continue
		}
		if b.StartTime == nil {
			continue
		}
		if bestSHA == "" || b.StartTime.After(bestAt) {
			bestSHA, bestAt = *b.ResolvedSourceVersion, *b.StartTime
		}
	}
	return bestSHA, nil
}

// handleDrift asserts that main's newest commit has actually been built, and
// alerts once per outage when it has not.
//
// Every failure returns nil. An async-invoke retry would re-run the whole check
// and re-send anything that already got through, and the 6-hourly tick covers a
// genuinely missed one — the same reasoning as handleHeartbeat.
func handleDrift(ctx context.Context) error {
	repo, project := os.Getenv(repoEnv), os.Getenv(projectEnv)
	if repo == "" || project == "" {
		log.Printf("drift: %s/%s unset; check disabled", repoEnv, projectEnv)
		return nil
	}

	head, at, err := headOfMain(ctx, repo)
	if err != nil {
		// Blindness, not a finding. Logged so it is visible in CloudWatch, and
		// left un-alerted so the channel never carries news about itself.
		log.Printf("drift: cannot read %s main: %v", repo, err)
		return nil
	}
	built, err := newestSuccessfulBuild(ctx, project)
	if err != nil {
		log.Printf("drift: cannot read builds for %s: %v", project, err)
		return nil
	}

	d, ok := opsalert.Check(opsalert.BuildState{
		HeadSHA: head, HeadTime: at, BuiltSHA: built,
	}, driftGrace, now())
	if !ok {
		log.Printf("drift: head %s, built %s — current",
			opsalert.ShortSHA(head), opsalert.ShortSHA(built))
		return nil
	}

	prev, _ := markers.token(ctx, d.MarkerKey())
	if !d.NeedsAlert(prev) {
		log.Printf("drift: already alerted for baseline %s; staying quiet",
			opsalert.ShortSHA(built))
		return nil
	}
	title, body := opsalert.FormatDrift(d)
	return send1(ctx, markers, alert{
		key:   d.MarkerKey(),
		note:  d.AlertToken(),
		title: title,
		body:  body,
	})
}
