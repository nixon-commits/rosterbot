package dynasty

import (
	"sort"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// Coverage is a per-team join-coverage diagnostic produced alongside the
// per-asset rows -- mirrors teamvalue.Row's Rostered/MatchedCount fields,
// reported as a separate type here because Row's grain is per-asset, not
// per-team (there is no natural "per-team row" to attach it to at this
// grain). Persisted alongside Row (coverage.ndjson in the same date
// partition, see WriteCoverage) precisely so the dashboard can show it --
// Row's per-asset rows alone cannot reconstruct RosteredCount, since an
// unmatched player produces no Row at all.
type Coverage struct {
	Dt            string `json:"dt"`
	TeamID        string `json:"team_id"`
	TeamName      string `json:"team_name"`
	RosteredCount int    `json:"rostered_count"`
	MatchedCount  int    `json:"matched_count"`
	// Unmatched lists rostered player names with no StatsGuy value in the run
	// that built this Coverage. json:"-" because it is a diagnostic for the
	// run that computed it, not part of the durable schema -- same discipline
	// as teamvalue.Row.Unmatched.
	Unmatched []string `json:"-"`
}

// TeamNames maps each roster to its display name: user.Metadata.TeamName,
// falling back to user.DisplayName, falling back to "Team <roster_id>". Shared
// by Aggregate/AggregatePicks and the football-trades command so a trade
// alert and the value store never disagree on a team's display name.
func TeamNames(rosters []sleeper.Roster, users []sleeper.User) map[int]string {
	byUser := make(map[string]sleeper.User, len(users))
	for _, u := range users {
		byUser[u.UserID] = u
	}
	names := make(map[int]string, len(rosters))
	for _, r := range rosters {
		u := byUser[r.OwnerID]
		name := u.Metadata.TeamName
		if name == "" {
			name = u.DisplayName
		}
		if name == "" {
			name = "Team " + strconv.Itoa(r.RosterID)
		}
		names[r.RosterID] = name
	}
	return names
}

// AggregatePlayers computes one Row per rostered player with a StatsGuy
// value, for the given day, plus a per-team Coverage diagnostic.
//
// Players are joined to their StatsGuy value by Sleeper player_id EXACTLY --
// no name normalization, unlike the HKB/Fantrax joins elsewhere in this repo
// (measured 2026-08-10: 396/396 valued ids resolve against Sleeper's player
// dump). A rostered player with no StatsGuy value increments the team's
// RosteredCount only and is appended to Unmatched; it produces no Row (there
// is no value to record).
//
// IsStarter comes from SelectStarters (roster.Starters, falling back to
// search_rank) -- never from StatsGuy value. Selecting starters by value
// would make an unvalued player unselectable and starters-coverage 100% by
// construction, the exact rosterbot-hx5 failure this design avoids.
func AggregatePlayers(date time.Time, league *sleeper.League, rosters []sleeper.Roster, users []sleeper.User, players map[string]sleeper.Player, bundle *statsguy.Bundle) ([]Row, []Coverage) {
	dt := date.UTC().Format("2006-01-02")
	names := TeamNames(rosters, users)
	wantStarters := StarterSlotCount(league.RosterPositions)

	var rows []Row
	coverage := make([]Coverage, 0, len(rosters))

	for _, r := range rosters {
		teamID := strconv.Itoa(r.RosterID)
		teamName := names[r.RosterID]
		cov := Coverage{Dt: dt, TeamID: teamID, TeamName: teamName}
		starters := SelectStarters(r, wantStarters, players)

		for _, playerID := range r.Players {
			cov.RosteredCount++
			sv, ok := bundle.Players[playerID]
			if !ok {
				name := playerDisplayName(players, playerID)
				cov.Unmatched = append(cov.Unmatched, name)
				continue
			}
			cov.MatchedCount++
			rows = append(rows, Row{
				Dt:                dt,
				TeamID:            teamID,
				TeamName:          teamName,
				AssetType:         "player",
				AssetID:           playerID,
				Name:              sv.Name,
				Position:          sv.Position,
				IsStarter:         starters[playerID],
				ValueSFDynasty:    sv.Value.SFDynasty,
				ValueNonSFDynasty: sv.Value.NonSFDynasty,
				ValueSFRedraft:    sv.Value.SFRedraft,
				ValueNonSFRedraft: sv.Value.NonSFRedraft,
			})
		}
		coverage = append(coverage, cov)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TeamID != rows[j].TeamID {
			return rows[i].TeamID < rows[j].TeamID
		}
		return rows[i].AssetID < rows[j].AssetID
	})
	return rows, coverage
}

// Aggregate computes every team's per-asset dynasty value rows -- rostered
// players joined to StatsGuy by Sleeper id, plus reconstructed owned draft
// picks -- for the given day, alongside the per-team join-coverage
// diagnostic. Combines AggregatePlayers and AggregatePicks; see each for the
// join and pricing rules.
func Aggregate(date time.Time, league *sleeper.League, rosters []sleeper.Roster, users []sleeper.User, tradedPicks []sleeper.TradedPick, players map[string]sleeper.Player, bundle *statsguy.Bundle) ([]Row, []Coverage) {
	playerRows, coverage := AggregatePlayers(date, league, rosters, users, players, bundle)
	pickRows := AggregatePicks(date, league, rosters, users, tradedPicks, bundle)

	rows := make([]Row, 0, len(playerRows)+len(pickRows))
	rows = append(rows, playerRows...)
	rows = append(rows, pickRows...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TeamID != rows[j].TeamID {
			return rows[i].TeamID < rows[j].TeamID
		}
		if rows[i].AssetType != rows[j].AssetType {
			return rows[i].AssetType < rows[j].AssetType // "pick" < "player" is fine; just deterministic
		}
		return rows[i].AssetID < rows[j].AssetID
	})
	return rows, coverage
}

func playerDisplayName(players map[string]sleeper.Player, id string) string {
	p, ok := players[id]
	if !ok {
		return id
	}
	name := p.FirstName
	if p.LastName != "" {
		if name != "" {
			name += " "
		}
		name += p.LastName
	}
	if name == "" {
		return id
	}
	return name
}
