package claims

import (
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

func buildHKBLookup(players []hkb.Player) map[string]hkb.Player {
	m := make(map[string]hkb.Player, len(players))
	for _, p := range players {
		m[playername.Normalize(p.Name)] = p
	}
	return m
}

// lookupHKB builds a SidePlayer for `name`, enriching with HKB data when found.
func lookupHKB(name, position string, lookup map[string]hkb.Player) SidePlayer {
	sp := SidePlayer{Name: name, Position: position}
	p, ok := lookup[playername.Normalize(name)]
	if !ok {
		return sp
	}
	sp.Ranked = true
	sp.Value = p.Value
	sp.Rank = p.Rank
	sp.Trend30D = p.ValueChange30Days
	sp.Level = p.Level
	sp.Prospect = p.Prospect
	if p.PitcherStats != nil {
		sp.IsPitcher = true
		sp.HasStats = true
		sp.ERA = p.PitcherStats.ERA
		sp.WHIP = p.PitcherStats.WHIP
	} else if p.HitterStats != nil {
		sp.HasStats = true
		sp.OPS = p.HitterStats.OPS
	}
	return sp
}
