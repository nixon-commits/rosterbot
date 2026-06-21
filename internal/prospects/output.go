package prospects

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult flattens the prospect Report into the iOS wire shape. Alerts keep
// their kind so the client can split call-ups from breakouts; upgrades flatten
// the drop→add pair to names+ranks.
func toWireResult(r Report) lineupapi.ProspectsResult {
	out := lineupapi.ProspectsResult{}
	for _, a := range r.Alerts {
		out.Alerts = append(out.Alerts, lineupapi.ProspectAlertOut{
			Name:     a.PlayerName,
			Team:     a.MLBTeam,
			Pos:      a.Position,
			Kind:     string(a.Kind),
			Priority: a.Priority,
			Detail:   a.Detail,
			Rank:     a.Rank,
		})
	}
	for _, set := range r.Upgrades {
		for _, u := range set.Candidates {
			out.Upgrades = append(out.Upgrades, lineupapi.ProspectUpgradeOut{
				Source:   set.Source,
				Drop:     u.Drop.Name,
				DropRank: u.Drop.Rank,
				Add:      u.Add.Name,
				AddRank:  u.Add.Rank,
				RankGap:  u.RankGap,
				NearTerm: u.NearTerm,
			})
		}
	}
	return out
}
