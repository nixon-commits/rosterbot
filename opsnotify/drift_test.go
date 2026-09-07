package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

var driftNow = time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)

// The real commits from the rosterbot-7p1i incident: bb1cc1e merged while
// 134f326's build was in flight, was rejected at the webhook, and never built.
const (
	dHead  = "bb1cc1e27e6bab69a754eb825d7351baa076f725"
	dBuilt = "134f326990287688710862f0ea7da30c15f6233c"
)

// fakeBuilds is a codeBuildAPI double. Ids are returned in the order given;
// Builds are returned in a DELIBERATELY different order so a test would catch
// newestSuccessfulBuild trusting position instead of start time.
type fakeBuilds struct {
	ids      []string
	builds   []cbtypes.Build
	listErr  error
	batchErr error
}

func (f *fakeBuilds) ListBuildsForProject(context.Context, *codebuild.ListBuildsForProjectInput, ...func(*codebuild.Options)) (*codebuild.ListBuildsForProjectOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &codebuild.ListBuildsForProjectOutput{Ids: f.ids}, nil
}

func (f *fakeBuilds) BatchGetBuilds(context.Context, *codebuild.BatchGetBuildsInput, ...func(*codebuild.Options)) (*codebuild.BatchGetBuildsOutput, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	return &codebuild.BatchGetBuildsOutput{Builds: f.builds}, nil
}

func build(sha string, ago time.Duration, status cbtypes.StatusType) cbtypes.Build {
	at := driftNow.Add(-ago)
	s := sha
	return cbtypes.Build{BuildStatus: status, ResolvedSourceVersion: &s, StartTime: &at}
}

// withDriftEnv installs the two coordinates and a CodeBuild double.
func withDriftEnv(t *testing.T, fb codeBuildAPI) {
	t.Helper()
	t.Setenv(repoEnv, "nixon-commits/rosterbot")
	t.Setenv(projectEnv, "Build45A36621")
	prev := builds
	builds = fb
	t.Cleanup(func() { builds = prev })
}

// withGitHub stubs the HTTP seam so no test reaches github.com.
func withGitHub(t *testing.T, sha string, ago time.Duration, err error) {
	t.Helper()
	prev := httpGet
	httpGet = func(context.Context, string) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		body := fmt.Sprintf(`{"sha":%q,"commit":{"committer":{"date":%q}}}`,
			sha, driftNow.Add(-ago).Format(time.RFC3339))
		return []byte(body), nil
	}
	t.Cleanup(func() { httpGet = prev })
}

func okBuilds() *fakeBuilds {
	return &fakeBuilds{
		ids: []string{"b3", "b2", "b1"},
		builds: []cbtypes.Build{
			build("aaaaaaa0000000000000000000000000000000ff", 30*time.Minute, cbtypes.StatusTypeFailed),
			build(dBuilt, 90*time.Minute, cbtypes.StatusTypeSucceeded),
			build("999999900000000000000000000000000000000f", 48*time.Hour, cbtypes.StatusTypeSucceeded),
		},
	}
}

func TestHandleDrift_AlertsOnAMergeThatNeverBuilt(t *testing.T) {
	freezeClock(t, driftNow)
	got := capture(t)
	fake := fakeMarkers(t)
	withDriftEnv(t, okBuilds())
	withGitHub(t, dHead, 6*time.Hour, nil)

	if err := handleDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1", len(*got))
	}
	body := (*got)[0]
	if !strings.Contains(body, "bb1cc1e") || !strings.Contains(body, "134f326") {
		t.Errorf("body %q must name both the unbuilt head and the last good build", body)
	}
	if len(fake.Keys()) != 1 {
		t.Fatalf("markers %v, want exactly one", fake.Keys())
	}
}

// The dedup contract end to end: a second tick during the same outage must be
// silent even though main has moved on.
func TestHandleDrift_SecondTickDuringTheSameOutageStaysQuiet(t *testing.T) {
	freezeClock(t, driftNow)
	got := capture(t)
	fakeMarkers(t)
	withDriftEnv(t, okBuilds())
	withGitHub(t, dHead, 6*time.Hour, nil)

	if err := handleDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A further merge lands; the trigger is still broken, so the baseline is
	// unchanged and this is the same outage.
	withGitHub(t, "ddddddd0000000000000000000000000000000fa", 2*time.Hour, nil)
	if err := handleDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1 — one outage is one alert", len(*got))
	}
}

func TestHandleDrift_QuietWhenProductionIsCurrent(t *testing.T) {
	freezeClock(t, driftNow)
	got := capture(t)
	withDriftEnv(t, okBuilds())
	fakeMarkers(t)
	withGitHub(t, dBuilt, 6*time.Hour, nil)

	if err := handleDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("got %d sends, want 0", len(*got))
	}
}

// Blindness is never a finding, and never an error either: returning one would
// make async-invoke retry a check that cannot succeed.
func TestHandleDrift_UnreadableSourcesStayQuietAndDoNotRetry(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"github unreachable": func(t *testing.T) {
			withDriftEnv(t, okBuilds())
			withGitHub(t, "", 0, errors.New("github 503"))
		},
		"codebuild unreachable": func(t *testing.T) {
			withDriftEnv(t, &fakeBuilds{listErr: errors.New("throttled")})
			withGitHub(t, dHead, 6*time.Hour, nil)
		},
		"coordinates unset": func(t *testing.T) {
			t.Setenv(repoEnv, "")
			t.Setenv(projectEnv, "")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			freezeClock(t, driftNow)
			got := capture(t)
			fakeMarkers(t)
			setup(t)

			if err := handleDrift(context.Background()); err != nil {
				t.Fatalf("err = %v, want nil (a retry cannot help)", err)
			}
			if len(*got) != 0 {
				t.Errorf("got %d sends, want 0", len(*got))
			}
		})
	}
}

