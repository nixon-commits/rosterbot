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
