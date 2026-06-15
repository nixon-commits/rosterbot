package claims

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/waivers"
)

// EnrichSignals tags each added player with a Statcast signal using the shared
// waivers tagging rules. No-op when bundle is nil or MLBAMID is unresolved.
func EnrichSignals(moves []Move, bundle *waivers.SavantBundle, th waivers.Thresholds) {
	if bundle == nil {
		return
	}
	for mi := range moves {
		for pi := range moves[mi].Added {
			p := &moves[mi].Added[pi]
			if p.MLBAMID == 0 {
				continue
			}
			var sig waivers.Signal
			if p.IsPitcher {
				sig, _ = waivers.TagPitcher(bundle, p.MLBAMID, th)
			} else {
				sig, _ = waivers.TagHitter(bundle, p.MLBAMID, th)
			}
			p.Signal = sig
		}
	}
}

// resolveAddedIDs resolves every added player's name to an MLBAM ID in place.
func resolveAddedIDs(moves []Move, cacheDir string) {
	var names []string
	for _, m := range moves {
		for _, p := range m.Added {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return
	}
	resolved, err := playername.ResolveMLBAMIDs(names, cacheDir)
	if err != nil || resolved == nil {
		return
	}
	for mi := range moves {
		for pi := range moves[mi].Added {
			p := &moves[mi].Added[pi]
			if id, ok := resolved.ByName[playername.Normalize(p.Name)]; ok {
				p.MLBAMID = id
			}
		}
	}
}

// enrichProjections fills ProjectedFPG for added players from FanGraphs
// depthcharts projections, scored with the league weights. Best-effort.
func enrichProjections(moves []Move, hitterWeights, pitcherWeights fantrax.ScoringWeights, cacheDir string, ttl time.Duration) {
	bat, _, err := projections.LoadBattingProjections(projections.ProjectionDepthCharts, cacheDir, ttl)
	if err != nil {
		return
	}
	// Pitcher load is best-effort and independent; perr is checked per-player below.
	pit, _, perr := projections.LoadPitcherProjections(projections.ProjectionDepthCharts, cacheDir, ttl)
	for mi := range moves {
		for pi := range moves[mi].Added {
			p := &moves[mi].Added[pi]
			if p.IsPitcher {
				if perr != nil {
					continue
				}
				if proj, ok := pit.GetPitcherProjection(p.Name, ""); ok {
					p.ProjectedFPG = projections.PitcherExpectedPtsFromProj(proj, pitcherWeights)
				}
				continue
			}
			if proj, ok := bat.GetProjection(p.Name, ""); ok {
				p.ProjectedFPG = projections.ExpectedPtsFromProj(proj, hitterWeights)
			}
		}
	}
}
