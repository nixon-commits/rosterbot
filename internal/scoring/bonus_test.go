package scoring

import "testing"

func TestIsCycle(t *testing.T) {
	tests := []struct {
		name string
		line HitterLine
		want bool
	}{
		{
			name: "true cycle",
			// 1B = H - 2B - 3B - HR = 4 - 1 - 1 - 1 = 1
			line: HitterLine{H: 4, Doubles: 1, Triples: 1, HR: 1},
			want: true,
		},
		{
			name: "cycle plus extra hits in the same game",
			// A second single/double the same day doesn't break the cycle.
			line: HitterLine{H: 6, Doubles: 2, Triples: 1, HR: 1},
			want: true,
		},
		{
			name: "missing the triple",
			line: HitterLine{H: 3, Doubles: 1, HR: 1},
			want: false,
		},
		{
			name: "missing the single — floored singles must not read as satisfied",
			// H=3, XBH=3 → raw 1B = 0. A 2B/3B/HR without any single at all.
			line: HitterLine{H: 3, Doubles: 1, Triples: 1, HR: 1},
			want: false,
		},
		{
			name: "hitless line",
			line: HitterLine{},
			want: false,
		},
		{
			name: "a season aggregate reads as a cycle too — IsCycle trusts the caller's scope",
			// This is exactly the case the doc comment warns about: a whole
			// season's worth of hits satisfies "at least one of each" for
			// nearly every regular. IsCycle can't tell season from game — the
			// caller is responsible for only ever passing a single game's
			// line, which the fantrax backfill path does and ApplyHitter's
			// general statMap loop deliberately does not.
			line: HitterLine{H: 150, Doubles: 30, Triples: 5, HR: 20},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCycle(tt.line); got != tt.want {
				t.Errorf("IsCycle(%+v) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestIsNoHitter(t *testing.T) {
	tests := []struct {
		name string
		line PitcherGameLine
		want bool
	}{
		{
			name: "9 complete innings, zero hits",
			line: PitcherGameLine{Outs: 27, Hits: 0},
			want: true,
		},
		{
			name: "no-hitter permits walks",
			line: PitcherGameLine{Outs: 27, Hits: 0, Walks: 4, HitBatsmen: 1},
			want: true,
		},
		{
			name: "extra-innings no-hitter still counts",
			line: PitcherGameLine{Outs: 30, Hits: 0},
			want: true,
		},
		{
			name: "one hit allowed breaks it",
			line: PitcherGameLine{Outs: 27, Hits: 1},
			want: false,
		},
		{
			name: "complete game short of 9 innings (rain-shortened) doesn't qualify",
			line: PitcherGameLine{Outs: 24, Hits: 0},
			want: false,
		},
		{
			name: "8.2 innings with zero hits — pulled one out short",
			line: PitcherGameLine{Outs: 26, Hits: 0},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNoHitter(tt.line); got != tt.want {
				t.Errorf("IsNoHitter(%+v) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestIsPerfectGame(t *testing.T) {
	tests := []struct {
		name string
		line PitcherGameLine
		want bool
	}{
		{
			name: "27 up, 27 down",
			line: PitcherGameLine{Outs: 27, BattersFaced: 27, BatterOuts: 27},
			want: true,
		},
		{
			name: "extra-innings perfect game",
			line: PitcherGameLine{Outs: 30, BattersFaced: 30, BatterOuts: 30},
			want: true,
		},
		{
			name: "a walk breaks it even though BattersFaced tracking is missing",
			line: PitcherGameLine{Outs: 27, Walks: 1, BattersFaced: 0},
			want: false,
		},
		{
			name: "a hit breaks it",
			line: PitcherGameLine{Outs: 27, Hits: 1, BattersFaced: 28},
			want: false,
		},
		{
			name: "no-hitter with a walk is not a perfect game",
			line: PitcherGameLine{Outs: 27, Hits: 0, Walks: 1, BattersFaced: 28},
			want: false,
		},
		{
			name: "reached on error — zero H/BB/HBP but BattersFaced exceeds Outs",
			// This is the case the H/BB/HBP-only check cannot see: the
			// pitcher's own line looks perfect, but one extra batter was
			// faced without producing an out, meaning someone reached base
			// by a means the pitcher's stat line doesn't separately track.
			line: PitcherGameLine{Outs: 27, Hits: 0, Walks: 0, HitBatsmen: 0, BattersFaced: 28},
			want: false,
		},
		{
			name: "BattersFaced not available in the feed — conservatively false",
			line: PitcherGameLine{Outs: 27, Hits: 0, Walks: 0, HitBatsmen: 0, BattersFaced: 0},
			want: false,
		},
		{
			name: "short of a complete game",
			line: PitcherGameLine{Outs: 24, BattersFaced: 24, BatterOuts: 24},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPerfectGame(tt.line); got != tt.want {
				t.Errorf("IsPerfectGame(%+v) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestIsPerfectGame_NoHitterWithARunnerErasedOnTheBases pins the case the
// BattersFaced identity alone cannot see, and which motivated adding BatterOuts.
//
// A batter reaches on an error (no hit, no walk, no HBP — none of the three
// explicit counts moves) and the next batter grounds into a double play. That
// plate appearance produces TWO outs from ONE batter faced, so across a
// complete game battersFaced and outs both land on 27 and the first identity
// holds on a game that was emphatically not perfect.
//
// BatterOuts is what separates them: only 26 of the 27 outs were charged to a
// batter's own plate appearance. Verified against real data — Gerrit Cole's
// 2023-04-16 complete game reports outs=27 with groundOuts+airOuts+strikeOuts
// = 26, while Domingo German's 2023-06-28 perfect game reports 27 for all three.
func TestIsPerfectGame_NoHitterWithARunnerErasedOnTheBases(t *testing.T) {
	line := PitcherGameLine{
		Outs: 27, Hits: 0, Walks: 0, HitBatsmen: 0,
		BattersFaced: 27, // 25 retired + 1 reached on error + 1 double play
		BatterOuts:   26, // the double play's second out was made on the bases
	}
	if IsPerfectGame(line) {
		t.Error("a no-hitter whose only baserunner was erased on a double play is NOT a perfect game")
	}
	if !IsNoHitter(line) {
		t.Error("it is still a no-hitter — errors are permitted by the modern definition")
	}
}

// TestIsPerfectGame_MissingBatterOutsIsNotAPerfectGame pins the conservative
// default for an absent field, matching the existing BattersFaced==0 rule.
// A game log that did not decode groundOuts/airOuts cannot support the
// predicate, and guessing awards a large bonus on no evidence.
func TestIsPerfectGame_MissingBatterOutsIsNotAPerfectGame(t *testing.T) {
	line := PitcherGameLine{Outs: 27, Hits: 0, Walks: 0, HitBatsmen: 0, BattersFaced: 27, BatterOuts: 0}
	if IsPerfectGame(line) {
		t.Error("absent BatterOuts must report false, not fall back to the weaker check")
	}
}
