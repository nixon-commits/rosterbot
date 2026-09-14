package fantrax

import (
	"errors"
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
)

func team(id, name string) auth_client.PlayoffSlot {
	return auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotTeam, TeamID: id, TeamName: name}
}

// bracket2026 mirrors the live 2026-09-14 capture: Round 1 has two byes and two
// played games, Round 2 is drawn but unplayed, the final is undrawn seeds.
func bracket2026() *auth_client.PlayoffBracket {
	seed := func(n int) auth_client.PlayoffSlot {
		return auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotSeed, Seed: n}
	}
	bye := auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotBye}
	return &auth_client.PlayoffBracket{Rounds: []auth_client.PlayoffRound{
		{Number: 1, Caption: "Round 1", ScoringPeriod: 23, StartDate: d(2026, 9, 7), EndDate: d(2026, 9, 13),
			Matchups: []auth_client.PlayoffMatchup{
				{Home: team("pfaadt", "Pfaadt Wood Kings"), Away: bye},
				{Home: team("jimmy", "jimmydyl"), Away: team("balk", "Intentional Balk"), HomeScore: 605, AwayScore: 515, Scored: true},
				{Home: team("yordan", "Yordan's"), Away: bye},
				{Home: team("houston", "Houston"), Away: team("vora", "Voradakis"), HomeScore: 796, AwayScore: 663, Scored: true},
			}},
		{Number: 2, Caption: "Round 2", ScoringPeriod: 24, StartDate: d(2026, 9, 14), EndDate: d(2026, 9, 20),
			Matchups: []auth_client.PlayoffMatchup{
				{Home: team("pfaadt", "Pfaadt Wood Kings"), Away: team("jimmy", "jimmydyl"), Scored: true},
				{Home: team("yordan", "Yordan's"), Away: team("houston", "Houston"), Scored: true},
			}},
		{Number: 3, Caption: "Final Round", ScoringPeriod: 25, StartDate: d(2026, 9, 21), EndDate: d(2026, 9, 27),
			Matchups: []auth_client.PlayoffMatchup{{Home: seed(1), Away: seed(2)}}},
	}, ChampionLabel: "Champion"}
}

// Each playoff round is a weekly scoring period on the same axis as the
// regular season, flagged so consumers that must treat it differently can.
func TestPlayoffPeriods_OnePerRoundFlaggedPlayoff(t *testing.T) {
	got := playoffPeriods(bracket2026())
	if len(got) != 3 {
		t.Fatalf("periods = %d, want 3", len(got))
	}
	want := []struct {
		n       WeeklyPeriod
		caption string
		start   time.Time
		end     time.Time
	}{
		{23, "Playoffs - Round 1", d(2026, 9, 7), d(2026, 9, 13)},
		{24, "Playoffs - Round 2", d(2026, 9, 14), d(2026, 9, 20)},
		{25, "Playoffs - Round 3", d(2026, 9, 21), d(2026, 9, 27)},
	}
	for i, w := range want {
		p := got[i]
		if p.Number != w.n || p.Caption != w.caption || !p.StartDate.Equal(w.start) || !p.EndDate.Equal(w.end) || !p.Playoff {
			t.Errorf("period %d = %+v, want {%d %q %s..%s playoff}", i, p, w.n, w.caption, w.start.Format("01-02"), w.end.Format("01-02"))
		}
	}
}

// Only pairings with a real team on BOTH sides become matchups: a bye is a
// week with no opponent and an undrawn seed is not a matchup yet. The date is
// the round's start in the SCHEDULE view's own format so the week-grouping
// code needs no special case.
func TestPlayoffMatchups_RealPairingsOnlyWithScoresAndStartDate(t *testing.T) {
	got := playoffMatchups(bracket2026())
	var drawn, pending int
	for _, m := range got {
		if m.AwayTeam.TeamID == "" {
			pending++
		} else {
			drawn++
		}
	}
	// 2 drawn in round 1, 2 in round 2; the undrawn final (seed placeholders)
	// yields one PENDING row per team still alive out of round 2 — all four,
	// since round 2 is unscored — see TestPlayoffMatchups_UndrawnRoundKeepsAdvancersAlive.
	if drawn != 4 || pending != 4 {
		t.Fatalf("matchups = %d drawn + %d pending, want 4 + 4", drawn, pending)
	}
	r1 := got[0]
	if r1.ScoringPeriod != 23 || r1.Date != "Mon Sep 7, 2026" {
		t.Errorf("round 1 matchup = period %d date %q, want 23 / Mon Sep 7, 2026", r1.ScoringPeriod, r1.Date)
	}
	if r1.AwayTeam.TeamID != "balk" || r1.AwayTeam.Total != 515 || r1.HomeTeam.TeamID != "jimmy" || r1.HomeTeam.Total != 605 {
		t.Errorf("round 1 matchup = %+v, want balk 515 @ jimmy 605", r1)
	}
	for _, m := range got {
		if m.ScoringPeriod == 25 && m.AwayTeam.TeamID != "" {
			t.Errorf("undrawn final produced a drawn pairing: %+v", m)
		}
	}
}

