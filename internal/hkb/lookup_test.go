package hkb

import "testing"

// The four keys that actually collide in the live HKB feed, measured
// 2026-08-14 (1752 players, 1748 distinct normalized keys). These are the
// fixtures because they are the real thing, including the two shapes that a
// single signal cannot separate: `luis garcia` is two NYY players (team is
// useless, level settles it) and `max muncy` is two MLB players (level is
// useless, team settles it).
func garciaJr() Player {
	return Player{Name: "Luis García Jr.", Team: "NYY", Level: "MLB", Age: 26.2, Value: 1142}
}
func garciaRookie() Player {
	return Player{Name: "Luis Garcia", Team: "NYY", Level: "ROOKIE_BALL", Age: 29.7, Value: 10}
}
func muncyATH() Player {
	return Player{Name: "Max Muncy", Team: "ATH", Level: "MLB", Age: 23.9, Value: 390}
}
func muncyLAD() Player {
	return Player{Name: "Max Muncy", Team: "LAD", Level: "MLB", Age: 35.9, Value: 346}
}

func TestLookup_FindReturnsAnUncontestedName(t *testing.T) {
	l := BuildLookup([]Player{garciaJr()})

	got, ok := l.Find("Luis Garcia Jr.")
	if !ok {
		t.Fatal("an uncontested name must resolve")
	}
	if got.Value != 1142 {
		t.Errorf("value = %d, want 1142", got.Value)
	}
}

// Find has nothing to disambiguate with, so it must decline rather than pick.
// Before rosterbot-5z7 this was last-write-wins: whichever row the scrape
// happened to emit second silently became the answer, so a rostered 1142-value
// MLB regular was published as a 10-value rookie-ball namesake — and, because a
// wrong match still counts as a match, it showed up as PERFECT join coverage.
func TestLookup_FindRefusesToPickBetweenNamesakes(t *testing.T) {
	l := BuildLookup([]Player{garciaJr(), garciaRookie()})

	if got, ok := l.Find("Luis Garcia Jr."); ok {
		t.Errorf("Find picked %s (%s, value %d) with nothing to go on", got.Name, got.Level, got.Value)
	}
}

// Two NYY players: the team hint cannot separate them, the minors flag can.
// This is the case that is live in production — Luis Garcia Jr. is rostered.
func TestLookup_FindForSeparatesNamesakesOnTheSameClub(t *testing.T) {
	l := BuildLookup([]Player{garciaJr(), garciaRookie()})

	got, ok := l.FindFor("Luis Garcia Jr.", Hint{MLBTeam: "NYY", MinorsEligible: false})
	if !ok {
		t.Fatal("an MLB player on a colliding key should resolve from the minors flag")
	}
	if got.Value != 1142 {
		t.Errorf("value = %d, want 1142 (the MLB regular, not the rookie-ball namesake)", got.Value)
	}
}

// Two active MLB players: the minors flag cannot separate them, the club can.
func TestLookup_FindForSeparatesNamesakesAtTheSameLevel(t *testing.T) {
	l := BuildLookup([]Player{muncyATH(), muncyLAD()})

	got, ok := l.FindFor("Max Muncy", Hint{MLBTeam: "LAD", MinorsEligible: false})
	if !ok {
		t.Fatal("two MLB namesakes on different clubs should resolve from the club")
	}
	if got.Value != 346 {
		t.Errorf("value = %d, want 346 (the LAD Muncy)", got.Value)
	}
}

// The point of the whole design: when the hint does not settle it, say so
// rather than guessing. An unknown renders as unknown and shows up in the
// Matched column; a guess renders as a confident number that nothing flags.
func TestLookup_FindForRefusesWhenTheHintCannotSettleIt(t *testing.T) {
	l := BuildLookup([]Player{muncyATH(), muncyLAD()})

	if got, ok := l.FindFor("Max Muncy", Hint{MinorsEligible: false}); ok {
		t.Errorf("resolved to %s (%s) with no club to go on", got.Name, got.Team)
	}
}

// A hint pointing at a club neither namesake plays for is stale or wrong, which
// is not evidence for either of them.
func TestLookup_FindForRefusesAHintMatchingNoRow(t *testing.T) {
	l := BuildLookup([]Player{muncyATH(), muncyLAD()})

	if got, ok := l.FindFor("Max Muncy", Hint{MLBTeam: "BOS", MinorsEligible: false}); ok {
		t.Errorf("resolved to %s (%s) on a hint matching neither row", got.Name, got.Team)
	}
}

