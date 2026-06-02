package recap

import (
	"math"
	"sort"
)

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

// GameOfTheWeek returns the matchup with the highest "weeeeeh factor" —
// the one that delivered the most narrative drama. Score:
//
//	score = LeadChanges + (1 - minWinnerWP)
//
// Lead changes (each crossing of the 0.5 line in the WP curve) capture
// back-and-forth swings; (1 - minWinnerWP) rewards eventual winners who
// were once "left for dead" mid-week. Tiebreak: smaller final margin →
// lower home TeamID.
//
// Unlike the old HeartAttack award this always returns a result when any
// matchups exist — even in a quiet week with zero lead changes, the
// closest game (smallest margin) wins. matchups is the per-week list;
// curves are matched by canonical team-pair key, so order is independent.
// Ties are scored with the comeback term zeroed (no "winner" to measure).
func GameOfTheWeek(curves []MatchupWPCurve, matchups []MatchupResult) *MatchupResult {
	if len(matchups) == 0 {
		return nil
	}
	cByPair := make(map[string]MatchupWPCurve, len(curves))
	for _, c := range curves {
		cByPair[canonPair(c.HomeTeamID, c.AwayTeamID)] = c
	}

	var best *MatchupResult
	var bestScore float64
	for i := range matchups {
		m := matchups[i]
		c, hasCurve := cByPair[canonPair(m.HomeTeamID, m.AwayTeamID)]

		score := 0.0
		if hasCurve {
			score += float64(c.LeadChanges)
			if !m.IsTie {
				homeWon := m.WinnerID == m.HomeTeamID
				if minWP, ok := MinWinnerWP(c.Points, homeWon); ok {
					score += 1.0 - minWP
				}
			}
		}

		switch {
		case best == nil:
		case score > bestScore:
		case score == bestScore && m.Margin < best.Margin:
		case score == bestScore && m.Margin == best.Margin && m.HomeTeamID < best.HomeTeamID:
		default:
			continue
		}
		copyM := m
		best = &copyM
		bestScore = score
	}
	return best
}

// comebackThreshold is the maximum mid-week WP a winner can have hit and
// still qualify for the Comeback award. 0.30 keeps it meaningful — only
// genuine "left for dead" comebacks count.
const comebackThreshold = 0.30

// Comeback returns the eventual winner with the lowest mid-week WP, gated
// at comebackThreshold. Returns nil if no winner had a mid-week WP below
// the threshold. Tiebreak: smallest min WP → TeamID asc.
func Comeback(curves []MatchupWPCurve, matchups []MatchupResult) *MatchupTeamSide {
	if len(curves) == 0 || len(matchups) == 0 {
		return nil
	}
	mByPair := make(map[string]MatchupResult, len(matchups))
	for _, m := range matchups {
		mByPair[canonPair(m.HomeTeamID, m.AwayTeamID)] = m
	}

	var best *MatchupTeamSide
	bestMin := math.Inf(1)
	for _, c := range curves {
		m, ok := mByPair[canonPair(c.HomeTeamID, c.AwayTeamID)]
		if !ok || m.IsTie {
			continue
		}
		homeWon := m.WinnerID == m.HomeTeamID
		minWP, ok := MinWinnerWP(c.Points, homeWon)
		if !ok || minWP >= comebackThreshold {
			continue
		}
		var side MatchupTeamSide
		if homeWon {
			side = MatchupTeamSide{
				TeamID: m.HomeTeamID, TeamName: m.HomeTeamName, Pts: m.HomePts,
				OppName: m.AwayTeamName, OppPts: m.AwayPts,
			}
		} else {
			side = MatchupTeamSide{
				TeamID: m.AwayTeamID, TeamName: m.AwayTeamName, Pts: m.AwayPts,
				OppName: m.HomeTeamName, OppPts: m.HomePts,
			}
		}
		switch {
		case best == nil:
		case minWP < bestMin:
		case minWP == bestMin && side.TeamID < best.TeamID:
		default:
			continue
		}
		copySide := side
		best = &copySide
		bestMin = minWP
	}
	return best
}

// Award name labels rendered in the season leaderboard. Match the per-week
// display labels used in template.html for consistency.
const (
	AwardMostEfficient  = "Most Efficient"
	AwardLeastEfficient = "Least Efficient"
	AwardHighestScore   = "Highest Score"
	AwardLowestScore    = "Lowest Score"
	AwardBiggestBlowout = "Biggest Blowout"
	AwardNarrowVictory  = "Narrow Victory"
	AwardHighestPtsLoss = "Highest Pts in Loss"
	AwardLowestPtsWin   = "Lowest Pts in Win"
	AwardBestStart      = "Best Start"
	AwardWorstStart     = "Worst Start"
	AwardComeback       = "Comeback"
)

// SeasonShellingsLimit caps how many worst pitcher starts of the season are
// surfaced in the leaderboard.
const SeasonShellingsLimit = 5

