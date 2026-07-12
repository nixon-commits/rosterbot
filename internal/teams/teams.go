// Package teams is the single source of truth for MLB team-abbreviation
// normalization. It has no dependencies on the rest of the tree (mirroring
// internal/positions for position-ID semantics) so every package that
// touches a team abbreviation — fantrax, projections, schedule, prospects —
// can converge on one canonical form without risking an import cycle.
package teams

import "strings"

// Normalize maps team abbreviations from various upstream sources
// (FanGraphs, MLB statsapi, Fantrax) to a single canonical form. Idempotent:
// safe to call on already-normalized input, and safe to call more than once
// across a pipeline.
func Normalize(team string) string {
	switch strings.ToUpper(strings.TrimSpace(team)) {
	case "SDP":
		return "SD"
	case "SFG":
		return "SF"
	case "KCR":
		return "KC"
	case "WSN":
		return "WSH"
	case "TBR":
		return "TB"
	case "AZ":
		return "ARI"
	case "CWS":
		return "CHW"
	case "OAK":
		return "ATH"
	default:
		return strings.ToUpper(strings.TrimSpace(team))
	}
}
