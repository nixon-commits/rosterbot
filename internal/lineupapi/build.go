package lineupapi

import (
	"math"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/positions"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// Inputs is the neutral, cmd-independent view of a single day's optimizer
// output that Build maps into the wire response. cmd/optimize adapts its
// internal dateResult into this; tests construct it directly with fake players.
type Inputs struct {
	Date         string         // YYYY-MM-DD
	LeagueID     string         // Fantrax league ID
	TeamID       string         // Fantrax team ID
	HitterSlots  []fantrax.Slot // active hitter slot definitions, in display order
	PitcherSlots []fantrax.Slot // active pitcher slot definitions, in display order
	Hitters      []optimizer.ScoredPlayer
	Pitchers     []optimizer.ScoredPitcher
	BenchedToday map[string]bool // normalized name -> benched in real MLB lineup
	DataWarnings []string        // pass-through data-availability warnings
}

// Build maps a day's optimizer output into the API response.
//
// Slots are emitted in roster order — every active slot first (filled from the
// players the optimizer assigned to it, an open slot rendered as null), then
// one "BN" row per reserve player. projected_points is the sum of starters that
// actually count (hitters with a game; pitchers with a game who are a confirmed
// SP starter or any RP), mirroring the CLI's "Combined Expected".
func Build(in Inputs) LineupResponse {
	resp := LineupResponse{
		Date:     in.Date,
		LeagueID: in.LeagueID,
		TeamID:   in.TeamID,
		Slots:    []Slot{},
		Warnings: []string{},
	}

	// Active players queued by the slot pos ID they were assigned to; multiple
	// slots can share a pos ID (4×OF, 3×UT), so we pop per slot instance below.
	activeByPos := map[string][]*Player{}
	var bench []Slot
	var projected float64

	for _, sp := range in.Hitters {
		pl := hitterPlayer(sp, in.BenchedToday)
		if sp.Player.Status != "Active" {
			bench = append(bench, Slot{Slot: "BN", Player: pl})
			continue
		}
		activeByPos[sp.Player.RosterPosition] = append(activeByPos[sp.Player.RosterPosition], pl)
		if sp.HasGame {
			projected += sp.ExpectedPts
		}
		if in.BenchedToday[projections.NormalizeName(sp.Player.Name)] {
			resp.Warnings = append(resp.Warnings, sp.Player.Name+" benched in real lineup")
		}
	}

	for _, sp := range in.Pitchers {
		pl := pitcherPlayer(sp)
		if sp.Player.Status != "Active" {
			bench = append(bench, Slot{Slot: "BN", Player: pl})
			continue
		}
		activeByPos[sp.Player.RosterPosition] = append(activeByPos[sp.Player.RosterPosition], pl)
		isRP := !strings.Contains(sp.Player.PosShortNames, "SP")
		if sp.HasGame && (sp.IsStarter || isRP) {
			projected += sp.ExpectedPts
		}
	}

	// Fill each defined active slot from its pos-ID queue; nil when none left.
	for _, sd := range append(append([]fantrax.Slot{}, in.HitterSlots...), in.PitcherSlots...) {
		name := positions.SlotName(sd.PosID)
		if name == "" {
			name = sd.PosName
		}
		var pl *Player
		if q := activeByPos[sd.PosID]; len(q) > 0 {
			pl = q[0]
			activeByPos[sd.PosID] = q[1:]
		}
		resp.Slots = append(resp.Slots, Slot{Slot: name, Player: pl})
	}

	resp.Slots = append(resp.Slots, bench...)
	resp.Warnings = append(resp.Warnings, in.DataWarnings...)
	resp.ProjectedPoints = round2(projected)
	return resp
}

// --- status / warning policy ---------------------------------------------
// This block encodes the product decisions about what the iOS UI can show.
// Adjust the status vocabulary and warning phrasing here; everything else in
// Build is mechanical slot plumbing.

func hitterPlayer(sp optimizer.ScoredPlayer, benched map[string]bool) *Player {
	status := "OK"
	switch {
	case sp.Player.Locked:
		status = "LOCKED"
	case benched[projections.NormalizeName(sp.Player.Name)]:
		status = "BENCHED"
	}
	return &Player{
		ID:     sp.Player.ID,
		Name:   sp.Player.Name,
		Team:   sp.Player.MLBTeam,
		Pos:    posCodes(sp.Player.Positions),
		Proj:   round2(sp.ExpectedPts),
		Status: status,
	}
}

func pitcherPlayer(sp optimizer.ScoredPitcher) *Player {
	status := "OK"
	if sp.Player.Locked {
		status = "LOCKED"
	}
	return &Player{
		ID:     sp.Player.ID,
		Name:   sp.Player.Name,
		Team:   sp.Player.MLBTeam,
		Pos:    posCodes(sp.Player.Positions),
		Proj:   round2(sp.ExpectedPts),
		Status: status,
	}
}

// --------------------------------------------------------------------------

// posCodes maps Fantrax numeric position IDs to readable codes (C, 1B, SP, …),
// deduping and dropping any unknown IDs. Always non-nil so the JSON is [] not
// null.
func posCodes(ids []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		n := positions.SlotName(id)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
