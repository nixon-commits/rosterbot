package opsalert

import (
	"fmt"
	"time"
)

// The build-drift check answers a third question, distinct from both the task
// failure path and the heartbeat: is what is DEPLOYED the newest thing that was
// merged?
//
// Streak reacts to a run that finished. Overdue asserts that a run happened.
// Both are about runs, and a run executing a stale image satisfies them
// perfectly — the ledger records a healthy SUCCEEDED job and the heartbeat sees
// a fresh launch, while the code doing the work is days behind main. They answer
// "did the job run", never "is it the right image" (rosterbot-7p1i).
//
// The gap is not hypothetical. CodeBuild's ConcurrentBuildLimit THROTTLES an
// excess build rather than queueing it, and rejects it at the webhook BEFORE any
// build record exists — so the merge produces no build, no outcome, no event and
// nothing red anywhere. Two deploys were dropped that way in two days, and the
// only trace in the entire system was an HTTP 400 in a GitHub webhook delivery
// log nobody reads.
//
// That particular cause is fixed: infra/ now asserts the limit off with an
// explicit -1 instead of omitting the property, which CloudFormation does not
// treat as an assertion. This check exists for the general case. It compares
// main's HEAD against the newest commit that actually built, so a trigger that
// fails for a reason nobody predicted still surfaces — and the dropped builds it
// was written for failed for a reason everybody believed was already fixed,
// which is the honest argument for checking the outcome instead of enumerating
// the causes.

// BuildState is the pair of facts the check compares, gathered by the caller.
//
// BuiltSHA is the commit of the newest SUCCESSFUL build, and the caller is
// expected to read it from CodeBuild rather than accumulate it from the
// build-outcome events this same Lambda already receives. A check derived from
// the event stream it is auditing cannot see that stream fail — the same reason
// internal/roster's CheckILStarters joins against MLB's probable starters
// instead of asking Fantrax whether Fantrax believes a player is injured.
//
// An empty HeadSHA, a zero HeadTime or an empty BuiltSHA all mean "could not
// determine", never "none": see Check.
type BuildState struct {
	HeadSHA  string
	HeadTime time.Time
	BuiltSHA string
}

// Drift is a commit on main that never reached a successful build.
type Drift struct {
	HeadSHA  string
	BuiltSHA string
	Age      time.Duration
}

// MarkerKey identifies the drift alert state. ONE key, not one per commit: the
// token stored under it is what deduplicates (see NeedsAlert). A per-commit key
// would re-alert every time main advanced during a single outage, turning one
// broken trigger into one push per merge — the same trap MarkerKey avoids on the
// heartbeat path, arrived at from the opposite direction.
func (Drift) MarkerKey() string { return "builddrift" }

// AlertToken is what an alert records under MarkerKey: the newest commit that
// had successfully built at the moment it fired.
//
// The BUILT sha, not the head sha, and that choice is the entire dedup.
// BuiltSHA is precisely the value that does NOT move while the trigger is
// broken — main keeps advancing, the last good build does not — so one outage
// records one token and every later tick recognises it. Keying on the head
// instead would alert once per merge for as long as the outage lasted. When a
// build finally succeeds the token moves, and the next outage is correctly
// treated as news.
func (d Drift) AlertToken() string {
	if d.BuiltSHA == "" {
		return "none"
	}
	return d.BuiltSHA
}

// NeedsAlert reports whether this drift is worth a push, given prev — the token
// recorded by the last drift alert, empty when there has been none.
//
// The middle case is load-bearing and mirrors Missed.NeedsAlert: once we have
// already alerted and can no longer even see a successful build to date the
// outage from, staying quiet is right. That state is indistinguishable from a
// fresh outage with an equally invisible baseline, so treating it as news would
// push again on every tick, forever — which is how an operator learns to ignore
// the channel.
func (d Drift) NeedsAlert(prev string) bool {
	switch {
	case prev == "":
		return true
	case d.BuiltSHA == "":
		return false
	default:
		return d.BuiltSHA != prev
	}
}

// Check reports whether main carries a commit that never reached a successful
// build, judged against grace.
//
// It returns no finding in four distinct situations, and the differences matter:
//
//   - HeadSHA is empty. main could not be read, so the check knows nothing.
//     Alerting here would be alerting on our own blindness, and the caller has
//     already logged the failure that produced it.
//   - HeadTime is zero. The head commit could not be dated, so grace cannot be
//     applied. Reporting drift anyway would fire on every merge younger than a
//     build — crying wolf on healthy deployments — and a check that cries wolf
//     gets muted, which costs more than the narrow blindness of skipping it.
//     A GitHub response this check cannot parse is a code bug visible in logs,
//     not a silent data condition.
//   - Head and built agree. Production is current; this is the ordinary case.
//   - The head commit is younger than grace. Its build is legitimately still in
//     flight. grace must comfortably exceed a full build (~7 min here), because
//     the cost of setting it too low is a false alert on every deploy.
//
// BuiltSHA being empty is NOT one of them. No successful build on record while
// main plainly has commits is a real, reportable finding — it is what a project
// that has never once built successfully looks like.
//
// A build that FAILED rather than being dropped also surfaces here, and that is
// intended rather than duplicated noise: the build-outcome push says "this build
// broke", while this one says "main has been undeployed for six hours". The
// second does not follow from the first, since the next merge usually repairs it.
func Check(st BuildState, grace time.Duration, now time.Time) (Drift, bool) {
	if st.HeadSHA == "" || st.HeadTime.IsZero() {
		return Drift{}, false
	}
	if st.HeadSHA == st.BuiltSHA {
		return Drift{}, false
	}
	age := now.Sub(st.HeadTime)
	if age < grace {
		return Drift{}, false
	}
	return Drift{HeadSHA: st.HeadSHA, BuiltSHA: st.BuiltSHA, Age: age}, true
}

// FormatDrift renders the Pushover title and body for a drift finding.
func FormatDrift(d Drift) (title, body string) {
	title = "Rosterbot deploy behind main"
	built := "no successful build on record"
	if d.BuiltSHA != "" {
		built = "last built " + ShortSHA(d.BuiltSHA)
	}
	body = fmt.Sprintf("⚠️ main is at %s, %s · unbuilt for %s",
		ShortSHA(d.HeadSHA), built, humanDuration(d.Age))
	return title, body
}

// ShortSHA trims a git commit SHA to its 7-character prefix; shorter or empty
// values pass through unchanged.
func ShortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
