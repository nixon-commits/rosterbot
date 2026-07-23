package gscheck

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeGSClient is an in-test GSCheckClient. Per-team GS is looked up by teamID.
type fakeGSClient struct {
	periods  []fantrax.ScoringPeriod
	teams    map[string]string
	min      *int
	max      *int
	gsByTeam map[string]int
}

func (f *fakeGSClient) GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error) {
	return f.periods, f.teams, map[string]string{}, nil
}
func (f *fakeGSClient) GetGSLimits(string, fantrax.WeeklyPeriod) (*int, *int, error) {
	return f.min, f.max, nil
}
func (f *fakeGSClient) GetTeamGS(teamID, _ string, _ fantrax.ScoringPeriod, _, _ time.Time, _ int, _ bool) (int, []fantrax.PitcherStart, error) {
	return f.gsByTeam[teamID], nil, nil
}

func ptrInt(i int) *int { return &i }

// nowUTC mirrors RunGSCheck's own notion of "today" so period fixtures line up
// with the internal time.Now() call.
func nowUTC() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

// captureStdout runs fn with os.Stdout redirected and returns everything printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

// justEndedPeriod builds a period that ended yesterday (so FindJustEndedPeriod
// selects it) and is therefore complete (EndDate < today → min violations active).
func justEndedPeriod() fantrax.ScoringPeriod {
	today := nowUTC()
	return fantrax.ScoringPeriod{
		Number:    5,
		Caption:   "Scoring Period 5",
		StartDate: today.AddDate(0, 0, -7),
		EndDate:   today.AddDate(0, 0, -1),
	}
}

func TestRunGSCheck_ViolationsAndCleanTallies(t *testing.T) {
	cfg := config.Config{TeamID: "t1", DryRun: true}
	f := &fakeGSClient{
		periods:  []fantrax.ScoringPeriod{justEndedPeriod()},
		teams:    map[string]string{"over": "OverTeam", "under": "UnderTeam", "ok": "OkTeam"},
		min:      ptrInt(7),
		max:      ptrInt(12),
		gsByTeam: map[string]int{"over": 14, "under": 5, "ok": 9},
	}

	out := captureStdout(t, func() {
		if err := RunGSCheck(f, cfg); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})

	if !strings.Contains(out, "OverTeam") || !strings.Contains(out, "OVER MAX") {
		t.Errorf("expected OverTeam over-max flag; got:\n%s", out)
	}
	if !strings.Contains(out, "UnderTeam") || !strings.Contains(out, "UNDER MIN") {
		t.Errorf("expected UnderTeam under-min flag; got:\n%s", out)
	}
	if strings.Contains(out, "OkTeam: 9 GS ***") {
		t.Errorf("OkTeam (9, within 7..12) must not be flagged; got:\n%s", out)
	}
}

// A correct per-team GS tally at/above min must NOT false-fire "UNDER MIN".
// This is the previously-uncatchable regression class (rosterbot-uv6/wd5): the
// GetTeamGS daily walk once undercounted every team to ~one day's GS and fired
// a whole-league under-min alert. With the seam, a correct tally is testable.
func TestRunGSCheck_CorrectTallyNoFalseUnderMin(t *testing.T) {
	cfg := config.Config{TeamID: "t1", DryRun: true}
	f := &fakeGSClient{
		periods:  []fantrax.ScoringPeriod{justEndedPeriod()},
		teams:    map[string]string{"a": "Alpha", "b": "Beta"},
		min:      ptrInt(7),
		max:      ptrInt(12),
		gsByTeam: map[string]int{"a": 8, "b": 10}, // both ≥ min, ≤ max
	}
	out := captureStdout(t, func() {
		if err := RunGSCheck(f, cfg); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})
	if !strings.Contains(out, "No violations found.") {
		t.Errorf("expected no violations; got:\n%s", out)
	}
	if strings.Contains(out, "UNDER MIN") {
		t.Errorf("false UNDER MIN on a correct tally; got:\n%s", out)
	}
}

func TestRunGSCheck_NotEndOfPeriod(t *testing.T) {
	today := nowUTC()
	// EndDate is days out → no just-ended period → clean no-op.
	periods := []fantrax.ScoringPeriod{{
		Number:    5,
		Caption:   "Scoring Period 5",
		StartDate: today.AddDate(0, 0, -4),
		EndDate:   today.AddDate(0, 0, 3),
	}}
	f := &fakeGSClient{periods: periods, teams: map[string]string{"a": "Alpha"}, min: ptrInt(7), max: ptrInt(12)}
	out := captureStdout(t, func() {
		if err := RunGSCheck(f, config.Config{TeamID: "t1", DryRun: true}); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})
	if !strings.Contains(out, "Nothing to check") {
		t.Errorf("expected nothing-to-check no-op; got:\n%s", out)
	}
}
