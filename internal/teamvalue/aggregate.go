package teamvalue

import (
	"sort"
	"time"

	"github.com/pmurley/go-fantrax/models"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/positions"
)

// Aggregate computes one Row per fantasy team from the full Fantrax player pool
// and the current HKB rankings, for the given day.
//
// Each rostered player (FantasyTeamID != "") is joined to its HKB dynasty Value
// by normalized name — the same playername.Normalize join used by internal/claims
// and internal/transactions. Matched value is bucketed by:
//   - minors vs MLB: Fantrax MinorsEligible (farm-eligible ≈ farm system);
//   - hitter vs pitcher: positions.IsPitcherSlot over the player's eligibility
//     IDs. A two-way player with any pitcher eligibility (e.g. Ohtani) resolves
//     to pitcher — a deterministic, documented tiebreak.
//
// A rostered player with no HKB match increments RosteredCount only (value and
// the four count leaves are matched-players-only), so MatchedCount < RosteredCount
// signals the totals undercount by the unmatched players. HKB draft picks
// (AssetType != "PLAYER") never match a rostered name and are naturally excluded.
//
// teamNames / teamLogos are denormalized into each Row (from
// GetScoringPeriodsAndTeams) so the read+render path needs no Fantrax call; the
// pool's own FantasyTeamName is a fallback when the name map lacks a team.
func Aggregate(date time.Time, pool []models.PoolPlayer, hkbPlayers []hkb.Player, teamNames, teamLogos map[string]string) []Row {
	lookup := buildHKBLookup(hkbPlayers)
	dt := date.UTC().Format("2006-01-02")

	byTeam := make(map[string]*Row)
	for _, pp := range pool {
		if pp.FantasyTeamID == "" {
			continue // free agent / waivers — not on any team
		}
		r := byTeam[pp.FantasyTeamID]
		if r == nil {
			name := teamNames[pp.FantasyTeamID]
			if name == "" {
				name = pp.FantasyTeamName
			}
			r = &Row{Dt: dt, TeamID: pp.FantasyTeamID, TeamName: name, LogoURL: teamLogos[pp.FantasyTeamID]}
			byTeam[pp.FantasyTeamID] = r
		}
		r.RosteredCount++

		hp, ok := lookup[playername.Normalize(pp.Name)]
		if !ok {
			continue // unmatched: counted as rostered, contributes no value
		}
		r.MatchedCount++

		pitcher := isPitcher(pp.Positions)
		minors := pp.MinorsEligible
		switch {
		case pitcher && minors:
			r.PitcherMinorsValue += hp.Value
			r.PitcherMinorsCount++
		case pitcher:
			r.PitcherMLBValue += hp.Value
			r.PitcherMLBCount++
		case minors:
			r.HitterMinorsValue += hp.Value
			r.HitterMinorsCount++
		default:
			r.HitterMLBValue += hp.Value
			r.HitterMLBCount++
		}
	}

	out := make([]Row, 0, len(byTeam))
	for _, r := range byTeam {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TeamID < out[j].TeamID })
	return out
}

// buildHKBLookup maps normalized player name → HKB player, matching the join in
// internal/claims and internal/transactions.
func buildHKBLookup(players []hkb.Player) map[string]hkb.Player {
	m := make(map[string]hkb.Player, len(players))
	for _, p := range players {
		m[playername.Normalize(p.Name)] = p
	}
	return m
}

// isPitcher reports whether any of the player's eligibility IDs is a pitcher slot.
func isPitcher(posIDs []string) bool {
	for _, id := range posIDs {
		if positions.IsPitcherSlot(id) {
			return true
		}
	}
	return false
}
