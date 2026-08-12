package opsalert

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The heartbeat check answers a question the failure path structurally cannot:
// did this job run at all?
//
// internal/opsalert's Streak works off ECS task events, so it only ever sees
// jobs that launched. A job whose EventBridge rule is disabled, whose cron was
// mistyped, or whose cluster is unreachable emits no event and writes no ledger
// record — there is nothing for Streak to be quiet or loud about. Silence is
// indistinguishable from health, which is the one failure mode an alerting
// system must not have.
//
// So the heartbeat inverts the direction: instead of reacting to a run, it
// asserts on a schedule that each job has a run recent enough to be alive.

// Schedule is one scheduled job's expected cadence, as handed to the notifier by
// the deployment that owns the actual EventBridge rules.
//
// MaxGapSeconds rather than a time.Duration because this crosses a wire: the CDK
// serializes the table into the Lambda's environment, so infra/ stays the single
// declaration of both the cron and the tolerance it implies. A second table
// hand-maintained in Go would drift from the schedules it claims to describe,
// and would do so silently — the drift only shows up as a missed alert.
type Schedule struct {
	Command       string `json:"command"`
	MaxGapSeconds int    `json:"max_gap_seconds"`
}

// MaxGap is how long this job may go without launching before it is overdue.
func (s Schedule) MaxGap() time.Duration {
	return time.Duration(s.MaxGapSeconds) * time.Second
}

// Missed is one job, for one tenant, that has not launched within its cadence.
//
// Last is the newest run seen for that (command, tenant) and is the zero time
// when the ledger held none within the horizon; LastID is that run's id. UserID
// is empty for a job with no per-tenant runs on record — every job before
// fan-out, and any job whose ledger history is untagged.
type Missed struct {
	Command string
	UserID  string
	LastID  string
	Last    time.Time
	Age     time.Duration
	MaxGap  time.Duration
}

// MarkerKey identifies this job's alert state, per tenant. One key per (job,
// tenant), not per run: deduplication compares the *token* stored under it (see
// NeedsAlert), because a per-run key stops working the moment the run it names
// falls out of the lookback horizon — which every long outage eventually does,
// and which would then read as a brand new outage and push again.
//
// The tenant belongs in the key for the mirror-image reason. Two tenants dark on
// the same job are two outages; sharing a key would let the first one's marker
// suppress the second's alert — the dedup working exactly as designed, against
// the wrong identity.
//
// The untagged key is deliberately byte-identical to the pre-fan-out one, so
// markers already written keep deduplicating across the deploy instead of
// re-alerting every outage in progress once. The separator is "@" rather than
// "/" because the marker store's keys are flat under this prefix; a "/" would
// silently nest them.
func (m Missed) MarkerKey() string {
	k := "heartbeat/" + slug(m.Command)
	if m.UserID != "" {
		k += "@" + slug(m.UserID)
	}
	return k
}

// AlertToken is what an alert records under MarkerKey: the newest run at the
// moment it fired, or "none" when the job had no run in the horizon at all.
func (m Missed) AlertToken() string {
	if m.LastID == "" {
		return "none"
	}
	return m.LastID
}

// NeedsAlert reports whether this overdue job is worth a push, given prev — the
// token recorded by the last alert for this job, empty when there has been none.
//
// The three cases, and why the middle one is load-bearing:
//
//   - No previous alert: this outage is news. Push.
//   - Previously alerted, and the newest run has since fallen out of the lookback
//     horizon (LastID == ""): the job is still dead and we can no longer see the
//     run that dates the outage. That state is indistinguishable from a *fresh*
//     outage whose last run is equally invisible, so a per-run key would push
//     again here — once per horizon, forever. Stay quiet: a job dead this long
//     has already been reported, and repeating it is how an operator learns to
//     ignore the channel.
//   - Previously alerted and the newest run has changed: the job ran in between,
//     so this is a second, distinct outage. Push.
func (m Missed) NeedsAlert(prev string) bool {
	switch {
	case prev == "":
		return true
	case m.LastID == "":
		return false
	default:
		return m.LastID != prev
	}
}

// runKey is what a run is judged under: one command, one tenant. Under
// per-tenant fan-out the command alone is not an identity — N tenants run the
// same command string — so keying on it makes "the Lineup job ran recently"
// true for everyone the moment any one tenant runs, and a tenant whose task
// stopped launching invisible. Permanently, and silently: exactly the blindness
// this whole check exists to remove, one level down.
type runKey struct{ command, userID string }

