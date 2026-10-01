package dynasty

import (
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// SnapPlayer is one player as the daily pickups snapshot records them: the
// fields the role-change detector compares day over day, nothing else. Kept
// small on purpose — about 900 rows a day are archived forever (measured
// 2026-09-30: 897 rosterable on-club players, out of 2,758 on-club before the
// position filter; the archive is NoBackfill: Sleeper keeps no depth-chart
// history).
type SnapPlayer struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Team               string `json:"team"`
	Position           string `json:"position"`
	DepthChartPosition string `json:"depth_chart_position,omitempty"`
	// DepthChartOrder is 0 when Sleeper listed no slot (its JSON null); the
	// detector treats 0 as "unlisted", distinct from 2+.
	DepthChartOrder int    `json:"depth_chart_order,omitempty"`
	InjuryStatus    string `json:"injury_status,omitempty"`
	Status          string `json:"status,omitempty"`
}

// PlayerSnapshot is one capture of the filtered player dump. HasDepthData
// is true when at least one included player has DepthChartOrder > 0, so a
// stale cache from an older binary (which has no depth-chart fields) can be
// distinguished from a capture with genuinely no depth data.
type PlayerSnapshot struct {
	CapturedAt   time.Time             `json:"captured_at"`
	Players      map[string]SnapPlayer `json:"players"`
	HasDepthData bool                  `json:"has_depth_data"`
}

// slotPositions expands a Sleeper roster slot into the positions it accepts.
// Plain position names map to themselves; bench, IR and taxi are not lineup
// slots and contribute nothing.
var slotPositions = map[string][]string{
	"FLEX":       {"RB", "WR", "TE"},
	"SUPER_FLEX": {"QB", "RB", "WR", "TE"},
	"REC_FLEX":   {"WR", "TE"},
	"WRRB_FLEX":  {"RB", "WR"},
	"IDP_FLEX":   {"DL", "LB", "DB"},
	"BN":         nil,
	"IR":         nil,
	"TAXI":       nil,
}

// RosterablePositions is the union of positions any of the leagues can start
// — the filter that keeps a kicker out of a league with no K slot (measured
// 2026-09-20: Sleeper's top-50 trending adds were 8 unrostered names, every
// one a K or DEF the dynasty league cannot roster).
func RosterablePositions(leagues []sleeper.League) map[string]bool {
	out := map[string]bool{}
	for _, lg := range leagues {
		for _, slot := range lg.RosterPositions {
			if exp, known := slotPositions[slot]; known {
				for _, p := range exp {
					out[p] = true
				}
				continue
			}
			out[slot] = true
		}
	}
	return out
}

// BuildPlayerSnapshot filters the dump to players on an NFL club at a
// rosterable position and flattens the nullable depth-chart order. Sets
// HasDepthData to true when at least one included player has DepthChartOrder > 0.
func BuildPlayerSnapshot(now time.Time, players map[string]sleeper.Player, positions map[string]bool) PlayerSnapshot {
	snap := PlayerSnapshot{CapturedAt: now.UTC(), Players: make(map[string]SnapPlayer, len(players)/4)}
	for id, p := range players {
		if p.Team == "" || !positions[p.Position] {
			continue
		}
		order := 0
		if p.DepthChartOrder != nil {
			order = *p.DepthChartOrder
		}
		if order > 0 {
			snap.HasDepthData = true
		}
		snap.Players[id] = SnapPlayer{
			ID: id, Name: PlayerDisplayName(p), Team: p.Team, Position: p.Position,
			DepthChartPosition: p.DepthChartPosition, DepthChartOrder: order,
			InjuryStatus: p.InjuryStatus, Status: p.Status,
		}
	}
	return snap
}

// PlayerDisplayName is the one spelling of a Sleeper player's name this
// package uses; playerDisplayName in aggregate.go delegates to it.
func PlayerDisplayName(p sleeper.Player) string {
	return strings.TrimSpace(p.FirstName + " " + p.LastName)
}
