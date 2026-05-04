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

// findCount returns the Count for teamID in a snapshot, or -1 if missing.
func findCount(snap *SeasonAwards, teamID string) int {
	for _, ln := range snap.Teams {
		if ln.TeamID == teamID {
			return ln.Count
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
	// Alpha: MostEff + HighestScore + Blowout winner + Best start = 4
	// Bravo: NarrowVictory winner + LowestPtsInWin = 2
	// Charlie: LeastEff + LowestScore + HighestPtsInLoss + Worst start = 4
	if c := findCount(snap, "a"); c != 4 {
		t.Errorf("Alpha count = %d, want 4", c)
	}
	if c := findCount(snap, "b"); c != 2 {
		t.Errorf("Bravo count = %d, want 2", c)
	}
	if c := findCount(snap, "c"); c != 4 {
		t.Errorf("Charlie count = %d, want 4", c)
	}
	// Sort: a and c tied at 4 → "a" first lexically; b last at 2.
	if snap.Teams[0].TeamID != "a" || snap.Teams[1].TeamID != "c" || snap.Teams[2].TeamID != "b" {
		t.Errorf("order = [%s,%s,%s], want [a,c,b]",
			snap.Teams[0].TeamID, snap.Teams[1].TeamID, snap.Teams[2].TeamID)
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
	for _, ln := range got[0].Teams {
		if ln.Count != 0 {
			t.Errorf("team %s count = %d, want 0 (no awards present)", ln.TeamID, ln.Count)
		}
	}
	if len(got[0].Teams) != 2 {
		t.Errorf("Teams len = %d, want 2 (every roster team listed even at zero)", len(got[0].Teams))
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
	if c := findCount(got[0], "a"); c != 0 {
		t.Errorf("Alpha count = %d, want 0 (orphan owner name should be silently dropped)", c)
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
	if got[0].ThroughWeek != 1 || got[1].ThroughWeek != 2 || got[2].ThroughWeek != 3 {
		t.Errorf("ThroughWeek values = %d,%d,%d", got[0].ThroughWeek, got[1].ThroughWeek, got[2].ThroughWeek)
	}
	// After week 1: a=1, b=0
	if c := findCount(got[0], "a"); c != 1 {
		t.Errorf("week 1 Alpha count = %d, want 1", c)
	}
	// After week 2: a=2, b=1
	if c := findCount(got[1], "a"); c != 2 {
		t.Errorf("week 2 Alpha count = %d, want 2", c)
	}
	if c := findCount(got[1], "b"); c != 1 {
		t.Errorf("week 2 Bravo count = %d, want 1", c)
	}
	// After week 3: a=2, b=2
	if c := findCount(got[2], "a"); c != 2 {
		t.Errorf("week 3 Alpha count = %d, want 2", c)
	}
	if c := findCount(got[2], "b"); c != 2 {
		t.Errorf("week 3 Bravo count = %d, want 2", c)
	}
	// Tiebreak: alpha first by ID asc.
	if got[2].Teams[0].TeamID != "a" {
		t.Errorf("week 3 first team = %s, want a (lex tiebreak)", got[2].Teams[0].TeamID)
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
	// Counts continue accumulating past the nil — Alpha now has 2.
	if c := findCount(got[2], "a"); c != 2 {
		t.Errorf("after nil-skip Alpha count = %d, want 2", c)
	}
}

// Reference time used implicitly by the existing test helpers — keeps the
// import of "time" consistent with the rest of the file.
var _ = time.Now
