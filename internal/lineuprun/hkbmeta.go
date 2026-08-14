package lineuprun

import (
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

// LoadHKBMeta returns HKB dynasty age/value keyed by normalized player name —
// the enrichment the published lineup JSON carries alongside each player, and
// the input the dashboard's age/value heatmaps are scaled from.
//
// The join key is playername.Normalize, the same key every other HKB consumer
// uses (internal/claims, internal/transactions, internal/teamvalue) and the same
// one lineupapi.Build looks the map up by, via projections.NormalizeName — a
// thin wrapper over it. A name that misses is simply absent from the map; the
// wire fields are omitempty, so a miss reads as "unknown" rather than as a
// player of age 0.
//
// # Failure policy
//
// The error is returned rather than swallowed, but every caller is expected to
// treat it as soft. HKB is a third-party scrape of a page that can change shape
// without notice, and this data is decoration on a lineup the optimizer already
// decided: a scrape outage must degrade the display, never block the apply.
// That is the same call internal/claims makes when it enriches picked-up
// players, and the opposite of LoadInputs' six fatal roster reads — those are
// load-bearing inputs to the optimization itself.
func LoadHKBMeta(cacheDir string) (map[string]lineupapi.Dynasty, error) {
	players, err := hkb.GetPlayers(cacheDir)
	if err != nil {
		return nil, err
	}
	return hkbMeta(players), nil
}

// hkbMeta is the pure half — the keying — split out from the fetch so the join
// is testable without reaching HKB. It is the half worth testing: a broken
// fetch is loud, whereas a join key that stops matching is silent and empties
// both columns while every player still renders.
func hkbMeta(players []hkb.Player) map[string]lineupapi.Dynasty {
	out := make(map[string]lineupapi.Dynasty, len(players))
	for _, p := range players {
		out[playername.Normalize(p.Name)] = lineupapi.Dynasty{Age: p.Age, Value: p.Value}
	}
	return out
}