// Overdue returns the (schedule, tenant) pairs with no run within the
// schedule's own MaxGap, oldest first, judged against recs.
//
// Any status counts, including RUNNING. The question here is strictly "did the
// job launch?" — whether it then succeeded is Streak's job, and treating a
// failed run as a missed run would double-report every ordinary outage.
//
// **Which tenants a schedule is asserted on comes from the ledger, not from the
// schedule.** Schedule carries a cadence, not a tenant roster, and this package
// is a stdlib-only leaf with no way to enumerate users — so the honest set is
// the tenants seen running that command within the horizon. Two consequences
// are worth stating plainly:
//
//   - A command with no tenant-tagged records is asserted once, under the empty
//     tenant. That is every job before fan-out and is byte-identical to the old
//     behaviour, including the "no run on record" case that this check was built
//     for.
//   - A tenant that has *never* run within the horizon cannot be discovered here
//     and is not reported. Detecting an onboarded tenant whose job never launched
//     even once needs the tenant roster, which lives in the identity store; this
//     function's guarantee is that a tenant which was running and *stopped* is
//     always caught.
//
// At cutover a mixed ledger briefly holds both untagged and tagged records for
// one command, so the untagged tenant is reported overdue once and then falls
// out of the horizon. A one-off "this stopped running" — which is true — is the
// right side to err on for a channel whose only failure mode that matters is
// silence.
//
// recs need not be sorted or complete; the caller is responsible only for the
// horizon (see the doc on Horizon below). A record with an unparseable
// StartedAt is skipped for freshness rather than treated as the epoch, which
// would make every job look permanently overdue — but its tenant is still
// remembered, since forgetting it would hide a job that has demonstrably been
// launching.
func Overdue(recs []Record, scheds []Schedule, now time.Time) []Missed {
	newest := map[runKey]Record{}
	tenants := map[string][]string{} // command -> tenants seen, first-seen order
	seen := map[runKey]bool{}
	for _, r := range recs {
		k := runKey{r.Command, r.UserID}
		if !seen[k] {
			seen[k] = true
			tenants[r.Command] = append(tenants[r.Command], r.UserID)
		}
		t := r.Started()
		if t.IsZero() {
			continue
		}
		if cur, ok := newest[k]; !ok || t.After(cur.Started()) {
			newest[k] = r
		}
	}

	var out []Missed
	for _, s := range scheds {
		if s.MaxGapSeconds <= 0 {
			continue // no declared cadence; nothing to assert
		}
		users := tenants[s.Command]
		if len(users) == 0 {
			users = []string{""} // nothing has ever run it: one untagged assertion
		}
		for _, u := range users {
			m := Missed{Command: s.Command, UserID: u, MaxGap: s.MaxGap()}
			if r, ok := newest[runKey{s.Command, u}]; ok {
				m.LastID, m.Last = r.ID, r.Started()
				m.Age = now.Sub(m.Last)
				if m.Age <= m.MaxGap {
					continue
				}
			}
			out = append(out, m)
		}
	}

	// Oldest first: when several jobs go dark at once — a disabled cluster, a
	// paused schedule set — the one that has been dead longest is the one that
	// dates the outage.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Last.IsZero() != out[j].Last.IsZero() {
			return out[i].Last.IsZero() // "no run at all" sorts first
		}
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.Before(out[j].Last)
		}
		if out[i].Command != out[j].Command {
			return out[i].Command < out[j].Command
		}
		return out[i].UserID < out[j].UserID
	})
	return out
}

// Horizon is how far back a caller must read the ledger for Overdue's answer to
// be sound: one run older than the largest declared cadence, plus slack.
//
// This is what removes the "never ran" ambiguity. Read a fixed *count* of
// records and a weekly job's run can fall off the end behind a busy hourly one,
// making a perfectly healthy job look like it never ran. Read a *time* horizon
// derived from the cadences themselves and the absence of a record within a
// job's MaxGap is proof of absence, not of a short lookback.
func Horizon(scheds []Schedule, now time.Time) time.Time {
	var max time.Duration
	for _, s := range scheds {
		if g := s.MaxGap(); g > max {
			max = g
		}
	}
	if max == 0 {
		return now
	}
	return now.Add(-2 * max)
}

// FormatMissed renders the Pushover (title, body) for one overdue job.
func FormatMissed(m Missed) (title, body string) {
	job := JobName(m.Command)
	title = "Rosterbot: " + job + " has not run"

	who := TenantTag(m.UserID)

	if m.Last.IsZero() {
		return title, fmt.Sprintf("🕳 %s%s · no run on record\nexpected every %s",
			strings.TrimSpace(m.Command), who, humanDuration(m.MaxGap))
	}
	return title, fmt.Sprintf("🕳 %s%s · last ran %s ago\nexpected every %s · last run %s",
		strings.TrimSpace(m.Command), who, humanDuration(m.Age), humanDuration(m.MaxGap),
		m.Last.Format("2006-01-02 15:04Z"))
}

// humanDuration renders a coarse "3d" / "14h" / "45m" — the alert is read on a
// lock screen, where "77h13m4.2s" is worse than "3d".
func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return "<1m"
	}
}

// slug makes a command safe as one storage-key segment. Commands carry spaces
// and flag dashes ("projection-site --out report"); a "/" would silently create
// a nested prefix and break the flat listing the marker store assumes.
func slug(command string) string {
	var b strings.Builder
	for _, r := range command {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "job"
	}
	return b.String()
}