// BatchGetBuilds does not promise the request's order. Trusting position would
// silently pick an older success and under-report the drift.
func TestNewestSuccessfulBuild_PicksNewestSuccessRegardlessOfOrder(t *testing.T) {
	withDriftEnv(t, okBuilds())
	got, err := newestSuccessfulBuild(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if got != dBuilt {
		t.Errorf("got %s, want %s (newest SUCCEEDED, ignoring the newer FAILED)", got, dBuilt)
	}
}

// No successful build is a real answer, not an error — Check reports it.
func TestNewestSuccessfulBuild_EmptyWhenNothingSucceeded(t *testing.T) {
	withDriftEnv(t, &fakeBuilds{
		ids:    []string{"b1"},
		builds: []cbtypes.Build{build("aaa", time.Hour, cbtypes.StatusTypeFailed)},
	})
	got, err := newestSuccessfulBuild(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestHeadOfMain_ParsesShaAndDate(t *testing.T) {
	withGitHub(t, dHead, 3*time.Hour, nil)
	sha, at, err := headOfMain(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if sha != dHead {
		t.Errorf("sha = %s, want %s", sha, dHead)
	}
	if !at.Equal(driftNow.Add(-3 * time.Hour)) {
		t.Errorf("time = %v, want %v", at, driftNow.Add(-3*time.Hour))
	}
}

// An undateable head must not become the epoch — that would report every
// deployment as infinitely overdue.
func TestHeadOfMain_UndateableHeadYieldsZeroTimeNotEpoch(t *testing.T) {
	prev := httpGet
	httpGet = func(context.Context, string) ([]byte, error) {
		return []byte(`{"sha":"` + dHead + `","commit":{"committer":{"date":"not-a-date"}}}`), nil
	}
	t.Cleanup(func() { httpGet = prev })

	sha, at, err := headOfMain(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if sha != dHead || !at.IsZero() {
		t.Errorf("got sha=%s at=%v, want sha set and zero time", sha, at)
	}
}

// --- Mode B: the check that the drift check exists at all (rosterbot-k2w0) ---

// A stack deployed without `-c enableBuild=true` removes BUILD_PROJECT along
// with the rule that would have noticed. The heartbeat must say so, once.
func TestHandleDriftConfig_AlertsOnceWhileTheProjectIsUnset(t *testing.T) {
	got := capture(t)
	fakeMarkers(t)
	t.Setenv(projectEnv, "")

	handleDriftConfig(context.Background())
	handleDriftConfig(context.Background())

	if len(*got) != 1 {
		t.Fatalf("got %d sends, want exactly 1 across two ticks: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "enableBuild") {
		t.Errorf("alert %q must name the flag that restores the pipeline", (*got)[0])
	}
	tok, found := markers.token(context.Background(), opsalert.DriftDark{}.MarkerKey())
	if !found || tok != opsalert.DriftDarkToken {
		t.Errorf("marker token = %q found=%v, want %q", tok, found, opsalert.DriftDarkToken)
	}
}

// The healthy path must be silent AND leave no marker: a marker written on
// every tick would make the prefix read as a history of alerts that never went out.
func TestHandleDriftConfig_QuietAndMarkerlessWhenConfigured(t *testing.T) {
	got := capture(t)
	fake := fakeMarkers(t)
	t.Setenv(projectEnv, "Build45A36621")

	handleDriftConfig(context.Background())

	if len(*got) != 0 {
		t.Fatalf("got %d sends, want 0: %v", len(*got), *got)
	}
	if len(fake.Keys()) != 0 {
		t.Errorf("markers %v, want none on a healthy tick", fake.Keys())
	}
}

// Dark → restored → dark again is two outages and must be two alerts. The
// restore has to overwrite the token, or the first alert's marker would mute
// every later disablement for the life of the bucket.
func TestHandleDriftConfig_ASecondDisablementAfterRestoreAlertsAgain(t *testing.T) {
	got := capture(t)
	fakeMarkers(t)
	ctx := context.Background()

	t.Setenv(projectEnv, "")
	handleDriftConfig(ctx)

	t.Setenv(projectEnv, "Build45A36621")
	handleDriftConfig(ctx)
	if len(*got) != 1 {
		t.Fatalf("restore must not send; got %d sends", len(*got))
	}
	tok, _ := markers.token(ctx, opsalert.DriftDark{}.MarkerKey())
	if tok != "Build45A36621" {
		t.Fatalf("after restore the marker must record the project, got %q", tok)
	}

	t.Setenv(projectEnv, "")
	handleDriftConfig(ctx)
	if len(*got) != 2 {
		t.Fatalf("got %d sends, want 2 — a second disablement is a second outage", len(*got))
	}
}

// The wiring is the whole point: the assertion has to ride the heartbeat,
// because HeartbeatRule is created outside the enableBuild branch and
// BuildDriftRule is not. Pinned through dispatch so a refactor that drops the
// call from the heartbeat case fails here rather than in production silence.
func TestDispatch_HeartbeatAssertsTheDriftCheckIsConfigured(t *testing.T) {
	withSchedules(t, nil) // heartbeat itself is a no-op; only the assertion can send
	got := capture(t)
	fakeMarkers(t)
	t.Setenv(projectEnv, "")

	ev := `{"version":"0","detail-type":"Rosterbot Heartbeat","source":"rosterbot.ops","detail":{}}`
	if err := dispatch(context.Background(), json.RawMessage(ev)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], "enableBuild") {
		t.Fatalf("got %v, want one dark-pipeline alert", *got)
	}
}
