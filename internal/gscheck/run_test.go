package gscheck

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeGSClient is an in-test GSCheckClient. Per-team GS is looked up by teamID.
//
// failuresLeft makes the transient case expressible: a team with N remaining
// failures errors N times and then succeeds, which is the profile of the
// 2026-08-17 drop that rosterbot-xit was filed for.
type fakeGSClient struct {
	periods      []fantrax.ScoringPeriod
	teams        map[string]string
	min          *int
	max          *int
	gsByTeam     map[string]int
	failuresLeft map[string]int // teamID → remaining fetch failures
	alwaysFail   map[string]bool
	calls        map[string]int // teamID → GetTeamGS invocations
}

func (f *fakeGSClient) GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error) {
	return f.periods, f.teams, map[string]string{}, nil
}
func (f *fakeGSClient) GetGSLimits(string, fantrax.WeeklyPeriod) (*int, *int, error) {
	return f.min, f.max, nil
}
func (f *fakeGSClient) GetTeamGS(teamID, _ string, _ fantrax.ScoringPeriod, _, _ time.Time, _ int, _ bool) (int, []fantrax.PitcherStart, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[teamID]++
	if f.alwaysFail[teamID] {
		return 0, nil, errors.New("upstream boom")
	}
	if n := f.failuresLeft[teamID]; n > 0 {
		f.failuresLeft[teamID] = n - 1
		return 0, nil, errors.New("transient upstream boom")
	}
	return f.gsByTeam[teamID], nil, nil
}

// fastRetries removes the retry backoff so the retry paths run at test speed.
func fastRetries(t *testing.T) {
	t.Helper()
	orig := teamFetchBackoff
	teamFetchBackoff = 0
	t.Cleanup(func() { teamFetchBackoff = orig })
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
	_ = w.Close()
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
		if err := RunGSCheck(t.Context(), f, cfg); err != nil {
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
		if err := RunGSCheck(t.Context(), f, cfg); err != nil {
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
		if err := RunGSCheck(t.Context(), f, config.Config{TeamID: "t1", DryRun: true}); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})
	if !strings.Contains(out, "Nothing to check") {
		t.Errorf("expected nothing-to-check no-op; got:\n%s", out)
	}
}

// A per-team fetch failure must not vanish. Before rosterbot-xit the team was
// dropped from results with a lone WARNING and the run exited 0, so the ledger
// recorded SUCCESS and nothing downstream could tell a 9-team report from a
// 10-team one.
func TestRunGSCheck_SkippedTeamFailsTheRunAndNamesIt(t *testing.T) {
	fastRetries(t)
	cfg := config.Config{TeamID: "t1", DryRun: true}
	f := &fakeGSClient{
		periods:    []fantrax.ScoringPeriod{justEndedPeriod()},
		teams:      map[string]string{"over": "OverTeam", "gone": "GoneTeam", "ok": "OkTeam"},
		min:        ptrInt(7),
		max:        ptrInt(12),
		gsByTeam:   map[string]int{"over": 14, "ok": 9},
		alwaysFail: map[string]bool{"gone": true},
	}

	var err error
	out := captureStdout(t, func() { err = RunGSCheck(t.Context(), f, cfg) })

	if err == nil {
		t.Fatal("a skipped team must fail the run so the ledger records non-SUCCESS")
	}
	if !strings.Contains(err.Error(), "GoneTeam") {
		t.Errorf("error must name the skipped team, got %q", err)
	}
	if !strings.Contains(out, "INCOMPLETE") || !strings.Contains(out, "GoneTeam") {
		t.Errorf("report must name the skipped team; got:\n%s", out)
	}
	// The surviving teams are still evaluated — a partial report is still worth
	// delivering, it just must not read as a complete one.
	if !strings.Contains(out, "OVER MAX") {
		t.Errorf("surviving teams' violations must still be reported; got:\n%s", out)
	}
	// The dry-run branch prints the Pushover summary BuildReport produced.
	if !strings.Contains(out, "NOT CHECKED") {
		t.Errorf("Pushover summary must disclose the coverage gap; got:\n%s", out)
	}
	if got := f.calls["gone"]; got != teamFetchAttempts {
		t.Errorf("expected %d attempts on the failing team, got %d", teamFetchAttempts, got)
	}
}

