package backtest

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

func d(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

func TestBuildHitterSeries_GroupsByPlayerAndDropsPitchers(t *testing.T) {
	days := []fantrax.DayRoster{
		{Date: d("2026-05-01"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "h1", FPts: 5, HadGame: true, IsPitcher: false},
			{PlayerID: "p1", FPts: 9, HadGame: true, IsPitcher: true},
		}},
		{Date: d("2026-05-02"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "h1", FPts: 7, HadGame: true, IsPitcher: false},
		}},
	}
	got := BuildHitterSeries(days)
	if _, ok := got["p1"]; ok {
		t.Fatalf("pitcher leaked into hitter series")
	}
	if len(got["h1"]) != 2 {
		t.Fatalf("h1 series len = %d, want 2", len(got["h1"]))
	}
	if got["h1"][1].FP != 7 || !got["h1"][1].Played {
		t.Fatalf("h1 day2 = %+v, want FP 7 played", got["h1"][1])
	}
}

// stubPtsSource returns a fixed pts/game per player name via the PtsPerGameSource path.
type stubPtsSource struct{ pts map[string]float64 }

func (s stubPtsSource) GetProjection(name, _ string) (*projections.Projection, bool) {
	return &projections.Projection{G: 1}, true
}
func (s stubPtsSource) GetPtsPerGame(name, _ string, _ fantrax.ScoringWeights) (float64, bool) {
	v, ok := s.pts[name]
	return v, ok
}

func TestBuildPitcherSeries_GroupsByPlayerAndDropsHitters(t *testing.T) {
	days := []fantrax.DayRoster{
		{Date: d("2026-05-01"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "h1", FPts: 5, HadGame: true, IsPitcher: false},
			{PlayerID: "p1", FPts: 9, HadGame: true, IsPitcher: true},
		}},
		{Date: d("2026-05-02"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "p1", FPts: 12, HadGame: true, IsPitcher: true},
		}},
	}
	got := BuildPitcherSeries(days)
	if _, ok := got["h1"]; ok {
		t.Fatalf("hitter leaked into pitcher series")
	}
	if len(got["p1"]) != 2 {
		t.Fatalf("p1 series len = %d, want 2", len(got["p1"]))
	}
	if got["p1"][1].FP != 12 || !got["p1"][1].Played {
		t.Fatalf("p1 day2 = %+v, want FP 12 played", got["p1"][1])
	}
}

// stubPitcherPtsSource returns a fixed pts/game per pitcher name via the
// PitcherPtsPerGameSource path.
type stubPitcherPtsSource struct{ pts map[string]float64 }

func (s stubPitcherPtsSource) GetPitcherProjection(name, _ string) (*projections.PitcherProjection, bool) {
	return &projections.PitcherProjection{G: 1}, true
}
func (s stubPitcherPtsSource) GetPitcherPtsPerGame(name, _ string, _ fantrax.ScoringWeights) (float64, bool) {
	v, ok := s.pts[name]
	return v, ok
}

func TestRunPitcherStrategyComparison_ScoresChosenLineupByActuals(t *testing.T) {
	// One SP slot, two SP-eligible pitchers who both started. Variant "likesA"
	// projects A higher, "likesB" projects B higher. Actuals: A scored 5, B 20.
	day := fantrax.DayRoster{
		Date: d("2026-05-10"),
		Players: []fantrax.DayPlayerFP{
			{PlayerID: "a", Name: "A", MLBTeam: "NYY", FPts: 5, HadGame: true, StatusID: "1", IsPitcher: true, Positions: []string{"015"}, PosShortNames: "SP"},
			{PlayerID: "b", Name: "B", MLBTeam: "NYY", FPts: 20, HadGame: true, StatusID: "1", IsPitcher: true, Positions: []string{"015"}, PosShortNames: "SP"},
		},
	}
	slots := []fantrax.Slot{{PosID: "015"}} // one SP slot

	variants := []PitcherStrategyVariant{
		{Name: "likesA", Build: func(time.Time) (projections.PitcherSource, error) {
			return stubPitcherPtsSource{pts: map[string]float64{"A": 99, "B": 1}}, nil
		}},
		{Name: "likesB", Build: func(time.Time) (projections.PitcherSource, error) {
			return stubPitcherPtsSource{pts: map[string]float64{"A": 1, "B": 99}}, nil
		}},
	}

	got, err := RunPitcherStrategyComparison(variants, []fantrax.DayRoster{day}, slots, fantrax.ScoringWeights{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]VariantResult{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if byName["likesA"].RealizedPts != 5 {
		t.Fatalf("likesA realized = %v, want 5 (it started A)", byName["likesA"].RealizedPts)
	}
	if byName["likesB"].RealizedPts != 20 {
		t.Fatalf("likesB realized = %v, want 20 (it started B)", byName["likesB"].RealizedPts)
	}
	// Hindsight-optimal is 20 (start B), so likesA's Gap is 5-20 = -15.
	if byName["likesA"].MeanGap != -15 {
		t.Fatalf("likesA gap = %v, want -15", byName["likesA"].MeanGap)
	}
}

func TestRunStrategyComparison_ScoresChosenLineupByActuals(t *testing.T) {
	// One UT slot, two hitters with a game. Variant "likesA" projects A higher,
	// "likesB" projects B higher. Actuals: A scored 2, B scored 8.
	day := fantrax.DayRoster{
		Date: d("2026-05-10"),
		Players: []fantrax.DayPlayerFP{
			{PlayerID: "a", Name: "A", MLBTeam: "NYY", FPts: 2, HadGame: true, StatusID: "1", Positions: []string{"014"}},
			{PlayerID: "b", Name: "B", MLBTeam: "NYY", FPts: 8, HadGame: true, StatusID: "1", Positions: []string{"014"}},
		},
	}
	slots := []fantrax.Slot{{PosID: "014"}} // one UT slot

	variants := []StrategyVariant{
		{Name: "likesA", Build: func(time.Time) (projections.Source, error) {
			return stubPtsSource{pts: map[string]float64{"A": 99, "B": 1}}, nil
		}},
		{Name: "likesB", Build: func(time.Time) (projections.Source, error) {
			return stubPtsSource{pts: map[string]float64{"A": 1, "B": 99}}, nil
		}},
	}

	got, err := RunStrategyComparison(variants, []fantrax.DayRoster{day}, slots, fantrax.ScoringWeights{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]VariantResult{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if byName["likesA"].RealizedPts != 2 {
		t.Fatalf("likesA realized = %v, want 2 (it started A)", byName["likesA"].RealizedPts)
	}
	if byName["likesB"].RealizedPts != 8 {
		t.Fatalf("likesB realized = %v, want 8 (it started B)", byName["likesB"].RealizedPts)
	}
	// Hindsight-optimal is 8 (start B), so likesA's Gap is 2-8 = -6.
	if byName["likesA"].MeanGap != -6 {
		t.Fatalf("likesA gap = %v, want -6", byName["likesA"].MeanGap)
	}
}
