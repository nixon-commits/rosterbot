package claims

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult maps the daily claims Ledger to the iOS wire shape (one row per
// added player; the first dropped player, if any, is attributed to the row).
func toWireResult(led Ledger) lineupapi.ClaimsResult {
	out := lineupapi.ClaimsResult{}
	for _, e := range led.Entries {
		c := lineupapi.ClaimOut{
			Team:      e.Team,
			ClaimType: e.ClaimType,
			Added:     e.Added.Name,
			AddedPos:  e.Added.Pos,
			NetValue:  e.NetValue,
			Signal:    e.Added.Signal,
		}
		if e.Dropped != nil {
			c.Dropped = e.Dropped.Name
		}
		out.Claims = append(out.Claims, c)
	}
	return out
}