// The transient case the bead was filed for: one failure, then success. The
// team must land in results and the run must stay green.
func TestRunGSCheck_RetryAbsorbsATransientFailure(t *testing.T) {
	fastRetries(t)
	f := &fakeGSClient{
		periods:      []fantrax.ScoringPeriod{justEndedPeriod()},
		teams:        map[string]string{"a": "Alpha", "b": "Beta"},
		min:          ptrInt(7),
		max:          ptrInt(12),
		gsByTeam:     map[string]int{"a": 14, "b": 9},
		failuresLeft: map[string]int{"a": 1},
	}

	var err error
	out := captureStdout(t, func() { err = RunGSCheck(t.Context(), f, config.Config{TeamID: "t1", DryRun: true}) })

	if err != nil {
		t.Fatalf("a retried-and-recovered team must not fail the run: %v", err)
	}
	if strings.Contains(out, "INCOMPLETE") || strings.Contains(out, "NOT CHECKED") {
		t.Errorf("recovered team must not be reported as skipped; got:\n%s", out)
	}
	if !strings.Contains(out, "Alpha: 14 GS") {
		t.Errorf("recovered team must appear in the tally; got:\n%s", out)
	}
	if got := f.calls["a"]; got != 2 {
		t.Errorf("expected 2 attempts (1 fail + 1 success), got %d", got)
	}
}

// A fully-successful run must behave exactly as before: nil error, and no
// coverage language anywhere in the output.
func TestRunGSCheck_AllTeamsSucceedIsUnchanged(t *testing.T) {
	f := &fakeGSClient{
		periods:  []fantrax.ScoringPeriod{justEndedPeriod()},
		teams:    map[string]string{"a": "Alpha", "b": "Beta"},
		min:      ptrInt(7),
		max:      ptrInt(12),
		gsByTeam: map[string]int{"a": 14, "b": 9},
	}
	var err error
	out := captureStdout(t, func() { err = RunGSCheck(t.Context(), f, config.Config{TeamID: "t1", DryRun: true}) })
	if err != nil {
		t.Fatalf("clean run must return nil: %v", err)
	}
	for _, bad := range []string{"INCOMPLETE", "NOT CHECKED", "could not be checked"} {
		if strings.Contains(out, bad) {
			t.Errorf("clean run must not mention %q; got:\n%s", bad, out)
		}
	}
}

// If every team fails there are no violations to report, so the run takes the
// "No violations found." exit — which must still be a failure, not a clean
// green run that happens to have found nothing.
func TestRunGSCheck_EveryTeamFailingIsNotACleanRun(t *testing.T) {
	fastRetries(t)
	f := &fakeGSClient{
		periods:    []fantrax.ScoringPeriod{justEndedPeriod()},
		teams:      map[string]string{"a": "Alpha"},
		min:        ptrInt(7),
		max:        ptrInt(12),
		alwaysFail: map[string]bool{"a": true},
	}
	var err error
	out := captureStdout(t, func() { err = RunGSCheck(t.Context(), f, config.Config{TeamID: "t1", DryRun: true}) })
	if err == nil {
		t.Fatal("a run that checked nothing must not return nil")
	}
	if !strings.Contains(out, "No violations found.") {
		t.Errorf("expected the no-violations exit; got:\n%s", out)
	}
}

// rosterbot-6zn: the tally banner used to print today's date as the walk end,
// claiming an 8-day walk over a 7-day period. It must agree with the period end
// on the "Checking:" line directly above it.
func TestRunGSCheck_TallyBannerEndsAtThePeriodEnd(t *testing.T) {
	period := justEndedPeriod()
	f := &fakeGSClient{
		periods:  []fantrax.ScoringPeriod{period},
		teams:    map[string]string{"a": "Alpha"},
		min:      ptrInt(7),
		max:      ptrInt(12),
		gsByTeam: map[string]int{"a": 9},
	}
	out := captureStdout(t, func() {
		if err := RunGSCheck(t.Context(), f, config.Config{TeamID: "t1", DryRun: true}); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})

	end := period.EndDate.Format("2006-01-02")
	want := "days " + period.StartDate.Format("2006-01-02") + " to " + end
	if !strings.Contains(out, want) {
		t.Errorf("tally banner must end at the period end (%q); got:\n%s", want, out)
	}
	if today := nowUTC().Format("2006-01-02"); today != end && strings.Contains(out, "to "+today+")") {
		t.Errorf("tally banner must not print today (%s) as the walk end; got:\n%s", today, out)
	}
}
