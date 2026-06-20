package prospects

import (
	"testing"
	"time"
)

func TestToWireResult(t *testing.T) {
	r := Report{
		Date: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC),
		Alerts: []ProspectAlert{
			{Kind: CalledUp, Priority: "high", PlayerName: "Jackson Holliday", MLBTeam: "BAL", Position: "SS", Detail: "promoted to MLB", Rank: 1},
		},
		Upgrades: []UpgradeSet{
			{Source: "FanGraphs", Candidates: []UpgradeCandidate{
				{Drop: RankedProspect{Name: "Old Guy", Rank: 80}, Add: RankedProspect{Name: "New Guy", Rank: 12, ETA: "2026"}, RankGap: 68, NearTerm: true},
			}},
		},
	}
	out := toWireResult(r)
	if len(out.Alerts) != 1 || out.Alerts[0].Name != "Jackson Holliday" || out.Alerts[0].Kind != "called-up" {
		t.Fatalf("alerts: %+v", out.Alerts)
	}
	if len(out.Upgrades) != 1 || out.Upgrades[0].Add != "New Guy" || out.Upgrades[0].RankGap != 68 || !out.Upgrades[0].NearTerm {
		t.Fatalf("upgrades: %+v", out.Upgrades)
	}
}
