package opsalert

import "testing"

func rec(command, status string) Record {
	return Record{ID: command + "-" + status, Command: command, Status: status}
}

// hist builds a newest-first history for one command from a status string,
// where 'F' is FAILED, 'S' is SUCCESS and 'R' is RUNNING. Left-most is newest.
func hist(command, statuses string) []Record {
	var out []Record
	for _, c := range statuses {
		switch c {
		case 'F':
			out = append(out, rec(command, StatusFailed))
		case 'S':
			out = append(out, rec(command, StatusSuccess))
		case 'R':
			out = append(out, rec(command, StatusRunning))
		}
	}
	return out
}

func TestStreak(t *testing.T) {
	tests := []struct {
		name       string
		statuses   string
		wantKind   Kind
		wantStreak int
	}{
		{"first failure after a success", "FSSS", Started, 1},
		{"first failure ever, no history behind it", "F", Started, 1},
		{"second consecutive failure is silent", "FFSSS", None, 2},
		{"third consecutive failure escalates", "FFFSSS", Escalated, 3},
		{"fourth consecutive failure is silent again", "FFFFSSS", None, 4},
		{"eleventh consecutive failure is silent", "FFFFFFFFFFFSSS", None, 11},
		{"success after failures recovers", "SFFFSS", Recovered, 3},
		{"success after a success says nothing", "SSSS", None, 0},
		{"single success, no history", "S", None, 0},
		{"no history at all", "", None, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Streak(hist("optimize", tt.statuses), "optimize")
			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tt.wantKind)
			}
			if got.Streak != tt.wantStreak {
				t.Errorf("Streak = %d, want %d", got.Streak, tt.wantStreak)
			}
			if got.Command != "optimize" {
				t.Errorf("Command = %q, want %q", got.Command, "optimize")
			}
		})
	}
}

// RUNNING records are the start-of-run ledger write, later overwritten at the
// same key by the terminal write. An in-flight run must not break a streak.
func TestStreak_IgnoresRunningRecords(t *testing.T) {
	got := Streak(hist("optimize", "RFFRFSS"), "optimize")
	if got.Kind != Escalated || got.Streak != 3 {
		t.Fatalf("got Kind=%v Streak=%d, want Escalated/3", got.Kind, got.Streak)
	}
}

// Interleaved jobs share one ledger; each command owns its own streak.
func TestStreak_OtherCommandsDoNotContaminate(t *testing.T) {
	recs := []Record{
		rec("optimize", StatusFailed),
		rec("grade", StatusFailed),
		rec("shadow", StatusFailed),
		rec("optimize", StatusSuccess),
		rec("grade", StatusFailed),
	}
	got := Streak(recs, "optimize")
	if got.Kind != Started || got.Streak != 1 {
		t.Fatalf("optimize: got Kind=%v Streak=%d, want Started/1", got.Kind, got.Streak)
	}
	if g := Streak(recs, "grade"); g.Streak != 2 || g.Kind != None {
		t.Fatalf("grade: got Kind=%v Streak=%d, want None/2", g.Kind, g.Streak)
	}
}

// The failing record is carried through so the caller can quote its log tail.
func TestStreak_CarriesTheFailingRecord(t *testing.T) {
	exit := 1
	recs := []Record{
		{ID: "task-a", Command: "optimize", Status: StatusFailed, ExitCode: &exit, LogTail: "boom"},
		{ID: "task-b", Command: "optimize", Status: StatusSuccess},
	}
	got := Streak(recs, "optimize")
	if got.Failure == nil {
		t.Fatal("Failure is nil, want the newest failed record")
	}
	if got.Failure.ID != "task-a" || got.Failure.LogTail != "boom" {
		t.Errorf("Failure = %+v, want task-a/boom", *got.Failure)
	}
}

func TestStreak_RecoveredCarriesNoFailingRecord(t *testing.T) {
	got := Streak(hist("optimize", "SFF"), "optimize")
	if got.Kind != Recovered {
		t.Fatalf("Kind = %v, want Recovered", got.Kind)
	}
	if got.Failure != nil {
		t.Errorf("Failure = %+v, want nil on recovery", *got.Failure)
	}
}

// Replay of the real 2026-07-01 incident: 11 consecutive hourly optimize
// failures, one shadow failure, then recovery. The whole point of the streak
// design is that this produces exactly four pushes, not twelve.
func TestStreak_ReplaysTheRealIncident(t *testing.T) {
	const optimize = "optimize --matchup --archive-projections"

	// Ledger grows newest-first, so prepend as the outage progresses.
	var ledger []Record
	prepend := func(r Record) { ledger = append([]Record{r}, ledger...) }

	// Healthy history before the incident.
	for i := 0; i < 5; i++ {
		prepend(rec(optimize, StatusSuccess))
	}

	var pushes []Kind
	record := func(command string) {
		if v := Streak(ledger, command); v.Kind != None {
			pushes = append(pushes, v.Kind)
		}
	}

	// 17:00Z..23:00Z — seven hourly optimize failures.
	for i := 0; i < 7; i++ {
		prepend(rec(optimize, StatusFailed))
		record(optimize)
	}
	// 23:41Z — shadow fails once, its own streak.
	prepend(rec("shadow", StatusFailed))
	record("shadow")
	// 00:00Z..03:00Z — four more optimize failures.
	for i := 0; i < 4; i++ {
		prepend(rec(optimize, StatusFailed))
		record(optimize)
	}
	// 04:00Z — optimize recovers.
	prepend(rec(optimize, StatusSuccess))
	record(optimize)

	want := []Kind{Started, Escalated, Started, Recovered}
	if len(pushes) != len(want) {
		t.Fatalf("got %d pushes %v, want %d %v", len(pushes), pushes, len(want), want)
	}
	for i := range want {
		if pushes[i] != want[i] {
			t.Errorf("push %d = %v, want %v", i, pushes[i], want[i])
		}
	}

	// And the recovery reports the true streak length.
	if v := Streak(ledger, optimize); v.Streak != 11 {
		t.Errorf("recovered streak = %d, want 11", v.Streak)
	}
}
