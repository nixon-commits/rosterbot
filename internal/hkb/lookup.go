package hkb

import "github.com/nixon-commits/rosterbot/internal/playername"

// Lookup resolves a display name to an HKB row. It exists because the
// normalized join key is not unique: playername.Normalize strips diacritics and
// Jr./Sr. suffixes — which is exactly what makes the join work at all — and in
// doing so it merges genuinely different players. Measured against the live feed
// on 2026-08-14: 1752 players, 1748 distinct keys, four collisions.
//
// The map this replaced assigned unconditionally, so the last row in scrape
// order won. That is worse than a miss in a specific way: a MISS is absent and
// renders as unknown (every wire field is omitempty), while a WRONG MATCH
// renders as a confident, plausible number AND counts toward join coverage.
// On 2026-08-14 a rostered Luis García Jr. (value 1142) was being valued at 10
// — the rookie-ball namesake — while the team it belongs to reported 49/49
// players matched. Nothing anywhere said a choice had been made.
//
// This is the same failure class internal/playername documents one level down
// for MLBAM ids (claimName, the retired-namesake bug), reappearing on the HKB
// join.
type Lookup struct {
	byKey map[string][]Player
}

// Hint is what the caller knows about the Fantrax player it is looking up. It
// is deliberately only the fields a caller can supply honestly: not every call
// site has a club (tradeboard.PoolPlayer carries none), and a caller with no
// minors information must use Find rather than pass a default that asserts
// something it does not know.
type Hint struct {
	// MLBTeam is the Fantrax club abbreviation, e.g. "NYY". Empty means the
	// caller has none — not that the player is a free agent.
	MLBTeam string
	// MinorsEligible is Fantrax's own flag for the player. Callers of FindFor
	// always have it; callers that do not have it use Find.
	MinorsEligible bool
}

// BuildLookup indexes players by their normalized name, keeping every row that
// shares a key rather than letting the last one win.
func BuildLookup(players []Player) Lookup {
	m := make(map[string][]Player, len(players))
	for _, p := range players {
		k := playername.Normalize(p.Name)
		m[k] = append(m[k], p)
	}
	return Lookup{byKey: m}
}

// Find resolves a name with no disambiguating information, so it declines any
// contested key. Used by the call sites that hold only a name and a position
// string (internal/claims, internal/transactions, tradeboard's offer path).
//
// It does not simply call FindFor with a zero Hint: a zero Hint asserts
// MinorsEligible=false, i.e. "this is an MLB player", which a caller with no
// minors information has no basis to claim. Declining is the honest answer.
func (l Lookup) Find(name string) (Player, bool) {
	rows := l.byKey[playername.Normalize(name)]
	if len(rows) == 1 {
		return rows[0], true
	}
	return Player{}, false
}

// FindFor resolves a name using what the caller knows about the Fantrax player.
// An uncontested name resolves regardless of the hint; a contested one is
// handed to resolveCollision, which may still decline.
func (l Lookup) FindFor(name string, hint Hint) (Player, bool) {
	rows := l.byKey[playername.Normalize(name)]
	switch len(rows) {
	case 0:
		return Player{}, false
	case 1:
		return rows[0], true
	default:
		return resolveCollision(rows, hint)
	}
}

// Collisions reports every normalized key carrying more than one row, for
// diagnostics and for the operator-facing log line at the call sites that can
// name the players involved.
func (l Lookup) Collisions() map[string][]Player {
	out := map[string][]Player{}
	for k, rows := range l.byKey {
		if len(rows) > 1 {
			out[k] = rows
		}
	}
	return out
}

