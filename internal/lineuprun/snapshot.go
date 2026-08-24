package lineuprun

import (
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// dateResult holds the per-date outputs of a single optimization run. Used to
// pass data between the parallel optimize pass and the sequential print/apply
// / archive pass.
type dateResult struct {
	date             time.Time
	period           fantrax.DailyPeriod
	isToday          bool
	hitterResult     optimizer.Result
	pitcherResult    optimizer.PitcherResult
	warnings         []string
	venues           map[string]string
	benchedToday     map[string]bool
	hitterBreakdowns map[string]*projections.HitterBreakdown
	hitterPipelines  map[string]*projections.HitterPipelineDetail
	pitcherPipelines map[string]*projections.PitcherPipelineDetail
	// budget is the GS budget the gate ran against, carried so the snapshot can
	// archive the forward view the gate believed (see forecastRows). nil on
	// every future date, since the gate applies to today only.
	budget *optimizer.GSBudget
}

// writeProjectionSnapshot archives the per-date projection values the optimizer
// used so a future `rosterbot backtest` can grade projection accuracy exactly
// (no reconstruction). snapshotRoot names the partition WITHIN st — "snapshots"
// for a normal optimize run, or a per-system shadow-capture partition (see
// Options.SnapshotRoot).
func writeProjectionSnapshot(dr dateResult, projSystem string, slotName map[string]string, hittersNoData, pitchersNoData bool, st backtest.SnapshotStore, snapshotRoot string) error {
	return backtest.WriteSnapshot(st, snapshotRoot, buildSnapshot(dr, projSystem, slotName, hittersNoData, pitchersNoData))
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
		GSLimit:          dr.pitcherResult.GateReport.Limit,
		GSFloor:          dr.pitcherResult.GateReport.Floor,
		GSForecast:       forecastRows(dr.budget),
	}

	for _, sp := range dr.hitterResult.Scored {
		snap.Hitters = append(snap.Hitters, backtest.SnapshotPlayer{
			PlayerID:       sp.Player.ID,
			Name:           sp.Player.Name,
			MLBTeam:        sp.Player.MLBTeam,
			ProjPtsPerGame: sp.ExpectedPts,
			HasGame:        sp.HasGame,
			IsPitcher:      false,
			Slot:           slotName[sp.Player.RosterPosition],
			Locked:         sp.Player.Locked,
			Status:         sp.Player.Status,
			Eligibility:    sp.Player.Positions,
		})
	}

	gsSuppressed := make(map[string]bool, len(dr.pitcherResult.GateReport.Suppressed))
	for _, s := range dr.pitcherResult.GateReport.Suppressed {
		gsSuppressed[s.PlayerID] = true
	}
	gsProtected := make(map[string]bool, len(dr.pitcherResult.GateReport.Protected))
	for _, s := range dr.pitcherResult.GateReport.Protected {
		gsProtected[s.PlayerID] = true
	}

	for _, sp := range dr.pitcherResult.Scored {
		role := "RP"
		if strings.Contains(sp.Player.PosShortNames, "SP") {
			role = "SP"
		}
		snap.Pitchers = append(snap.Pitchers, backtest.SnapshotPlayer{
			PlayerID:         sp.Player.ID,
			Name:             sp.Player.Name,
			MLBTeam:          sp.Player.MLBTeam,
			ProjPtsPerGame:   sp.ExpectedPts,
			HasGame:          sp.HasGame,
			IsStarter:        sp.IsStarter,
			Role:             role,
			IsPitcher:        true,
			Slot:             slotName[sp.Player.RosterPosition],
			Locked:           sp.Player.Locked,
			Status:           sp.Player.Status,
			GSSuppressed:     gsSuppressed[sp.Player.ID],
			GSFloorProtected: gsProtected[sp.Player.ID],
			Eligibility:      sp.Player.Positions,
		})
	}

	return snap
}

// forecastRows flattens the gate's forward view into the archived form.
//
// Days at or before today are dropped: the forecast covers today+1 onward by
// construction, and a same-day row would record demand the gate never treated
// as future. Nil when no budget was in force, matching the GSLimit==0
// convention for a --matchup pre-write.
func forecastRows(b *optimizer.GSBudget) []backtest.GSForecastDay {
	if b == nil {
		return nil
	}
	var rows []backtest.GSForecastDay
	for _, f := range b.Forecast {
		if !f.Date.After(b.Today) {
			continue
		}
		rows = append(rows, backtest.GSForecastDay{
			Date:           f.Date.Format("2006-01-02"),
			ConfirmedCount: len(f.ConfirmedStarters),
			Estimated:      f.Estimated,
		})
	}
	return rows
}
