package opsalert

import (
	"strings"
	"testing"
	"time"
)

var hbNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

// ran builds a ledger record for command, ago before hbNow.
func ran(id, command string, ago time.Duration, status string) Record {
	return Record{
		ID:        id,
		Command:   command,
		Status:    status,
		StartedAt: hbNow.Add(-ago).Format(time.RFC3339),
	}
}

// ranU is ran for one tenant: the same command string, run by user.
func ranU(id, command, user string, ago time.Duration, status string) Record {
	r := ran(id, command, ago, status)
	r.UserID = user
	return r
}

func hourly(command string) Schedule  { return Schedule{Command: command, MaxGapSeconds: 13 * 3600} }
func daily(command string) Schedule   { return Schedule{Command: command, MaxGapSeconds: 26 * 3600} }
func weeklyS(command string) Schedule { return Schedule{Command: command, MaxGapSeconds: 8 * 86400} }

func TestOverdue(t *testing.T) {
	tests := []struct {
		name  string
		recs  []Record
		sched []Schedule
		want  []string // commands, in expected order
	}{
		{
			name:  "a job inside its cadence is not overdue",
			recs:  []Record{ran("a", "grade", 3*time.Hour, StatusSuccess)},
			sched: []Schedule{daily("grade")},
		},
		{
			name:  "a job past its cadence is overdue",
			recs:  []Record{ran("a", "grade", 30*time.Hour, StatusSuccess)},
			sched: []Schedule{daily("grade")},
			want:  []string{"grade"},
		},
		{
			// The whole reason this check exists: nothing launched, so no ECS
			// event fired and Streak saw nothing to be quiet about.
			name:  "a job with no record at all is overdue",
			recs:  nil,
			sched: []Schedule{daily("grade")},
			want:  []string{"grade"},
		},
		{
			// A failed run still launched. Reporting it here would double-report
			// every ordinary outage that Streak already covers.
			name:  "a recent FAILED run counts as having launched",
			recs:  []Record{ran("a", "grade", 2*time.Hour, StatusFailed)},
			sched: []Schedule{daily("grade")},
		},
		{
			// Same reasoning one step further: a run in flight is a launch.
			name:  "a recent RUNNING run counts as having launched",
			recs:  []Record{ran("a", "grade", 2*time.Hour, StatusRunning)},
			sched: []Schedule{daily("grade")},
		},
		{
			name: "the newest run wins regardless of input order",
			recs: []Record{
				ran("old", "grade", 40*time.Hour, StatusSuccess),
				ran("new", "grade", 2*time.Hour, StatusSuccess),
				ran("mid", "grade", 20*time.Hour, StatusSuccess),
			},
			sched: []Schedule{daily("grade")},
		},
		{
			name: "each job is judged against its own cadence",
			recs: []Record{
				ran("a", "optimize", 20*time.Hour, StatusSuccess),   // hourly-ish: overdue
				ran("b", "grade", 20*time.Hour, StatusSuccess),      // daily: fine
				ran("c", "recap-site", 20*time.Hour, StatusSuccess), // weekly: fine
			},
			sched: []Schedule{hourly("optimize"), daily("grade"), weeklyS("recap-site")},
			want:  []string{"optimize"},
		},
		{
			// A weekly job six days quiet is healthy. Judging it by a shared
			// threshold instead of its own would cry wolf every single week.
			name:  "a weekly job six days quiet is not overdue",
			recs:  []Record{ran("a", "recap-site", 6*24*time.Hour, StatusSuccess)},
			sched: []Schedule{weeklyS("recap-site")},
		},
		{
			name: "oldest first, with never-run jobs ahead of merely stale ones",
			recs: []Record{
				ran("a", "grade", 40*time.Hour, StatusSuccess),
				ran("b", "waivers", 60*time.Hour, StatusSuccess),
			},
			sched: []Schedule{daily("grade"), daily("waivers"), daily("claims")},
			want:  []string{"claims", "waivers", "grade"},
		},
		{
			// An unparseable timestamp must not read as the epoch, which would
			// make an otherwise-healthy job look decades overdue.
			name:  "a record with an unusable timestamp is ignored, not treated as ancient",
			recs:  []Record{{ID: "a", Command: "grade", Status: StatusSuccess, StartedAt: "not a time"}},
			sched: []Schedule{daily("grade")},
			want:  []string{"grade"},
		},
		{
			name:  "a schedule with no declared cadence is never asserted on",
			recs:  nil,
			sched: []Schedule{{Command: "grade"}},
		},
		{
			// Exactly at the boundary is still healthy; the alert fires past it.
			name:  "a job exactly at its cadence is not yet overdue",
			recs:  []Record{ran("a", "grade", 26*time.Hour, StatusSuccess)},
			sched: []Schedule{daily("grade")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Overdue(tt.recs, tt.sched, hbNow)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d overdue %v, want %d %v", len(got), commands(got), len(tt.want), tt.want)
			}
			for i, c := range tt.want {
				if got[i].Command != c {
					t.Errorf("overdue[%d] = %q, want %q", i, got[i].Command, c)
				}
			}
		})
	}
}

