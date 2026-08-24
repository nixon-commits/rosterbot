package scoring

// Cycle, no-hitter, and perfect-game are single-game bonus EVENTS, not
// counting stats — unlike TB/XBH they do not sum sensibly across multiple
// games. A HitterLine spanning a season legitimately has >=1 single, double,
// triple, and home run for nearly every regular, so folding a "has one of
// each" test into ApplyHitter's general statMap loop would award almost
// every hitter the CYC bonus on every projection/blend call. Detection
// therefore lives here as separate helpers, scoped to a single game by the
// caller — never wired into ApplyHitter/ApplyPitcher, and never called from
// the projections or live-scoring paths (rosterbot-ec1: those stay
// intentionally blind to unprojectable single-game events).

// IsCycle reports whether a single game's raw line contains at least one
// single, double, triple, and home run — hitting for the cycle. Singles use
// the same H-2B-3B-HR floored-at-zero derivation ApplyHitter already applies,
// so the two never disagree about what a "single" is.
func IsCycle(l HitterLine) bool {
	singles := l.H - l.Doubles - l.Triples - l.HR
	if singles < 0 {
		singles = 0
	}
	return singles >= 1 && l.Doubles >= 1 && l.Triples >= 1 && l.HR >= 1
}

// PitcherGameLine is the subset of one pitching outing's raw line that the
// no-hitter/perfect-game predicates need. It is deliberately separate from
// PitcherLine: Outs and BattersFaced carry no point weight of their own —
// they exist only to make these two predicates sound — so folding them into
// PitcherLine would need an explicit skip everywhere else that type is used.
type PitcherGameLine struct {
	Outs         int // outs recorded this game; >=27 means a complete 9 (or more)
	Hits         int // hits allowed
	Walks        int // walks allowed
	HitBatsmen   int // hit-by-pitch allowed
	BattersFaced int // total plate appearances faced; 0 means "not available"
	// BatterOuts is groundOuts+airOuts+strikeOuts — outs charged to a batter's
	// own plate appearance, as opposed to outs made on the bases. 0 means "not
	// available".
	BatterOuts int
}

// IsNoHitter reports a complete game (>=27 outs) with zero hits allowed. The
// modern (1991-revised) MLB definition permits walks, hit batters, and
// errors in a no-hitter — only hits allowed must be zero over the complete
// game — so Outs and Hits are the only inputs this needs, and both are
// already decoded off the same MLB statsapi game-log line the rest of the
// pitching score is built from.
//
// KNOWN, ACCEPTED IMPRECISION: a pitcher who throws nine hitless innings and
// is then relieved in extras is credited here even if the reliever gives up a
// hit, which by the official definition means the game was not a no-hitter.
// Ruling that out needs the team's line, not the pitcher's, and the case is
// vanishingly rare next to the cost of a second API call per backfilled
// player-day. Recorded rather than silently assumed away.
func IsNoHitter(l PitcherGameLine) bool {
	return l.Outs >= 27 && l.Hits == 0
}

// IsPerfectGame reports a complete game in which no batter reached base by
// any means.
//
// Hits==0 && Walks==0 && HitBatsmen==0 is necessary but NOT sufficient: a
// fielding error or catcher's interference also puts a runner on without
// touching any of those three counts, and no "reached on error" field exists
// anywhere on a pitcher's own statsapi line. Two independent identities close
// that gap, and BOTH are required.
//
// BattersFaced == Outs. Every plate appearance is a batter faced; only some
// produce an out. So BattersFaced exceeds Outs by the number of runners
// allowed, and equality means nobody reached.
//
// BatterOuts == Outs. The first identity alone is not enough, because outs are
// also made on the BASES. A runner who reaches on an error and is then erased
// on a double play contributes one batter faced and one extra out, leaving
// BattersFaced == Outs intact on a game that was a no-hitter and not a perfect
// game. BatterOuts counts only outs charged to a batter's own plate
// appearance, so it falls short of Outs exactly when an out was made on the
// bases — which is the case the first identity misses.
//
// Both verified against real statsapi data rather than reasoned about: Domingo
// German's 2023-06-28 perfect game reports outs=27, battersFaced=27,
// groundOuts+airOuts+strikeOuts=27 — all three equal. Gerrit Cole's complete
// game of 2023-04-16 reports outs=27 with batterOuts=26, i.e. exactly the
// base-running out that the BattersFaced identity cannot see.
//
// A zero BattersFaced or BatterOuts means the field was absent from the decoded
// game log, and this reports false rather than falling back to a weaker check
// that a real error would silently pass. Absence of evidence is not evidence.
func IsPerfectGame(l PitcherGameLine) bool {
	return l.Outs >= 27 &&
		l.BattersFaced > 0 && l.BattersFaced == l.Outs &&
		l.BatterOuts > 0 && l.BatterOuts == l.Outs &&
		l.Hits == 0 && l.Walks == 0 && l.HitBatsmen == 0
}
