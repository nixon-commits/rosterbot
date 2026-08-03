// Command opsnotify is the AWS Lambda that turns an ops event into a Pushover
// notification on the personal ops channel. It serves two event sources:
//
//   - "CodeBuild Build State Change" — image build outcomes (rosterbot-00j)
//   - "ECS Task State Change"        — scheduled job failures (rosterbot-naz)
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

	"github.com/nixon-commits/rosterbot/internal/notify"
)

// send is the Pushover seam. Replaced in tests so handler tests never reach the
// network; wired to the real sender in main once the SSM creds are read.
var send = func(title, message string) error { return nil }

// ledger is the run-ledger reader, nil until main wires it (and in the
// CodeBuild-only tests, which never touch the ECS path).
var ledger *ledgerReader

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
		return notify.SendPushover(userKey, apiToken, title, message)
	}

	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		l, err := newLedgerReader(ctx, bucket)
		if err != nil {
			log.Fatalf("ledger reader: %v", err)
		}
		ledger = l
	}

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
