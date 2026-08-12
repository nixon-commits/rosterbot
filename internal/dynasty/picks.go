package dynasty

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// AggregatePicks reconstructs every team's owned future draft picks from
// traded_picks + the league's draft_rounds setting, priced generically per
// round at StatsGuy's "mid" variant tier (Estimated: true on every row --
// there is no draft order yet for an unresolved future pick, so there is no
// team standing to prefer the "early"/"late" tiers over "mid").
//
// Draft years are taken from whatever years StatsGuy's own pick grid prices
// (distinctPickYears), not a hardcoded lookahead -- self-correcting as
// StatsGuy's grid depth changes across seasons, rather than a magic "+3"
// constant.
//
// Ownership defaults to each team owning its own picks in every (year,
// round); a traded_picks record overrides that default -- Sleeper's
// traded_picks endpoint reports CURRENT ownership per pick, not a full trade
// history, so at most one record applies per (year, round, original owner).
// A round with no "mid"-variant price in the bundle is silently skipped
// (measured 2026-08-10: zero of 58 live traded-pick records fell outside
// StatsGuy's priced grid, but an unpriced round must not crash the run).
func AggregatePicks(date time.Time, league *sleeper.League, rosters []sleeper.Roster, users []sleeper.User, tradedPicks []sleeper.TradedPick, bundle *statsguy.Bundle) []Row {
	dt := date.UTC().Format("2006-01-02")
	names := TeamNames(rosters, users)
	draftRounds := league.Settings["draft_rounds"]
	if draftRounds <= 0 {
		return nil
	}

	years := distinctPickYears(bundle)

	// override[year][round][originalRosterID] = currentOwnerRosterID
	type pickKey struct {
		year, round, orig int
	}
	override := make(map[pickKey]int, len(tradedPicks))
	for _, tp := range tradedPicks {
		year, err := strconv.Atoi(tp.Season)
		if err != nil {
			continue
		}
		override[pickKey{year, tp.Round, tp.RosterID}] = tp.OwnerID
	}

	var rows []Row
	for _, r := range rosters {
		for _, year := range years {
			for round := 1; round <= draftRounds; round++ {
				currentOwner := r.RosterID
				if owner, ok := override[pickKey{year, round, r.RosterID}]; ok {
					currentOwner = owner
				}
				value, ok := MidVariantPrice(bundle, year, round)
				if !ok {
					continue // unpriced round: skip, don't crash
				}
				teamID := strconv.Itoa(currentOwner)
				name := fmt.Sprintf("%d Round %d Pick", year, round)
				if currentOwner != r.RosterID {
					name += " (via " + names[r.RosterID] + ")"
				}
				rows = append(rows, Row{
					Dt:                dt,
					TeamID:            teamID,
					TeamName:          names[currentOwner],
					AssetType:         "pick",
					AssetID:           fmt.Sprintf("pick:%d:%d:%d", year, round, r.RosterID),
					Name:              name,
					Estimated:         true,
					ValueSFDynasty:    value.SFDynasty,
					ValueNonSFDynasty: value.NonSFDynasty,
					ValueSFRedraft:    value.SFRedraft,
					ValueNonSFRedraft: value.NonSFRedraft,
				})
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TeamID != rows[j].TeamID {
			return rows[i].TeamID < rows[j].TeamID
		}
		return rows[i].AssetID < rows[j].AssetID
	})
	return rows
}

// MidVariantPrice looks up a round's generic "mid" tier price for the given
// year -- the price used for an unresolved future pick, where there is no
// draft order yet and so no team standing to prefer "early"/"late" over
// "mid". Reused by AggregatePicks (season-wide reconstruction) and the
// football-trades command (pricing a specific pick asset in one transaction),
// so the two can never price the same pick differently.
func MidVariantPrice(bundle *statsguy.Bundle, year, round int) (statsguy.FormatValues, bool) {
	for _, p := range bundle.Picks {
		if p.Variant == "mid" && p.Year == year && p.Round == round {
			return p.Value, true
		}
	}
	return statsguy.FormatValues{}, false
}

func distinctPickYears(bundle *statsguy.Bundle) []int {
	seen := make(map[int]bool)
	for _, p := range bundle.Picks {
		seen[p.Year] = true
	}
	years := make([]int, 0, len(seen))
	for y := range seen {
		years = append(years, y)
	}
	sort.Ints(years)
	return years
}
