package valuereport

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/teamvalue"
)

func fixtureRows() []teamvalue.Row {
	return []teamvalue.Row{
		// day 1
		{Dt: "2026-07-12", TeamID: "t2", TeamName: "Beta", LogoURL: "https://x/b.png",
			HitterMLBValue: 100, PitcherMLBValue: 50, HitterMinorsValue: 20, RosteredCount: 10, MatchedCount: 9},
		{Dt: "2026-07-12", TeamID: "t1", TeamName: "Alpha", LogoURL: "https://x/a.png",
			HitterMLBValue: 200, PitcherMLBValue: 80, HitterMinorsValue: 40, RosteredCount: 12, MatchedCount: 12},
		// day 2
		{Dt: "2026-07-13", TeamID: "t1", TeamName: "Alpha", LogoURL: "https://x/a.png",
			HitterMLBValue: 210, PitcherMLBValue: 80, HitterMinorsValue: 40, RosteredCount: 12, MatchedCount: 12},
		{Dt: "2026-07-13", TeamID: "t2", TeamName: "Beta", LogoURL: "https://x/b.png",
			HitterMLBValue: 100, PitcherMLBValue: 55, HitterMinorsValue: 20, RosteredCount: 10, MatchedCount: 8},
	}
}

func TestBuildModel_Empty(t *testing.T) {
	m := BuildModel(nil)
	if !m.Empty {
		t.Fatal("want Empty=true for no rows")
	}
}

func TestBuildModel_Shape(t *testing.T) {
	m := BuildModel(fixtureRows())
	if m.Empty {
		t.Fatal("want non-empty")
	}
	if len(m.Dates) != 2 || m.Dates[0] != "2026-07-12" || m.Dates[1] != "2026-07-13" {
		t.Fatalf("dates wrong: %v", m.Dates)
	}
	if m.FirstDate != "2026-07-12" || m.LastDate != "2026-07-13" {
		t.Fatalf("first/last wrong: %s %s", m.FirstDate, m.LastDate)
	}
	if len(m.Teams) != 2 || m.Teams[0].ID != "t1" || m.Teams[1].ID != "t2" {
		t.Fatalf("teams not sorted by ID: %+v", m.Teams)
	}
	// Color is stable per sorted position.
	if m.Teams[0].Color != palette[0] || m.Teams[1].Color != palette[1] {
		t.Fatalf("colors not assigned deterministically: %+v", m.Teams)
	}
	if len(m.Series) != 4 {
		t.Fatalf("want 4 series points, got %d", len(m.Series))
	}
}

func TestBuildModel_LatestSortedByTotalDesc(t *testing.T) {
	m := BuildModel(fixtureRows())
	if len(m.Latest) != 2 {
		t.Fatalf("want 2 latest rows (last day only), got %d", len(m.Latest))
	}
	// Day 2: Alpha total = 210+80+40 = 330, Beta = 100+55+20 = 175.
	if m.Latest[0].TeamID != "t1" || m.Latest[0].Total != 330 {
		t.Fatalf("latest[0] wrong: %+v", m.Latest[0])
	}
	if m.Latest[1].TeamID != "t2" || m.Latest[1].Total != 175 {
		t.Fatalf("latest[1] wrong: %+v", m.Latest[1])
	}
	// Coverage carried through: Beta on day 2 matched 8/10.
	if m.Latest[1].MatchedCount != 8 || m.Latest[1].RosteredCount != 10 {
		t.Fatalf("coverage not carried: %+v", m.Latest[1])
	}
}
