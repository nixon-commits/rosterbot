package main

import (
	"os"
	"strings"
	"testing"
)

// The CI deploy must name its stacks.
//
// This app has held more than one stack since InfraCertStack was added for the
// us-east-1 rosterbot.dev certificate, and cdk refuses a bare invocation on a
// multi-stack app: "Since this app includes more than a single stack, specify
// which stacks to use (wildcards are supported) or specify `--all`". It exits 1.
//
// That is a nastier break than it sounds, because nothing local catches it.
// `go build`, `go vet`, `go test`, `make build-modules` and `make check-pins`
// are all green; a developer running `cdk deploy --all` by hand sees it work.
// The failure appears only in CodeBuild, on main, AFTER merge — and because the
// deploy is the POST_BUILD step, every subsequent push to main fails there too
// until someone reads the log. It happened exactly this way on 2026-08-18: the
// rosterbot.dev merge went green through every gate and then took the deploy
// pipeline down, which also stranded the freshly-created DNS records.
//
// Asserting on explicitness rather than on stack count is deliberate — naming
// stacks is always correct, so this stays right whether the app has one stack
// or five, and a future third stack cannot reintroduce the break.
func TestBuildspec_NamesStacksExplicitly(t *testing.T) {
	raw, err := os.ReadFile("../buildspec.yml")
	if err != nil {
		t.Fatalf("read buildspec: %v", err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		code, _, _ := strings.Cut(line, "#") // drop trailing comments; a wholly commented line becomes empty
		if !strings.Contains(code, "cdk deploy") && !strings.Contains(code, "cdk destroy") {
			continue
		}
		if strings.Contains(code, "--all") || strings.Contains(code, "InfraStack") || strings.Contains(code, "InfraCertStack") {
			continue
		}
		t.Errorf("buildspec.yml:%d invokes cdk without naming a stack:\n\t%s\n"+
			"cdk exits 1 on a multi-stack app unless given --all or explicit stack names. "+
			"This breaks only in CodeBuild, after merge, and takes the deploy pipeline "+
			"down until fixed.", i+1, strings.TrimSpace(line))
	}
}
