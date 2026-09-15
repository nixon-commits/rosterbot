package recap

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
)

func seasonFixture() (*auth_client.PlayoffBracket, []fantrax.StandingRow) {
	team := func(id, name string) auth_client.PlayoffSlot {
		return auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotTeam, TeamID: id, TeamName: name}
	}
	seed := func(n int) auth_client.PlayoffSlot {
		return auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotSeed, Seed: n}
	}
	bye := auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotBye}
	b := &auth_client.PlayoffBracket{Rounds: []auth_client.PlayoffRound{
		{Number: 1, Caption: "Round 1", ScoringPeriod: 23, StartDate: pday(2026, 9, 7), EndDate: pday(2026, 9, 13), Matchups: []auth_client.PlayoffMatchup{
			{Home: team("pfaadt", "Pfaadt Wood Kings"), Away: bye},
			{Home: team("jimmy", "jimmydyl"), Away: team("balk", "Intentional Balk"), HomeScore: 605, AwayScore: 515, Scored: true},
		}},
		{Number: 2, Caption: "Final Round", ScoringPeriod: 24, StartDate: pday(2026, 9, 14), EndDate: pday(2026, 9, 20), Matchups: []auth_client.PlayoffMatchup{
			{Home: seed(1), Away: seed(2)},
		}},
	}, ChampionLabel: "Champion"}
	st := []fantrax.StandingRow{
		{Rank: 1, TeamID: "pfaadt", TeamName: "Pfaadt Wood Kings", Wins: 42, Losses: 2, WinPct: .955, PointsFor: 15912, PointsAgainst: 12000, Streak: "W9"},
		{Rank: 2, TeamID: "jimmy", TeamName: "jimmydyl", Wins: 26, Losses: 18, WinPct: .591, GamesBack: 16, PointsFor: 14023, PointsAgainst: 13900},
	}
	return b, st
}

// The season page is built from Fantrax's own standings, the bracket with
// byes and undecided seeds kept honest, and the final season awards. Until
// the final is decided the champion slot reads TBD, never a placeholder team.
func TestBuildSeasonPage(t *testing.T) {
	b, st := seasonFixture()
	awards := &SeasonAwards{ThroughWeek: 23, Categories: []SeasonAwardCategory{{AwardName: AwardHighestScore, Teams: []SeasonAwardTeam{{TeamID: "pfaadt", TeamName: "Pfaadt Wood Kings", Count: 7}}}}}
	p := BuildSeasonPage("2026", st, b, awards, map[string]string{"pfaadt": "https://x/logo.png"}, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC))

	if len(p.Standings) != 2 || p.Standings[0].TeamName != "Pfaadt Wood Kings" || p.Standings[0].Wins != 42 {
		t.Errorf("standings = %+v", p.Standings)
	}
	if len(p.Bracket) != 2 {
		t.Fatalf("bracket rounds = %d, want 2", len(p.Bracket))
	}
	r1 := p.Bracket[0]
	if r1.Matchups[0].Away.Kind != "bye" || r1.Matchups[0].Home.Name != "Pfaadt Wood Kings" {
		t.Errorf("bye pairing = %+v", r1.Matchups[0])
	}
	if g := r1.Matchups[1]; g.WinnerID != "jimmy" || !g.Scored || g.AwayScore != 515 {
		t.Errorf("played pairing = %+v, want jimmy the winner 605-515", g)
	}
	if f := p.Bracket[1].Matchups[0]; f.Home.Kind != "seed" || f.Home.Label != "Seed 1" || f.Away.Label != "Seed 2" {
		t.Errorf("undrawn final = %+v, want Seed 1 vs Seed 2", f)
	}
	if p.Champion != nil || p.ChampionLabel != "TBD" {
		t.Errorf("champion = %+v / %q, want nil / TBD while undecided", p.Champion, p.ChampionLabel)
	}
	if p.Awards == nil || p.Awards.ThroughWeek != 23 {
		t.Errorf("awards not carried: %+v", p.Awards)
	}

	b.Champion = &auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotTeam, TeamID: "jimmy", TeamName: "jimmydyl"}
	p = BuildSeasonPage("2026", st, b, awards, nil, time.Time{})
	if p.Champion == nil || p.Champion.TeamID != "jimmy" || p.ChampionLabel != "jimmydyl" {
		t.Errorf("decided champion = %+v / %q", p.Champion, p.ChampionLabel)
	}
}

// The rendered page carries every section and links every week; the week
// pages, in turn, no longer carry the awards — they live here once.
func TestRenderSeason(t *testing.T) {
	b, st := seasonFixture()
	awards := &SeasonAwards{ThroughWeek: 23, Categories: []SeasonAwardCategory{{AwardName: AwardHighestScore, Teams: []SeasonAwardTeam{{TeamID: "pfaadt", TeamName: "Pfaadt Wood Kings", Count: 7}}}}}
	p := BuildSeasonPage("2026", st, b, awards, nil, time.Time{})
	nav := []WeekLink{{WeekNumber: 22, WeekLabel: "Week 22", Filename: "week-22.html"}, {WeekLabel: "Season", Filename: "season.html", IsCurrent: true}}
	var buf bytes.Buffer
	if err := RenderSeason(&buf, p, nav); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Pfaadt Wood Kings", "42", "jimmydyl", "bye", "Seed 1", "Seed 2", "TBD", "Season Awards", "week-22.html", "605", "515"} {
		if !strings.Contains(out, want) {
			t.Errorf("season page lacks %q", want)
		}
	}
	if !strings.Contains(out, "<title>") {
		t.Error("season page has no title")
	}
}

func TestRenderSite_WeekPagesNoLongerCarryAwards(t *testing.T) {
	r := sampleRecap()
	season := &SeasonAwards{ThroughWeek: 5, Categories: []SeasonAwardCategory{{AwardName: AwardHighestScore, Teams: []SeasonAwardTeam{{TeamID: "t1", TeamName: "Some Team", Count: 3}}}}}
	var buf bytes.Buffer
	if err := RenderSite(&buf, r, []WeekLink{{WeekNumber: 5, WeekLabel: "Week 5", Filename: "week-05.html", IsCurrent: true}}, season); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Season Awards") {
		t.Error("week page still renders the season awards; they belong on season.html")
	}
}
