// Package statsguy is the dynasty-football value provider, named after its
// provider like internal/hkb. It holds the StatsGuy Bundle (player values +
// draft pick values, both keyed for direct join against Sleeper data) and the
// cached loader. Mirrors internal/statcast.Bundle/LoadBundle so both
// football-values and football-trades depend on this data leaf, not on each
// other's command package.
package statsguy

// FormatValues holds a player or pick's value across StatsGuy's four league
// formats. Get looks a value up by its raw JSON key (e.g. the DYNASTY_FORMAT
// env var's value), returning 0 for an unrecognized format.
type FormatValues struct {
	SFDynasty    int `json:"sf_dynasty"`
	NonSFDynasty int `json:"non_sf_dynasty"`
	SFRedraft    int `json:"sf_redraft"`
	NonSFRedraft int `json:"non_sf_redraft"`
}

func (v FormatValues) Get(format string) int {
	switch format {
	case "sf_dynasty":
		return v.SFDynasty
	case "non_sf_dynasty":
		return v.NonSFDynasty
	case "sf_redraft":
		return v.SFRedraft
	case "non_sf_redraft":
		return v.NonSFRedraft
	default:
		return 0
	}
}

// Player is one StatsGuy-valued player. ID is the Sleeper player_id exactly
// (measured 2026-08-10: 396/396 valued ids resolve against Sleeper's player
// dump) — no name normalization needed to join against Sleeper rosters.
type Player struct {
	ID       string
	Name     string
	Team     string
	Position string
	Value    FormatValues
}

// Pick is one StatsGuy-valued draft pick slot. A pick is identified either by
// Variant ("early"/"mid"/"late", tier-averaged within a round) or by Slot
// (1-based, once the round's draft order is settled) — exactly one is set.
type Pick struct {
	ID      string
	Year    int
	Round   int
	Variant string
	Slot    int
	Value   FormatValues
}

// Bundle is the joined StatsGuy value set: every valued player keyed by
// Sleeper player_id, plus the full priced pick grid.
type Bundle struct {
	Players map[string]Player
	Picks   []Pick
}
