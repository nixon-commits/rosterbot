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

// findCategory returns the SeasonAwardCategory for the given award name, or
// nil if absent from the snapshot.
func findCategory(snap *SeasonAwards, awardName string) *SeasonAwardCategory {
	for i := range snap.Categories {
		if snap.Categories[i].AwardName == awardName {
			return &snap.Categories[i]
		}
	}
	return nil
}

// teamCountInCategory returns the cumulative count for teamID under the given
// award category, or -1 if the team isn't listed.
func teamCountInCategory(snap *SeasonAwards, awardName, teamID string) int {
	cat := findCategory(snap, awardName)
	if cat == nil {
		return -1
	}
	for _, t := range cat.Teams {
		if t.TeamID == teamID {
			return t.Count
		}
	}
	return -1
}

func TestAggregateSeasonAwards_AttributesEachAwardOnce(t *testing.T) {
	teams := []TeamWeek{
		tw("a", "Alpha", 100, 110),
		tw("b", "Bravo", 90, 100),
		tw("c", "Charlie", 80, 100),
	}
	r := &Recap{
		WeekNumber: 1,
		Teams:      teams,
		Awards: Awards{
			MostEfficient:    &TeamWeek{TeamID: "a", TeamName: "Alpha"},
			LeastEfficient:   &TeamWeek{TeamID: "c", TeamName: "Charlie"},
			HighestScore:     &TeamWeek{TeamID: "a", TeamName: "Alpha"},
			LowestScore:      &TeamWeek{TeamID: "c", TeamName: "Charlie"},
			BiggestBlowout:   &MatchupResult{HomeTeamID: "a", AwayTeamID: "c", WinnerID: "a", LoserID: "c"},
			NarrowVictory:    &MatchupResult{HomeTeamID: "b", AwayTeamID: "c", WinnerID: "b", LoserID: "c"},
			HighestPtsInLoss: &MatchupTeamSide{TeamID: "c"},
			LowestPtsInWin:   &MatchupTeamSide{TeamID: "b"},
			BestSingleStart:  &PitcherStartLine{OwnerTeam: "Alpha"},
			WorstSingleStart: &PitcherStartLine{OwnerTeam: "Charlie"},
		},
	}
	got := AggregateSeasonAwards([]*Recap{r})
	if len(got) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(got))
	}
	snap := got[0]
	if snap.ThroughWeek != 1 {
		t.Errorf("ThroughWeek = %d, want 1", snap.ThroughWeek)
	}
	// Spot-check attribution per award category.
	checks := []struct {
		award, team string
		want        int
	}{
		{AwardMostEfficient, "a", 1},
		{AwardLeastEfficient, "c", 1},
		{AwardHighestScore, "a", 1},
		{AwardLowestScore, "c", 1},
		{AwardBiggestBlowout, "a", 1},
		{AwardNarrowVictory, "b", 1},
		{AwardHighestPtsLoss, "c", 1},
		{AwardLowestPtsWin, "b", 1},
		{AwardBestStart, "a", 1},
		{AwardWorstStart, "c", 1},
	}
	for _, c := range checks {
		if got := teamCountInCategory(snap, c.award, c.team); got != c.want {
			t.Errorf("%s for %s = %d, want %d", c.award, c.team, got, c.want)
		}
	}
	// Categories appear in the configured display order.
	if snap.Categories[0].AwardName != AwardMostEfficient {
		t.Errorf("first category = %q, want %q", snap.Categories[0].AwardName, AwardMostEfficient)
	}
}

func TestAggregateSeasonAwards_NilAwardsAreSkipped(t *testing.T) {
	teams := []TeamWeek{tw("a", "Alpha", 100, 110), tw("b", "Bravo", 90, 100)}
	r := &Recap{
		WeekNumber: 1,
		Teams:      teams,
		Awards:     Awards{}, // all nil
	}
	got := AggregateSeasonAwards([]*Recap{r})
	if len(got) != 1 {
		t.Fatalf("want 1 snapshot")
	}
	if len(got[0].Categories) != 0 {
		t.Errorf("Categories should be empty when no awards present, got %d", len(got[0].Categories))
	}
}

func TestAggregateSeasonAwards_PitcherStartUnmappableNameSkipped(t *testing.T) {
	teams := []TeamWeek{tw("a", "Alpha", 100, 110)}
	r := &Recap{
		WeekNumber: 1,
		Teams:      teams,
		Awards: Awards{
			BestSingleStart: &PitcherStartLine{OwnerTeam: "GhostFranchise"}, // not in Teams
		},
	}
	got := AggregateSeasonAwards([]*Recap{r})
	if cat := findCategory(got[0], AwardBestStart); cat != nil {
		t.Errorf("Best Start category should be omitted when only earner is unmappable, got %+v", cat)
	}
}

