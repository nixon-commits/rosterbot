package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
)

// TestBuildSnapshot_MapsRichFields verifies the pure snapshot builder copies
// the projected value plus the extra look-back fields (slot, locked,
// eligibility, role, was-started) off the optimizer results.
func TestBuildSnapshot_MapsRichFields(t *testing.T) {
	slotName := map[string]string{"012": "OF", "017": "P"}
	dr := dateResult{
		date: time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC),
		hitterResult: optimizer.Result{
			Scored: []optimizer.ScoredPlayer{
				{
					Player: fantrax.Player{
						ID: "h1", Name: "Started OF", MLBTeam: "NYY",
						Positions: []string{"012", "014"}, RosterPosition: "012",
						Status: "Active", Locked: true,
					},
					ExpectedPts: 8.5, HasGame: true,
				},
				{
					Player: fantrax.Player{
						ID: "h2", Name: "Bench OF", MLBTeam: "BOS",
						Positions: []string{"012"}, RosterPosition: "", Status: "Reserve",
					},
					ExpectedPts: 4.0, HasGame: true,
				},
			},
		},
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{
				{
					Player: fantrax.Player{
						ID: "p1", Name: "Ace", MLBTeam: "LAD",
						Positions: []string{"015"}, PosShortNames: "SP",
						RosterPosition: "017", Status: "Active",
					},
					ExpectedPts: 16.0, HasGame: true, IsStarter: true,
				},
			},
		},
	}

	snap := buildSnapshot(dr, "depthcharts", slotName)

	if snap.Date != "2026-05-29" {
		t.Errorf("Date = %q, want 2026-05-29", snap.Date)
	}
	if snap.ProjectionSystem != "depthcharts" {
		t.Errorf("ProjectionSystem = %q, want depthcharts", snap.ProjectionSystem)
	}
	if len(snap.Hitters) != 2 {
		t.Fatalf("want 2 hitters, got %d", len(snap.Hitters))
	}
	h := snap.Hitters[0]
	if h.PlayerID != "h1" || h.ProjPtsPerGame != 8.5 || !h.WasStarted || h.Slot != "OF" || !h.Locked {
		t.Errorf("started hitter mapping wrong: %+v", h)
	}
	if len(h.Eligibility) != 2 || h.Eligibility[0] != "012" {
		t.Errorf("eligibility mapping wrong: %+v", h.Eligibility)
	}
	if bench := snap.Hitters[1]; bench.WasStarted || bench.Slot != "" {
		t.Errorf("bench hitter should not be started or hold a slot: %+v", bench)
	}
	if len(snap.Pitchers) != 1 {
		t.Fatalf("want 1 pitcher, got %d", len(snap.Pitchers))
	}
	p := snap.Pitchers[0]
	if p.Role != "SP" || !p.IsStarter || p.Slot != "P" || !p.IsPitcher {
		t.Errorf("pitcher mapping wrong: %+v", p)
	}
}
