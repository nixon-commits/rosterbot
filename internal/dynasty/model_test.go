package dynasty

import (
	"testing"
	"time"
)

func TestBuildModel_Empty(t *testing.T) {
	m := BuildModel(nil, nil, time.Now())
	if !m.Empty {
		t.Fatal("want Empty=true for no rows")
	}
}

func TestBuildModel_StartersExcludesBenchAndSumsOnlyPlayers(t *testing.T) {
	rows := []Row{
		{Dt: "2026-08-11", TeamID: "1", TeamName: "A", AssetType: "player", IsStarter: true, ValueSFDynasty: 100},
		{Dt: "2026-08-11", TeamID: "1", TeamName: "A", AssetType: "player", IsStarter: false, ValueSFDynasty: 50}, // bench, excluded from starters
		{Dt: "2026-08-11", TeamID: "1", TeamName: "A", AssetType: "pick", IsStarter: false, ValueSFDynasty: 30},   // pick, never a starter
	}
	coverage := []Coverage{{Dt: "2026-08-11", TeamID: "1", TeamName: "A", RosteredCount: 2, MatchedCount: 2}}

	m := BuildModel(rows, coverage, time.Now())
	if m.Empty {
		t.Fatal("want non-empty")
	}
	if len(m.Teams) != 1 {
		t.Fatalf("len(Teams) = %d, want 1", len(m.Teams))
	}
	team := m.Teams[0]
	if team.StartersSFDynasty != 100 {
		t.Errorf("StartersSFDynasty = %d, want 100 (bench + pick excluded)", team.StartersSFDynasty)
	}
	if team.FullRosterSFDynasty != 180 {
		t.Errorf("FullRosterSFDynasty = %d, want 180 (100+50+30)", team.FullRosterSFDynasty)
	}
	if team.RosteredCount != 2 || team.MatchedCount != 2 {
		t.Errorf("coverage not carried: %+v", team)
	}
}

func TestBuildModel_OnlyLatestDateIncluded(t *testing.T) {
	rows := []Row{
		{Dt: "2026-08-10", TeamID: "1", TeamName: "A", AssetType: "player", IsStarter: true, ValueSFDynasty: 9999},
		{Dt: "2026-08-11", TeamID: "1", TeamName: "A", AssetType: "player", IsStarter: true, ValueSFDynasty: 100},
	}
	coverage := []Coverage{
		{Dt: "2026-08-10", TeamID: "1", TeamName: "A", RosteredCount: 1, MatchedCount: 1},
		{Dt: "2026-08-11", TeamID: "1", TeamName: "A", RosteredCount: 1, MatchedCount: 1},
	}

	m := BuildModel(rows, coverage, time.Now())
	if m.LatestDate != "2026-08-11" {
		t.Fatalf("LatestDate = %q, want 2026-08-11", m.LatestDate)
	}
	if len(m.Teams) != 1 || m.Teams[0].StartersSFDynasty != 100 {
		t.Fatalf("Teams = %+v, want only the latest date's row (100, not the stale 9999)", m.Teams)
	}
}

func TestBuildModel_SortedByStartersSFDynastyDescending(t *testing.T) {
	rows := []Row{
		{Dt: "2026-08-11", TeamID: "1", TeamName: "Low", AssetType: "player", IsStarter: true, ValueSFDynasty: 50},
		{Dt: "2026-08-11", TeamID: "2", TeamName: "High", AssetType: "player", IsStarter: true, ValueSFDynasty: 500},
	}
	m := BuildModel(rows, nil, time.Now())
	if len(m.Teams) != 2 || m.Teams[0].TeamID != "2" || m.Teams[1].TeamID != "1" {
		t.Fatalf("Teams not sorted starters-value descending: %+v", m.Teams)
	}
}

func TestBuildModel_AllFourFormatsCarried(t *testing.T) {
	rows := []Row{
		{Dt: "2026-08-11", TeamID: "1", TeamName: "A", AssetType: "player", IsStarter: true,
			ValueSFDynasty: 100, ValueNonSFDynasty: 200, ValueSFRedraft: 300, ValueNonSFRedraft: 400},
	}
	m := BuildModel(rows, nil, time.Now())
	team := m.Teams[0]
	if team.StartersSFDynasty != 100 || team.StartersNonSFDynasty != 200 ||
		team.StartersSFRedraft != 300 || team.StartersNonSFRedraft != 400 {
		t.Errorf("not all four formats carried: %+v", team)
	}
}
