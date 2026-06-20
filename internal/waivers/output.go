package waivers

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult maps the waiver Report to the iOS wire shape. Rank is the 1-based
// position in the already-sorted Top slice. Hitter/pitcher diagnostics are
// emitted as-is; omitempty drops the irrelevant set on the wire.
func toWireResult(r Report) lineupapi.WaiversResult {
	out := lineupapi.WaiversResult{Total: r.Total}
	for i, c := range r.Top {
		out.Picks = append(out.Picks, lineupapi.WaiverPickOut{
			Name:         c.Name,
			Team:         c.MLBTeam,
			Pos:          c.Position,
			IsPitcher:    c.IsPitcher,
			Signal:       c.Signal.String(),
			ProjectedFPG: c.ProjectedFPG,
			DropName:     c.DropName,
			Gap:          c.Gap,
			Xwoba:        c.XwOBA,
			Woba:         c.WOBA,
			BarrelPct:    c.Barrel,
			HardHitPct:   c.HardHit,
			Era:          c.ERA,
			Xera:         c.XERA,
			Rank:         i + 1,
		})
	}
	return out
}
