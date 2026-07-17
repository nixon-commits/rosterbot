// Command lambda is the AWS Lambda entry point for the read-only lineup API
// (GET /v1/lineup/today), fronted by a Lambda Function URL.
//
// It does no optimizer or Fantrax work: it authenticates the Bearer token and
// returns the precomputed JSON the hourly `optimize` run published to S3. The
// heavy lifting (headless-Chrome Fantrax login + projections + optimization)
// already happened on the producer, so this stays a fast, cheap read.
//
// Build/deploy is driven by the CDK GoFunction in infra/ (Entry: ../lambda).
package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
)

func main() {
	ctx := context.Background()

	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		log.Fatal("STATE_BUCKET is not set")
	}

	lineups, err := s3lineup.New(ctx, bucket, "lineup/")
	if err != nil {
		log.Fatalf("init s3 lineup store: %v", err)
	}
	runs, err := s3lineup.NewRuns(ctx, bucket, "runledger/")
	if err != nil {
		log.Fatalf("init s3 runs store: %v", err)
	}
	notifs, err := s3lineup.NewNotifications(ctx, bucket, "notifications/")
	if err != nil {
		log.Fatalf("init s3 notifications store: %v", err)
	}
	output, err := s3lineup.NewOutput(ctx, bucket, "runs/")
	if err != nil {
		log.Fatalf("init s3 output store: %v", err)
	}
	identities, err := s3lineup.NewIdentity(ctx, bucket, "webauthn/")
	if err != nil {
		log.Fatalf("init s3 identity store: %v", err)
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	jobs, err := newECSRunner(ecs.NewFromConfig(cfg))
	if err != nil {
		log.Fatalf("init job runner: %v", err)
	}

	token, err := loadToken(ctx)
	if err != nil {
		log.Fatalf("load API token: %v", err)
	}
	sessionSecret, err := loadSessionSecret(ctx)
	if err != nil {
		log.Fatalf("load session secret: %v", err)
	}
	rpID := os.Getenv("RP_ID")
	rpOrigin := os.Getenv("RP_ORIGIN")
	if rpID == "" || rpOrigin == "" {
		log.Fatal("RP_ID and RP_ORIGIN must be set")
	}
	wa, err := lineupapi.NewWebAuthn(rpID, rpOrigin, "rosterbot")
	if err != nil {
		log.Fatalf("init webauthn config: %v", err)
	}

	handler := lineupapi.Handler(lineupapi.Config{
		Token:         token,
		Lineups:       lineups,
		Runs:          runs,
		Jobs:          jobs,
		Notifications: notifs,
		Output:        output,
		Identities:    identities,
		WebAuthn:      wa,
		SessionSecret: []byte(sessionSecret),
	})
	lambda.Start(adapt(handler))
}

// loadToken reads the bearer token from SSM Parameter Store (SecureString) named
// by API_TOKEN_PARAM. Fetched once at cold start so the secret never lives in
// the function's plaintext configuration.
func loadToken(ctx context.Context) (string, error) {
	name := os.Getenv("API_TOKEN_PARAM")
	if name == "" {
		name = "/rosterbot/ROSTERBOT_API_TOKEN"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", err
	}
	out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name:           &name,
		WithDecryption: boolPtr(true),
	})
	if err != nil {
		return "", err
	}
	return *out.Parameter.Value, nil
}

// loadSessionSecret reads the session-cookie HMAC secret from SSM Parameter
// Store (SecureString) named by SESSION_SECRET_PARAM. Fetched once at cold
// start, mirroring loadToken.
func loadSessionSecret(ctx context.Context) (string, error) {
	name := os.Getenv("SESSION_SECRET_PARAM")
	if name == "" {
		name = "/rosterbot/DASHBOARD_SESSION_SECRET"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", err
	}
	out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name:           &name,
		WithDecryption: boolPtr(true),
	})
	if err != nil {
		return "", err
	}
	return *out.Parameter.Value, nil
}

// adapt bridges a Lambda Function URL event to the standard http.Handler by
// replaying it through an in-memory recorder. Keeps the handler a plain
// net/http handler shared with the local `serve` command.
func adapt(h http.Handler) func(context.Context, events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	return func(_ context.Context, evt events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
		target := evt.RawPath
		if evt.RawQueryString != "" {
			target += "?" + evt.RawQueryString
		}
		req := httptest.NewRequest(evt.RequestContext.HTTP.Method, target, strings.NewReader(evt.Body))
		for k, v := range evt.Headers {
			req.Header.Set(k, v)
		}
		// Function URL payload format 2.0 delivers incoming cookies in the
		// dedicated Cookies field, not Headers — join them into a single
		// Cookie header (the standard wire format) so r.Cookie(...) in the
		// handler can see them.
		if len(evt.Cookies) > 0 {
			req.Header.Set("Cookie", strings.Join(evt.Cookies, "; "))
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		headers := map[string]string{}
		if ct := rec.Header().Get("Content-Type"); ct != "" {
			headers["Content-Type"] = ct
		}
		return events.LambdaFunctionURLResponse{
			StatusCode: rec.Code,
			Headers:    headers,
			// Set-Cookie headers must go on the dedicated Cookies field, not
			// Headers — the response Headers map can only hold one value per
			// key, which would silently drop every cookie the handler sets
			// (ceremony cookie on begin, session cookie on finish, clears on
			// logout).
			Cookies: rec.Header()["Set-Cookie"],
			Body:    rec.Body.String(),
		}, nil
	}
}

func boolPtr(b bool) *bool { return &b }
