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

// The CI deploy skips changeset creation.
//
// `cdk deploy` defaults to create-changeset-then-execute, and nothing in this
// pipeline ever reads the changeset: --require-approval is never, and no human
// reviews a CI deploy before it lands. The creation step alone measured ~26s
// per stack per build (build 290, 2026-08-29) — paid even when the deploy ends
// in "(no changes)". --method=direct issues the plain UpdateStack call, which
// still rolls back on failure; only the unread preview is dropped.
func TestBuildspec_DeploysWithoutAChangeset(t *testing.T) {
	raw, err := os.ReadFile("../buildspec.yml")
	if err != nil {
		t.Fatalf("read buildspec: %v", err)
	}
	deploys := 0
	for i, line := range strings.Split(string(raw), "\n") {
		code, _, _ := strings.Cut(line, "#")
		if !strings.Contains(code, "cdk deploy") {
			continue
		}
		deploys++
		if !strings.Contains(code, "--method=direct") {
			t.Errorf("buildspec.yml:%d deploys via the default changeset method:\n\t%s\n"+
				"nothing consumes the changeset in CI, and creating it costs ~26s per stack "+
				"on every push to main. Add --method=direct.", i+1, strings.TrimSpace(line))
		}
	}
	if deploys == 0 {
		t.Fatal("buildspec.yml contains no `cdk deploy` line; this test is asserting against nothing")
	}
}

// Node is installed by an install-phase command from a cached dist tarball —
// never via runtime-versions.
//
// CodeBuild installs runtime-versions during DOWNLOAD_SOURCE, BEFORE the S3
// cache is restored (measured, build 295: `n $NODE_22_VERSION` ran 18:03:36,
// "Downloading S3 cache" 18:04:10) — so a runtime-versions nodejs pin
// re-downloads Node from nodejs.org on every fresh host, ~33-45s, and no
// cache path can ever help it. The install phase runs after the restore,
// which is what makes the tarball cache (NODE_DIST_CACHE) work at all.
// Reintroducing a nodejs: runtime pin would not break anything visibly — the
// build would just quietly pay the download again forever.
func TestBuildspec_NodeInstallsAfterTheCacheRestore(t *testing.T) {
	raw, err := os.ReadFile("../buildspec.yml")
	if err != nil {
		t.Fatalf("read buildspec: %v", err)
	}
	lines := strings.Split(string(raw), "\n")

	cacheDir := ""
	installs := false
	for i, line := range lines {
		code, _, _ := strings.Cut(line, "#")
		trimmed := strings.TrimSpace(code)
		if strings.HasPrefix(trimmed, "nodejs:") {
			t.Errorf("buildspec.yml:%d selects nodejs via runtime-versions:\n\t%s\n"+
				"runtime-versions install runs before the S3 cache restore (measured, build 295), "+
				"so this download can never hit the cache; install Node from $NODE_DIST_CACHE in the "+
				"install phase instead", i+1, strings.TrimSpace(line))
		}
		if val, ok := strings.CutPrefix(trimmed, "NODE_DIST_CACHE:"); ok {
			cacheDir = strings.Trim(strings.TrimSpace(val), `"`)
		}
		if strings.Contains(code, "NODE_22_VERSION") && strings.Contains(code, "NODE_DIST_CACHE") {
			installs = true
		}
	}
	if cacheDir == "" {
		t.Fatal("buildspec.yml does not pin NODE_DIST_CACHE under env: variables:; " +
			"the install-phase Node step and the cache: paths: block both key off it")
	}
	if !installs {
		t.Error("no install command references both NODE_DIST_CACHE and NODE_22_VERSION; " +
			"Node must be installed from the cached tarball at the image's own pinned version")
	}
	// The image's npm tree must be removed before the tarball is extracted.
	// tar overwrites the files the new Node ships but does not delete the
	// files the image's pre-existing npm had that the new one does not, and
	// the mixed tree crashes npm on load ("Class extends value undefined is
	// not a constructor or null" — build 296, the failure that took the
	// deploy pipeline down at INSTALL). node itself is a single binary and
	// survives the overwrite, which is why `node --version` passing right
	// before the crash proves nothing about npm.
	cleans := false
	for _, line := range lines {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, "rm -rf") && strings.Contains(code, "/usr/local/lib/node_modules/npm") {
			cleans = true
			break
		}
	}
	if !cleans {
		t.Error("the install phase does not `rm -rf /usr/local/lib/node_modules/npm` before extracting the Node tarball; " +
			"tar over the image's npm leaves a mixed tree that crashes npm on load (build 296)")
	}
	want := cacheDir + "/**/*"
	covered := false
	for _, line := range lines {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, want) {
			covered = true
			break
		}
	}
	if !covered {
		t.Errorf("NODE_DIST_CACHE points at %s but no cache: paths: entry covers it (want %q); "+
			"without coverage every fresh host re-downloads Node from nodejs.org", cacheDir, want)
	}
}

