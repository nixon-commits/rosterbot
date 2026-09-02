package rostervalues

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

func now() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }

// fixture is one league of two teams plus a free agent.
//
// "mine" rosters five: two HKB-ranked (Miller, Arquette), two HKB does not
// rank at all (Journeyman, Deep Prospect), and one the namesake guard must
// decline (Luis Garcia, because "theirs" rosters a second Luis Garcia and HKB
// carries one row for the name). "theirs" rosters two, both ranked.
func fixture() ([]PoolPlayer, []hkb.Player, []Team) {
	pool := []PoolPlayer{
		{ID: "1", Name: "Mason Miller", MLBTeam: "SD", Positions: "RP,P", FantasyTeamID: "mine"},
		{ID: "2", Name: "Aiva Arquette", MLBTeam: "MIA", Positions: "SS", FantasyTeamID: "mine", MinorsEligible: true},
		{ID: "3", Name: "Some Journeyman", MLBTeam: "MIA", Positions: "1B,<b>UT</b>", FantasyTeamID: "mine"},
		{ID: "4", Name: "Deep Prospect", MLBTeam: "TB", Positions: "OF", FantasyTeamID: "mine", MinorsEligible: true},
		{ID: "5", Name: "Luis Garcia", MLBTeam: "HOU", Positions: "SP", FantasyTeamID: "mine"},
		{ID: "6", Name: "Luis Garcia", MLBTeam: "WSH", Positions: "2B", FantasyTeamID: "theirs"},
		{ID: "7", Name: "Free Agent", MLBTeam: "NYY", Positions: "OF"},
		{ID: "8", Name: "Their Star", MLBTeam: "LAD", Positions: "OF", FantasyTeamID: "theirs"},
	}
	hkbPlayers := []hkb.Player{
		{Name: "Mason Miller", Team: "SD", Value: 3177, Rank: 22, Level: "MLB", ActiveLevels: "MLB", Age: 26.8,
			Positions: []string{"RP"}, RankChange30Days: 4, ValueChange30Days: 120,
			RankHistory30Days: []int{26, 25, 22}},
		{Name: "Aiva Arquette", Team: "MIA", Value: 524, Rank: 323, Level: "AA", Prospect: true,
			RankHistory30Days: []int{0, 0, 340, 323}},
		{Name: "Luis Garcia", Team: "HOU", Value: 900, Rank: 150},
		{Name: "Their Star", Team: "LAD", Value: 5000, Rank: 3},
	}
	teams := []Team{
		{ID: "mine", Name: "Nixon Dynasty"},
		{ID: "theirs", Name: "Rivals"},
	}
	return pool, hkbPlayers, teams
}

func build(t *testing.T) map[string]Table {
	t.Helper()
	pool, hkbPlayers, teams := fixture()
	return BuildAll(now(), "2026-09-02", pool, hkbPlayers, teams)
}

func rows(tbl Table) map[string]Player {
	out := map[string]Player{}
	for _, p := range append(append([]Player(nil), tbl.MLB...), tbl.Prospects...) {
		out[p.Name] = p
	}
	return out
}

// TestBuildAll_KeepsEveryRosteredPlayer is the assertion that separates this
// artifact from the pool's: an unranked player on your own roster is not
// dropped, so rostered == mlb + prospects, not matched == mlb + prospects.
func TestBuildAll_KeepsEveryRosteredPlayerAndSegmentsExhaustively(t *testing.T) {
	mine := build(t)["mine"]

	c := mine.Counts
	if c.Rostered != 5 || c.MLB+c.Prospects != c.Rostered {
		t.Fatalf("counts %+v: rostered must equal mlb + prospects", c)
	}
	if c.Matched != 2 || c.Unmatched != 2 || c.NamesakeDeclined != 1 {
		t.Errorf("join counts %+v, want matched 2 / unmatched 2 / declined 1", c)
	}
	if c.Matched+c.Unmatched+c.NamesakeDeclined != c.Rostered {
		t.Errorf("join denominators %+v do not sum to rostered", c)
	}
}

func TestBuildAll_OrdersRankedByValueThenUnrankedLast(t *testing.T) {
	mine := build(t)["mine"]

	var names []string
	for _, p := range mine.MLB {
		names = append(names, p.Name)
	}
	// Miller (3177) first; the two unranked MLB rows after him, name-ascending.
	want := []string{"Mason Miller", "Luis Garcia", "Some Journeyman"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("mlb order = %v, want %v", names, want)
	}
}

func TestBuildAll_UnrankedRowsCarryAReasonAndNoHKBFields(t *testing.T) {
	byName := rows(build(t)["mine"])

	j := byName["Some Journeyman"]
	if j.UnrankedReason != ReasonNoMatch {
		t.Errorf("reason = %q, want %q", j.UnrankedReason, ReasonNoMatch)
	}
	g := byName["Luis Garcia"]
	if g.UnrankedReason != ReasonNamesake {
		t.Errorf("namesake reason = %q, want %q", g.UnrankedReason, ReasonNamesake)
	}
	for _, p := range []Player{j, g} {
		if p.HKBValue != nil || p.HKBRank != nil || p.RankChange30D != nil || p.ValueChange30D != nil ||
			p.RankHistory30D != nil || p.RankHistoryStartsAt != nil || p.Age != nil ||
			p.Pos != nil || p.Level != "" || p.ActiveLevels != "" {
			t.Errorf("%s: HKB-derived fields must be absent on an unranked row: %+v", p.Name, p)
		}
		if len(p.FantraxPos) == 0 {
			t.Errorf("%s: Fantrax positions must survive an unranked row", p.Name)
		}
	}
	// The pool's markup never reaches the artifact, ranked or not.
	if strings.Join(j.FantraxPos, ",") != "1B,UT" {
		t.Errorf("fantrax_pos = %v, want [1B UT] with the <b> stripped", j.FantraxPos)
	}

	// On the wire the absent fields are ABSENT, not zero: a client reading
	// hkb_value: 0 would render a real-looking "0" for a player nobody valued.
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"hkb_value", "hkb_rank", "rank_change_30d", "value_change_30d",
		"rank_history_30d", "rank_history_starts_at", "age", "level", "pos",
	} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("unranked row leaked %q: %s", key, data)
		}
	}
	if !strings.Contains(string(data), `"unranked_reason":"no HKB match"`) {
		t.Errorf("reason missing on the wire: %s", data)
	}
}