func commands(ms []Missed) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Command
	}
	return out
}

// Under fan-out N tenants run the same command string, so "the Lineup job ran
// recently" becomes true for every tenant the moment *any* tenant runs. A
// tenant whose task stopped launching is then invisible — permanently, and
// silently, which is the exact blindness this whole check exists to remove.
func TestOverdue_ReportsTheDarkTenantOnly(t *testing.T) {
	recs := []Record{
		ranU("a1", "optimize", "a", 1*time.Hour, StatusSuccess),
		ranU("b1", "optimize", "b", 1*time.Hour, StatusSuccess),
		ranU("c1", "optimize", "c", 30*time.Hour, StatusSuccess), // dark since yesterday
	}
	got := Overdue(recs, []Schedule{hourly("optimize")}, hbNow)
	if len(got) != 1 {
		t.Fatalf("got %d overdue %v, want 1 (tenant c)", len(got), tenantKeys(got))
	}
	if got[0].Command != "optimize" || got[0].UserID != "c" {
		t.Errorf("overdue = %q/%q, want optimize/c", got[0].Command, got[0].UserID)
	}
	if got[0].LastID != "c1" {
		t.Errorf("LastID = %q, want c1 (tenant c's own newest run)", got[0].LastID)
	}
}

// Every tenant dark is every tenant reported, oldest first — the whole-cluster
// outage must not collapse into a single line.
func TestOverdue_ReportsEveryDarkTenant(t *testing.T) {
	recs := []Record{
		ranU("a1", "optimize", "a", 30*time.Hour, StatusSuccess),
		ranU("b1", "optimize", "b", 50*time.Hour, StatusSuccess),
	}
	got := Overdue(recs, []Schedule{hourly("optimize")}, hbNow)
	if len(got) != 2 || got[0].UserID != "b" || got[1].UserID != "a" {
		t.Fatalf("got %v, want [b a] (oldest first)", tenantKeys(got))
	}
}

// The tenant a job is judged for comes from the ledger, so a command whose
// records are all untagged behaves exactly as it did before fan-out: one
// judgement, empty tenant.
func TestOverdue_UntaggedRecordsJudgeOneUntaggedTenant(t *testing.T) {
	got := Overdue([]Record{ran("a", "grade", 30*time.Hour, StatusSuccess)},
		[]Schedule{daily("grade")}, hbNow)
	if len(got) != 1 {
		t.Fatalf("got %d overdue, want 1", len(got))
	}
	if got[0].UserID != "" {
		t.Errorf("UserID = %q, want empty", got[0].UserID)
	}
}

