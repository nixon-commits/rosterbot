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

// ---- Near-miss path (resolveMiss) -----------------------------------------
// Fixtures are the real 2026-08-17 misses. Latz and Thornton are name variants
// on the same club; Rodriguez-Cruz is a compound surname Fantrax truncates.

func latzHKB() Player {
	return Player{Name: "Jacob Latz", Team: "TEX", Level: "MLB", Value: 238}
}
func rodriguezCruzHKB() Player {
	return Player{Name: "Elmer Rodriguez-Cruz", Team: "NYY", Level: "MLB", Value: 516}
}

// An aliased name is the whole point of the path: HKB spells him "Jacob", the
// Fantrax pool spells him "Jake", and no amount of normalizing closes that.
func TestLookup_FindForResolvesAnAliasedName(t *testing.T) {
	l := BuildLookup([]Player{latzHKB()})

	got, ok := l.FindFor("Jake Latz", Hint{MLBTeam: "TEX", MinorsEligible: false})
	if !ok || got.Value != 238 {
		t.Errorf("FindFor = (%d, %v), want (238, true)", got.Value, ok)
	}
}

// The hyphen is already a word break by the time Normalize is done, so this is
// a token-prefix case, not a punctuation one — "elmer rodriguez" against
// "elmer rodriguez cruz".
func TestLookup_FindForResolvesATruncatedCompoundSurname(t *testing.T) {
	l := BuildLookup([]Player{rodriguezCruzHKB()})

	got, ok := l.FindFor("Elmer Rodriguez", Hint{MLBTeam: "NYY", MinorsEligible: true})
	if !ok || got.Value != 516 {
		t.Errorf("FindFor = (%d, %v), want (516, true)", got.Value, ok)
	}
}

// The club is the evidence that makes widening defensible at all. An alias
// whose row sits on another club is stale — a trade the two feeds disagree
// about — and stale evidence resolves to a miss, never to the row anyway.
func TestLookup_FindForDeclinesAnAliasOnTheWrongClub(t *testing.T) {
	moved := latzHKB()
	moved.Team = "SEA"
	l := BuildLookup([]Player{moved})

	if got, ok := l.FindFor("Jake Latz", Hint{MLBTeam: "TEX", MinorsEligible: false}); ok {
		t.Errorf("resolved to %s on %s — the club no longer agrees", got.Name, got.Team)
	}
}

// A caller with no club has no evidence, and widening on no evidence is
// guessing. Find never reaches this path at all; FindFor with an empty MLBTeam
// must behave the same way.
func TestLookup_MissWithoutAClubHintIsNotWidened(t *testing.T) {
	l := BuildLookup([]Player{latzHKB()})

	if _, ok := l.FindFor("Jake Latz", Hint{}); ok {
		t.Error("resolved with no club hint — nothing licensed that choice")
	}
	if _, ok := l.Find("Jake Latz"); ok {
		t.Error("Find resolved a near miss — it has no hint to gate on")
	}
}

// The guard that a surname rule would have failed. HKB carries 28 (club,
// surname) pairs covering two different players; MIA meyer is one of them. Max
// Meyer is in HKB, Noble Meyer is the one we rostered, and they are not the
// same person — so this must stay unmatched rather than inherit Max's value.
func TestLookup_FindForDeclinesASameClubSurnameThatIsNotAliased(t *testing.T) {
	l := BuildLookup([]Player{{Name: "Max Meyer", Team: "MIA", Level: "MLB", Value: 400}})

	if got, ok := l.FindFor("Noble Meyer", Hint{MLBTeam: "MIA", MinorsEligible: true}); ok {
		t.Errorf("resolved to %s (value %d) — sharing a club and a surname is not identity",
			got.Name, got.Value)
	}
}

// A player HKB genuinely does not rank has no row to find under any spelling,
// and must keep reporting as unmatched — that shortfall is real information.
func TestLookup_FindForDeclinesAPlayerHKBDoesNotRank(t *testing.T) {
	l := BuildLookup([]Player{latzHKB(), rodriguezCruzHKB()})

	if _, ok := l.FindFor("Josiah Ragsdale", Hint{MLBTeam: "STL", MinorsEligible: true}); ok {
		t.Error("resolved a player with no HKB row at all")
	}
}