// The Go cache dirs are pinned as env vars AND covered by the buildspec cache
// block, and the two must agree.
//
// `cdk synth` recompiles the aws-cdk-go bindings plus both bundled lambda
// modules on every build — 130s of the 380s total on build 290, all of it
// GOCACHE-cold work. The S3 cache (see infra.go's Build project) restores
// whatever the cache: paths: block names, so the caching only works if the
// directories Go actually writes are the directories the block lists. Pinning
// the dirs via env vars (rather than trusting the image's defaults) is what
// makes that agreement checkable: this test extracts each pinned path and
// requires a cache entry covering it. A mismatch has no other signal — the
// build is merely slow again, never red.
func TestBuildspec_GoCacheDirsArePinnedAndCached(t *testing.T) {
	raw, err := os.ReadFile("../buildspec.yml")
	if err != nil {
		t.Fatalf("read buildspec: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	dirs := map[string]string{}
	for _, name := range []string{"GOCACHE", "GOMODCACHE"} {
		for _, line := range lines {
			code, _, _ := strings.Cut(line, "#")
			if val, ok := strings.CutPrefix(strings.TrimSpace(code), name+":"); ok {
				dirs[name] = strings.Trim(strings.TrimSpace(val), `"`)
			}
		}
		if dirs[name] == "" {
			t.Errorf("buildspec.yml does not pin %s under env: variables:; "+
				"without the pin the cache: paths: block is guessing at the image default, "+
				"and a wrong guess makes every build GOCACHE-cold with no error anywhere", name)
		}
	}
	for name, dir := range dirs {
		want := dir + "/**/*"
		covered := false
		for _, line := range lines {
			code, _, _ := strings.Cut(line, "#")
			if strings.Contains(code, want) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s points at %s but no cache: paths: entry covers it (want %q); "+
				"the S3 cache restores only what the block names, so this dir is rebuilt from scratch every build", name, dir, want)
		}
	}
}

// The CI deploy retries a specific, self-clearing failure instead of dying
// to it, and does NOT retry anything else.
//
// ConcurrentBuildLimit stays -1 (infra.go, rosterbot-7p1i/rosterbot-ill):
// CodeBuild's limit THROTTLES the excess build rather than queueing it, so
// raising it back above -1 would silently DISCARD a build with no record, no
// log and no Pushover — worse than the race it would "fix". The real,
// remaining problem is that nothing serializes two builds that both reach
// `cdk deploy` on InfraStack within a few minutes of each other; the loser's
// CloudFormation UpdateStack fails while the stack is still ..._IN_PROGRESS
// from the winner, and that lock clears on its own within minutes. This test
// pins that the deploy line routes through infra/cdk_deploy_retry.sh (rather
// than a bare `cdk deploy`) and that the script names a bounded attempt
// count and the signature list the retry decision is keyed on — not just
// that SOME retrying happens, since a loop with no bound or an
// unconditional retry would silently mask a real deploy failure forever.
// The retry DECISION itself (does this specific canned output retry, does
// that one not) is exercised as running shell code, not grepped text, by
// TestIsConcurrentUpdateFailure_* and TestCdkDeployWithRetry_* in
// cdk_deploy_retry_test.go.
func TestBuildspec_RetriesCdkDeployOnConcurrentUpdate(t *testing.T) {
	buildspecRaw, err := os.ReadFile("../buildspec.yml")
	if err != nil {
		t.Fatalf("read buildspec: %v", err)
	}
	buildspec := string(buildspecRaw)

	deployLine := ""
	for _, line := range strings.Split(buildspec, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, "cdk deploy") {
			deployLine = strings.TrimSpace(code)
			break
		}
	}
	if deployLine == "" {
		t.Fatal("buildspec.yml contains no `cdk deploy` line; this test is asserting against nothing")
	}
	if !strings.Contains(deployLine, "cdk_deploy_retry.sh") {
		t.Fatalf("buildspec.yml's cdk deploy line does not route through cdk_deploy_retry.sh:\n\t%s\n"+
			"a bare `cdk deploy` dies unretried on the transient stack-lock failure two builds "+
			"landing minutes apart can both hit (rosterbot-udd)", deployLine)
	}

	scriptRaw, err := os.ReadFile("cdk_deploy_retry.sh")
	if err != nil {
		t.Fatalf("read cdk_deploy_retry.sh: %v", err)
	}
	script := string(scriptRaw)

	if !strings.Contains(script, `CDK_DEPLOY_RETRY_MAX_ATTEMPTS="${CDK_DEPLOY_RETRY_MAX_ATTEMPTS:-5}"`) {
		t.Error("cdk_deploy_retry.sh does not default CDK_DEPLOY_RETRY_MAX_ATTEMPTS to a bounded count; " +
			"an unbounded retry on a genuinely stuck stack would hang the build forever instead of failing loudly")
	}

	// Every signature the brief and the live incidents named must be present
	// literally — a rewrite that drops one narrows the retry silently, with
	// no test failure anywhere else to catch it.
	wantSignatures := []string{
		"UPDATE_IN_PROGRESS",
		"CREATE_IN_PROGRESS",
		"_IN_PROGRESS state",
		"is currently being updated",
		"OperationInProgressException",
		"ResourceConflictException",
		"cannot be updated",
		"can not be updated",
	}
	for _, sig := range wantSignatures {
		if !strings.Contains(script, sig) {
			t.Errorf("cdk_deploy_retry.sh's concurrent-update matcher does not mention %q; "+
				"the CDK CLI surfaces CloudFormation's message TEXT (which varies) far more often "+
				"than a stable exception class, so this signature must be matched literally", sig)
		}
	}

	// The non-retry path must still propagate a real failure. Grep-only
	// coverage of "the script exits nonzero somewhere" would pass on a
	// script that swallows the final error; require the specific pattern
	// that carries the deploy's own exit code out of the function.
	if !strings.Contains(script, "return \"$rc\"") {
		t.Error(`cdk_deploy_retry.sh does not appear to return the deploy's own exit code ($rc) on ` +
			"a non-matching failure or exhausted retries; masking it with a fixed exit status would " +
			"turn a real deploy failure into a misleading one")
	}
}

