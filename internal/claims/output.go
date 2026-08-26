package claims

import "github.com/nixon-commits/rosterbot/internal/lineupapi/jobwire"

// toWireResult maps the daily claims Ledger to the iOS wire shape (one row per
// added player; the first dropped player, if any, is attributed to the row).
func toWireResult(led Ledger) jobwire.ClaimsResult {
	out := jobwire.ClaimsResult{}
	for _, e := range led.Entries {
		c := jobwire.ClaimOut{
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