// A schedule whose command has no records at all still reports once, with an
// empty tenant — "nothing launched" is the case with no tenant to name, and it
// is the case the check was built for.
func TestOverdue_NoRecordsAtAllReportsTheUntaggedJob(t *testing.T) {
	got := Overdue(nil, []Schedule{daily("grade")}, hbNow)
	if len(got) != 1 || got[0].UserID != "" || !got[0].Last.IsZero() {
		t.Fatalf("got %v, want one untagged never-ran entry", tenantKeys(got))
	}
}

// A tenant whose only record carries an unusable timestamp must still be named.
// Skipping it for freshness is right; forgetting the tenant exists would hide a
// job that has demonstrably been launching.
func TestOverdue_TenantWithOnlyAnUnusableTimestampIsStillReported(t *testing.T) {
	recs := []Record{
		ranU("a1", "optimize", "a", 1*time.Hour, StatusSuccess),
		{ID: "b1", Command: "optimize", UserID: "b", Status: StatusSuccess, StartedAt: "not a time"},
	}
	got := Overdue(recs, []Schedule{hourly("optimize")}, hbNow)
	if len(got) != 1 || got[0].UserID != "b" {
		t.Fatalf("got %v, want [optimize/b]", tenantKeys(got))
	}
}

func tenantKeys(ms []Missed) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Command + "/" + m.UserID
	}
	return out
}

// A command carries spaces and dashes; a "/" in the key would silently nest the
// marker under a sub-prefix the flat listing never sees.
func TestMissedMarkerKey_IsOneFlatKeySegment(t *testing.T) {
	m := Missed{Command: "projection-site --out report/x", LastID: "abc"}
	got := m.MarkerKey()
	want := "heartbeat/projection-site_--out_report_x"
	if got != want {
		t.Errorf("MarkerKey() = %q, want %q", got, want)
	}
}

// The key must not vary with the run it names — the run is what falls out of the
// horizon, and a key that moved with it would re-alert once per horizon forever.
func TestMissedMarkerKey_DoesNotVaryWithTheRun(t *testing.T) {
	a := Missed{Command: "grade", LastID: "r1"}
	b := Missed{Command: "grade"} // same job, last run no longer visible
	if a.MarkerKey() != b.MarkerKey() {
		t.Errorf("key moved with the run: %q vs %q", a.MarkerKey(), b.MarkerKey())
	}
}

// One marker key per (job, tenant). Sharing it would let the first tenant's
// alert suppress every other tenant's for the same outage — the dedup working
// exactly as designed, against the wrong identity.
func TestMissedMarkerKey_IsPerTenant(t *testing.T) {
	a := Missed{Command: "optimize", UserID: "u1"}
	b := Missed{Command: "optimize", UserID: "u2"}
	if a.MarkerKey() == b.MarkerKey() {
		t.Errorf("two tenants share marker key %q", a.MarkerKey())
	}
	// Still one flat segment under the prefix: the marker store keys are flat
	// and a "/" would silently nest them.
	if got := strings.Count(a.MarkerKey(), "/"); got != 1 {
		t.Errorf("MarkerKey() = %q has %d slashes, want 1", a.MarkerKey(), got)
	}
	// The untagged key is byte-identical to the pre-fan-out one, so markers
	// already sitting in S3 keep deduplicating across the deploy rather than
	// re-alerting every ongoing outage once.
	if got := (Missed{Command: "grade"}).MarkerKey(); got != "heartbeat/grade" {
		t.Errorf("untagged MarkerKey() = %q, want %q", got, "heartbeat/grade")
	}
	// A tenant id is opaque and may carry anything; it must be slugged like the
	// command is.
	if got := (Missed{Command: "grade", UserID: "a/b c"}).MarkerKey(); strings.Count(got, "/") != 1 {
		t.Errorf("MarkerKey() = %q, want the tenant id slugged into one segment", got)
	}
}

