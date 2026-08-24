package availablepool

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

func now() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }

// TestSignConvention pins the meaning the client renders as a green up-arrow.
//
// HKB's two raw fields are computed with OPPOSITE formulas (verified against
// the live feed 2026-08-24):
//
//	rankChange30Days  == rankHistory30Days[0] - rank
//	valueChange30Days == value - valueHistory30Days[0]
//
// Both mean "positive is good" only because rank is inverted. The natural
// future cleanup — making them symmetrical — would flip rank and turn every
// riser badge into a confident lie on a screen whose whole purpose is spotting
// risers. That is what this test exists to stop.
func TestSignConvention(t *testing.T) {
	// A climber: rank improved 300 -> 250, value gained 400 -> 500.
	climber := hkb.Player{
		Name: "Climber", Value: 500, Rank: 250,
		RankChange30Days:   50,  // history[0] - rank == 300 - 250
		ValueChange30Days:  100, // value - history[0] == 500 - 400
		RankHistory30Days:  []int{300, 275, 250},
		ValueHistory30Days: []int{400, 450, 500},
	}
	// A faller: rank worsened 250 -> 300, value lost 500 -> 400.
	faller := hkb.Player{
		Name: "Faller", Value: 400, Rank: 300,
		RankChange30Days:   -50,
		ValueChange30Days:  -100,
		RankHistory30Days:  []int{250, 275, 300},
		ValueHistory30Days: []int{500, 450, 400},
	}

	for _, tc := range []struct {
		p          hkb.Player
		wantRankUp bool
	}{{climber, true}, {faller, false}} {
		// The raw fields must agree with the formulas above, or the fixture
		// itself has drifted from the feed this contract was measured on.
		if got := tc.p.RankHistory30Days[0] - tc.p.Rank; got != tc.p.RankChange30Days {
			t.Fatalf("%s: rank formula drifted: history[0]-rank=%d, field=%d",
				tc.p.Name, got, tc.p.RankChange30Days)
		}
		if got := tc.p.Value - tc.p.ValueHistory30Days[0]; got != tc.p.ValueChange30Days {
			t.Fatalf("%s: value formula drifted: value-history[0]=%d, field=%d",
				tc.p.Name, got, tc.p.ValueChange30Days)
		}

		rank, value := normaliseChanges(tc.p)
		if (rank > 0) != tc.wantRankUp {
			t.Errorf("%s: rank_change_30d=%d, want positive==%v (positive MUST mean climbed)",
				tc.p.Name, rank, tc.wantRankUp)
		}
		if (value > 0) != tc.wantRankUp {
			t.Errorf("%s: value_change_30d=%d, want positive==%v (positive MUST mean gained)",
				tc.p.Name, value, tc.wantRankUp)
		}
	}
}

// TestSegmentsAreExhaustive is the assertion that can actually fail.
//
// The rule this replaced — mlb = Level=="MLB", prospects = Prospect &&
// Level!="MLB" — was disjoint by construction, so a "no player in both arrays"
// test could never fire. Meanwhile 67 live players were Prospect==false with
// Level!="MLB" and appeared in neither array: major leaguers optioned or
// rehabbing, whose HKB Level reads AAA/AA. Quinn Priester (307) was the
// highest-value casualty.
func TestSegmentsAreExhaustive(t *testing.T) {
	hkbPlayers := []hkb.Player{
		{Name: "Quinn Priester", Value: 307, Rank: 400, Level: "AAA", Prospect: false, Team: "MIL"},
		{Name: "Kendry Rojas", Value: 406, Rank: 368, Level: "MLB", Prospect: true, Team: "MIN"},
		{Name: "Aiva Arquette", Value: 524, Rank: 323, Level: "AA", Prospect: true, Team: "MIA"},
		{Name: "Kyle Manzardo", Value: 391, Rank: 373, Level: "MLB", Prospect: false, Team: "CLE"},
	}
	pool := []PoolPlayer{
		{ID: "1", Name: "Quinn Priester", MLBTeam: "MIL", FantasyStatus: "FA"},
		{ID: "2", Name: "Kendry Rojas", MLBTeam: "MIN", FantasyStatus: "FA", MinorsEligible: true},
		{ID: "3", Name: "Aiva Arquette", MLBTeam: "MIA", FantasyStatus: "FA", MinorsEligible: true},
		{ID: "4", Name: "Kyle Manzardo", MLBTeam: "CLE", FantasyStatus: "FA"},
	}

	got := Build(now(), "2026-08-24", pool, hkbPlayers)

	if n := got.Counts.MLB + got.Counts.Prospects; n != got.Counts.Matched {
		t.Errorf("segments do not sum to matched: %d+%d != %d",
			got.Counts.MLB, got.Counts.Prospects, got.Counts.Matched)
	}
	if got.Counts.Matched != len(pool) {
		t.Errorf("matched=%d, want all %d unowned players placed in a segment",
			got.Counts.Matched, len(pool))
	}
	// Priester is the specific regression: not a prospect, not at MLB level.
	if !hasPlayer(got.MLB, "Quinn Priester") {
		t.Error("Quinn Priester (AAA, prospect=false) must land in mlb, not vanish")
	}
	// A called-up prospect is a prospect, wherever he is currently playing.
	if !hasPlayer(got.Prospects, "Kendry Rojas") {
		t.Error("Kendry Rojas (MLB level, prospect=true) must land in prospects")
	}
}

