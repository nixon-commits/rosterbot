package main

import (
	"testing"

	"github.com/aws/jsii-runtime-go"
)

// The bot image is scanned when it is pushed (rosterbot-nhpg). It is rebuilt
// only on a push to main, so without the scan an aged image reports nothing.
func TestBotImageRepositoryScansOnPush(t *testing.T) {
	tmpl, _ := infraTemplate(t)
	tmpl.HasResourceProperties(jsii.String("AWS::ECR::Repository"), map[string]any{
		"RepositoryName":             "rosterbot",
		"ImageScanningConfiguration": map[string]any{"ScanOnPush": true},
	})
}
