//go:build diag

package fantrax

import (
	"fmt"
	"os"
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

// TestDiagPlayoffBracket prints the live playoff bracket through
// GetPlayoffBracket — the typed path every consumer uses — so a shape change
// on Fantrax's side shows up as a wrong round or a mis-typed slot here, not
// as a silent regular-season "season end" downstream. This is the harness
// that found rosterbot-0lyz: the standings SCHEDULE view stops at the last
// regular-season period, and only view=PLAYOFFS carries the bracket.
//
//	go test -tags diag -run TestDiagPlayoffBracket -v ./internal/fantrax/
//
// Needs FANTRAX_LEAGUE_ID / FANTRAX_TEAM_ID and a session (`set -a; source
// .env; set +a`); runs with the cache bypassed so it reports Fantrax, not disk.
func TestDiagPlayoffBracket(t *testing.T) {
	leagueID := os.Getenv("FANTRAX_LEAGUE_ID")
	if leagueID == "" {
		t.Skip("set FANTRAX_LEAGUE_ID (and a session) to run")
	}
	// go test runs in the package dir; the session cookie cache lives at the
	// repo root.
	t.Chdir("../..")
	c, err := NewClient(leagueID, os.Getenv("FANTRAX_TEAM_ID"))
	if err != nil {
		t.Fatalf("fantrax client: %v", err)
	}

	b, err := c.GetPlayoffBracket()
	if err != nil {
		t.Fatalf("GetPlayoffBracket: %v", err)
	}
	for _, r := range b.Rounds {
		t.Logf("round %d %q: weekly period %d, %s..%s", r.Number, r.Caption, r.ScoringPeriod,
			r.StartDate.Format("2006-01-02"), r.EndDate.Format("2006-01-02"))
		for _, m := range r.Matchups {
			t.Logf("    %-28s %6.0f  @  %-28s %6.0f  scored=%v",
				slotLabel(m.Away), m.AwayScore, slotLabel(m.Home), m.HomeScore, m.Scored)
		}
	}
	if b.Champion != nil {
		t.Logf("champion: %s (%s)", b.Champion.TeamName, b.Champion.TeamID)
	} else {
		t.Logf("champion undecided (raw label %q)", b.ChampionLabel)
	}
}

// slotLabel renders one side of a pairing: the team name, "bye", or "seed N".
func slotLabel(s auth_client.PlayoffSlot) string {
	switch s.Kind {
	case auth_client.PlayoffSlotBye:
		return "bye"
	case auth_client.PlayoffSlotSeed:
		return fmt.Sprintf("seed %d", s.Seed)
	default:
		return s.TeamName
	}
}

// TestDiagPlayoffGSLimits prints the live GS min/max for every playoff round,
// through the same GetGSLimits the lineup path and gs-check use, so a round
// Fantrax configures differently from a regular-season week (or not at all)
// is a printed fact rather than a silent gate change.
//
//	go test -tags diag -run TestDiagPlayoffGSLimits -v ./internal/fantrax/
func TestDiagPlayoffGSLimits(t *testing.T) {
	leagueID := os.Getenv("FANTRAX_LEAGUE_ID")
	if leagueID == "" {
		t.Skip("set FANTRAX_LEAGUE_ID (and a session) to run")
	}
	t.Chdir("../..")
	c, err := NewClient(leagueID, os.Getenv("FANTRAX_TEAM_ID"))
	if err != nil {
		t.Fatalf("fantrax client: %v", err)
	}
	b, err := c.GetPlayoffBracket()
	if err != nil {
		t.Fatalf("GetPlayoffBracket: %v", err)
	}
	for _, r := range b.Rounds {
		lo, hi, err := c.GetGSLimits(os.Getenv("FANTRAX_TEAM_ID"), WeeklyPeriod(r.ScoringPeriod))
		if err != nil {
			t.Logf("round %d (period %d): GetGSLimits error: %v", r.Number, r.ScoringPeriod, err)
			continue
		}
		t.Logf("round %d (period %d): GS min=%s max=%s", r.Number, r.ScoringPeriod, fmtIntPtr(lo), fmtIntPtr(hi))
	}
}

func fmtIntPtr(p *int) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprint(*p)
}
