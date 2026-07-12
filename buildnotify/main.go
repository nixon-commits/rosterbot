// Command buildnotify is the AWS Lambda that turns a CodeBuild "Build State
// Change" event into a Pushover notification. It is wired by the CDK GoFunction
// in infra/ (Entry: ../buildnotify) and invoked by the BuildNotifyRule
// EventBridge rule. Separate module so aws-lambda-go stays out of the main
// rosterbot binary's dependency graph (mirrors lambda/).
package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/nixon-commits/rosterbot/internal/notify"
)

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

	lambda.Start(func(_ context.Context, ev events.CodeBuildEvent) error {
		title, body := formatMessage(ev)
		if err := notify.SendPushover(userKey, apiToken, title, body); err != nil {
			// Return the error so EventBridge async-invoke retries rather than
			// silently swallowing a missed alert.
			log.Printf("send pushover: %v", err)
			return err
		}
		return nil
	})
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
