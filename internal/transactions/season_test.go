package transactions

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"
)

// SeasonTrades is the exported grouping the year-end page uses: rows sharing
// a TradeGroupID become one Trade with one side per receiving team.
func TestSeasonTrades_GroupsRowsIntoSides(t *testing.T) {
	d := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{TradeGroupID: "g1", ToTeamName: "Alpha", FromTeamName: "Bravo", PlayerName: "Some Star", PlayerPosition: "OF", ProcessedDate: d},
		{TradeGroupID: "g1", ToTeamName: "Bravo", FromTeamName: "Alpha", PlayerName: "Some Arm", PlayerPosition: "SP", ProcessedDate: d},
		{TradeGroupID: "g2", ToTeamName: "Charlie", FromTeamName: "Delta", PlayerName: "Other Guy", PlayerPosition: "SS", ProcessedDate: d.AddDate(0, 0, 1)},
	}
	got := SeasonTrades(txs, nil)
	if len(got) != 2 {
		t.Fatalf("trades = %d, want 2", len(got))
	}
	if len(got[0].Sides) != 2 || got[0].Sides[0].TeamName != "Alpha" || got[0].Sides[1].TeamName != "Bravo" {
		t.Errorf("trade 1 sides = %+v", got[0].Sides)
	}
	if got[0].Sides[0].Players[0].Name != "Some Star" || got[0].Sides[0].Players[0].Ranked {
		t.Errorf("unranked player should carry its name with Ranked=false: %+v", got[0].Sides[0].Players[0])
	}
}
