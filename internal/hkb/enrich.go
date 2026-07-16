package hkb

import "github.com/nixon-commits/rosterbot/internal/playername"

// Enriched holds the fields derived from matching a display name against the
// HKB dataset -- the mapping shared by every package that joins its own
// player records onto HKB rankings (internal/claims, internal/transactions).
type Enriched struct {
	Name              string
	Position          string
	Ranked            bool // found in HKB
	Value             int
	Age               float64
	Rank              int
	ValueChange30Days int
	Level             string
	Prospect          bool
	FYPD              bool

	// Stats -- at most one populated, based on player type.
	IsPitcher bool
	HasStats  bool
	OPS       float64
	ERA       float64
	WHIP      float64
}

// BuildLookup builds a map from normalized player name to HKB player, the
// join key shared by every HKB consumer.
func BuildLookup(players []Player) map[string]Player {
	m := make(map[string]Player, len(players))
	for _, p := range players {
		m[playername.Normalize(p.Name)] = p
	}
	return m
}

// Enrich looks up name in lookup and returns the HKB enrichment fields for
// it, or a name/position-only Enriched if not found. isPitcherHint seeds
// IsPitcher from an out-of-band source (e.g. a roster position string) so an
// unranked pitcher is still correctly flagged when no HKB entry exists.
func Enrich(name, position string, lookup map[string]Player, isPitcherHint bool) Enriched {
	e := Enriched{Name: name, Position: position, IsPitcher: isPitcherHint}
	p, ok := lookup[playername.Normalize(name)]
	if !ok {
		return e
	}
	e.Ranked = true
	e.Value = p.Value
	e.Age = p.Age
	e.Rank = p.Rank
	e.ValueChange30Days = p.ValueChange30Days
	e.Level = p.Level
	e.Prospect = p.Prospect
	e.FYPD = p.FYPD
	if p.PitcherStats != nil {
		e.IsPitcher = true
		e.HasStats = true
		e.ERA = p.PitcherStats.ERA
		e.WHIP = p.PitcherStats.WHIP
	} else if p.HitterStats != nil {
		e.HasStats = true
		e.OPS = p.HitterStats.OPS
	}
	return e
}
