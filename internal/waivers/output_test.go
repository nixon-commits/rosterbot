package waivers

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/statcast"
)

func TestToWireResult(t *testing.T) {
	r := Report{
		Total: 2,
		Top: []Candidate{
			{Name: "Hitter X", MLBTeam: "BAL", Position: "OF", Signal: statcast.SignalHot, ProjectedFPG: 4.2,
				Metrics: statcast.SignalMetrics{WOBA: 0.360, XwOBA: 0.400, Barrel: 14, HardHit: 48}, DropName: "Bench Y", Gap: 1.1},
			{Name: "Pitcher Z", MLBTeam: "NYY", Position: "SP", IsPitcher: true, Signal: statcast.SignalBuyLow,
				ProjectedFPG: 9.5, Metrics: statcast.SignalMetrics{ERA: 4.5, XERA: 3.2}},
		},
	}
	out := toWireResult(r)
	if out.Total != 2 || len(out.Picks) != 2 {
		t.Fatalf("counts: %+v", out)
	}
	if out.Picks[0].Rank != 1 || out.Picks[0].Signal != "HOT" || out.Picks[0].BarrelPct != 14 {
		t.Fatalf("pick0: %+v", out.Picks[0])
	}
	if out.Picks[1].Rank != 2 || !out.Picks[1].IsPitcher || out.Picks[1].Xera != 3.2 {
		t.Fatalf("pick1: %+v", out.Picks[1])
	}
}
