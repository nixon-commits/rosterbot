package recap

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/transactions"
)

// Full-season efficiency pools each team over the weeks it actually played:
// every regular-season week, and only the playoff rounds it was paired in —
// a bye week or an eliminated week contributes nothing, so a team that was
// out early is not diluted by weeks it did not manage. The week count rides
// beside the rate so the reader can see the denominator.
func TestSeasonEfficiency_PoolsOverWeeksPlayed(t *testing.T) {
	week := func(n int, teams map[string][2]float64, pairs [][2]string, byes []string) *Recap {
		r := &Recap{WeekNumber: n}
		for id, ao := range teams {
			r.Teams = append(r.Teams, TeamWeek{TeamID: id, TeamName: strings.ToUpper(id), ActualPts: ao[0], OptimalPts: ao[1]})
		}
		for _, p := range pairs {
			r.Matchups = append(r.Matchups, MatchupResult{HomeTeamID: p[0], AwayTeamID: p[1]})
		}
		for _, b := range byes {
			r.Byes = append(r.Byes, ByeLine{TeamID: b, TeamName: strings.ToUpper(b)})
		}
		return r
	}
	regular := week(22, map[string][2]float64{"a": {80, 100}, "b": {60, 100}, "c": {90, 100}, "d": {50, 100}}, [][2]string{{"a", "b"}, {"c", "d"}}, nil)
	playoff := week(23, map[string][2]float64{"a": {100, 100}, "b": {40, 100}, "c": {10, 100}, "d": {5, 100}}, [][2]string{{"a", "b"}}, []string{"c"})

	got := SeasonEfficiency([]*Recap{regular, playoff})
	want := map[string]struct {
		weeks int
		eff   float64
	}{"a": {2, 0.9}, "b": {2, 0.5}, "c": {1, 0.9}, "d": {1, 0.5}}
	if len(got) != 4 {
		t.Fatalf("lines = %d, want 4", len(got))
	}
	for _, l := range got {
		w := want[l.TeamID]
		if l.Weeks != w.weeks || l.Efficiency < w.eff-1e-9 || l.Efficiency > w.eff+1e-9 {
			t.Errorf("%s = weeks %d eff %.3f, want weeks %d eff %.3f", l.TeamID, l.Weeks, l.Efficiency, w.weeks, w.eff)
		}
	}
	if got[0].Efficiency < got[len(got)-1].Efficiency {
		t.Errorf("not sorted by efficiency desc: %+v", got)
	}
	if got[0].Rank != 1 || got[len(got)-1].Rank != 4 {
		t.Errorf("ranks not assigned: %+v", got)
	}
}

// Trades of the year rank by HKB value swing — the gap between the sides'
// raw totals — and show both sides with every asset, so the reader sees what
// moved, not just who "won".
func TestTradesOfTheYear_RanksBySwing(t *testing.T) {
	mk := func(day int, a, b string, va, vb int, favored string) transactions.Trade {
		return transactions.Trade{
			ProcessedDate: time.Date(2026, 7, day, 0, 0, 0, 0, time.UTC),
			Sides: []transactions.TradeSide{
				{TeamName: a, Total: va, Players: []transactions.TradePlayer{{Name: a + " star", Value: va, Ranked: true}}},
				{TeamName: b, Total: vb, Players: []transactions.TradePlayer{{Name: b + " pick", Value: vb, Ranked: true, IsPick: true}}},
			},
		}
	}
	trades := []transactions.Trade{
		mk(1, "Alpha", "Bravo", 1000, 900, ""),
		mk(2, "Charlie", "Delta", 2000, 500, "Charlie"),
		mk(3, "Echo", "Foxtrot", 300, 900, "Foxtrot"),
	}
	got := TradesOfTheYear(trades, 2)
	if len(got) != 2 || got[0].Swing != 1500 || got[1].Swing != 600 {
		t.Fatalf("top 2 by swing = %+v", got)
	}
	if len(got[0].Sides) != 2 || got[0].Sides[0].TeamName != "Charlie" || got[0].Sides[0].Players[0].Name != "Charlie star" || !got[0].Sides[1].Players[0].IsPick {
		t.Errorf("sides not carried: %+v", got[0].Sides)
	}
	if !got[0].Date.Equal(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %s", got[0].Date)
	}
}

// The three data sections render on the season page: the efficiency table
// with week counts, the trades with both sides, and the season leaders
// reusing the weekly board's lines (same thresholds, same owner badge).
func TestRenderSeason_DataSections(t *testing.T) {
	b, st := seasonFixture()
	extras := SeasonExtras{
		Efficiency: []EfficiencyLine{{Rank: 1, TeamID: "h", TeamName: "Houston Swang and Bang", Weeks: 23, Actual: 15594, Optimal: 19118, Efficiency: 0.8157}},
		Trades: []TradeLine{{Date: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), Swing: 1500, FavoredTeam: "Charlie",
			Sides: []TradeSideLine{{TeamName: "Charlie", Total: 2000, Players: []TradePlayerLine{{Name: "Charlie star", Value: 2000}}}, {TeamName: "Delta", Total: 500, Players: []TradePlayerLine{{Name: "2027 1st", Value: 500, IsPick: true}}}}}},
		WOBALeaders: []LeaderLine{{Name: "Aaron Judge", MLBTeam: "NYY", OwnerTeam: "jimmydyl", OwnerTeamID: "jimmy", Value: .420}},
		FIPLeaders:  []LeaderLine{{Name: "Tarik Skubal", MLBTeam: "DET", OwnerTeam: "Pfaadt Wood Kings", OwnerTeamID: "pfaadt", Value: 2.31}},
	}
	p := BuildSeasonPage("2026", st, b, nil, nil, time.Time{}, extras)
	var buf bytes.Buffer
	if err := RenderSeason(&buf, p, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Lineup Efficiency", "81.6%", "23 wks", "Trades of the Year", "Charlie star", "2027 1st", "Delta", "League Leaders", "Aaron Judge", "Tarik Skubal", ".420", "2.31"} {
		if !strings.Contains(out, want) {
			t.Errorf("season page lacks %q", want)
		}
	}
}
