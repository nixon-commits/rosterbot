// Command dispatch is the per-tenant fan-out for rosterbot's scheduled jobs
// (rosterbot-crq.13).
//
// An EventBridge rule that used to target ECS directly now targets this
// function, handing it the job's argv. It enumerates the active tenants and
// launches one Fargate task each, with that tenant's id in the environment.
//
// It exists as a Lambda because ONE RULE CAN ONLY EVER PRODUCE ONE RunTask:
// EventBridge's ECS target takes a container override whose environment values
// are required static strings, so there is no way to express "one task per row
// in a table" in a rule. The indirection is the feature.
//
// Only the genuinely per-tenant schedules point here. The league-wide jobs —
// waivers, transactions, gs-check, archive, team-values — still target ECS
// directly, which is what keeps the expensive rate-limited upstreams
// (FanGraphs, Savant, HKB, MLB) at one call per day rather than one per tenant.
package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/lambda/internal/ecsrun"
)

func main() {
	ctx := context.Background()

	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		log.Fatal("IDENTITY_TABLE is not set; there is no tenant directory to fan out over")
	}
	// The identity store is reused rather than reimplemented against DynamoDB
	// here. ListActive is already paginated, and its doc comment says why: an
	// unpaginated scan truncates at 1 MB, and a fan-out that quietly drops
	// tenants past that boundary is a failure mode with no symptom.
	users, err := ddbuser.New(ctx, table)
	if err != nil {
		log.Fatalf("init tenant directory: %v", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	runner, err := ecsrun.New(ecs.NewFromConfig(awsCfg))
	if err != nil {
		log.Fatalf("init task runner: %v", err)
	}

	d := dispatcher{tenants: users, launcher: runner}
	lambda.Start(d.handle)
}