// A missing or incomplete cdk-outputs.json fails the build with an explicit,
// legible message naming what's missing — BEFORE the two python one-liners
// that read DashboardBucketName / DashboardCdnId out of it, whose only
// failure signal used to be a bare KeyError/FileNotFoundError traceback.
func TestBuildspec_FailsExplicitlyOnMissingOutputs(t *testing.T) {
	raw, err := os.ReadFile("../buildspec.yml")
	if err != nil {
		t.Fatalf("read buildspec: %v", err)
	}
	buildspec := string(raw)

	checkIdx := strings.Index(buildspec, "cdk-outputs.json: not found")
	if checkIdx == -1 {
		t.Fatal("buildspec.yml has no explicit check naming a missing cdk-outputs.json; " +
			"a missing file must fail with a message that says so, not a bare traceback")
	}
	if !strings.Contains(buildspec, "missing key(s)") {
		t.Fatal("buildspec.yml's explicit outputs check does not name a missing key; " +
			"DashboardBucketName/DashboardCdnId absent from InfraStack must fail with a message " +
			"naming which key is missing")
	}

	firstReadIdx := strings.Index(buildspec, `json.load(open("cdk-outputs.json"))["InfraStack"]["DashboardBucketName"]`)
	if firstReadIdx == -1 {
		t.Fatal(`buildspec.yml no longer reads DashboardBucketName the expected way; update this test's needle`)
	}
	if checkIdx >= firstReadIdx {
		t.Fatalf("the explicit missing-outputs check (offset %d) does not precede the first "+
			"unguarded python read of cdk-outputs.json (offset %d); a missing file/key must fail "+
			"at the explicit check, not three lines later as a bare traceback", checkIdx, firstReadIdx)
	}

	// The explicit check must actually run under CI (i.e. it isn't dead
	// commented-out prose) and must gate on both required keys.
	for _, key := range []string{"DashboardBucketName", "DashboardCdnId"} {
		found := false
		for _, line := range strings.Split(buildspec, "\n") {
			code, _, _ := strings.Cut(line, "#")
			if strings.Contains(code, key) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("buildspec.yml never references %q outside a comment; the explicit check must "+
				"name both required keys", key)
		}
	}
}