// Entries are what the recap renders, and a bye is a fact about the week
// that the recap must be able to show, so byes are entries with the home side
// only and the flag set; undrawn seeds are still nothing.
func TestPlayoffEntries_IncludeByesFlagged(t *testing.T) {
	got := playoffEntries(bracket2026())
	var byes, games int
	for _, e := range got {
		if !e.Playoff {
			t.Errorf("playoff entry not flagged: %+v", e)
		}
		if e.Bye {
			byes++
			if e.AwayID != "" || e.HomeID == "" {
				t.Errorf("bye entry should carry the home side only: %+v", e)
			}
		} else {
			games++
		}
	}
	if byes != 2 || games != 4 {
		t.Errorf("byes=%d games=%d, want 2 and 4", byes, games)
	}
}

func stubSchedule(t *testing.T, periods []ScoringPeriod, teams map[string]string) {
	t.Helper()
	orig := fetchScheduleFn
	fetchScheduleFn = func(*Client) (scheduleInfo, error) {
		return scheduleInfo{Periods: periods, Teams: teams, Logos: map[string]string{}}, nil
	}
	t.Cleanup(func() { fetchScheduleFn = orig })
}

var regularSeason = []ScoringPeriod{
	{Number: 21, Caption: "Scoring Period 21", StartDate: d(2026, 8, 24), EndDate: d(2026, 8, 30)},
	{Number: 22, Caption: "Scoring Period 22", StartDate: d(2026, 8, 31), EndDate: d(2026, 9, 6)},
}

// The weekly period list a consumer sees is the regular season followed by
// the bracket, in one list, so FindCurrentPeriod / FindJustEndedPeriod see
// playoff weeks without any change.
func TestGetScoringPeriodsAndTeams_AppendsPlayoffRounds(t *testing.T) {
	c := &Client{leagueID: "lg1"}
	stubSchedule(t, regularSeason, map[string]string{"balk": "Intentional Balk"})
	stubPlayoffFetch(t, func(*Client) (*auth_client.PlayoffBracket, error) { return bracket2026(), nil })

	periods, teams, _, err := c.GetScoringPeriodsAndTeams()
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 5 || periods[2].Number != 23 || !periods[2].Playoff || periods[1].Playoff {
		t.Fatalf("periods = %+v, want 21, 22 then playoff 23-25", periods)
	}
	if teams["balk"] != "Intentional Balk" {
		t.Errorf("teams lost through the merge: %v", teams)
	}
	if p := FindCurrentPeriod(periods, d(2026, 9, 15)); p == nil || p.Number != 24 {
		t.Errorf("FindCurrentPeriod(09-15) = %+v, want round 2 (period 24)", p)
	}
	s, e, err := c.GetSeasonDateRange()
	if err != nil || !e.Equal(d(2026, 9, 27)) || !s.Equal(d(2026, 8, 24)) {
		t.Errorf("season range = %s..%s (%v), want ..2026-09-27", s.Format("01-02"), e.Format("01-02"), err)
	}
}

// A league with no playoffs configured is the regular season alone, not an
// error: the fork's ErrNoPlayoffTree is the "none" signal.
func TestGetScoringPeriodsAndTeams_NoPlayoffsIsRegularSeasonOnly(t *testing.T) {
	c := &Client{leagueID: "lg1"}
	stubSchedule(t, regularSeason, nil)
	stubPlayoffFetch(t, func(*Client) (*auth_client.PlayoffBracket, error) {
		return nil, errors.Join(auth_client.ErrNoPlayoffTree, errors.New("(view \"PLAYOFFS\")"))
	})
	periods, _, _, err := c.GetScoringPeriodsAndTeams()
	if err != nil || len(periods) != 2 {
		t.Fatalf("periods = %d (%v), want the 2 regular-season periods and no error", len(periods), err)
	}
}

// Any other bracket failure is a failure: silently dropping the bracket is the
// exact bug this exists to fix (the season would end at the regular season).
func TestGetScoringPeriodsAndTeams_BracketFetchFailureIsAnError(t *testing.T) {
	c := &Client{leagueID: "lg1"}
	stubSchedule(t, regularSeason, nil)
	stubPlayoffFetch(t, func(*Client) (*auth_client.PlayoffBracket, error) { return nil, errors.New("fantrax 524") })
	if _, _, _, err := c.GetScoringPeriodsAndTeams(); err == nil {
		t.Fatal("want an error when the bracket fetch fails, got nil")
	}
}

func stubAllMatchups(t *testing.T, ms []auth_client.Matchup) {
	t.Helper()
	orig := fetchAllMatchupsFn
	fetchAllMatchupsFn = func(*Client) (*auth_client.AllMatchupsResult, error) {
		return &auth_client.AllMatchupsResult{Matchups: ms}, nil
	}
	t.Cleanup(func() { fetchAllMatchupsFn = orig })
}

