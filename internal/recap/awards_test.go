package recap

import (
	"testing"
	"time"
)

func tw(id, name string, actual, optimal float64) TeamWeek {
	eff := 0.0
	if optimal > 0 {
		eff = actual / optimal
	}
	return TeamWeek{TeamID: id, TeamName: name, ActualPts: actual, OptimalPts: optimal, Efficiency: eff}
}

func TestMostLeastEfficient(t *testing.T) {
	teams := []TeamWeek{
		tw("1", "A", 200, 250), // 0.80
		tw("2", "B", 180, 200), // 0.90 — most efficient
		tw("3", "C", 100, 200), // 0.50 — least efficient
		tw("4", "D", 150, 250), // 0.60
		tw("5", "E", 0, 0),     // skipped (no optimal)
	}

	if got := MostEfficient(teams); got == nil || got.TeamID != "2" {
		t.Fatalf("MostEfficient: want team 2, got %+v", got)
	}
	if got := LeastEfficient(teams); got == nil || got.TeamID != "3" {
		t.Fatalf("LeastEfficient: want team 3, got %+v", got)
	}
}

func TestMostLeastEfficientEmpty(t *testing.T) {
	if got := MostEfficient(nil); got != nil {
		t.Errorf("MostEfficient(nil) should be nil, got %+v", got)
	}
	if got := LeastEfficient([]TeamWeek{}); got != nil {
		t.Errorf("LeastEfficient(empty) should be nil, got %+v", got)
	}
	// All teams with zero optimal — no eligible team.
	zeros := []TeamWeek{{TeamID: "1", OptimalPts: 0}}
	if got := MostEfficient(zeros); got != nil {
		t.Errorf("MostEfficient(all-zero) should be nil, got %+v", got)
	}
}

func mr(homeID, awayID string, homePts, awayPts float64) MatchupResult {
	margin := homePts - awayPts
	if margin < 0 {
		margin = -margin
	}
	m := MatchupResult{
		HomeTeamID: homeID, HomeTeamName: homeID,
		AwayTeamID: awayID, AwayTeamName: awayID,
		HomePts: homePts, AwayPts: awayPts,
		Margin: margin,
	}
	switch {
	case homePts > awayPts:
		m.WinnerID, m.LoserID = homeID, awayID
	case awayPts > homePts:
		m.WinnerID, m.LoserID = awayID, homeID
	default:
		m.IsTie = true
	}
	return m
}

func TestBlowoutNarrow(t *testing.T) {
	matchups := []MatchupResult{
		mr("A", "B", 100, 90),  // 10
		mr("C", "D", 200, 100), // 100 — biggest
		mr("E", "F", 50, 49),   // 1 — narrow
		mr("G", "H", 75, 75),   // tie — should be skipped for narrow
	}

	if got := BiggestBlowout(matchups); got == nil || got.HomeTeamID != "C" {
		t.Fatalf("BiggestBlowout: want C-D, got %+v", got)
	}
	if got := NarrowVictory(matchups); got == nil || (got.HomeTeamID != "E" && got.AwayTeamID != "E") {
		t.Fatalf("NarrowVictory: want E-F (skipping tie), got %+v", got)
	}
}

func TestBlowoutNarrowAllTies(t *testing.T) {
	matchups := []MatchupResult{mr("A", "B", 50, 50), mr("C", "D", 75, 75)}
	if got := BiggestBlowout(matchups); got != nil {
		t.Errorf("BiggestBlowout(all-ties): want nil, got %+v", got)
	}
	if got := NarrowVictory(matchups); got != nil {
		t.Errorf("NarrowVictory(all-ties): want nil, got %+v", got)
	}
}

func TestHighestPtsInLossAndLowestPtsInWin(t *testing.T) {
	matchups := []MatchupResult{
		mr("A", "B", 200, 199), // A wins narrow; B loses high
		mr("C", "D", 80, 60),   // C wins low; D loses low
		mr("E", "F", 150, 140), // both regular
	}

	highLoss := HighestPtsInLoss(matchups)
	if highLoss == nil || highLoss.TeamID != "B" || highLoss.Pts != 199 {
		t.Fatalf("HighestPtsInLoss: want B with 199, got %+v", highLoss)
	}

	lowWin := LowestPtsInWin(matchups)
	if lowWin == nil || lowWin.TeamID != "C" || lowWin.Pts != 80 {
		t.Fatalf("LowestPtsInWin: want C with 80, got %+v", lowWin)
	}
}

