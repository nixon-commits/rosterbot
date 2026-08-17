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
	"fmt"
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
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/kmscreds"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
	"github.com/nixon-commits/rosterbot/lambda/internal/ecsrun"
)

// buildStores constructs every S3-backed store this function serves from, and
// returns them as a partially-filled Config.
//
// It is a separate function from main so that the composition root is
// testable. That is not incidental tidiness: Trades and TradeValues were
// declared on Config, routed in handler.go, and granted IAM in infra.go, but
// never set here — so GET /v1/trades and /v1/trades/values returned 501 in
// production from the day the Trades tab shipped, while local `serve` (which
// does wire them, cmd/serve.go) looked perfectly healthy. Nothing failed;
// a field was simply absent from a struct literal. TestBuildStores_WiresEvery
// StoreField in main_test.go is the guard, and it fails on the NEXT store
// field added to Config until that field is wired here too.
//
// Fields owned elsewhere in main — Token, Jobs, WebAuthn, SessionSecret —
// are deliberately left zero here and listed in that test's allowlist.
func buildStores(ctx context.Context, bucket string) (lineupapi.Config, error) {
	var cfg lineupapi.Config

	// The tenant whose state this API serves (rosterbot-crq.11). Composition
	// goes through layout.Artifact.PrefixFor rather than a second copy of the
	// rule here: producers and readers drifting apart is silent and total —
	// jobs writing to user=<uid>/ while the dashboard reads the un-segmented
	// prefix means a dashboard showing nothing while every job reports success.
	//
	// It is a single value for now, matching the tasks. Serving each caller
	// their OWN tenant means building these stores per request instead of at
	// cold start, which is the read half of the fan-out and lands with it.
	tenant := os.Getenv("ROSTERBOT_USER_ID")

	// Prefixes come from internal/statestore/layout, the single declaration of
	// the STATE_BUCKET key layout, rather than being retyped here. layout is a
	// zero-dependency leaf, so importing it from this module costs nothing.
	store := func(prefix string) (*s3lineup.Store, error) {
		return s3lineup.New(ctx, bucket, prefix)
	}

	var err error
	if cfg.Lineups, err = store(layout.Lineup.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 lineup store: %w", err)
	}
	if cfg.Runs, err = s3lineup.NewRuns(ctx, bucket, layout.RunLedger.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 runs store: %w", err)
	}
	if cfg.Notifications, err = s3lineup.NewNotifications(ctx, bucket, layout.Notification.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 notifications store: %w", err)
	}
	if cfg.Output, err = s3lineup.NewOutput(ctx, bucket, layout.RunOutput.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 output store: %w", err)
	}
	if cfg.Progress, err = s3lineup.NewProgress(ctx, bucket, layout.Progress.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 progress store: %w", err)
	}
	if cfg.Infra, err = s3lineup.NewInfra(ctx, bucket); err != nil {
		return cfg, fmt.Errorf("init s3 infra store: %w", err)
	}
	if cfg.Identities, err = s3lineup.NewIdentity(ctx, bucket, identityPrefix); err != nil {
		return cfg, fmt.Errorf("init s3 identity store: %w", err)
	}

	// The tenant directory (rosterbot-crq.10). A session's subject resolves
	// against this; the bearer token deliberately does not, so an operator can
	// still get in when this table is unreachable or has not been migrated yet.
	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		return cfg, fmt.Errorf("IDENTITY_TABLE is not set; passkey sessions cannot be resolved")
	}
	userStore, err := ddbuser.New(ctx, table)
	if err != nil {
		return cfg, fmt.Errorf("init dynamodb user store: %w", err)
	}
	cfg.Users = userStore
	cfg.Enrollments = userStore
	cfg.Connections = userStore

	// Sealer only — never an Opener. The IAM grants this function kms:Encrypt
	// and not kms:Decrypt, and the type mirrors it so the same mistake is a
	// compile error rather than a runtime AccessDenied nobody sees until it
	// happens.
	if key := os.Getenv("FANTRAX_CRED_KEY"); key != "" {
		sealer, err := kmscreds.NewSealer(ctx, key)
		if err != nil {
			return cfg, fmt.Errorf("init credential sealer: %w", err)
		}
		cfg.Sealer = sealer
	}

	// The Trades tab's two artifacts. Separate prefixes because they have
	// different producers and cadences (see layout.go); both are passkey-gated
	// rather than published to the world-readable report/ path, because a
	// pending offer is not something to publish before deciding on it.
	if cfg.Trades, err = store(layout.Trades.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 trades store: %w", err)
	}
	if cfg.TradeValues, err = store(layout.TradeValues.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 trade values store: %w", err)
	}

	// The three private dashboard reports. They live in the state bucket, not
	// the dashboard bucket's world-readable report/ prefix, so serving them
	// through this passkey-gated API is the only way the SPA can reach them.
	if cfg.Reports, err = store(layout.Reports.PrefixFor(tenant)); err != nil {
		return cfg, fmt.Errorf("init s3 reports store: %w", err)
	}

	return cfg, nil
}

