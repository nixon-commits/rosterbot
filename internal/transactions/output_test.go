package transactions

import (
	"testing"
	"time"
)

func TestToWireResult(t *testing.T) {
	trades := []Trade{
		{
			ProcessedDate: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
			Sides: [2]TradeSide{
				{TeamName: "A", Players: []TradePlayer{{Name: "P1", Position: "OF", Value: 30}}},
				{TeamName: "B", Players: []TradePlayer{{Name: "P2", Position: "SP", Value: 25}}},
			},
		},
	}
	out := toWireResult(trades)
	if len(out.Trades) != 1 {
		t.Fatalf("count: %+v", out)
	}
	tr := out.Trades[0]
	if len(tr.Teams) != 2 || tr.Teams[0] != "A" || tr.Teams[1] != "B" {
		t.Fatalf("teams: %+v", tr.Teams)
	}
	if len(tr.Players) != 2 || tr.Players[0].FromTeam != "A" || tr.Players[1].Valuation != 25 {
		t.Fatalf("players: %+v", tr.Players)
	}
	if tr.ProcessedAt != "2026-06-20T12:00:00Z" {
		t.Fatalf("processed_at: %q", tr.ProcessedAt)
	}
}