// An unranked player has no HKB prospect flag, so the segment falls back to
// the one fact Fantrax does supply. Deep Prospect is minors-eligible and lands
// in prospects; Some Journeyman is not and lands in mlb.
func TestBuildAll_UnrankedSegmentFollowsMinorsEligibility(t *testing.T) {
	mine := build(t)["mine"]

	inProspects := func(name string) bool {
		for _, p := range mine.Prospects {
			if p.Name == name {
				return true
			}
		}
		return false
	}
	if !inProspects("Deep Prospect") {
		t.Errorf("an unranked minors-eligible player belongs in prospects: %+v", mine.Prospects)
	}
	if inProspects("Some Journeyman") {
		t.Errorf("an unranked non-minors player belongs in mlb")
	}
	// And a RANKED player follows HKB's flag, not Fantrax's: Arquette is
	// minors-eligible AND HKB prospect, so both agree here; Miller is neither.
	if !inProspects("Aiva Arquette") {
		t.Errorf("a ranked HKB prospect belongs in prospects")
	}
}

// The header, the rows and the rank all derive from ONE sum — the ranked rows
// on each roster — so the three can never disagree on screen. That sum can
// differ from the Team Values dashboard by exactly the namesake declines
// (Garcia's 900 is not counted here), which is documented rather than hidden.
func TestBuildAll_HeaderSumsTheRowsAndRanksEveryTeamByThatSum(t *testing.T) {
	all := build(t)
	mine, theirs := all["mine"], all["theirs"]

	if mine.TeamValue != 3177+524 {
		t.Errorf("mine team_value = %d, want %d (the two ranked rows)", mine.TeamValue, 3177+524)
	}
	if theirs.TeamValue != 5000 {
		t.Errorf("theirs team_value = %d, want 5000", theirs.TeamValue)
	}
	if mine.LeagueRank != 2 || theirs.LeagueRank != 1 {
		t.Errorf("ranks: mine %d theirs %d, want 2 and 1", mine.LeagueRank, theirs.LeagueRank)
	}
	if mine.TeamCount != 2 || theirs.TeamCount != 2 {
		t.Errorf("team_count: mine %d theirs %d, want 2", mine.TeamCount, theirs.TeamCount)
	}
	if mine.TeamID != "mine" || mine.TeamName != "Nixon Dynasty" {
		t.Errorf("identity: %q %q", mine.TeamID, mine.TeamName)
	}
	if mine.GeneratedAt != "2026-09-02T12:00:00Z" || mine.HKBAsOf != "2026-09-02" {
		t.Errorf("timestamps %q %q", mine.GeneratedAt, mine.HKBAsOf)
	}
}

// Same two rules the pool pins: leading zeros in rank history are "unranked",
// and the deltas pass through availablepool.NormaliseChanges unchanged.
func TestBuildAll_ZeroSentinelAndSignSurvive(t *testing.T) {
	byName := rows(build(t)["mine"])

	a := byName["Aiva Arquette"]
	if a.RankHistoryStartsAt == nil || *a.RankHistoryStartsAt != 2 {
		t.Errorf("starts_at = %v, want 2 (leading zeros are 'unranked', not rank 0)", a.RankHistoryStartsAt)
	}
	m := byName["Mason Miller"]
	if m.RankChange30D == nil || *m.RankChange30D != 4 || m.ValueChange30D == nil || *m.ValueChange30D != 120 {
		t.Errorf("deltas must pass through unchanged: %+v", m)
	}
	if m.HKBValue == nil || *m.HKBValue != 3177 || m.HKBRank == nil || *m.HKBRank != 22 {
		t.Errorf("value/rank: %+v", m)
	}
	if m.Age == nil || *m.Age != 26.8 || m.Level != "MLB" || strings.Join(m.Pos, ",") != "RP" ||
		strings.Join(m.FantraxPos, ",") != "RP,P" {
		t.Errorf("descriptive fields: %+v", m)
	}
}

func TestBuildAll_SkipsUnownedAndNeverInventsATeam(t *testing.T) {
	all := build(t)
	if _, ok := all[""]; ok {
		t.Errorf("free agents must not produce a table")
	}
	if len(all) != 2 {
		t.Errorf("tables = %d, want 2", len(all))
	}
	for _, tbl := range all {
		for _, p := range append(tbl.MLB, tbl.Prospects...) {
			if p.Name == "Free Agent" {
				t.Errorf("free agent appeared on %s", tbl.TeamID)
			}
		}
	}
}

// A team the league table does not name still gets a table: the name is
// cosmetic, the roster is not. team_name is simply absent.
func TestBuildAll_UnnamedTeamStillGetsATable(t *testing.T) {
	pool, hkbPlayers, _ := fixture()
	all := BuildAll(now(), "2026-09-02", pool, hkbPlayers, nil)
	mine, ok := all["mine"]
	if !ok {
		t.Fatalf("no table for a team missing from the name list")
	}
	if mine.TeamName != "" || mine.TeamCount != 2 || mine.LeagueRank != 2 {
		t.Errorf("unnamed team: %+v", mine)
	}
}
