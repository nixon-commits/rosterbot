package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// A private repository answers the head read with 404 and an expired token
// with 401. Either way the check cannot judge, and before this it logged the
// error and returned — a blind check indistinguishable from a healthy one.
// It is now a finding, once per outage, deduplicated like every other alert.
func TestHandleDrift_UnreadableHeadAlertsOncePerOutage(t *testing.T) {
	freezeClock(t, driftNow)
	got := capture(t)
	fakeMarkers(t)
	withDriftEnv(t, okBuilds())
	withGitHub(t, "", 0, errors.New("github https://api.github.com/repos/nixon-commits/rosterbot/commits/main: 404 Not Found"))

	for i := 0; i < 2; i++ {
		if err := handleDrift(context.Background()); err != nil {
			t.Fatalf("tick %d: err = %v, want nil (a retry cannot help)", i, err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1 — one outage is one alert: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "nixon-commits/rosterbot") || !strings.Contains((*got)[0], "404 Not Found") {
		t.Errorf("alert %q must name the repository and the error", (*got)[0])
	}
	tok, found := markers.token(context.Background(), opsalert.DriftBlind{}.MarkerKey())
	if !found || tok != opsalert.DriftBlindToken {
		t.Errorf("marker token = %q found=%v, want %q", tok, found, opsalert.DriftBlindToken)
	}
}

// Recovery has to overwrite the marker, or the first outage's alert mutes every
// later one for the life of the bucket — the DriftDark restore rule.
func TestHandleDrift_ReadableHeadClearsTheBlindMarkerSoASecondOutageAlertsAgain(t *testing.T) {
	freezeClock(t, driftNow)
	got := capture(t)
	fakeMarkers(t)
	withDriftEnv(t, okBuilds())

	withGitHub(t, "", 0, errors.New("github 401 Unauthorized"))
	if err := handleDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	withGitHub(t, dBuilt, 6*time.Hour, nil) // readable, and current
	if err := handleDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("recovery must not send; got %d sends: %v", len(*got), *got)
	}
	tok, _ := markers.token(context.Background(), opsalert.DriftBlind{}.MarkerKey())
	if tok != opsalert.DriftBlindClearToken {
		t.Fatalf("marker after a readable tick = %q, want %q", tok, opsalert.DriftBlindClearToken)
	}
	withGitHub(t, "", 0, errors.New("github 401 Unauthorized"))
	if err := handleDrift(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Fatalf("got %d sends, want 2 — a second outage is a second alert", len(*got))
	}
}

// authHeaderServer records the Authorization header of the one request it
// serves and answers a minimal commit document.
func authHeaderServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"sha":"abc","commit":{"committer":{"date":"2026-08-20T18:00:00Z"}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// The read carries the token as a bearer when one is configured. GitHub
// answers an anonymous read of a private repository with 404, so a header that
// silently failed to attach would read exactly like the repository vanishing.
func TestHTTPGet_SendsTheTokenAsABearer(t *testing.T) {
	srv, seen := authHeaderServer(t)
	prev := githubToken
	githubToken = "ghp_test"
	t.Cleanup(func() { githubToken = prev })

	if _, err := httpGet(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if *seen != "Bearer ghp_test" {
		t.Errorf("Authorization = %q, want %q", *seen, "Bearer ghp_test")
	}
}

// Without a token the read stays anonymous, which is what keeps a public
// repository working before the parameter exists — the change has to be
// deployable ahead of the visibility flip.
func TestHTTPGet_StaysAnonymousWithoutAToken(t *testing.T) {
	srv, seen := authHeaderServer(t)
	prev := githubToken
	githubToken = ""
	t.Cleanup(func() { githubToken = prev })

	if _, err := httpGet(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if *seen != "" {
		t.Errorf("Authorization = %q, want none", *seen)
	}
}

// fakeParams is a paramAPI double: one value or one error, and a count of how
// often it was asked.
type fakeParams struct {
	value string
	err   error
	calls int
}

func (f *fakeParams) GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	v := f.value
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: &v}}, nil
}

// The parameter NAME comes from the CDK, under the same `if buildProject != nil`
// that sets GITHUB_REPO: a stack deployed without the build project has no
// drift check and must not pay an SSM read (or need the IAM) for it.
func TestLoadGitHubToken_SkipsTheReadWhenNoParameterIsNamed(t *testing.T) {
	t.Setenv(githubTokenParamEnv, "")
	f := &fakeParams{value: "should-not-be-read"}
	if got := loadGitHubToken(context.Background(), f); got != "" {
		t.Errorf("token = %q, want empty", got)
	}
	if f.calls != 0 {
		t.Errorf("GetParameter called %d times, want 0", f.calls)
	}
}

func TestLoadGitHubToken_ReadsTheNamedParameter(t *testing.T) {
	t.Setenv(githubTokenParamEnv, "/rosterbot/GITHUB_READ_TOKEN")
	f := &fakeParams{value: "ghp_live"}
	if got := loadGitHubToken(context.Background(), f); got != "ghp_live" {
		t.Errorf("token = %q, want %q", got, "ghp_live")
	}
}

// Unlike the Pushover parameters, a failed read here must not take the Lambda
// down: the failure and crash alert paths are the load-bearing ones. The check
// runs anonymously instead, and the blind alert reports what that costs.
func TestLoadGitHubToken_EmptyRatherThanFatalOnAFailedRead(t *testing.T) {
	t.Setenv(githubTokenParamEnv, "/rosterbot/GITHUB_READ_TOKEN")
	f := &fakeParams{err: errors.New("ParameterNotFound")}
	if got := loadGitHubToken(context.Background(), f); got != "" {
		t.Errorf("token = %q, want empty", got)
	}
}
