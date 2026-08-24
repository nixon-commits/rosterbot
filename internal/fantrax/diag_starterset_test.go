//go:build diag

package fantrax

import (
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestDiagWeeklyStarterSet reports, for each completed matchup week, how many
// DISTINCT pitchers took an active-slot start. Never runs in CI.
//
// This is the harness that settled rosterbot-tmb9. That bead asked whether the
// GS forecast should count only active-slot SPs rather than every rostered SP,
// on the strength of a large gap between the two forecast totals. The gap is
// real; it is not the answer.
//
// The answer is a cardinality argument that needs no status classification at
// all — and so is immune to rosterbot-x3xo, which showed the projection
// snapshots' status/was_started fields record roster state at snapshot-write
// time rather than deployment. This walk reads Fantrax's own frozen per-day
// rosters through GetTeamPitcherStarts, which gates on the active slot, so a
// row here means that pitcher occupied an active slot and started that day.
//
// If the weekly distinct-starter count materially exceeds the active P slot
// count, a forecast restricted to "SPs in an active slot today" cannot see the
// week's actual starters — it is the wrong cardinality before any question
// about per-pitcher conversion rates arises.
//
//	DIAG_STARTERSET_WEEKS=6 \
//	go test -tags diag -run TestDiagWeeklyStarterSet -v ./internal/fantrax/
func TestDiagWeeklyStarterSet(t *testing.T) {
	weeksStr := os.Getenv("DIAG_STARTERSET_WEEKS")
	if weeksStr == "" {
		t.Skip("set DIAG_STARTERSET_WEEKS=6 to run")
	}
	weeks, err := strconv.Atoi(weeksStr)
	if err != nil || weeks <= 0 {
		t.Fatalf("DIAG_STARTERSET_WEEKS must be a positive integer: %v", err)
	}

	// go test runs in the package dir; the session cookie cache
	// (.fantrax-cache) and the data cache (.cache) both live at the repo root.
	t.Chdir("../..")

	c, err := NewClient(os.Getenv("FANTRAX_LEAGUE_ID"), os.Getenv("FANTRAX_TEAM_ID"))
	if err != nil {
		t.Fatalf("fantrax client: %v", err)
	}
	c.SetCache(".cache")

	seasonStart, _, err := c.GetSeasonDateRange()
	if err != nil {
		t.Fatalf("season range: %v", err)
	}
	periods, _, _, err := c.GetScoringPeriodsAndTeams()
	if err != nil {
		t.Fatalf("scoring periods: %v", err)
	}
	teamID := os.Getenv("FANTRAX_TEAM_ID")
	today := time.Now().UTC().Truncate(24 * time.Hour)

	// Completed periods only, newest last.
	var done []ScoringPeriod
	for _, sp := range periods {
		if sp.EndDate.Before(today) {
			done = append(done, sp)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].Number < done[j].Number })
	if len(done) > weeks {
		done = done[len(done)-weeks:]
	}

	var totStarts, totDistinct int
	for _, sp := range done {
		starts, err := c.GetTeamPitcherStarts(teamID, sp.StartDate, sp.EndDate,
			seasonStart, ".cache", PastPeriodTTL)
		if err != nil {
			t.Fatalf("pitcher starts for period %d: %v", sp.Number, err)
		}
		distinct := map[string]int{}
		for _, s := range starts {
			distinct[s.PitcherName]++
		}
		names := make([]string, 0, len(distinct))
		for n := range distinct {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Logf("period %2d  %s..%s  starts=%2d  distinctPitchers=%2d  %v",
			sp.Number, sp.StartDate.Format("01-02"), sp.EndDate.Format("01-02"),
			len(starts), len(distinct), names)
		totStarts += len(starts)
		totDistinct += len(distinct)
	}
	if len(done) == 0 {
		t.Fatal("no completed periods found")
	}
	t.Logf("TOTALS over %d weeks: starts=%d  mean distinct pitchers/week=%.2f",
		len(done), totStarts, float64(totDistinct)/float64(len(done)))
	t.Logf("Active P slots in this league: 6. A weekly distinct-starter count above that " +
		"means an active-slot-only forecast cannot see the week's starters.")
}