// An uncontested name needs no disambiguation and must not be made to earn it —
// most callers pass a hint for every player, and the overwhelming majority of
// names have exactly one row.
func TestLookup_FindForIgnoresTheHintWhenThereIsNothingToResolve(t *testing.T) {
	l := BuildLookup([]Player{garciaJr()})

	got, ok := l.FindFor("Luis Garcia Jr.", Hint{MLBTeam: "SEA", MinorsEligible: true})
	if !ok || got.Value != 1142 {
		t.Errorf("FindFor = (%d, %v), want the only row (1142, true) regardless of hint", got.Value, ok)
	}
}

// Measured 2026-08-14 against both live feeds: 30 of 32 clubs use identical
// abbreviations and exactly two disagree. Raw string equality would therefore
// silently never match for Arizona or White Sox players — the same shape of
// silent join failure as the collision this file exists to fix.
func TestSameTeam_BridgesTheTwoDisagreeingAbbreviations(t *testing.T) {
	for _, tc := range []struct {
		hkb, fantrax string
		want         bool
	}{
		{"NYY", "NYY", true},
		{"AZ", "ARI", true},  // Arizona
		{"CWS", "CHW", true}, // White Sox
		{"ARI", "AZ", true},  // and the reverse, since neither side owns "the" spelling
		{"NYY", "BOS", false},
		{"FA", "NYY", false}, // HKB's free-agent marker names no club
		{"", "NYY", false},
		{"NYY", "", false},
	} {
		if got := sameTeam(tc.hkb, tc.fantrax); got != tc.want {
			t.Errorf("sameTeam(%q, %q) = %v, want %v", tc.hkb, tc.fantrax, got, tc.want)
		}
	}
}

// The rule Jon set (2026-08-14): two lines of evidence naming different players
// is not a tie to be broken, it is a statement that we do not know who this is.
// Club says the AAA row, level says the MLB row — refuse rather than pick a
// winner between them.
func TestLookup_FindForRefusesWhenClubAndLevelDisagree(t *testing.T) {
	rows := []Player{
		{Name: "Split Case", Team: "NYY", Level: "AAA", Value: 900},
		{Name: "Split Case", Team: "BOS", Level: "MLB", Value: 40},
	}
	l := BuildLookup(rows)

	if got, ok := l.FindFor("Split Case", Hint{MLBTeam: "NYY", MinorsEligible: false}); ok {
		t.Errorf("resolved to %s (%s/%s) although club and level name different rows",
			got.Name, got.Team, got.Level)
	}
}

// The 16% case, protected: a minors-ELIGIBLE player may well be at MLB (Volpe,
// Domínguez, Hyeseong Kim were all measured that way), so the level signal must
// not be read backwards to prefer the minor-league row. With the club unable to
// separate two same-club namesakes, the honest answer is that we do not know.
func TestLookup_FindForWillNotPickTheMinorLeagueRowForAnEligiblePlayer(t *testing.T) {
	rows := []Player{
		{Name: "Called Up", Team: "NYY", Level: "MLB", Value: 900},
		{Name: "Called Up", Team: "NYY", Level: "LOW_A", Value: 10},
	}
	l := BuildLookup(rows)

	if got, ok := l.FindFor("Called Up", Hint{MLBTeam: "NYY", MinorsEligible: true}); ok {
		t.Errorf("resolved to %s (%s, value %d) — minors eligibility is not a location",
			got.Name, got.Level, got.Value)
	}
}

// Both signals available and pointing the same way is the easy case, and it
// must still resolve — a rule that only ever refuses would be safe and useless.
func TestLookup_FindForResolvesWhenBothSignalsAgree(t *testing.T) {
	rows := []Player{
		{Name: "Agreed", Team: "NYY", Level: "MLB", Value: 900},
		{Name: "Agreed", Team: "BOS", Level: "LOW_A", Value: 10},
	}
	l := BuildLookup(rows)

	got, ok := l.FindFor("Agreed", Hint{MLBTeam: "NYY", MinorsEligible: false})
	if !ok || got.Value != 900 {
		t.Errorf("FindFor = (%d, %v), want (900, true)", got.Value, ok)
	}
}
