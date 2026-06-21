package gscheck

import "github.com/nixon-commits/rosterbot/internal/lineupapi"

// toWireResult maps GS violations to the iOS wire shape. The limit and over_by
// are derived from the league max/min (the Violation itself carries only the
// used count and kind). For an "over" violation limit=gsMax and over_by =
// used-gsMax; for "under" limit=gsMin.
func toWireResult(vs []Violation, period string, gsMax, gsMin int) lineupapi.GSCheckResult {
	out := lineupapi.GSCheckResult{Period: period}
	for _, v := range vs {
		o := lineupapi.GSViolationOut{Team: v.TeamName, Used: v.GSUsed}
		switch v.Kind {
		case ViolationMax:
			o.Kind = "over"
			o.Limit = gsMax
			if v.GSUsed > gsMax {
				o.OverBy = v.GSUsed - gsMax
			}
		case ViolationMin:
			o.Kind = "under"
			o.Limit = gsMin
		}
		out.Violations = append(out.Violations, o)
	}
	return out
}