// resolveCollision picks which of several same-named HKB rows a Fantrax player
// refers to, or declines. rows always has len >= 2 — FindFor short-circuits 0
// and 1.
//
// Two independent signals are applied, each narrowing rows on its own:
//
//	SIGNAL   SETTLES   FAILS ON
//	club     3 of 4    luis garcia — two NYY players
//	level    3 of 4    max muncy — two MLB players
//	both     4 of 4    —
//
// Neither is sufficient alone, which is why this takes a hint at all instead of
// applying one fixed preference.
//
// The combination rule is: if both signals name a row and they name DIFFERENT
// rows, refuse. Two lines of evidence pointing at different players is the
// clearest possible statement that we do not know which player this is, and a
// tie-break between them would be a coin flip wearing a confident face. A
// signal that rules out EVERY row is treated the same way — a hint naming a club
// neither namesake plays for is stale or wrong, and stale evidence is not
// evidence for whoever is left.
//
// Declining is a real answer here, not a failure: the caller renders the player
// as unknown and counts them unmatched, which is visible in join coverage. A
// guess renders as a plausible number that nothing anywhere flags, which is the
// property that let this bug sit unnoticed in production.
func resolveCollision(rows []Player, hint Hint) (Player, bool) {
	clubPick, clubImpossible := narrowBy(rows, hint.MLBTeam != "", func(p Player) bool {
		return sameTeam(p.Team, hint.MLBTeam)
	})
	levelPick, levelImpossible := narrowBy(rows, levelIsUsable(hint), func(p Player) bool {
		return p.Level == levelMLB
	})

	if clubImpossible || levelImpossible {
		return Player{}, false
	}
	switch {
	case clubPick >= 0 && levelPick >= 0:
		if clubPick != levelPick {
			return Player{}, false // the two signals name different players
		}
		return rows[clubPick], true
	case clubPick >= 0:
		return rows[clubPick], true
	case levelPick >= 0:
		return rows[levelPick], true
	}
	return Player{}, false
}

// narrowBy applies one signal to rows. It reports the index of the single
// surviving row — or -1 when the signal is unavailable or leaves more than one
// candidate, i.e. abstains — and separately whether the signal excluded every
// row. That last case is a contradiction rather than an absence of information,
// and the caller refuses on it.
func narrowBy(rows []Player, available bool, keep func(Player) bool) (pick int, impossible bool) {
	if !available {
		return -1, false
	}
	pick, n := -1, 0
	for i, p := range rows {
		if keep(p) {
			n++
			pick = i
		}
	}
	switch n {
	case 0:
		return -1, true
	case 1:
		return pick, false
	default:
		return -1, false
	}
}

// levelMLB is HKB's marker for a major leaguer. The rest of its vocabulary is
// AAA, AA, HIGH_A, LOW_A, ROOKIE_BALL, and "" (19 rows in the live feed carry no
// level at all).
const levelMLB = "MLB"

// levelIsUsable gates the level signal, which is deliberately ONE-WAY: applied
// when Fantrax says the player is NOT minors eligible, and abstaining entirely
// when it says he is.
//
// The asymmetry is measured, not assumed. Cross-tabbing MinorsEligible against
// HKB Level over the 512 cleanly-resolving rostered players on 2026-08-14
// (diag_levels_test.go, run with -tags diag):
//
//	minors=false -> level MLB      396 of 404   98%
//	minors=true  -> level non-MLB   91 of 108   84%
//
// The directions are not equally trustworthy. "Not minors eligible" means an
// established major leaguer, and the 2% that miss are veterans HKB currently
// lists at AAA on a rehab or an option (Pablo López, Musgrove, Westburg).
// But minors eligibility is an ELIGIBILITY, not a location: 17 of 108 are
// prospects who have been called up and are listed at MLB (Volpe, Domínguez,
// Hyeseong Kim). Using that direction to prefer the minor-league row would
// resolve a called-up prospect against his minor-league namesake — a confident
// wrong answer of exactly the kind this change exists to remove, at a 16% rate.
//
// Residual exposure of the direction we do use: a collision where the true row
// is one of that 2%, another row IS at MLB, and the club signal cannot separate
// them. The club signal covers most of it, since a disagreement refuses.
func levelIsUsable(hint Hint) bool { return !hint.MinorsEligible }

// sameTeam reports whether an HKB club abbreviation and a Fantrax one name the
// same club.
//
// Measured 2026-08-14 across both live feeds: 30 of 32 clubs use byte-identical
// abbreviations and exactly two disagree — Arizona (HKB "AZ", Fantrax "ARI")
// and the White Sox (HKB "CWS", Fantrax "CHW"). Comparing the raw strings would
// therefore never match for those two clubs, and would do it silently, which is
// the same shape of failure as the collision itself. HKB's "FA" and the empty
// string name no club and match nothing.
func sameTeam(hkbTeam, fantraxTeam string) bool {
	if hkbTeam == "" || fantraxTeam == "" || hkbTeam == "FA" {
		return false
	}
	return canonicalTeam(hkbTeam) == canonicalTeam(fantraxTeam)
}

// canonicalTeam folds the two abbreviations the feeds spell differently onto one
// spelling. Kept as a map rather than inline cases so a third disagreement is a
// one-line addition next to its evidence.
func canonicalTeam(t string) string {
	switch t {
	case "AZ", "ARI":
		return "ARI"
	case "CWS", "CHW":
		return "CHW"
	}
	return t
}