func TestAggregateSeasonAwards_CumulativeAcrossWeeks(t *testing.T) {
	teams := []TeamWeek{tw("a", "Alpha", 100, 110), tw("b", "Bravo", 90, 100)}
	w1 := &Recap{
		WeekNumber: 1,
		Teams:      teams,
		Awards:     Awards{HighestScore: &TeamWeek{TeamID: "a"}},
	}
	w2 := &Recap{
		WeekNumber: 2,
		Teams:      teams,
		Awards:     Awards{HighestScore: &TeamWeek{TeamID: "a"}, MostEfficient: &TeamWeek{TeamID: "b"}},
	}
	w3 := &Recap{
		WeekNumber: 3,
		Teams:      teams,
		Awards:     Awards{HighestScore: &TeamWeek{TeamID: "b"}},
	}
	got := AggregateSeasonAwards([]*Recap{w1, w2, w3})
	if len(got) != 3 {
		t.Fatalf("want 3 snapshots, got %d", len(got))
	}
	// After week 1: a has 1 HighestScore.
	if c := teamCountInCategory(got[0], AwardHighestScore, "a"); c != 1 {
		t.Errorf("week 1 Alpha HighestScore = %d, want 1", c)
	}
	// After week 2: a has 2 HighestScore, b has 1 BestTeam.
	if c := teamCountInCategory(got[1], AwardHighestScore, "a"); c != 2 {
		t.Errorf("week 2 Alpha HighestScore = %d, want 2", c)
	}
	if c := teamCountInCategory(got[1], AwardMostEfficient, "b"); c != 1 {
		t.Errorf("week 2 Bravo BestTeam = %d, want 1", c)
	}
	// After week 3: a still has 2 HighestScore (from w1+w2), b has 1.
	if c := teamCountInCategory(got[2], AwardHighestScore, "a"); c != 2 {
		t.Errorf("week 3 Alpha HighestScore = %d, want 2", c)
	}
	if c := teamCountInCategory(got[2], AwardHighestScore, "b"); c != 1 {
		t.Errorf("week 3 Bravo HighestScore = %d, want 1", c)
	}
	// Within HighestScore category at week 3: a (count 2) before b (count 1).
	cat := findCategory(got[2], AwardHighestScore)
	if cat.Teams[0].TeamID != "a" {
		t.Errorf("week 3 HighestScore first team = %s, want a (count desc)", cat.Teams[0].TeamID)
	}
}

func TestAggregateSeasonAwards_NilRecapInSlice(t *testing.T) {
	teams := []TeamWeek{tw("a", "Alpha", 100, 110)}
	r := &Recap{WeekNumber: 1, Teams: teams, Awards: Awards{HighestScore: &TeamWeek{TeamID: "a"}}}
	got := AggregateSeasonAwards([]*Recap{r, nil, r})
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	if got[1] != nil {
		t.Errorf("nil recap should produce nil snapshot, got %+v", got[1])
	}
	// Counts continue accumulating past the nil — Alpha now has 2 HighestScore.
	if c := teamCountInCategory(got[2], AwardHighestScore, "a"); c != 2 {
		t.Errorf("after nil-skip Alpha HighestScore = %d, want 2", c)
	}
}

func TestGameOfTheWeek_LeadChangesWin(t *testing.T) {
	// Curve "mid" has 6 lead changes; "low" has 1. Lead changes dominate
	// even when the deeper-comeback matchup has bigger margin swing.
	low := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.4}, {HomeWP: 0.3}, {HomeWP: 0.2},
		{HomeWP: 0.15}, {HomeWP: 0.1}, {HomeWP: 0.05}, {HomeWP: 0.0},
	}
	mid := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.6}, {HomeWP: 0.4}, {HomeWP: 0.6},
		{HomeWP: 0.4}, {HomeWP: 0.6}, {HomeWP: 0.4}, {HomeWP: 0.55},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: low, LeadChanges: 1},
		{HomeTeamID: "C", AwayTeamID: "D", Points: mid, LeadChanges: 6},
		{HomeTeamID: "E", AwayTeamID: "F", Points: low, LeadChanges: 1},
	}
	matchups := []MatchupResult{
		mr("A", "B", 100, 200), // blowout
		mr("C", "D", 102, 100), // narrow
		mr("E", "F", 90, 120),
	}

	got := GameOfTheWeek(curves, matchups)
	if got == nil || got.HomeTeamID != "C" {
		t.Fatalf("GameOfTheWeek: want C-D, got %+v", got)
	}
}

