// Command opsnotify is the AWS Lambda that turns an ops event into a Pushover
// notification on the personal ops channel. It serves two event sources:
//
//   - "CodeBuild Build State Change" — image build outcomes (rosterbot-00j)
//   - "ECS Task State Change"        — scheduled job failures (rosterbot-naz)
//   - "Rosterbot Heartbeat"          — jobs that never launched (rosterbot-ys8)
//
// The first two are reactive: something has to happen for them to fire, so a job
// whose schedule is disabled or whose cluster is unreachable produces no event
// and no alert. The heartbeat is the scheduled counterweight that turns that
// silence into a signal.
//
// Both are wired by the CDK in infra/ (Entry: ../opsnotify). The function
// itself is created unconditionally; only the CodeBuild rule sits behind the
// enableBuild context gate, because job-failure alerting must survive a stack
// deployed without it.
//
// Separate module so aws-lambda-go stays out of the main rosterbot binary's
// dependency graph (mirrors lambda/). It deliberately does NOT import
// internal/lineupapi — that would drag internal/fantrax and chromedp into a
// notifier — so the ledger's records are decoded into internal/opsalert.Record.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/nixon-commits/rosterbot/internal/pushover"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// send is the Pushover seam. Replaced in tests so handler tests never reach the
// network; wired to the real sender in main once the SSM creds are read.
var send = func(title, message string) error { return nil }

// ledger is the run-ledger reader, nil until main wires it (and in the
// CodeBuild-only tests, which never touch the ECS path).
var ledger *ledgerReader

// markers deduplicates alerts across repeated deliveries of one event. Nil is a
// working configuration — every alert simply goes out — so a state bucket that
// is unset or unwritable degrades to the pre-deduplication behaviour rather than
// to silence.
var markers *markerStore

// rosterBlob reads the active tenant roster. Nil disables the augmentation and
// leaves Overdue on its ledger-derived tenant set.
var rosterBlob *s3blob.Blob

func main() {
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	ssmc := ssm.NewFromConfig(cfg)
	// Read once at cold start; the alert path being down is itself worth a hard
	// init failure so it surfaces in CloudWatch.
	userKey := mustParam(ctx, ssmc, "/rosterbot/PUSHOVER_USER_KEY")
	apiToken := mustParam(ctx, ssmc, "/rosterbot/PUSHOVER_API_TOKEN")
	send = func(title, message string) error {
		return pushover.Send(userKey, apiToken, title, message)
	}

	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		l, err := newLedgerReader(ctx, bucket)
		if err != nil {
			log.Fatalf("ledger reader: %v", err)
		}
		ledger = l

		b, err := s3blob.New(ctx, bucket, markerPrefix)
		if err != nil {
			log.Fatalf("marker store: %v", err)
		}
		markers = &markerStore{blob: b}

		// The active tenant roster the fan-out dispatcher publishes
		// (rosterbot-crq.4). Optional in every direction: a read failure, a
		// missing object or a malformed body all degrade Overdue to its
		// ledger-derived tenant set, which is what it used before this existed.
		// The prefix comes from layout rather than a literal because the WRITER
		// is in a different Go module with no compiler link to here — the exact
		// arrangement that let this Lambda's ledger reader drift away from its
		// writer and blind alerting for three days (rosterbot-3vr).
		rb, err := s3blob.New(ctx, bucket, layout.TenantRoster.S3Prefix)
		if err != nil {
			log.Printf("tenant roster disabled: %v", err)
		} else {
			rosterBlob = rb
		}
	}
	schedules = loadSchedules()

	lambda.Start(dispatch)
}

// dispatch routes an EventBridge event to the handler for its detail-type. The
// raw message is decoded twice — once for the envelope, once into the concrete
// event — because aws-lambda-go ships no ECS Task State Change type, so the two
// sources cannot share one typed parameter.
func dispatch(ctx context.Context, raw json.RawMessage) error {
	var env events.CloudWatchEvent
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}

	switch env.DetailType {
	case "CodeBuild Build State Change":
		var ev events.CodeBuildEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		title, body := formatBuild(ev)
		return sendOrLog(title, body)

	case "ECS Task State Change":
		return handleTask(ctx, env.Detail)

	case heartbeatDetailType:
		return handleHeartbeat(ctx)

	default:
		log.Printf("ignoring unhandled detail-type %q", env.DetailType)
		return nil
	}
}

// sendOrLog sends unless the title is empty (the "stay quiet" signal), and
// returns the error so EventBridge async-invoke retries rather than silently
// swallowing a missed alert.
func sendOrLog(title, body string) error {
	if title == "" {
		return nil
	}
	if err := send(title, body); err != nil {
		log.Printf("send pushover: %v", err)
		return err
	}
	return nil
}

func mustParam(ctx context.Context, c *ssm.Client, name string) string {
	withDecryption := true
	out, err := c.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           &name,
		WithDecryption: &withDecryption,
	})
	if err != nil {
		log.Fatalf("read %s: %v", name, err)
	}
	return *out.Parameter.Value
}