func TestTopBattersAndPitchers(t *testing.T) {
	day1 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)

	active := []PlayerLine{
		{PlayerID: "h1", Name: "Alpha", FPts: 30, Date: day1, OwnerTeam: "A"},
		{PlayerID: "h2", Name: "Bravo", FPts: 25, Date: day1, OwnerTeam: "A"},
		{PlayerID: "h3", Name: "Charlie", FPts: 40, Date: day2, OwnerTeam: "B"},
		{PlayerID: "h4", Name: "Delta", FPts: 5, Date: day1, OwnerTeam: "C"},
		{PlayerID: "p1", Name: "Pacer", FPts: 35, Date: day1, OwnerTeam: "A", IsPitcher: true},
		{PlayerID: "p2", Name: "Quark", FPts: 12, Date: day2, OwnerTeam: "B", IsPitcher: true},
	}

	bat := TopBatters(active, 3)
	if len(bat) != 3 {
		t.Fatalf("TopBatters: want 3, got %d", len(bat))
	}
	if bat[0].Name != "Charlie" || bat[1].Name != "Alpha" || bat[2].Name != "Bravo" {
		t.Fatalf("TopBatters order wrong: %+v", bat)
	}
	for _, b := range bat {
		if b.IsPitcher {
			t.Errorf("TopBatters returned a pitcher: %+v", b)
		}
	}

	pit := TopPitchers(active, 5)
	if len(pit) != 2 {
		t.Fatalf("TopPitchers: want 2 (only 2 pitchers), got %d", len(pit))
	}
	if pit[0].Name != "Pacer" || pit[1].Name != "Quark" {
		t.Fatalf("TopPitchers order wrong: %+v", pit)
	}
	for _, p := range pit {
		if !p.IsPitcher {
			t.Errorf("TopPitchers returned a non-pitcher: %+v", p)
		}
	}
}

func TestTopPlayersHandlesShortInput(t *testing.T) {
	short := []PlayerLine{{Name: "Solo", FPts: 10, OwnerTeam: "X"}}
	if got := TopBatters(short, 5); len(got) != 1 || got[0].Name != "Solo" {
		t.Fatalf("TopBatters short: %+v", got)
	}
	if got := TopBatters(nil, 5); len(got) != 0 {
		t.Errorf("TopBatters(nil): want empty, got %+v", got)
	}
	if got := TopPitchers(nil, 5); len(got) != 0 {
		t.Errorf("TopPitchers(nil): want empty, got %+v", got)
	}
}

func TestBestWorstSingleStart(t *testing.T) {
	day := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	starts := []PitcherStartLine{
		{Name: "Ace", Date: day, FPts: 35.5, OwnerTeam: "A"},
		{Name: "Dud", Date: day, FPts: -8.2, OwnerTeam: "B"},
		{Name: "Mid", Date: day, FPts: 12.0, OwnerTeam: "C"},
	}
	if got := BestSingleStart(starts); got == nil || got.Name != "Ace" {
		t.Fatalf("BestSingleStart: want Ace, got %+v", got)
	}
	if got := WorstSingleStart(starts); got == nil || got.Name != "Dud" {
		t.Fatalf("WorstSingleStart: want Dud, got %+v", got)
	}
	if got := BestSingleStart(nil); got != nil {
		t.Errorf("BestSingleStart(nil): want nil, got %+v", got)
	}
}

func TestHighestLowestScore(t *testing.T) {
	teams := []TeamWeek{
		tw("1", "A", 200, 250),
		tw("2", "B", 350, 400), // highest
		tw("3", "C", 100, 200), // lowest
		tw("4", "D", 150, 250),
	}

	if got := HighestScore(teams); got == nil || got.TeamID != "2" {
		t.Fatalf("HighestScore: want team 2, got %+v", got)
	}
	if got := LowestScore(teams); got == nil || got.TeamID != "3" {
		t.Fatalf("LowestScore: want team 3, got %+v", got)
	}

	if got := HighestScore(nil); got != nil {
		t.Errorf("HighestScore(nil) should be nil, got %+v", got)
	}
	if got := LowestScore([]TeamWeek{}); got != nil {
		t.Errorf("LowestScore(empty) should be nil, got %+v", got)
	}
}

func TestHighestLowestScoreTieBreaker(t *testing.T) {
	teams := []TeamWeek{
		tw("zulu", "Zulu", 200, 250),
		tw("alpha", "Alpha", 200, 250),
	}
	if got := HighestScore(teams); got == nil || got.TeamID != "alpha" {
		t.Fatalf("HighestScore tiebreak: want alpha (lex first), got %+v", got)
	}
	if got := LowestScore(teams); got == nil || got.TeamID != "alpha" {
		t.Fatalf("LowestScore tiebreak: want alpha (lex first), got %+v", got)
	}
}

func TestEfficiencyTieBreaker(t *testing.T) {
	// Two teams tied at 0.90 efficiency. Tiebreak by team ID for stability.
	teams := []TeamWeek{
		tw("zulu", "Zulu", 90, 100),
		tw("alpha", "Alpha", 90, 100),
	}
	got := MostEfficient(teams)
	if got == nil || got.TeamID != "alpha" {
		t.Fatalf("MostEfficient tiebreak: want alpha (lex first), got %+v", got)
	}
}
