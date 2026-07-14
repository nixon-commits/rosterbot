package lineuprun

import (
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// dateResult holds the per-date outputs of a single optimization run. Used to
// pass data between the parallel optimize pass and the sequential print/apply
// / archive pass.
type dateResult struct {
	date             time.Time
	period           int
	isToday          bool
	hitterResult     optimizer.Result
	pitcherResult    optimizer.PitcherResult
	warnings         []string
	venues           map[string]string
	benchedToday     map[string]bool
	hitterBreakdowns map[string]*projections.HitterBreakdown
	hitterPipelines  map[string]*projections.HitterPipelineDetail
	pitcherPipelines map[string]*projections.PitcherPipelineDetail
}

// writeProjectionSnapshot archives the per-date projection values the optimizer
// used so a future `rosterbot backtest` can grade projection accuracy exactly
// (no reconstruction). snapshotRoot is the exact directory to write into —
// callers pass either the flat .backtest/snapshots/ path (normal optimize run)
// or a per-system shadow-capture partition (see Options.SnapshotRoot).
func writeProjectionSnapshot(dr dateResult, projSystem string, slotName map[string]string, hittersNoData, pitchersNoData bool, snapshotRoot string) error {
	return backtest.WriteSnapshot(snapshotRoot, buildSnapshot(dr, projSystem, slotName, hittersNoData, pitchersNoData))
}

// buildSnapshot is the pure mapping from a day's optimizer results to the
// serializable snapshot. Beyond the projected value it records the look-back
// fields — slot occupied, locked, position eligibility, role, and whether we
// started the player — so future analysis can slice projection error along any
// of those dimensions. slotName maps a player's RosterPosition (slot pos ID) to
// its display name; benched players (no active slot) get an empty Slot.
func buildSnapshot(dr dateResult, projSystem string, slotName map[string]string, hittersNoData, pitchersNoData bool) backtest.Snapshot {
	snap := backtest.Snapshot{
		Date:             dr.date.Format("2006-01-02"),
		ProjectionSystem: projSystem,
		GeneratedAt:      time.Now().UTC(),
		HittersNoData:    hittersNoData,
		PitchersNoData:   pitchersNoData,
	}

	for _, sp := range dr.hitterResult.Scored {
		snap.Hitters = append(snap.Hitters, backtest.SnapshotPlayer{
			PlayerID:       sp.Player.ID,
			Name:           sp.Player.Name,
			MLBTeam:        sp.Player.MLBTeam,
			ProjPtsPerGame: sp.ExpectedPts,
			HasGame:        sp.HasGame,
			WasStarted:     sp.Player.Status == "Active",
			IsPitcher:      false,
			Slot:           slotName[sp.Player.RosterPosition],
			Locked:         sp.Player.Locked,
			Eligibility:    sp.Player.Positions,
		})
	}

	for _, sp := range dr.pitcherResult.Scored {
		role := "RP"
		if strings.Contains(sp.Player.PosShortNames, "SP") {
			role = "SP"
		}
		snap.Pitchers = append(snap.Pitchers, backtest.SnapshotPlayer{
			PlayerID:       sp.Player.ID,
			Name:           sp.Player.Name,
			MLBTeam:        sp.Player.MLBTeam,
			ProjPtsPerGame: sp.ExpectedPts,
			HasGame:        sp.HasGame,
			WasStarted:     sp.Player.Status == "Active",
			IsStarter:      sp.IsStarter,
			Role:           role,
			IsPitcher:      true,
			Slot:           slotName[sp.Player.RosterPosition],
			Locked:         sp.Player.Locked,
			Eligibility:    sp.Player.Positions,
		})
	}

	return snap
}
