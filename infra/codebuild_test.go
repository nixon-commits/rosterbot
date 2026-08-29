package main

import (
	"strings"
	"testing"
)

// Pins for the Build project's latency configuration. The build's wall time is
// dominated by cache-cold work, measured on build 290 (2026-08-29, 6:20 total):
// 130s of `cdk synth` compiling aws-cdk-go from a cold GOCACHE, ~26s creating a
// CloudFormation changeset nothing consumes, and 16s cloning history nothing
// reads. The S3 cache and shallow clone below are what keep those out of every
// build; each is a single property that could be "tidied" away without any
// local signal — the build only gets slower, never red.

// buildProjectProps returns the Properties of the template's one CodeBuild
// project. Exactly one: zero means infraTemplate stopped synthesizing with
// enableBuild=true and every pin here is asserting against nothing.
func buildProjectProps(t *testing.T) map[string]any {
	t.Helper()
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)
	var props map[string]any
	n := 0
	for _, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::CodeBuild::Project" {
			continue
		}
		n++
		props, _ = res["Properties"].(map[string]any)
	}
	if n != 1 {
		t.Fatalf("template holds %d CodeBuild projects, want exactly 1; "+
			"if 0, infraTemplate no longer synthesizes with enableBuild=true and these pins see nothing", n)
	}
	return props
}

// The clone is shallow because nothing in the build reads git history: TAG
// comes from CODEBUILD_RESOLVED_SOURCE_VERSION, the Go builds run with
// -buildvcs=false, and the buildspec invokes no git command. Depth 1 trims the
// clone; unset depth re-fetches the full history on every push, forever.
func TestBuild_ClonesShallow(t *testing.T) {
	props := buildProjectProps(t)
	src, _ := props["Source"].(map[string]any)
	if depth, ok := src["GitCloneDepth"].(float64); !ok || depth != 1 {
		t.Errorf("Source.GitCloneDepth = %v, want 1 — nothing in the build reads git history, "+
			"and without a depth every push clones all of it", src["GitCloneDepth"])
	}
}

// The S3 cache is what carries GOCACHE/GOMODCACHE (see buildspec.yml's cache
// block) across build hosts. Without it every build recompiles the aws-cdk-go
// bindings and both lambda modules from scratch inside `cdk synth` — the
// single largest segment of the build, and one that no test or lint can see
// because a cache-cold build is merely slow, not wrong.
func TestBuild_CachesToS3(t *testing.T) {
	props := buildProjectProps(t)
	cache, _ := props["Cache"].(map[string]any)
	if cache["Type"] != "S3" {
		t.Fatalf("Cache.Type = %v, want S3 — the buildspec's cache paths are silently ignored without it", cache["Type"])
	}
	if cache["Location"] == nil {
		t.Error("Cache.Location is unset; an S3 cache with no location caches nowhere")
	}
}

// The cache bucket must expire its objects. A build cache is regenerable by
// definition, and an unbounded S3 prefix is the same slow leak the state
// bucket's lifecycle rules exist to stop (563 GB against ~1 GB live, measured
// 2026-08-18, before those rules landed).
func TestBuildCacheBucket_ExpiresItsObjects(t *testing.T) {
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)
	found := false
	for id, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::S3::Bucket" || !strings.Contains(id, "BuildCache") {
			continue
		}
		found = true
		props, _ := res["Properties"].(map[string]any)
		lc, _ := props["LifecycleConfiguration"].(map[string]any)
		rules, _ := lc["Rules"].([]any)
		expires := false
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			if d, ok := rule["ExpirationInDays"].(float64); ok && d > 0 {
				expires = true
			}
		}
		if !expires {
			t.Errorf("BuildCache bucket %s has no ExpirationInDays lifecycle rule; a build cache that never expires grows without bound", id)
		}
	}
	if !found {
		t.Fatal("no AWS::S3::Bucket with a BuildCache logical id in the template; the Build project's S3 cache needs a bucket this stack owns")
	}
}
