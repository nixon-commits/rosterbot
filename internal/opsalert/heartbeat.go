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

// Missed is one job that has not launched within its cadence.
//
// Last is the newest run seen for the command and is the zero time when the
// ledger held none within the horizon; LastID is that run's id.
type Missed struct {
	Command string
	LastID  string
	Last    time.Time
	Age     time.Duration
	MaxGap  time.Duration
}

// MarkerKey identifies this job's alert state. One key per job, not per run:
// deduplication compares the *token* stored under it (see NeedsAlert), because a
// per-run key stops working the moment the run it names falls out of the
// lookback horizon — which every long outage eventually does, and which would
// then read as a brand new outage and push again.
func (m Missed) MarkerKey() string {
	return "heartbeat/" + slug(m.Command)
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

// Overdue returns the schedules with no run within their own MaxGap, oldest
// first, judged against recs.
//
// Any status counts, including RUNNING. The question here is strictly "did the
// job launch?" — whether it then succeeded is Streak's job, and treating a
// failed run as a missed run would double-report every ordinary outage.
//
// recs need not be sorted or complete; the caller is responsible only for the
// horizon (see the doc on Records below). A record with an unparseable
// StartedAt is skipped rather than treated as the epoch, which would make every
// job look permanently overdue.
func Overdue(recs []Record, scheds []Schedule, now time.Time) []Missed {
	newest := map[string]Record{}
	for _, r := range recs {
		t := r.Started()
		if t.IsZero() {
			continue
		}
		if cur, ok := newest[r.Command]; !ok || t.After(cur.Started()) {
			newest[r.Command] = r
		}
	}

	var out []Missed
	for _, s := range scheds {
		if s.MaxGapSeconds <= 0 {
			continue // no declared cadence; nothing to assert
		}
		m := Missed{Command: s.Command, MaxGap: s.MaxGap()}
		if r, ok := newest[s.Command]; ok {
			m.LastID, m.Last = r.ID, r.Started()
			m.Age = now.Sub(m.Last)
			if m.Age <= m.MaxGap {
				continue
			}
		}
		out = append(out, m)
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
		return out[i].Command < out[j].Command
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

	if m.Last.IsZero() {
		return title, fmt.Sprintf("🕳 %s · no run on record\nexpected every %s",
			strings.TrimSpace(m.Command), humanDuration(m.MaxGap))
	}
	return title, fmt.Sprintf("🕳 %s · last ran %s ago\nexpected every %s · last run %s",
		strings.TrimSpace(m.Command), humanDuration(m.Age), humanDuration(m.MaxGap),
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