// awardOrder controls which categories appear in the season cumulative
// leaderboard. Game of the Week is intentionally excluded — it shows at
// the top of each week's page, so cumulative counts would be redundant.
var awardOrder = []string{
	AwardMostEfficient,
	AwardLeastEfficient,
	AwardHighestScore,
	AwardLowestScore,
	AwardBiggestBlowout,
	AwardNarrowVictory,
	AwardHighestPtsLoss,
	AwardLowestPtsWin,
	AwardBestStart,
	AwardWorstStart,
	AwardComeback,
}

// AggregateSeasonAwards walks recaps in order and returns one cumulative
// *SeasonAwards per recap, where snapshot i covers awards earned in weeks
// 0..i inclusive. The output is grouped by award category with each category
// listing every team that has earned that award at least once and how many
// times. TopBatters / TopPitchers are excluded. Each snapshot also carries
// a Shellings list — the season's worst per-week pitcher starts, capped at
// SeasonShellingsLimit, sorted by FPts ascending.
//
// Pitcher-start awards arrive with an OwnerTeam *name* rather than ID, so
// the aggregator builds a per-recap name→ID map from Recap.Teams to
// attribute them. Names that don't resolve are silently skipped.
//
// Within each category, teams are sorted by Count descending then TeamID
// ascending. Categories with zero earners (e.g., no SP starts have happened
// yet) are omitted from the snapshot.
func AggregateSeasonAwards(recaps []*Recap) []*SeasonAwards {
	out := make([]*SeasonAwards, 0, len(recaps))
	// counts[awardName][teamID] = total earned through current week.
	counts := map[string]map[string]int{}
	names := map[string]string{}
	// allShellings is every recap's WorstSingleStart so far (with WeekNumber
	// stamped on). Re-sorted on each snapshot.
	var allShellings []PitcherStartLine

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
		}

		add := func(label, id string) {
			if id == "" {
				return
			}
			if counts[label] == nil {
				counts[label] = map[string]int{}
			}
			counts[label][id]++
		}
		a := r.Awards
		if a.MostEfficient != nil {
			add(AwardMostEfficient, a.MostEfficient.TeamID)
		}
		if a.LeastEfficient != nil {
			add(AwardLeastEfficient, a.LeastEfficient.TeamID)
		}
		if a.HighestScore != nil {
			add(AwardHighestScore, a.HighestScore.TeamID)
		}
		if a.LowestScore != nil {
			add(AwardLowestScore, a.LowestScore.TeamID)
		}
		if a.BiggestBlowout != nil {
			add(AwardBiggestBlowout, a.BiggestBlowout.WinnerID)
		}
		if a.NarrowVictory != nil {
			add(AwardNarrowVictory, a.NarrowVictory.WinnerID)
		}
		if a.HighestPtsInLoss != nil {
			add(AwardHighestPtsLoss, a.HighestPtsInLoss.TeamID)
		}
		if a.LowestPtsInWin != nil {
			add(AwardLowestPtsWin, a.LowestPtsInWin.TeamID)
		}
		if a.BestSingleStart != nil {
			if id, ok := nameToID[a.BestSingleStart.OwnerTeam]; ok {
				add(AwardBestStart, id)
			}
		}
		if a.WorstSingleStart != nil {
			if id, ok := nameToID[a.WorstSingleStart.OwnerTeam]; ok {
				add(AwardWorstStart, id)
			}
			s := *a.WorstSingleStart
			s.WeekNumber = r.WeekNumber
			allShellings = append(allShellings, s)
		}
		if a.Comeback != nil {
			add(AwardComeback, a.Comeback.TeamID)
		}

		// Build snapshot in fixed display order, skipping awards no team has
		// earned yet.
		categories := make([]SeasonAwardCategory, 0, len(awardOrder))
		for _, label := range awardOrder {
			tc := counts[label]
			if len(tc) == 0 {
				continue
			}
			teams := make([]SeasonAwardTeam, 0, len(tc))
			for id, c := range tc {
				teams = append(teams, SeasonAwardTeam{
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
			categories = append(categories, SeasonAwardCategory{
				AwardName: label,
				Teams:     teams,
			})
		}
		// Build the shellings list — sort all season's worst-per-week starts
		// by FPts ascending, take the top N. Tiebreak on (Name, Date) for
		// stable ordering.
		shellings := make([]PitcherStartLine, len(allShellings))
		copy(shellings, allShellings)
		sort.SliceStable(shellings, func(i, j int) bool {
			if shellings[i].FPts != shellings[j].FPts {
				return shellings[i].FPts < shellings[j].FPts
			}
			if shellings[i].Name != shellings[j].Name {
				return shellings[i].Name < shellings[j].Name
			}
			return shellings[i].Date.Before(shellings[j].Date)
		})
		if len(shellings) > SeasonShellingsLimit {
			shellings = shellings[:SeasonShellingsLimit]
		}

		out = append(out, &SeasonAwards{
			ThroughWeek: r.WeekNumber,
			Categories:  categories,
			Shellings:   shellings,
		})
	}
	return out
}
