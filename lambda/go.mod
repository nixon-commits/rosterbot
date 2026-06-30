// Separate module so aws-lambda-go stays out of the main rosterbot binary's
// dependency graph, and so `go build ./...` / `go vet ./...` at the repo root
// (which does not descend into nested modules) never needs it. Run
// `cd lambda && go mod tidy` once (network) before building/deploying.
//
// The module path is under github.com/nixon-commits/rosterbot/ so it is allowed
// to import the repo's internal/ packages; the replace points back at the
// parent checkout.
module github.com/nixon-commits/rosterbot/lambda

go 1.26.1

require (
	github.com/aws/aws-lambda-go v1.49.0
	github.com/aws/aws-sdk-go-v2/config v1.32.26
	github.com/aws/aws-sdk-go-v2/service/ecs v1.84.0
	github.com/aws/aws-sdk-go-v2/service/ssm v1.62.0
	github.com/nixon-commits/rosterbot v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2 v1.42.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.13 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.25 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.29 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.104.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.2.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.31.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.36.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.43.4 // indirect
	github.com/aws/smithy-go v1.27.1 // indirect
	github.com/chromedp/cdproto v0.0.0-20260427013145-5737772c319b // indirect
	github.com/chromedp/chromedp v0.15.1 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-json-experiment/json v0.0.0-20260505212615-e40f80bf6836 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/pmurley/go-fantrax v0.1.16 // indirect
	github.com/pmurley/go-mlb v0.1.2 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/nixon-commits/rosterbot => ../
