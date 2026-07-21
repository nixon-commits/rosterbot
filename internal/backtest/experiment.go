package backtest

import (
	"fmt"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// ExperimentClient is the slice of the Fantrax client the recency experiment
// needs: the rosters that supply name→ID (and pitcher role) maps, plus the
// pitcher scoring weights. Satisfied by *fantrax.Client.
type ExperimentClient interface {
	GetHitterRoster() ([]fantrax.Player, error)
	GetPitcherRoster() ([]fantrax.Player, error)
	GetPitcherScoringWeights() (fantrax.ScoringWeights, error)
}

// ExperimentOptions configures the recency-strategy comparison.
type ExperimentOptions struct {
	// ProjectionSystem is the shared base projection every variant blends on
	// top of (and that the "base" variant uses alone).
	ProjectionSystem string
	CacheDir         string
	ProjectionTTL    time.Duration

	HitterSlots   []fantrax.Slot
	PitcherSlots  []fantrax.Slot
	HitterScoring fantrax.ScoringWeights
	BlendMinGP    int
}

// ExperimentReport holds both halves of the recency comparison.
type ExperimentReport struct {
	System   string
	Days     int
	Hitters  []VariantResult
	Pitchers []VariantResult
}

// recencyWeights are the strategies compared, in report order. "base" is
// handled separately (it blends nothing at all).
var recencyWeights = []struct {
	name string
	w    projections.WeightFunc
}{
	{"ytd", projections.YTDWeight},
	{"w14", projections.WindowWeight(14)},
	{"w30", projections.WindowWeight(30)},
	{"decay21", projections.DecayWeight(21)},
}

// RunRecencyExperiment compares recency-weighting strategies (no blend at all
// vs YTD vs 14d/30d/decay) by replaying the optimizer over gradeDays under each
// strategy, for hitters and pitchers alike. The base projection is shared
// across variants, so the comparison isolates the recency blend.
//
// gradeDays is the window being graded; seriesDays is an extended range
// reaching back before it. The trailing-window strategies need history
// predating the graded days, or every window collapses to the same in-window
// games. Production lineups are unaffected — this is backtest-only.
func RunRecencyExperiment(
	ft ExperimentClient,
	gradeDays []fantrax.DayRoster,
	seriesDays []fantrax.DayRoster,
	opts ExperimentOptions,
) (*ExperimentReport, error) {
	hitters, err := runHitterRecency(ft, gradeDays, seriesDays, opts)
	if err != nil {
		return nil, err
	}
	pitchers, err := runPitcherRecency(ft, gradeDays, seriesDays, opts)
	if err != nil {
		return nil, err
	}
	return &ExperimentReport{
		System:   opts.ProjectionSystem,
		Days:     len(gradeDays),
		Hitters:  hitters,
		Pitchers: pitchers,
	}, nil
}

func runHitterRecency(
	ft ExperimentClient,
	gradeDays, seriesDays []fantrax.DayRoster,
	opts ExperimentOptions,
) ([]VariantResult, error) {
	// Base hitter projection source, shared across all variants. Mirrors the
	// base-source construction in internal/lineuprun.
	fgSrc, _, err := projections.LoadBattingProjections(opts.ProjectionSystem, opts.CacheDir, opts.ProjectionTTL)
	if err != nil {
		return nil, fmt.Errorf("load base projections: %w", err)
	}
	baseSrc := projections.NewChainedSource(fgSrc, projections.NewRollingSource())

	roster, err := ft.GetHitterRoster()
	if err != nil {
		return nil, fmt.Errorf("hitter roster: %w", err)
	}
	nameToID := make(map[string]string, len(roster))
	for _, p := range roster {
		nameToID[projections.NormalizeName(p.Name)] = p.ID
	}

	series := BuildHitterSeries(seriesDays)
	baseline := fgSrc.AverageFPG(opts.HitterScoring)

	variants := []StrategyVariant{{
		Name:  "base",
		Build: func(time.Time) (projections.Source, error) { return baseSrc, nil },
	}}
	for _, rw := range recencyWeights {
		w := rw.w
		variants = append(variants, StrategyVariant{
			Name: rw.name,
			Build: func(asOf time.Time) (projections.Source, error) {
				return projections.NewBlendedSource(
					baseSrc, weightedSeries(series, asOf, w),
					opts.HitterScoring, nameToID, opts.BlendMinGP, baseline,
				), nil
			},
		})
	}

	return RunStrategyComparison(variants, gradeDays, opts.HitterSlots, opts.HitterScoring)
}

func runPitcherRecency(
	ft ExperimentClient,
	gradeDays, seriesDays []fantrax.DayRoster,
	opts ExperimentOptions,
) ([]VariantResult, error) {
	fgSrc, _, err := projections.LoadPitcherProjections(opts.ProjectionSystem, opts.CacheDir, opts.ProjectionTTL)
	if err != nil {
		return nil, fmt.Errorf("load base pitcher projections: %w", err)
	}
	baseSrc := projections.NewPitcherChainedSource(fgSrc, projections.NewPitcherRollingSource())

	scoring, err := ft.GetPitcherScoringWeights()
	if err != nil {
		return nil, fmt.Errorf("get pitcher scoring weights: %w", err)
	}

	roster, err := ft.GetPitcherRoster()
	if err != nil {
		return nil, fmt.Errorf("pitcher roster: %w", err)
	}
	nameToID := make(map[string]string, len(roster))
	playerPos := make(map[string][]string, len(roster))
	for _, p := range roster {
		nameToID[projections.NormalizeName(p.Name)] = p.ID
		playerPos[p.ID] = p.Positions
	}

	series := BuildPitcherSeries(seriesDays)
	baseline := fgSrc.AverageFPG(scoring)

	variants := []PitcherStrategyVariant{{
		Name:  "base",
		Build: func(time.Time) (projections.PitcherSource, error) { return baseSrc, nil },
	}}
	for _, rw := range recencyWeights {
		w := rw.w
		variants = append(variants, PitcherStrategyVariant{
			Name: rw.name,
			Build: func(asOf time.Time) (projections.PitcherSource, error) {
				return projections.NewPitcherBlendedSource(
					baseSrc, weightedSeries(series, asOf, w),
					scoring, nameToID, playerPos, opts.BlendMinGP, baseline,
				), nil
			},
		})
	}

	return RunPitcherStrategyComparison(variants, gradeDays, opts.PitcherSlots, scoring)
}

// weightedSeries collapses each player's FPts series into a single RecentStat
// as of asOf, under weight function w.
func weightedSeries(series map[string][]projections.DayFP, asOf time.Time, w projections.WeightFunc) map[string]fantrax.RecentStat {
	out := make(map[string]fantrax.RecentStat, len(series))
	for id, s := range series {
		out[id] = projections.WeightedRecent(s, asOf, w)
	}
	return out
}

// FormatExperiment renders the comparison as a human-readable report, mirroring
// FormatReport's build-then-format split.
func FormatExperiment(r *ExperimentReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\nRecency strategy comparison (hitters, %d days)\n", r.Days)
	fmt.Fprintf(&b, "  base = %s with no recency blend at all, isolating whether blending recent form on top of an\n", r.System)
	fmt.Fprintf(&b, "  already-in-season-updated RoS projection helps or hurts (MAE/bias are n/a for base: ChainedSource doesn't\n")
	fmt.Fprintf(&b, "  implement PtsPerGameSource, so only realized/mean-gap are comparable for that row).\n")
	writeVariantTable(&b, r.Hitters)

	fmt.Fprintf(&b, "\nRecency strategy comparison (pitchers, %d days)\n", r.Days)
	fmt.Fprintf(&b, "  base = %s with no recent blend at all. Recency signal is per-appearance pitcher FPts\n", r.System)
	fmt.Fprintf(&b, "  (SP per-start, RP per-outing); role-aware stabilization applies (SP 15 GP, RP 25 GP). MAE/bias are\n")
	fmt.Fprintf(&b, "  n/a for base: PitcherChainedSource doesn't implement PitcherPtsPerGameSource.\n")
	writeVariantTable(&b, r.Pitchers)

	return b.String()
}

// writeVariantTable renders a VariantResult table, showing "n/a" for MAE/bias
// when the variant's source doesn't expose per-game projections.
func writeVariantTable(b *strings.Builder, results []VariantResult) {
	fmt.Fprintf(b, "%-10s %12s %10s %8s %8s\n", "mode", "realized", "mean gap", "MAE", "bias")
	for _, r := range results {
		mae, bias := "n/a", "n/a"
		if r.HasProjDiag {
			mae = fmt.Sprintf("%.2f", r.MAE)
			bias = fmt.Sprintf("%.2f", r.Bias)
		}
		fmt.Fprintf(b, "%-10s %12.1f %10.2f %8s %8s\n", r.Name, r.RealizedPts, r.MeanGap, mae, bias)
	}
}
