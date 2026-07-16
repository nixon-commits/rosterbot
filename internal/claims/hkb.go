package claims

import (
	"strings"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

func buildHKBLookup(players []hkb.Player) map[string]hkb.Player {
	return hkb.BuildLookup(players)
}

// isPitcherPosition reports whether the Fantrax position string identifies a pitcher.
func isPitcherPosition(pos string) bool {
	for _, p := range strings.Split(pos, ",") {
		switch strings.TrimSpace(strings.ToUpper(p)) {
		case "SP", "RP", "P":
			return true
		}
	}
	return false
}

// lookupHKB builds a SidePlayer for `name`, enriching with HKB data when found.
// IsPitcher is set from the position string first so that unranked pitchers are
// correctly identified even when no HKB entry exists.
func lookupHKB(name, position string, lookup map[string]hkb.Player) SidePlayer {
	e := hkb.Enrich(name, position, lookup, isPitcherPosition(position))
	return SidePlayer{
		Name:      e.Name,
		Position:  e.Position,
		Ranked:    e.Ranked,
		Value:     e.Value,
		Rank:      e.Rank,
		Trend30D:  e.ValueChange30Days,
		Level:     e.Level,
		Prospect:  e.Prospect,
		IsPitcher: e.IsPitcher,
		HasStats:  e.HasStats,
		OPS:       e.OPS,
		ERA:       e.ERA,
		WHIP:      e.WHIP,
	}
}
