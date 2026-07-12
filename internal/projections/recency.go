package projections

import (
	"math"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// DayFP is one player-day of fantasy production.
type DayFP struct {
	// Date must be a UTC-midnight value (one per scoring day). WeightedRecent
	// computes age via integer day-division, which is exact only when both Date
	// and the eval date are midnight-aligned.
	Date   time.Time
	FP     float64
	Played bool
}

// WeightFunc returns the weight for a day given its age in whole days from the
// as-of (evaluation) date. Age is always >= 1 for eligible days (the eval day
// and anything later is excluded by WeightedRecent's leakage guard).
type WeightFunc func(ageDays int) float64

// YTDWeight weights every prior game equally (the current season-to-date model).
func YTDWeight(_ int) float64 { return 1 }

// WindowWeight weights games in the trailing n days equally, others zero.
func WindowWeight(n int) WeightFunc {
	return func(age int) float64 {
		if age >= 1 && age <= n {
			return 1
		}
		return 0
	}
}

// DecayWeight applies exponential decay with the given half-life in days.
func DecayWeight(halfLifeDays float64) WeightFunc {
	lambda := math.Log(2) / halfLifeDays
	return func(age int) float64 {
		if age < 1 {
			return 0
		}
		return math.Exp(-lambda * float64(age))
	}
}

// WindowedRecent builds a per-player RecentStat map from a range of daily
// rosters, applying weight w as of asOf. It groups each player's per-day FPts
// into a DayFP series and collapses it via WeightedRecent, keeping only
// pitchers when wantPitchers is true, only hitters otherwise. Used by the
// production optimizer to feed a bounded rolling-window recency signal into the
// blend (the unbounded-YTD alternative reads a single cumulative snapshot).
func WindowedRecent(days []fantrax.DayRoster, asOf time.Time, w WeightFunc, wantPitchers bool) map[string]fantrax.RecentStat {
	series := make(map[string][]DayFP)
	for _, d := range days {
		for _, p := range d.Players {
			if p.IsPitcher != wantPitchers {
				continue
			}
			series[p.PlayerID] = append(series[p.PlayerID], DayFP{
				Date:   d.Date,
				FP:     p.FPts,
				Played: p.HadGame,
			})
		}
	}
	out := make(map[string]fantrax.RecentStat, len(series))
	for id, s := range series {
		out[id] = WeightedRecent(s, asOf, w)
	}
	return out
}

// WeightedRecent collapses a player's per-day series into a RecentStat as of
// evalDate, using only games strictly before evalDate (leakage guard).
//
//	FPtsPerGame = Σ(w·dayFP) / Σ(w)   over played days
//	GamesPlayed = count of played days with non-zero weight
func WeightedRecent(series []DayFP, evalDate time.Time, weight WeightFunc) fantrax.RecentStat {
	var sumW, sumWFP float64
	var games int
	for _, d := range series {
		if !d.Date.Before(evalDate) { // leakage guard: only days < evalDate
			continue
		}
		if !d.Played {
			continue
		}
		age := int(evalDate.Sub(d.Date).Hours() / 24)
		w := weight(age)
		if w == 0 {
			continue
		}
		sumW += w
		sumWFP += w * d.FP
		games++
	}
	if sumW == 0 {
		return fantrax.RecentStat{}
	}
	return fantrax.RecentStat{FPtsPerGame: sumWFP / sumW, GamesPlayed: games}
}
