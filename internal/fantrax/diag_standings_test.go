//go:build diag

package fantrax

import (
	"os"
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

// TestDiagStandings prints the fork's processed standings for the default
// (COMBINED) view, so the year-end page's standings source can be checked
// against Fantrax's own table before it is trusted.
//
//	go test -tags diag -run TestDiagStandings -v ./internal/fantrax/
func TestDiagStandings(t *testing.T) {
	leagueID := os.Getenv("FANTRAX_LEAGUE_ID")
	if leagueID == "" {
		t.Skip("set FANTRAX_LEAGUE_ID (and a session) to run")
	}
	t.Chdir("../..")
	c, err := NewClient(leagueID, os.Getenv("FANTRAX_TEAM_ID"))
	if err != nil {
		t.Fatalf("fantrax client: %v", err)
	}
	for _, view := range []auth_client.StandingsView{auth_client.StandingsViewCombined, auth_client.StandingsViewAll} {
		st, err := c.auth.GetStandings(auth_client.WithStandingsView(view))
		if err != nil {
			t.Logf("%s: error %v", view, err)
			continue
		}
		t.Logf("%s: %d teams, %d matchups, league %q", view, len(st.Teams), len(st.Matchups), st.LeagueName)
		for _, tm := range st.Teams {
			t.Logf("  #%-2d %-26s %2d-%2d-%d  pct %.3f  gb %4.1f  PF %7.1f  PA %7.1f  streak %q  div %q", tm.Rank, tm.Name, tm.Wins, tm.Losses, tm.Ties, tm.WinPct, tm.GamesBack, tm.PointsFor, tm.PointsAgainst, tm.Streak, tm.DivRecord)
		}
	}
}
