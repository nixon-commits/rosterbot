package backtest

import (
	"fmt"
	"math"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// StrategyVariant is one named projection strategy the replay harness evaluates.
// Build returns the hitter Source the variant would use to set a lineup on the
// given evaluation date, using only data available before that date.
type StrategyVariant struct {
	Name  string
	Build func(asOf time.Time) (projections.Source, error)
}

// VariantResult is the aggregate scorecard for one variant across the window.
type VariantResult struct {
	Name        string
	RealizedPts float64 // total actual FPts of the lineups this variant set
	MeanGap     float64 // mean daily (realized − hindsight-optimal), hitter slots
	MAE         float64 // mean abs per-player projection error vs actual
	Bias        float64 // signed mean per-player error (projected − actual)
	HasProjDiag bool    // false when the variant's Source doesn't implement PtsPerGameSource, so MAE/Bias are unset zero-values rather than real 0.00 error
}

// RunStrategyComparison replays the hitter optimizer for each variant across the
// given days and reports realized points, mean Gap to hindsight-optimal, and
// per-player projection MAE/Bias. Hitters only; pitchers are ignored.
func RunStrategyComparison(
	variants []StrategyVariant,
	days []fantrax.DayRoster,
	hitterSlots []fantrax.Slot,
	scoring fantrax.ScoringWeights,
) ([]VariantResult, error) {
	type acc struct {
		realized, gapSum, absErr, signErr float64
		errN, dayN                        int
	}
	accs := make([]acc, len(variants))

	for _, day := range days {
		hitters, _ := splitPlayers(day.Players)
		actualByID := make(map[string]float64, len(hitters))
		for _, p := range hitters {
			actualByID[p.PlayerID] = p.FPts
		}
		// Hindsight-optimal hitter points for the day (existing helper).
		optimal := hitterOptimalPts(optimizeHitters(hitters, hitterSlots))

		for i, v := range variants {
			src, err := v.Build(day.Date)
			if err != nil {
				return nil, fmt.Errorf("variant %s build %s: %w", v.Name, day.Date.Format("2006-01-02"), err)
			}
			roster := toPlayers(hitters)
			playing := teamsWithGames(hitters)
			res := optimizer.OptimizeLineup(roster, playing, src, scoring, hitterSlots, nil)

			chosen := chosenHitterIDs(res)
			var realized float64
			for id := range chosen {
				realized += actualByID[id]
			}
			accs[i].realized += realized
			accs[i].gapSum += realized - optimal
			accs[i].dayN++

			// Diagnostics: per-player projection error over hitters who had a game.
			// Only sources exposing per-game projections contribute (the type
			// assertion is invariant for a given src, so hoist it out of the loop).
			if proj, ok := src.(projections.PtsPerGameSource); ok {
				for _, p := range hitters {
					if !p.HadGame {
						continue
					}
					pred, has := proj.GetPtsPerGame(p.Name, p.MLBTeam, scoring)
					if !has {
						continue
					}
					e := pred - p.FPts
					accs[i].absErr += math.Abs(e)
					accs[i].signErr += e
					accs[i].errN++
				}
			}
		}
	}

	out := make([]VariantResult, len(variants))
	for i, v := range variants {
		a := accs[i]
		out[i] = VariantResult{Name: v.Name, RealizedPts: a.realized}
		if a.dayN > 0 {
			out[i].MeanGap = a.gapSum / float64(a.dayN)
		}
		if a.errN > 0 {
			out[i].MAE = a.absErr / float64(a.errN)
			out[i].Bias = a.signErr / float64(a.errN)
			out[i].HasProjDiag = true
		}
	}
	return out, nil
}

// chosenHitterIDs returns the set of player IDs the optimizer placed in active
// slots (shares the isInLineup predicate with hitterOptimalPts). No HasGame
// gate here: a no-game player the optimizer slotted contributes 0 actual FPts
// to realized, so including them is harmless and keeps the set's meaning honest.
func chosenHitterIDs(r optimizer.Result) map[string]bool {
	benched, activated := lineupSets(r.ToBench, r.ToActivate)
	chosen := make(map[string]bool)
	for _, sp := range r.Scored {
		if isInLineup(sp.Player, benched, activated) {
			chosen[sp.Player.ID] = true
		}
	}
	return chosen
}

// BuildHitterSeries groups per-day hitter FPts into a per-player DayFP series.
func BuildHitterSeries(days []fantrax.DayRoster) map[string][]projections.DayFP {
	series := make(map[string][]projections.DayFP)
	for _, day := range days {
		for _, p := range day.Players {
			if p.IsPitcher {
				continue
			}
			series[p.PlayerID] = append(series[p.PlayerID], projections.DayFP{
				Date:   day.Date,
				FP:     p.FPts,
				Played: p.HadGame,
			})
		}
	}
	return series
}