func TestGameOfTheWeek_AlwaysReturns(t *testing.T) {
	// Two blowouts with no lead changes — formula must still pick a winner.
	// Tiebreak: smaller margin → home TeamID asc. Both have minWinnerWP=0.5
	// (so the comeback term contributes 0.5 to each — equal).
	flat := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.55}, {HomeWP: 0.6}, {HomeWP: 0.65},
		{HomeWP: 0.7}, {HomeWP: 0.8}, {HomeWP: 0.9}, {HomeWP: 1.0},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: flat, LeadChanges: 0},
		{HomeTeamID: "C", AwayTeamID: "D", Points: flat, LeadChanges: 0},
	}
	matchups := []MatchupResult{
		mr("A", "B", 200, 100), // margin 100
		mr("C", "D", 110, 100), // margin 10 ← closer
	}
	got := GameOfTheWeek(curves, matchups)
	if got == nil || got.HomeTeamID != "C" {
		t.Fatalf("GameOfTheWeek: want C-D (closer margin), got %+v", got)
	}
}

func TestGameOfTheWeek_ComebackBreaksLeadTie(t *testing.T) {
	// Two matchups, both with 1 lead change. The eventual winner in matchup
	// A sank to 0.10 (deep comeback); in matchup C, only to 0.40. A wins.
	deep := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.4}, {HomeWP: 0.2}, {HomeWP: 0.1},
		{HomeWP: 0.3}, {HomeWP: 0.55}, {HomeWP: 0.75}, {HomeWP: 1.0},
	}
	shallow := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.45}, {HomeWP: 0.40}, {HomeWP: 0.42},
		{HomeWP: 0.55}, {HomeWP: 0.7}, {HomeWP: 0.85}, {HomeWP: 1.0},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: deep, LeadChanges: 1},
		{HomeTeamID: "C", AwayTeamID: "D", Points: shallow, LeadChanges: 1},
	}
	matchups := []MatchupResult{
		mr("A", "B", 110, 100),
		mr("C", "D", 110, 100),
	}
	got := GameOfTheWeek(curves, matchups)
	if got == nil || got.HomeTeamID != "A" {
		t.Fatalf("GameOfTheWeek: want A-B (deeper comeback), got %+v", got)
	}
}

func TestComeback(t *testing.T) {
	// Matchup 1: home wins after trailing badly mid-week (eligible).
	deep := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.4}, {HomeWP: 0.2}, {HomeWP: 0.15},
		{HomeWP: 0.4}, {HomeWP: 0.6}, {HomeWP: 0.7}, {HomeWP: 1.0},
	}
	// Matchup 2: home wins, mild dip but never below 0.30 (ineligible).
	mild := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.45}, {HomeWP: 0.4}, {HomeWP: 0.5},
		{HomeWP: 0.6}, {HomeWP: 0.7}, {HomeWP: 0.85}, {HomeWP: 1.0},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: deep},
		{HomeTeamID: "C", AwayTeamID: "D", Points: mild},
	}
	matchups := []MatchupResult{
		mr("A", "B", 200, 180),
		mr("C", "D", 150, 145),
	}

	got := Comeback(curves, matchups)
	if got == nil || got.TeamID != "A" {
		t.Fatalf("Comeback: want A, got %+v", got)
	}
}

func TestComebackNoEligible(t *testing.T) {
	mild := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.55}, {HomeWP: 0.6}, {HomeWP: 0.65},
		{HomeWP: 0.7}, {HomeWP: 0.75}, {HomeWP: 0.8}, {HomeWP: 1.0},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: mild},
	}
	matchups := []MatchupResult{mr("A", "B", 200, 180)}
	if got := Comeback(curves, matchups); got != nil {
		t.Errorf("Comeback: want nil (no eligible), got %+v", got)
	}
}

func TestAggregateSeasonAwards_NewCategories(t *testing.T) {
	r := &Recap{
		WeekNumber: 1,
		Teams: []TeamWeek{
			{TeamID: "1", TeamName: "Wahoos"},
			{TeamID: "2", TeamName: "Sliders"},
		},
		Awards: Awards{
			Comeback: &MatchupTeamSide{TeamID: "1", TeamName: "Wahoos"},
		},
	}
	snaps := AggregateSeasonAwards([]*Recap{r})
	if len(snaps) != 1 || snaps[0] == nil {
		t.Fatalf("snaps: want 1 non-nil, got %+v", snaps)
	}
	want := map[string]string{
		AwardComeback: "1",
	}
	got := map[string]string{}
	for _, cat := range snaps[0].Categories {
		if len(cat.Teams) > 0 {
			got[cat.AwardName] = cat.Teams[0].TeamID
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want team %q, got %q", k, v, got[k])
		}
	}
}

// Reference time used implicitly by the existing test helpers — keeps the
// import of "time" consistent with the rest of the file.
var _ = time.Now
