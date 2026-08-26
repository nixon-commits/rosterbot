package playername

// ProbableMatch classifies a player against a date's probable-starters map
// (normalized name → MLB club abbreviation, the shape
// schedule.ProbableStarters returns).
//
// This join used to be hand-copied at three sites — the pitcher optimizer,
// the IL-start check, and the GS-budget forecast — with CLAUDE.md pinning two
// of them as "byte-identical" by prose alone and the third unremarked;
// optimizer.SPEligiblePlayer's own doc records how three hand-copies of one
// predicate end. One function, one place for the semantics to change.
//
// The club comparison is plain ==, deliberately: both sides already pass
// through teams.Normalize upstream (the probables map's values and the
// player's MLBTeam), so the AZ/ARI class of mismatch cannot arise here.
type ProbableMatch int

const (
	// NotProbable: the name is not in the map — no club has announced this
	// player as starting.
	NotProbable ProbableMatch = iota
	// ConfirmedStarter: announced as starting for HIS OWN club — the only
	// answer worth a full-value start.
	ConfirmedStarter
	// TeamMismatch: the name is announced, but for a different club — a
	// lagging trade on one side or the other. A distinct answer, not a miss:
	// callers that can surface it should (roster.CheckILStarters reports it
	// rather than dropping it); for the rest, treating it like NotProbable is
	// the conservative reading.
	TeamMismatch
)

// MatchProbable classifies (name, mlbTeam) against probables. name is the raw
// display name — normalization happens here, so no caller can join on the
// wrong key.
func MatchProbable(name, mlbTeam string, probables map[string]string) ProbableMatch {
	team, ok := probables[Normalize(name)]
	if !ok {
		return NotProbable
	}
	if team != mlbTeam {
		return TeamMismatch
	}
	return ConfirmedStarter
}
