//go:build diag

package fantrax

import (
	"os"
	"testing"
	"time"
)

// TestDiagGSWalkIncludesToday runs the real per-day active-slot GS walk against
// live Fantrax for a window ending on a chosen "today", and prints the per-day
// deltas. Never runs in CI; this is the harness that reproduced rosterbot-cg8l
// and verifies the fix against production data.
//
// The bug: the GS budget counted today's starts from the LIVE roster (active +
// on a locked team + today's probable), so a pitcher rotated back to Reserve
// after his game silently gave his start back. On Wed 2026-08-19 production
// peaked at the correct 5/12 used at 00:00Z and fell to 3/12 an hour later.
//
// Point DIAG_GS_TODAY at that Wednesday and the walk must report 5 — 1 from
// Mon-Tue plus Wednesday's four active-slot starts — no matter what the roster
// looks like now, days later:
//
//	DIAG_GS_TODAY=2026-08-19 DIAG_GS_WEEK_START=2026-08-17 \
//	go test -tags diag -run TestDiagGSWalkIncludesToday -v ./internal/fantrax/
func TestDiagGSWalkIncludesToday(t *testing.T) {
	todayStr := os.Getenv("DIAG_GS_TODAY")
	if todayStr == "" {
		t.Skip("set DIAG_GS_TODAY=YYYY-MM-DD to run")
	}
	today, err := time.Parse("2006-01-02", todayStr)
	if err != nil {
		t.Fatalf("DIAG_GS_TODAY must be YYYY-MM-DD: %v", err)
	}
	weekStart, err := time.Parse("2006-01-02", os.Getenv("DIAG_GS_WEEK_START"))
	if err != nil {
		t.Fatalf("DIAG_GS_WEEK_START must be YYYY-MM-DD: %v", err)
	}

	// go test runs in the package dir; the session cookie cache (.fantrax-cache)
	// and the data cache (.cache) both live at the repo root.
	if err := os.Chdir("../.."); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}

	c, err := NewClient(os.Getenv("FANTRAX_LEAGUE_ID"), os.Getenv("FANTRAX_TEAM_ID"))
	if err != nil {
		t.Fatalf("fantrax client: %v", err)
	}
	c.SetCache(".cache")

	seasonStart, _, err := c.GetSeasonDateRange()
	if err != nil {
		t.Fatalf("season range: %v", err)
	}

	teamID := os.Getenv("FANTRAX_TEAM_ID")
	sp := ScoringPeriod{StartDate: weekStart, EndDate: today}

	// verbose=true prints each day's per-pitcher delta, which is the evidence.
	total, _, err := c.GetTeamGS(teamID, "", sp, seasonStart, today, 0, true)
	if err != nil {
		t.Fatalf("GetTeamGS: %v", err)
	}
	t.Logf("walk %s..%s (today=%s) => %d active-slot GS",
		weekStart.Format("2006-01-02"), today.Format("2006-01-02"), todayStr, total)

	// The same window ending yesterday is what the old code walked; the
	// difference between the two is exactly what used to be re-derived from the
	// live roster every run.
	prev := today.AddDate(0, 0, -1)
	throughYesterday, _, err := c.GetTeamGS(teamID, "",
		ScoringPeriod{StartDate: weekStart, EndDate: prev}, seasonStart, prev, 0, false)
	if err != nil {
		t.Fatalf("GetTeamGS (through yesterday): %v", err)
	}
	t.Logf("walk %s..%s => %d; today alone contributed %d start(s)",
		weekStart.Format("2006-01-02"), prev.Format("2006-01-02"),
		throughYesterday, total-throughYesterday)
}
