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

// HighestScore returns the team with the most raw ActualPts. Tiebreaker on
// TeamID for stability. Returns nil if teams is empty.
func HighestScore(teams []TeamWeek) *TeamWeek {
	return pickScore(teams, func(a, b *TeamWeek) bool {
		if a.ActualPts != b.ActualPts {
			return a.ActualPts > b.ActualPts
		}
		return a.TeamID < b.TeamID
	})
}

// LowestScore returns the team with the fewest raw ActualPts.
func LowestScore(teams []TeamWeek) *TeamWeek {
	return pickScore(teams, func(a, b *TeamWeek) bool {
		if a.ActualPts != b.ActualPts {
			return a.ActualPts < b.ActualPts
		}
		return a.TeamID < b.TeamID
	})
}

func pickScore(teams []TeamWeek, less func(a, b *TeamWeek) bool) *TeamWeek {
	var best *TeamWeek
	for i := range teams {
		t := &teams[i]
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

// TopBatters returns the top n active hitter player-day scoring lines, sorted
// by FPts descending. Stable tiebreaker on (Name, OwnerTeam, Date).
func TopBatters(active []PlayerLine, n int) []PlayerLine {
	return topPlayers(filterByPitcher(active, false), n)
}

// TopPitchers returns the top n active pitcher player-day scoring lines.
func TopPitchers(active []PlayerLine, n int) []PlayerLine {
	return topPlayers(filterByPitcher(active, true), n)
}

func filterByPitcher(lines []PlayerLine, pitcher bool) []PlayerLine {
	out := make([]PlayerLine, 0, len(lines))
	for _, l := range lines {
		if l.IsPitcher == pitcher {
			out = append(out, l)
		}
	}
	return out
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

// AggregateSeasonAwards walks recaps in order and returns one cumulative
// *SeasonAwards per recap, where snapshot i covers awards earned in weeks 0..i
// inclusive. Each per-team award (single-team, matchup, matchup-side, and
// pitcher-start) adds 1 to its team's tally. TopBatters / TopPitchers are
// intentionally excluded — those are individual-player highlights and
// counting them would inflate teams that simply roster many high-scorers.
//
// Pitcher-start awards arrive with an OwnerTeam *name* rather than ID, so the
// aggregator builds a per-recap name→ID map from Recap.Teams to attribute
// them. Names that don't resolve are silently skipped (defensive — a typo or
// stale team rename shouldn't crash the build).
//
// Output Teams slice is sorted by Count descending, then TeamID ascending for
// deterministic output. Every team that appears in any recap's Teams list is
// included in every snapshot, even with Count=0, so the leaderboard table
// always lists the full league.
func AggregateSeasonAwards(recaps []*Recap) []*SeasonAwards {
	out := make([]*SeasonAwards, 0, len(recaps))
	counts := map[string]int{}
	names := map[string]string{}

	for _, r := range recaps {
		if r == nil {
			out = append(out, nil)
			continue
		}

		// Refresh name map from this week's Teams (handles renames over time).
		nameToID := map[string]string{}
		for _, t := range r.Teams {
			names[t.TeamID] = t.TeamName
			nameToID[t.TeamName] = t.TeamID
			if _, ok := counts[t.TeamID]; !ok {
				counts[t.TeamID] = 0 // ensure team appears in snapshot even at zero
			}
		}

		add := func(id string) {
			if id == "" {
				return
			}
			counts[id]++
		}
		a := r.Awards
		if a.MostEfficient != nil {
			add(a.MostEfficient.TeamID)
		}
		if a.LeastEfficient != nil {
			add(a.LeastEfficient.TeamID)
		}
		if a.HighestScore != nil {
			add(a.HighestScore.TeamID)
		}
		if a.LowestScore != nil {
			add(a.LowestScore.TeamID)
		}
		if a.BiggestBlowout != nil {
			add(a.BiggestBlowout.WinnerID)
		}
		if a.NarrowVictory != nil {
			add(a.NarrowVictory.WinnerID)
		}
		if a.HighestPtsInLoss != nil {
			add(a.HighestPtsInLoss.TeamID)
		}
		if a.LowestPtsInWin != nil {
			add(a.LowestPtsInWin.TeamID)
		}
		if a.BestSingleStart != nil {
			if id, ok := nameToID[a.BestSingleStart.OwnerTeam]; ok {
				add(id)
			}
		}
		if a.WorstSingleStart != nil {
			if id, ok := nameToID[a.WorstSingleStart.OwnerTeam]; ok {
				add(id)
			}
		}

		// Snapshot.
		teams := make([]SeasonAwardLine, 0, len(counts))
		for id, c := range counts {
			teams = append(teams, SeasonAwardLine{
				TeamID:   id,
				TeamName: names[id],
				Count:    c,
			})
		}
		sort.Slice(teams, func(i, j int) bool {
			if teams[i].Count != teams[j].Count {
				return teams[i].Count > teams[j].Count
			}
			return teams[i].TeamID < teams[j].TeamID
		})
		out = append(out, &SeasonAwards{
			ThroughWeek: r.WeekNumber,
			Teams:       teams,
		})
	}
	return out
}
