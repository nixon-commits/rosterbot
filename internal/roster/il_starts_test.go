package roster

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

var testDate = time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)

func TestCheckILStarters_ILPitcherWithAnnouncedStart(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Jacob deGrom", MLBTeam: "TEX", Status: "Injured Reserve", IsInjured: true},
	}
	probables := map[string]string{"jacob degrom": "TEX"}

	starts, _ := CheckILStarters(players, probables, testDate)

	if len(starts) != 1 {
		t.Fatalf("expected 1 IL start, got %d", len(starts))
	}
	if starts[0].Player.ID != "p1" {
		t.Errorf("expected player p1, got %s", starts[0].Player.ID)
	}
	if !starts[0].StartDate.Equal(testDate) {
		t.Errorf("expected start date %s, got %s", testDate, starts[0].StartDate)
	}
}

// A normalized name is not unique — MLB has had two active Will Smiths. Requiring
// the club to agree is what keeps a namesake from being reported as our start.
func TestCheckILStarters_NamesakeOnAnotherClubIsNotAStart(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Will Smith", MLBTeam: "LAD", Status: "Injured Reserve", IsInjured: true},
	}
	probables := map[string]string{"will smith": "KC"}

	starts, mismatch := CheckILStarters(players, probables, testDate)

	if len(starts) != 0 {
		t.Fatalf("expected 0 IL starts for a namesake on another club, got %d", len(starts))
	}
	if len(mismatch) != 1 {
		t.Fatalf("expected the name hit to be reported as a mismatch, got %d", len(mismatch))
	}
}

// An active pitcher taking the ball is the normal case, not an alert.
func TestCheckILStarters_ActivePitcherStartingIsNotAnAlert(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Paul Skenes", MLBTeam: "PIT", Status: "Active"},
		{ID: "p2", Name: "Tarik Skubal", MLBTeam: "DET", Status: "Reserve"},
		{ID: "p3", Name: "Zack Wheeler", MLBTeam: "PHI", Status: "Minors"},
	}
	probables := map[string]string{
		"paul skenes":  "PIT",
		"tarik skubal": "DET",
		"zack wheeler": "PHI",
	}

	starts, _ := CheckILStarters(players, probables, testDate)

	if len(starts) != 0 {
		t.Fatalf("expected 0 IL starts for non-IL players, got %d", len(starts))
	}
}

func TestCheckILStarters_NoProbablesYieldsNoStarts(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Jacob deGrom", MLBTeam: "TEX", Status: "Injured Reserve", IsInjured: true},
	}

	starts, mismatch := CheckILStarters(players, map[string]string{}, testDate)

	if len(starts) != 0 || len(mismatch) != 0 {
		t.Fatalf("expected no starts and no mismatches, got %d/%d", len(starts), len(mismatch))
	}
}