func TestMissedNeedsAlert(t *testing.T) {
	tests := []struct {
		name   string
		lastID string
		prev   string
		want   bool
	}{
		{"first alert for this job", "r1", "", true},
		{"first alert, job never ran at all", "", "", true},
		{"same outage, same last run", "r1", "r1", false},
		{"same outage, last run has aged out of the horizon", "", "r1", false},
		{"a job that never ran, re-checked", "", "none", false},
		{"the job ran, then went dark again", "r2", "r1", true},
		{"a job that never ran has now run and gone dark", "r1", "none", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Missed{Command: "grade", LastID: tt.lastID}
			if got := m.NeedsAlert(tt.prev); got != tt.want {
				t.Errorf("NeedsAlert(%q) with LastID %q = %v, want %v",
					tt.prev, tt.lastID, got, tt.want)
			}
		})
	}
}

func TestMissedAlertToken(t *testing.T) {
	if got := (Missed{LastID: "r1"}).AlertToken(); got != "r1" {
		t.Errorf("AlertToken() = %q, want %q", got, "r1")
	}
	if got := (Missed{}).AlertToken(); got != "none" {
		t.Errorf("AlertToken() = %q, want %q", got, "none")
	}
}

// Horizon must reach past the longest cadence, or a weekly job's healthy run
// falls outside the lookback and reads as "never ran".
func TestHorizon_ReachesPastTheLongestCadence(t *testing.T) {
	scheds := []Schedule{hourly("optimize"), daily("grade"), weeklyS("recap-site")}
	h := Horizon(scheds, hbNow)
	if !h.Before(hbNow.Add(-8 * 24 * time.Hour)) {
		t.Errorf("horizon %v does not reach past the 8d weekly cadence", h)
	}
}

func TestHorizon_NoSchedules(t *testing.T) {
	if got := Horizon(nil, hbNow); !got.Equal(hbNow) {
		t.Errorf("Horizon(nil) = %v, want %v", got, hbNow)
	}
}

func TestFormatMissed(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		title, body := FormatMissed(Missed{
			Command: "grade", LastID: "x",
			Last:   hbNow.Add(-40 * time.Hour),
			Age:    40 * time.Hour,
			MaxGap: 26 * time.Hour,
		})
		if want := "Rosterbot: grade has not run"; title != want {
			t.Errorf("title = %q, want %q", title, want)
		}
		for _, want := range []string{"grade", "40h ago", "26h", "2026-08-01 20:00Z"} {
			if !contains(body, want) {
				t.Errorf("body %q missing %q", body, want)
			}
		}
	})

	t.Run("no run on record", func(t *testing.T) {
		_, body := FormatMissed(Missed{Command: "grade", MaxGap: 26 * time.Hour})
		if !contains(body, "no run on record") {
			t.Errorf("body = %q", body)
		}
	})

	// Two tenants dark on the same job produce two alerts whose only difference
	// is whose job it is. If that is not in the message, the operator reads the
	// second push as a duplicate of the first.
	t.Run("names the tenant", func(t *testing.T) {
		_, body := FormatMissed(Missed{Command: "grade", UserID: "u1abcdef", MaxGap: 26 * time.Hour})
		if !contains(body, "u1abcdef") {
			t.Errorf("body = %q, want it to name tenant u1abcdef", body)
		}
	})

	// An untagged job's message is byte-identical to the pre-fan-out one.
	t.Run("says nothing about a tenant when there is none", func(t *testing.T) {
		_, body := FormatMissed(Missed{Command: "grade", MaxGap: 26 * time.Hour})
		if contains(body, "user") {
			t.Errorf("body = %q, want no tenant mention", body)
		}
	})
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "<1m"},
		{45 * time.Minute, "45m"},
		{14 * time.Hour, "14h"},
		{47 * time.Hour, "47h"},
		{77*time.Hour + 13*time.Minute, "3d"},
	}
	for _, tt := range tests {
		if got := humanDuration(tt.d); got != tt.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
