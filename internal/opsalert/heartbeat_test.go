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