// TestNamesakeGuard covers the confident-wrong-match class that hkb.FindFor
// structurally cannot see: HKB carries one row for a name Fantrax splits across
// two players, so the key is uncontested and FindFor answers confidently.
func TestNamesakeGuard(t *testing.T) {
	hkbPlayers := []hkb.Player{
		{Name: "Mason Miller", Value: 3177, Rank: 48, Level: "MLB", Team: "SD"},
		{Name: "Aiva Arquette", Value: 524, Rank: 323, Level: "AA", Prospect: true, Team: "MIA"},
	}
	pool := []PoolPlayer{
		// The owned SD closer. Present so the guard can SEE the contest — this
		// is why Build takes the full pool, not just the unowned rows.
		{ID: "05ucd", Name: "Mason Miller", MLBTeam: "SD", FantasyStatus: "Camfrost"},
		// The unowned minor leaguer who would inherit 3177.
		{ID: "06hw9", Name: "Mason Miller", MLBTeam: "KC", FantasyStatus: "FA", MinorsEligible: true},
		{ID: "3", Name: "Aiva Arquette", MLBTeam: "MIA", FantasyStatus: "FA", MinorsEligible: true},
	}

	got := Build(now(), "2026-08-24", pool, hkbPlayers)

	if hasPlayer(got.MLB, "Mason Miller") || hasPlayer(got.Prospects, "Mason Miller") {
		t.Fatal("the unowned Mason Miller inherited the closer's value; guard did not fire")
	}
	if got.Counts.NamesakeDeclined != 1 {
		t.Errorf("namesake_declined=%d, want 1", got.Counts.NamesakeDeclined)
	}
	if len(got.Declined) != 1 || got.Declined[0].WouldBeValue != 3177 {
		t.Errorf("declined=%+v, want one entry carrying the 3177 blast radius", got.Declined)
	}
}

// TestOwnedPlayersAreNeverEmitted is the failure that costs a real roster move.
func TestOwnedPlayersAreNeverEmitted(t *testing.T) {
	hkbPlayers := []hkb.Player{{Name: "Owned Guy", Value: 900, Rank: 100, Level: "MLB", Team: "NYY"}}
	pool := []PoolPlayer{{ID: "1", Name: "Owned Guy", MLBTeam: "NYY", FantasyStatus: "Camfrost"}}

	got := Build(now(), "2026-08-24", pool, hkbPlayers)
	if got.Counts.Matched != 0 || len(got.MLB)+len(got.Prospects) != 0 {
		t.Errorf("owned player emitted as available: %+v", got)
	}
}

// TestHKBPlayerWithNoPoolRowIsUnreachable: absence of a pool row is absence of
// evidence that the player is unowned.
func TestHKBPlayerWithNoPoolRowIsUnreachable(t *testing.T) {
	hkbPlayers := []hkb.Player{{Name: "Ghost", Value: 5000, Rank: 1, Level: "MLB", Team: "LAD"}}
	got := Build(now(), "2026-08-24", nil, hkbPlayers)
	if len(got.MLB)+len(got.Prospects) != 0 {
		t.Errorf("emitted an HKB player with no pool row: %+v", got)
	}
}

func TestParseStatusStripsMarkup(t *testing.T) {
	// The live value on 2026-08-24, verbatim. 20 players carried it.
	status, clears := ParseStatus("W <small>(Tue)</small>")
	if status != "W" {
		t.Errorf("status=%q, want %q", status, "W")
	}
	if clears != "Tue" {
		t.Errorf("waiver_clears_on=%q, want %q", clears, "Tue")
	}
	if s, c := ParseStatus("FA"); s != "FA" || c != "" {
		t.Errorf("ParseStatus(FA)=(%q,%q), want (FA,\"\")", s, c)
	}
	if s, _ := ParseStatus(""); s != "FA" {
		t.Errorf("empty status must normalise to FA, got %q", s)
	}
}

