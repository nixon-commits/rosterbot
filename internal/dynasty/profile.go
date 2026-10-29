package dynasty

import (
	"fmt"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// LeagueKind is a league's format class as derived from Sleeper's UNDOCUMENTED
// settings.type. The mapping is what the operator's six leagues returned on
// 2026-09-21 (bd memory sleeper-multi-league-format-signals-2026-09-21); it is
// a default, not a fact, which is why DeriveProfile takes overrides.
type LeagueKind string

const (
	KindRedraft    LeagueKind = "redraft"
	KindKeeper     LeagueKind = "keeper"
	KindDynasty    LeagueKind = "dynasty"
	KindGuillotine LeagueKind = "guillotine"
	KindUnknown    LeagueKind = "unknown"
)

// LeagueProfile is everything the multi-league jobs derive from one league
// once: which StatsGuy column grades its trades and ranks its pickups, and
// the two facts that column was chosen from.
type LeagueProfile struct {
	LeagueID  string
	Name      string
	Superflex bool
	Kind      LeagueKind
	// Format is the StatsGuy column (sf_dynasty | non_sf_dynasty | sf_redraft
	// | non_sf_redraft). FormatSource says how it was chosen: "derived" from
	// Kind+Superflex, or "override" from SLEEPER_FORMAT_OVERRIDES. Every run
	// prints both, because settings.type is undocumented and a wrong column
	// renders as a confident number rather than a visible miss.
	Format       string
	FormatSource string
}

var knownFormats = map[string]bool{
	"sf_dynasty": true, "non_sf_dynasty": true, "sf_redraft": true, "non_sf_redraft": true,
}

// KnownFormat reports whether f is one of StatsGuy's four value columns.
func KnownFormat(f string) bool { return knownFormats[f] }

// DeriveProfile derives a league's profile. Superflex comes from
// roster_positions, which is documented and reliable. Kind comes from
// settings.type. An override for this league id replaces the derived Format
// and is labelled as such; it never rewrites Kind, which is reported as
// observed.
func DeriveProfile(lg sleeper.League, overrides map[string]string) LeagueProfile {
	p := LeagueProfile{LeagueID: lg.LeagueID, Name: lg.Name, Kind: KindUnknown}
	for _, pos := range lg.RosterPositions {
		if pos == "SUPER_FLEX" {
			p.Superflex = true
			break
		}
	}
	if t, ok := lg.Settings["type"]; ok {
		switch t {
		case 0:
			p.Kind = KindRedraft
		case 1:
			p.Kind = KindKeeper
		case 2:
			p.Kind = KindDynasty
		case 3:
			p.Kind = KindGuillotine
		}
	}
	p.Format = formatFor(p.Kind, p.Superflex)
	p.FormatSource = "derived"
	if f, ok := overrides[lg.LeagueID]; ok {
		p.Format = f
		p.FormatSource = "override"
	}
	return p
}

// formatFor maps a league's kind and superflex-ness to the StatsGuy column
// that should price it.
//
// OPERATOR-WRITTEN (spec Section 2). Dynasty and unknown leagues price on the
// dynasty column (multi-year value); unknown is priced on the dynasty column
// too (sf_dynasty when superflex, else non_sf_dynasty) — the same column the
// deployment's original single-league default used, not a fallback to a
// DYNASTY_FORMAT env var, which the multi-league jobs no longer read. Keeper
// leagues price on the redraft column — a league that keeps a handful of
// players is closer to redraft than to dynasty. Redraft and guillotine price
// on the redraft column (single-season value). Superflex picks sf_* over
// non_sf_*.
func formatFor(kind LeagueKind, superflex bool) string {
	dynastyLike := kind == KindDynasty || kind == KindUnknown
	switch {
	case dynastyLike && superflex:
		return "sf_dynasty"
	case dynastyLike:
		return "non_sf_dynasty"
	case superflex:
		return "sf_redraft"
	default:
		return "non_sf_redraft"
	}
}

// ParseFormatOverrides parses SLEEPER_FORMAT_OVERRIDES: a comma-separated
// list of <league_id>=<format>. Whitespace around entries is ignored. An
// empty string is an empty map. Any malformed entry, unknown format, or
// repeated league id is an error rather than a silent skip — an override that
// did not apply would reproduce the exact silent-wrong-column failure the
// override exists to prevent.
func ParseFormatOverrides(s string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		id, format, ok := strings.Cut(entry, "=")
		id, format = strings.TrimSpace(id), strings.TrimSpace(format)
		if !ok || id == "" || format == "" {
			return nil, fmt.Errorf("SLEEPER_FORMAT_OVERRIDES: entry %q is not <league_id>=<format>", entry)
		}
		if !KnownFormat(format) {
			return nil, fmt.Errorf("SLEEPER_FORMAT_OVERRIDES: %q: unknown format %q (want sf_dynasty|non_sf_dynasty|sf_redraft|non_sf_redraft)", entry, format)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("SLEEPER_FORMAT_OVERRIDES: league %s listed twice", id)
		}
		out[id] = format
	}
	return out, nil
}
