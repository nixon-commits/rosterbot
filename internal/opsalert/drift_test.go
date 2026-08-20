package opsalert

import (
	"testing"
	"time"
)

var driftNow = time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)

// The two commits from the incident this check was written for
// (rosterbot-7p1i): bb1cc1e was merged while 134f326's build held the project's
// concurrency slot, so CodeBuild rejected its webhook and it never built. The
// tests replay that state rather than inventing one.
const (
	shaHead  = "bb1cc1e27e6bab69a754eb825d7351baa076f725"
	shaBuilt = "134f326990287688710862f0ea7da30c15f6233c"
)

func TestCheck_ReportsAMergeThatNeverBuilt(t *testing.T) {
	d, ok := Check(BuildState{
		HeadSHA:  shaHead,
		HeadTime: driftNow.Add(-6 * time.Hour),
		BuiltSHA: shaBuilt,
	}, 30*time.Minute, driftNow)
	if !ok {
		t.Fatal("want drift reported")
	}
	if d.HeadSHA != shaHead || d.BuiltSHA != shaBuilt {
		t.Errorf("shas: got head=%s built=%s", d.HeadSHA, d.BuiltSHA)
	}
	if d.Age != 6*time.Hour {
		t.Errorf("age: got %v want 6h", d.Age)
	}
}

func TestCheck_QuietWhenProductionIsCurrent(t *testing.T) {
	if _, ok := Check(BuildState{
		HeadSHA:  shaHead,
		HeadTime: driftNow.Add(-6 * time.Hour),
		BuiltSHA: shaHead,
	}, 30*time.Minute, driftNow); ok {
		t.Fatal("head == built must not report drift")
	}
}

// The grace window is what stops the check firing on every healthy deploy: a
// merge younger than a build has no successful build yet BY DESIGN.
func TestCheck_QuietWhileTheBuildIsStillPlausiblyRunning(t *testing.T) {
	for _, age := range []time.Duration{0, time.Minute, 29 * time.Minute} {
		if _, ok := Check(BuildState{
			HeadSHA:  shaHead,
			HeadTime: driftNow.Add(-age),
			BuiltSHA: shaBuilt,
		}, 30*time.Minute, driftNow); ok {
			t.Errorf("age %v is inside grace; must not report drift", age)
		}
	}
}

// Blindness is not a finding. Alerting when we could not read main would be
// alerting about the checker, not about the deployment.
func TestCheck_QuietWhenItCannotJudge(t *testing.T) {
	cases := map[string]BuildState{
		"unreadable head":   {HeadSHA: "", HeadTime: driftNow.Add(-6 * time.Hour), BuiltSHA: shaBuilt},
		"undateable head":   {HeadSHA: shaHead, BuiltSHA: shaBuilt},
		"neither available": {},
	}
	for name, st := range cases {
		if _, ok := Check(st, 30*time.Minute, driftNow); ok {
			t.Errorf("%s: must not report drift", name)
		}
	}
}

// A project with no successful build at all, while main plainly has commits, is
// a real finding — not the same as being unable to read anything.
func TestCheck_ReportsWhenNothingHasEverBuilt(t *testing.T) {
	d, ok := Check(BuildState{
		HeadSHA:  shaHead,
		HeadTime: driftNow.Add(-6 * time.Hour),
		BuiltSHA: "",
	}, 30*time.Minute, driftNow)
	if !ok {
		t.Fatal("want drift reported")
	}
	if d.AlertToken() != "none" {
		t.Errorf("token: got %q want %q", d.AlertToken(), "none")
	}
}

// The dedup contract. Keying the token on the BUILT sha is what makes one
// outage one alert: main advances during the outage, the last good build does
// not.
func TestDrift_AlertsOncePerOutageWhileMainKeepsAdvancing(t *testing.T) {
	var marker string

	fire := func(head string) bool {
		d, ok := Check(BuildState{
			HeadSHA:  head,
			HeadTime: driftNow.Add(-6 * time.Hour),
			BuiltSHA: shaBuilt,
		}, 30*time.Minute, driftNow)
		if !ok {
			t.Fatal("expected drift")
		}
		if !d.NeedsAlert(marker) {
			return false
		}
		marker = d.AlertToken()
		return true
	}

	if !fire(shaHead) {
		t.Fatal("first tick: a new outage is news and must push")
	}
	// Two more merges land while the trigger is still broken. Each moves main
	// but not the baseline, so neither is a second outage.
	if fire("cccccccdddddddddddddddddddddddddddddddda") {
		t.Error("second merge during the same outage must stay quiet")
	}
	if fire("eeeeeeeffffffffffffffffffffffffffffffffb") {
		t.Error("third merge during the same outage must stay quiet")
	}

	// A build finally succeeds at the new head, then the trigger breaks again.
	recovered := Drift{HeadSHA: "9999999", BuiltSHA: "eeeeeeeffffffffffffffffffffffffffffffffb"}
	if !recovered.NeedsAlert(marker) {
		t.Error("a distinct later outage must push again")
	}
}

// Mirrors Missed.NeedsAlert's middle case: once alerted, losing sight of the
// baseline must not read as a fresh outage on every tick, forever.
func TestDrift_StaysQuietWhenTheBaselineDisappearsAfterAlerting(t *testing.T) {
	d := Drift{HeadSHA: shaHead, BuiltSHA: ""}
	if !d.NeedsAlert("") {
		t.Error("no previous alert: must push")
	}
	if d.NeedsAlert(shaBuilt) {
		t.Error("already alerted and baseline now invisible: must stay quiet")
	}
}

func TestFormatDrift_NamesBothCommitsAndTheAge(t *testing.T) {
	_, body := FormatDrift(Drift{HeadSHA: shaHead, BuiltSHA: shaBuilt, Age: 6 * time.Hour})
	for _, want := range []string{"bb1cc1e", "134f326", "6h"} {
		if !contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}

	_, body = FormatDrift(Drift{HeadSHA: shaHead, Age: 2 * time.Hour})
	if !contains(body, "no successful build on record") {
		t.Errorf("empty built sha must be described, got %q", body)
	}
}