func TestFirstRankedHandlesTheZeroSentinel(t *testing.T) {
	// Conor Essenburg's real shape: unranked for 8 days, then 848 -> 674.
	if got := firstRanked([]int{0, 0, 0, 0, 0, 0, 0, 0, 848, 674}); got != 8 {
		t.Errorf("firstRanked=%d, want 8 (first day actually ranked)", got)
	}
	if got := firstRanked([]int{300, 275, 250}); got != 0 {
		t.Errorf("firstRanked=%d, want 0 for a fully-ranked history", got)
	}
	// Never ranked: slicing from len yields an empty series. Returning 0 would
	// assert "ranked since day one" for a player never ranked at all.
	if got := firstRanked([]int{0, 0, 0}); got != 3 {
		t.Errorf("firstRanked=%d, want 3 (len) for an all-zero history", got)
	}
}

func TestIsUnownedMatchesWaiversDefinition(t *testing.T) {
	for _, s := range []string{"FA", "", "W", "W <small>(Tue)</small>"} {
		if !IsUnowned(s) {
			t.Errorf("IsUnowned(%q)=false, want true", s)
		}
	}
	for _, s := range []string{"Camfrost", "NIXON", "JZ"} {
		if IsUnowned(s) {
			t.Errorf("IsUnowned(%q)=true, want false", s)
		}
	}
}

// TestFantraxPosIsSeparateFromHKBPos: the two are different facts. HKB says
// what a player is; Fantrax says where this league will let him play, which is
// the question a pickup screen actually asks.
func TestFantraxPosIsSeparateFromHKBPos(t *testing.T) {
	hkbPlayers := []hkb.Player{
		{Name: "Flex Guy", Value: 500, Rank: 300, Level: "AAA", Prospect: true,
			Team: "MIL", Positions: []string{"SS"}, ActiveLevels: "MLB/AAA"},
	}
	pool := []PoolPlayer{{
		ID: "1", Name: "Flex Guy", MLBTeam: "MIL", FantasyStatus: "FA",
		MinorsEligible: true,
		// Markup guarded even though this league's pool does not currently
		// emit it — upstream documents the field as HTML, and this value lands
		// in a durable artifact.
		Positions: "<b>SS</b>,INF,UT",
	}}

	got := Build(now(), "2026-08-24", pool, hkbPlayers)
	if len(got.Prospects) != 1 {
		t.Fatalf("want 1 prospect, got %d", len(got.Prospects))
	}
	p := got.Prospects[0]
	if want := []string{"SS", "INF", "UT"}; !equalStrings(p.FantraxPos, want) {
		t.Errorf("fantrax_pos=%v, want %v (markup stripped, comma split)", p.FantraxPos, want)
	}
	if want := []string{"SS"}; !equalStrings(p.Pos, want) {
		t.Errorf("pos=%v, want %v (HKB's own list, unchanged)", p.Pos, want)
	}
	if p.ActiveLevels != "MLB/AAA" {
		t.Errorf("active_levels=%q, want %q", p.ActiveLevels, "MLB/AAA")
	}
}

// TestActiveLevelsSurvivesAnMLBLevel is the Waldschmidt/Pratt case: a client
// badging "anything whose level is not MLB" shows nothing for a shuttling
// player, and these sit at the top of the mlb segment.
func TestActiveLevelsSurvivesAnMLBLevel(t *testing.T) {
	hkbPlayers := []hkb.Player{
		{Name: "Ryan Waldschmidt", Value: 1369, Rank: 134, Level: "MLB",
			Prospect: false, Team: "AZ", ActiveLevels: "MLB/AAA"},
	}
	pool := []PoolPlayer{{ID: "06dlz", Name: "Ryan Waldschmidt", MLBTeam: "AZ", FantasyStatus: "FA"}}

	got := Build(now(), "2026-08-24", pool, hkbPlayers)
	if len(got.MLB) != 1 {
		t.Fatalf("want 1 mlb row, got %d", len(got.MLB))
	}
	if got.MLB[0].Level != "MLB" || got.MLB[0].ActiveLevels != "MLB/AAA" {
		t.Errorf("level=%q active_levels=%q; a level of MLB must not erase the shuttling marker",
			got.MLB[0].Level, got.MLB[0].ActiveLevels)
	}
}

func TestParsePositionsHandlesAbsence(t *testing.T) {
	if got := parsePositions(""); got != nil {
		t.Errorf("parsePositions(\"\")=%v, want nil — absent, not an empty position", got)
	}
	if got := parsePositions("SP"); !equalStrings(got, []string{"SP"}) {
		t.Errorf("parsePositions(SP)=%v, want [SP]", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasPlayer(ps []Player, name string) bool {
	for _, p := range ps {
		if p.Name == name {
			return true
		}
	}
	return false
}