// identityPrefix is the WebAuthn identity store's key prefix. It has no
// layout.Artifact because it is not an operator-facing artifact — GET /v1/infra
// deliberately does not list it, and the task role cannot read it.
const identityPrefix = "webauthn/"

func main() {
	ctx := context.Background()

	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		log.Fatal("STATE_BUCKET is not set")
	}

	apiCfg, err := buildStores(ctx, bucket)
	if err != nil {
		log.Fatal(err)
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	jobs, err := ecsrun.New(ecs.NewFromConfig(cfg))
	if err != nil {
		log.Fatalf("init job runner: %v", err)
	}

	token, err := loadSSMParam(ctx, "API_TOKEN_PARAM", "/rosterbot/ROSTERBOT_API_TOKEN")
	if err != nil {
		log.Fatalf("load API token: %v", err)
	}
	sessionSecret, err := loadSSMParam(ctx, "SESSION_SECRET_PARAM", "/rosterbot/DASHBOARD_SESSION_SECRET")
	if err != nil {
		log.Fatalf("load session secret: %v", err)
	}
	// RP_ID/RP_ORIGIN can't be plain env vars set from the CloudFront
	// distribution's own domain name: the distribution's origin is this
	// function's Function URL, so a direct env-var reference to the
	// distribution's DomainName attribute creates a circular CloudFormation
	// dependency (Lambda -> Distribution -> FunctionUrl -> Lambda). Instead
	// infra.go publishes the domain into SSM params after the distribution is
	// created, and hands this function only the (static) param names.
	rpID, err := loadSSMParam(ctx, "RP_ID_PARAM", "/rosterbot/DASHBOARD_RP_ID")
	if err != nil {
		log.Fatalf("load RP_ID: %v", err)
	}
	rpOrigin, err := loadSSMParam(ctx, "RP_ORIGIN_PARAM", "/rosterbot/DASHBOARD_RP_ORIGIN")
	if err != nil {
		log.Fatalf("load RP_ORIGIN: %v", err)
	}
	wa, err := lineupapi.NewWebAuthn(rpID, rpOrigin, "rosterbot")
	if err != nil {
		log.Fatalf("init webauthn config: %v", err)
	}

	// Only the four non-store fields are set here; every store came from
	// buildStores above, where the guard test can see them.
	// PER-CALLER STORES. The eight per-tenant stores buildStores wired above
	// now serve only as the single-tenant fallback shape; this provider is what
	// actually answers a request, resolved from the authenticated caller rather
	// than from the environment variable this process booted with.
	//
	// Without it every signed-in user read the operator's lineup, runs, trades,
	// reports and notification feed — the read half of the fan-out, which
	// crq.11 left undone while the write half shipped.
	apiCfg.Tenants = newTenantStores(bucket, lineupapi.UserID(os.Getenv("ROSTERBOT_USER_ID")))

	apiCfg.Token = token
	apiCfg.Jobs = jobs
	apiCfg.WebAuthn = wa
	apiCfg.SessionSecret = []byte(sessionSecret)

	lambda.Start(adapt(lineupapi.Handler(apiCfg)))
}

// loadSSMParam reads a value from SSM Parameter Store. The parameter's name
// is taken from the env var envVar, falling back to fallbackName when unset
// (matching a bare `rosterbot serve` run against a manually-populated
// parameter). Fetched fresh on every call; callers that want cold-start-only
// caching call this once during init.
func loadSSMParam(ctx context.Context, envVar, fallbackName string) (string, error) {
	name := os.Getenv(envVar)
	if name == "" {
		name = fallbackName
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