// The team-scoped week lookups and the recap's entries both read the merged
// list: a playoff team has a matchup week for its round, a team that is out
// has none, and the recap can see the byes.
func TestGetAllMatchupEntries_MergesPlayoffPairingsAndByes(t *testing.T) {
	c := &Client{leagueID: "lg1", teamID: "balk"}
	stubAllMatchups(t, []auth_client.Matchup{
		{ScoringPeriod: 22, Date: "Mon Aug 31, 2026", AwayTeam: auth_client.MatchTeam{TeamID: "balk"}, HomeTeam: auth_client.MatchTeam{TeamID: "vora"}},
	})
	stubPlayoffFetch(t, func(*Client) (*auth_client.PlayoffBracket, error) { return bracket2026(), nil })

	entries, err := c.GetAllMatchupEntries()
	if err != nil {
		t.Fatal(err)
	}
	var byes, playoff, regular int
	for _, e := range entries {
		switch {
		case e.Bye:
			byes++
		case e.Playoff:
			playoff++
		default:
			regular++
		}
	}
	if regular != 1 || playoff != 4 || byes != 2 {
		t.Errorf("regular=%d playoff=%d byes=%d, want 1/4/2", regular, playoff, byes)
	}

	// Round 1 is a matchup week for this (eliminated-after-round-1) team...
	ws, we, err := c.GetMatchupWeekBounds(d(2026, 9, 10), d(2026, 3, 25))
	if err != nil || !ws.Equal(d(2026, 9, 7)) || !we.Equal(d(2026, 9, 13)) {
		t.Errorf("round 1 bounds = %s..%s (%v), want 09-07..09-13", ws.Format("01-02"), we.Format("01-02"), err)
	}
	// ...and round 2 is not.
	if ws, _, _ := c.GetMatchupWeekBounds(d(2026, 9, 15), d(2026, 3, 25)); !ws.IsZero() {
		t.Errorf("round 2 bounds for an eliminated team = %s, want none", ws.Format("01-02"))
	}
}

// A regular-season week 22 opponent who is also the Round 1 opponent must
// not merge the two weeks into one fortnight: grouping is per scoring period.
func TestMatchupWeekRanges_BackToBackSameOpponentAreSeparateWeeks(t *testing.T) {
	ms := []auth_client.Matchup{
		{ScoringPeriod: 22, Date: "Mon Aug 31, 2026", AwayTeam: auth_client.MatchTeam{TeamID: "balk"}, HomeTeam: auth_client.MatchTeam{TeamID: "jimmy"}},
		{ScoringPeriod: 23, Date: "Mon Sep 7, 2026", AwayTeam: auth_client.MatchTeam{TeamID: "balk"}, HomeTeam: auth_client.MatchTeam{TeamID: "jimmy"}},
	}
	got := matchupWeekRanges(ms, "balk")
	if len(got) != 2 {
		t.Fatalf("ranges = %+v, want two separate weeks", got)
	}
	if !got[0].end.Equal(d(2026, 9, 6)) || !got[1].start.Equal(d(2026, 9, 7)) {
		t.Errorf("ranges = %s..%s, %s..%s; want 08-31..09-06 and 09-07..09-13",
			got[0].start.Format("01-02"), got[0].end.Format("01-02"), got[1].start.Format("01-02"), got[1].end.Format("01-02"))
	}
}

type fixedWeekBounder struct{ ws, we time.Time }

func (f fixedWeekBounder) GetMatchupWeekBounds(_, _ time.Time) (time.Time, time.Time, error) {
	return f.ws, f.we, nil
}

// "No matchup week" is an ordinary answer during the bracket (a bye, or a
// team that is out), so callers must be able to test for it with errors.Is
// rather than parse a message.
func TestLastCompletedMatchupWeek_NoWeekIsTyped(t *testing.T) {
	_, _, err := LastCompletedMatchupWeek(fixedWeekBounder{}, d(2026, 3, 25), d(2026, 9, 21))
	if !errors.Is(err, ErrNoMatchupWeek) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNoMatchupWeek)", err)
	}
}

// The league-wide sibling of LastCompletedMatchupWeek: the latest weekly
// period that ended strictly before today, from the periods list rather than
// one team's schedule — a recap is league-scoped and must not depend on
// whether the operator's team is still alive.
func TestLastCompletedPeriod(t *testing.T) {
	periods := append(append([]ScoringPeriod{}, regularSeason...), playoffPeriods(bracket2026())...)
	cases := []struct {
		today time.Time
		want  WeeklyPeriod
	}{
		{d(2026, 9, 14), 23}, // Monday after round 1
		{d(2026, 9, 13), 22}, // round 1's last day is not over
		{d(2026, 9, 28), 25},
		{d(2026, 10, 15), 25},
	}
	for _, tc := range cases {
		got := LastCompletedPeriod(periods, tc.today)
		if got == nil || got.Number != tc.want {
			t.Errorf("LastCompletedPeriod(%s) = %+v, want period %d", tc.today.Format("01-02"), got, tc.want)
		}
	}
	if got := LastCompletedPeriod(periods, d(2026, 8, 25)); got != nil {
		t.Errorf("before any period ended: got %+v, want nil", got)
	}
}
