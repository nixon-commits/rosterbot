package teamvalue

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/positions"
)

func pp(name, teamID string, minors bool, posIDs ...string) models.PoolPlayer {
	return models.PoolPlayer{Name: name, FantasyTeamID: teamID, MinorsEligible: minors, Positions: posIDs}
}

func hp(name string, value int) hkb.Player {
	return hkb.Player{Name: name, Value: value}
}

var testDate = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

func TestAggregate_BucketsAndCounts(t *testing.T) {
	pool := []models.PoolPlayer{
		pp("Mike Trout", "t1", false, positions.OF),      // hitter, MLB
		pp("Jackson Holliday", "t1", true, positions.SS), // hitter, minors
		pp("Tarik Skubal", "t1", false, positions.SP),    // pitcher, MLB
		pp("Andrew Painter", "t1", true, positions.SP),   // pitcher, minors
		pp("Some Free Agent", "", false, positions.OF),   // excluded (no team)
	}
	hkbPlayers := []hkb.Player{
		hp("Mike Trout", 500),
		hp("Jackson Holliday", 300),
		hp("Tarik Skubal", 400),
		hp("Andrew Painter", 200),
		hp("Some Free Agent", 999), // present in HKB but not rostered → ignored
	}
	names := map[string]string{"t1": "Alpha"}
	logos := map[string]string{"t1": "https://x/a.png"}

	rows := Aggregate(testDate, pool, hkbPlayers, names, logos)
	if len(rows) != 1 {
		t.Fatalf("want 1 team row, got %d", len(rows))
	}
	r := rows[0]
	if r.Dt != "2026-07-12" || r.TeamID != "t1" || r.TeamName != "Alpha" || r.LogoURL != "https://x/a.png" {
		t.Fatalf("row identity wrong: %+v", r)
	}
	if r.HitterMLBValue != 500 || r.HitterMinorsValue != 300 || r.PitcherMLBValue != 400 || r.PitcherMinorsValue != 200 {
		t.Errorf("value leaves wrong: %+v", r)
	}
	if r.HitterMLBCount != 1 || r.HitterMinorsCount != 1 || r.PitcherMLBCount != 1 || r.PitcherMinorsCount != 1 {
		t.Errorf("count leaves wrong: %+v", r)
	}
	if r.RosteredCount != 4 || r.MatchedCount != 4 {
		t.Errorf("coverage counts wrong: rostered=%d matched=%d, want 4/4", r.RosteredCount, r.MatchedCount)
	}
	if r.TotalValue() != 1400 || r.MinorsValue() != 500 || r.MLBValue() != 900 {
		t.Errorf("derived totals wrong: total=%d minors=%d mlb=%d", r.TotalValue(), r.MinorsValue(), r.MLBValue())
	}
}

func TestAggregate_UnmatchedCountsAsRosteredOnly(t *testing.T) {
	pool := []models.PoolPlayer{
		pp("Mike Trout", "t1", false, positions.OF),
		pp("Obscure Prospect", "t1", true, positions.SS), // no HKB entry
	}
	hkbPlayers := []hkb.Player{hp("Mike Trout", 500)}

	rows := Aggregate(testDate, pool, hkbPlayers, nil, nil)
	r := rows[0]
	if r.RosteredCount != 2 {
		t.Errorf("RosteredCount = %d, want 2", r.RosteredCount)
	}
	if r.MatchedCount != 1 {
		t.Errorf("MatchedCount = %d, want 1 (unmatched excluded)", r.MatchedCount)
	}
	if r.TotalValue() != 500 {
		t.Errorf("TotalValue = %d, want 500 (unmatched adds no value)", r.TotalValue())
	}
	if r.HitterMinorsCount != 0 {
		t.Errorf("unmatched minor leaguer should not increment a count leaf, got %d", r.HitterMinorsCount)
	}
}

// A two-way player with both hitter and pitcher eligibility resolves to pitcher.
func TestAggregate_TwoWayResolvesToPitcher(t *testing.T) {
	pool := []models.PoolPlayer{pp("Shohei Ohtani", "t1", false, positions.OF, positions.SP)}
	hkbPlayers := []hkb.Player{hp("Shohei Ohtani", 700)}

	r := Aggregate(testDate, pool, hkbPlayers, nil, nil)[0]
	if r.PitcherMLBValue != 700 || r.HitterMLBValue != 0 {
		t.Errorf("two-way should bucket to pitcher: %+v", r)
	}
}

// Name join tolerates diacritics/suffixes via playername.Normalize.
func TestAggregate_NormalizedNameJoin(t *testing.T) {
	pool := []models.PoolPlayer{pp("Ronald Acuña Jr.", "t1", false, positions.OF)}
	hkbPlayers := []hkb.Player{hp("Ronald Acuna", 600)}

	r := Aggregate(testDate, pool, hkbPlayers, nil, nil)[0]
	if r.MatchedCount != 1 || r.HitterMLBValue != 600 {
		t.Errorf("normalized-name join failed: %+v", r)
	}
}

// TeamName falls back to the pool's FantasyTeamName when the names map omits it.
func TestAggregate_TeamNameFallback(t *testing.T) {
	p := pp("Mike Trout", "t9", false, positions.OF)
	p.FantasyTeamName = "PoolName"
	rows := Aggregate(testDate, []models.PoolPlayer{p}, []hkb.Player{hp("Mike Trout", 1)}, nil, nil)
	if rows[0].TeamName != "PoolName" {
		t.Errorf("want fallback to pool FantasyTeamName, got %q", rows[0].TeamName)
	}
}

func TestAggregate_DeterministicOrder(t *testing.T) {
	pool := []models.PoolPlayer{
		pp("P One", "t3", false, positions.OF),
		pp("P Two", "t1", false, positions.OF),
		pp("P Three", "t2", false, positions.OF),
	}
	hkbPlayers := []hkb.Player{hp("P One", 1), hp("P Two", 1), hp("P Three", 1)}
	rows := Aggregate(testDate, pool, hkbPlayers, nil, nil)
	if len(rows) != 3 || rows[0].TeamID != "t1" || rows[1].TeamID != "t2" || rows[2].TeamID != "t3" {
		t.Fatalf("want teams sorted by ID, got %v", []string{rows[0].TeamID, rows[1].TeamID, rows[2].TeamID})
	}
}
