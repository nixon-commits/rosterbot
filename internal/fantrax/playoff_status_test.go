package fantrax

import (
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

// bracket2026Undrawn is bracket2026 as Fantrax rendered it for part of
// 2026-09-14: Round 2 shown as seed placeholders although Round 1 had
// decided who advanced. The advancers are alive; only the pairings are
// unknown.
func bracket2026Undrawn() *auth_client.PlayoffBracket {
	b := bracket2026()
	seed := func(n int) auth_client.PlayoffSlot {
		return auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotSeed, Seed: n}
	}
	b.Rounds[1].Matchups = []auth_client.PlayoffMatchup{
		{Home: seed(1), Away: seed(4)},
		{Home: seed(2), Away: seed(3)},
	}
	return b
}

// A team's standing in a round is read off the bracket: named against a
// team → alive, named against a bye → bye, absent from a drawn round after a
// recorded loss → eliminated in that earlier round, absent from every round
// → never seeded. An undrawn round cannot eliminate anyone: a team that won
// or sat out the previous round is alive in it with the opponent unknown.
func TestPlayoffStatusFor(t *testing.T) {
	drawn, undrawn := bracket2026(), bracket2026Undrawn()
	cases := []struct {
		name    string
		b       *auth_client.PlayoffBracket
		team    string
		period  int
		want    PlayoffStanding
		round   int
		opp     string
		undrawn bool
	}{
		{"alive in round 1 against a team", drawn, "balk", 23, PlayoffAlive, 1, "jimmydyl", false},
		{"bye in round 1", drawn, "pfaadt", 23, PlayoffBye, 1, "", false},
		{"eliminated after losing round 1", drawn, "balk", 24, PlayoffEliminated, 1, "jimmydyl", false},
		{"alive in a drawn round 2", drawn, "jimmy", 24, PlayoffAlive, 2, "Pfaadt Wood Kings", false},
		{"alive in an undrawn round 2 after a win", undrawn, "jimmy", 24, PlayoffAlive, 2, "", true},
		{"alive in an undrawn round 2 after a bye", undrawn, "pfaadt", 24, PlayoffAlive, 2, "", true},
		{"eliminated is still eliminated when the next round is undrawn", undrawn, "balk", 24, PlayoffEliminated, 1, "jimmydyl", false},
		{"never seeded", drawn, "bt95", 24, PlayoffNotSeeded, 0, "", false},
		{"eliminated in round 1, asked about the final", drawn, "vora", 25, PlayoffEliminated, 1, "Houston", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlayoffStatusFor(tc.b, tc.team, tc.period)
			if got.Standing != tc.want || got.Round != tc.round || got.Opponent != tc.opp || got.Undrawn != tc.undrawn {
				t.Errorf("PlayoffStatusFor(%s, %d) = %+v, want {%v round %d opp %q undrawn %v}",
					tc.team, tc.period, got, tc.want, tc.round, tc.opp, tc.undrawn)
			}
		})
	}
	if s := PlayoffStatusFor(drawn, "balk", 24); s.ScoreFor != 515 || s.ScoreAgainst != 605 {
		t.Errorf("eliminated status should carry the losing score line, got %+v", s)
	}
}

// When Fantrax renders an in-progress round as seed placeholders, the teams
// that advanced from the previous round must still have a matchup week —
// otherwise the lineup path reads "no matchup" and stops an alive team's
// lineup for the day. The row is pending (no opponent), which the recap's
// pair builder already ignores.
func TestPlayoffMatchups_UndrawnRoundKeepsAdvancersAlive(t *testing.T) {
	got := playoffMatchups(bracket2026Undrawn())
	pending := map[string]bool{}
	for _, m := range got {
		if m.ScoringPeriod == 24 {
			if m.AwayTeam.TeamID != "" {
				t.Errorf("undrawn round 2 produced a drawn row: %+v", m)
			}
			pending[m.HomeTeam.TeamID] = true
		}
	}
	for _, id := range []string{"pfaadt", "jimmy", "yordan", "houston"} {
		if !pending[id] {
			t.Errorf("advancer %s has no pending round-2 row; it would read as eliminated", id)
		}
	}
	for _, id := range []string{"balk", "vora"} {
		if pending[id] {
			t.Errorf("eliminated team %s got a round-2 row", id)
		}
	}
	ws, we := MatchupWeekBounds(got, "jimmy", d(2026, 3, 25), d(2026, 9, 15))
	if !ws.Equal(d(2026, 9, 14)) || !we.Equal(d(2026, 9, 20)) {
		t.Errorf("alive team in an undrawn round has bounds %s..%s, want 09-14..09-20", ws.Format("01-02"), we.Format("01-02"))
	}
	if ws, _ := MatchupWeekBounds(got, "balk", d(2026, 3, 25), d(2026, 9, 15)); !ws.IsZero() {
		t.Errorf("eliminated team got round-2 bounds %s", ws.Format("01-02"))
	}
	// The recap's entries never carry a pending row: an undrawn round has no
	// pairing to show.
	for _, e := range playoffEntries(bracket2026Undrawn()) {
		if e.ScoringPeriod == 24 {
			t.Errorf("undrawn round leaked into recap entries: %+v", e)
		}
	}
}
