package recap

import "sort"

// MostEfficient returns the team with the highest Actual/Optimal ratio. Teams
// with non-positive optimal points are ignored. Ties broken by lexicographic
// TeamID for stability. Returns nil if no team qualifies.
func MostEfficient(teams []TeamWeek) *TeamWeek {
	return pickEfficiency(teams, func(a, b *TeamWeek) bool {
		if a.Efficiency != b.Efficiency {
			return a.Efficiency > b.Efficiency
		}
		return a.TeamID < b.TeamID
	})
}

// LeastEfficient returns the team with the lowest Actual/Optimal ratio. Same
// filtering and tiebreaker as MostEfficient.
func LeastEfficient(teams []TeamWeek) *TeamWeek {
	return pickEfficiency(teams, func(a, b *TeamWeek) bool {
		if a.Efficiency != b.Efficiency {
			return a.Efficiency < b.Efficiency
		}
		return a.TeamID < b.TeamID
	})
}

func pickEfficiency(teams []TeamWeek, less func(a, b *TeamWeek) bool) *TeamWeek {
	var best *TeamWeek
	for i := range teams {
		t := &teams[i]
		if t.OptimalPts <= 0 {
			continue
		}
		if best == nil || less(t, best) {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	out := *best
	return &out
}

// BiggestBlowout returns the matchup with the largest absolute margin among
// non-tie matchups. Ties broken by HomeTeamID for stability.
func BiggestBlowout(matchups []MatchupResult) *MatchupResult {
	return pickMatchup(matchups, func(a, b *MatchupResult) bool {
		if a.Margin != b.Margin {
			return a.Margin > b.Margin
		}
		return a.HomeTeamID < b.HomeTeamID
	})
}

// NarrowVictory returns the non-tie matchup with the smallest margin.
func NarrowVictory(matchups []MatchupResult) *MatchupResult {
	return pickMatchup(matchups, func(a, b *MatchupResult) bool {
		if a.Margin != b.Margin {
			return a.Margin < b.Margin
		}
		return a.HomeTeamID < b.HomeTeamID
	})
}

func pickMatchup(matchups []MatchupResult, less func(a, b *MatchupResult) bool) *MatchupResult {
	var best *MatchupResult
	for i := range matchups {
		m := &matchups[i]
		if m.IsTie {
			continue
		}
		if best == nil || less(m, best) {
			best = m
		}
	}
	if best == nil {
		return nil
	}
	out := *best
	return &out
}

// HighestPtsInLoss returns the team that scored the most points yet still lost
// their matchup. Returns nil if no losses occurred.
func HighestPtsInLoss(matchups []MatchupResult) *MatchupTeamSide {
	var best *MatchupTeamSide
	for _, m := range matchups {
		if m.IsTie {
			continue
		}
		loser := loserSide(m)
		if best == nil || loser.Pts > best.Pts ||
			(loser.Pts == best.Pts && loser.TeamID < best.TeamID) {
			s := loser
			best = &s
		}
	}
	return best
}

// LowestPtsInWin returns the team that scored the fewest points yet still won
// their matchup. Returns nil if no wins occurred.
func LowestPtsInWin(matchups []MatchupResult) *MatchupTeamSide {
	var best *MatchupTeamSide
	for _, m := range matchups {
		if m.IsTie {
			continue
		}
		winner := winnerSide(m)
		if best == nil || winner.Pts < best.Pts ||
			(winner.Pts == best.Pts && winner.TeamID < best.TeamID) {
			s := winner
			best = &s
		}
	}
	return best
}

func winnerSide(m MatchupResult) MatchupTeamSide {
	if m.WinnerID == m.HomeTeamID {
		return MatchupTeamSide{TeamID: m.HomeTeamID, TeamName: m.HomeTeamName, Pts: m.HomePts, OppName: m.AwayTeamName, OppPts: m.AwayPts}
	}
	return MatchupTeamSide{TeamID: m.AwayTeamID, TeamName: m.AwayTeamName, Pts: m.AwayPts, OppName: m.HomeTeamName, OppPts: m.HomePts}
}

func loserSide(m MatchupResult) MatchupTeamSide {
	if m.LoserID == m.HomeTeamID {
		return MatchupTeamSide{TeamID: m.HomeTeamID, TeamName: m.HomeTeamName, Pts: m.HomePts, OppName: m.AwayTeamName, OppPts: m.AwayPts}
	}
	return MatchupTeamSide{TeamID: m.AwayTeamID, TeamName: m.AwayTeamName, Pts: m.AwayPts, OppName: m.HomeTeamName, OppPts: m.HomePts}
}

// PlayersOfWeek returns the top n active player-day scoring lines, sorted by
// FPts descending. Stable tiebreaker on (Name, OwnerTeam, Date) so duplicate
// scores produce deterministic ordering.
func PlayersOfWeek(active []PlayerLine, n int) []PlayerLine {
	return topPlayers(active, n)
}

// BenchwarmersOfWeek returns the top n benched player-day scoring lines.
func BenchwarmersOfWeek(bench []PlayerLine, n int) []PlayerLine {
	return topPlayers(bench, n)
}

func topPlayers(lines []PlayerLine, n int) []PlayerLine {
	if n <= 0 || len(lines) == 0 {
		return nil
	}
	out := make([]PlayerLine, len(lines))
	copy(out, lines)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FPts != out[j].FPts {
			return out[i].FPts > out[j].FPts
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].OwnerTeam != out[j].OwnerTeam {
			return out[i].OwnerTeam < out[j].OwnerTeam
		}
		return out[i].Date.Before(out[j].Date)
	})
	if n > len(out) {
		n = len(out)
	}
	return out[:n]
}

// BestSingleStart returns the highest-scoring SP start of the week. Returns
// nil if no SP starts were recorded.
func BestSingleStart(starts []PitcherStartLine) *PitcherStartLine {
	return pickStart(starts, func(a, b *PitcherStartLine) bool {
		if a.FPts != b.FPts {
			return a.FPts > b.FPts
		}
		return a.Name < b.Name
	})
}

// WorstSingleStart returns the lowest-scoring SP start of the week.
func WorstSingleStart(starts []PitcherStartLine) *PitcherStartLine {
	return pickStart(starts, func(a, b *PitcherStartLine) bool {
		if a.FPts != b.FPts {
			return a.FPts < b.FPts
		}
		return a.Name < b.Name
	})
}

func pickStart(starts []PitcherStartLine, less func(a, b *PitcherStartLine) bool) *PitcherStartLine {
	var best *PitcherStartLine
	for i := range starts {
		s := &starts[i]
		if best == nil || less(s, best) {
			best = s
		}
	}
	if best == nil {
		return nil
	}
	out := *best
	return &out
}
