package lineuprun

import (
	"encoding/json"
	"fmt"
	"io"
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

// projInputs is what the run's two projection loads reported about
// themselves, per role: the system actually used, whether it produced nothing,
// and when its data was last fetched from FanGraphs.
//
// It is a struct rather than six more positional parameters because four of
// the six are role pairs of the same type — a transposed pair would attribute
// one role's freshness to the other silently, which is precisely the failure
// FetchedAt exists to make visible.
type projInputs struct {
	// HitterSystem/PitcherSystem are the RESOLVED systems the loads actually
	// used (post RoS-first fallback), per role since rosterbot-5qvs.
	HitterSystem  string
	PitcherSystem string
	// HittersNoData/PitchersNoData record a role whose source had nothing that
	// day; see backtest.Snapshot for why grading treats it separately.
	HittersNoData  bool
	PitchersNoData bool
	// HitterFetchedAt/PitcherFetchedAt are projections.LoadResult.FetchedAt —
	// when the numbers were last fetched upstream, NOT when this run happened.
	// Zero means unknown (CSV fallback, no data, or an undated cache entry),
	// and grading treats it as unjudgeable rather than old.
	HitterFetchedAt  time.Time
	PitcherFetchedAt time.Time
}

// writeProjectionSnapshot archives the per-date projection values the
// optimizer used so a future `rosterbot backtest` can grade projection
// accuracy exactly (no reconstruction). snapshotRoot names the partition
// WITHIN st — "snapshots" for a normal optimize run, or a per-system
// shadow-capture partition (see Options.SnapshotRoot).
//
// The hourly run rewrites the same date's file all day, and a naive
// last-write-wins wipes out the projection a LOCKED player's slot was
// actually decided on: once his team's game starts, the following run's
// rescore reflects what happened (injury, benching, a final line), not what
// the optimizer saw when it made the call — see the SnapshotPlayer.Locked doc
// for exactly what that flag means and its confirmed measured scope
// (rosterbot-w79p; the Cade Cavalli 2026-08-29 case this fixes). So this does
// a per-player merge against whatever was already archived for the date:
// any player whose EXISTING entry is Locked survives the write wholesale
// (every field, since that row is what the optimizer saw at lock time);
// every other player — including one only locked in THIS write — takes the
// fresh value, preserving the intraday-refinement behavior a --matchup
// pre-write of a future (never-locked) date relies on. Date-level fields
// (GeneratedAt, GS forecast/limits, …) always come from the fresh write, so
// the day's final write keeps passing the sameETDate staleness guard. A
// missing prior snapshot is a plain fresh write; a prior snapshot that can't
// be read back degrades to a fresh write with a printed warning, never
// silently.
func writeProjectionSnapshot(out io.Writer, dr dateResult, pi projInputs, slotName map[string]string, st backtest.SnapshotStore, snapshotRoot string) error {
	fresh := buildSnapshot(dr, pi, slotName)

	data, found, err := st.Get(backtest.SnapshotKey(snapshotRoot, fresh.Date))
	switch {
	case err != nil:
		fmt.Fprintf(out, "  ⚠ existing snapshot for %s unreadable, writing fresh: %v\n", fresh.Date, err)
	case found:
		var existing backtest.Snapshot
		if uerr := json.Unmarshal(data, &existing); uerr != nil {
			fmt.Fprintf(out, "  ⚠ existing snapshot for %s corrupt, writing fresh: %v\n", fresh.Date, uerr)
		} else {
			fresh.Hitters = mergeLockedPlayers(fresh.Hitters, existing.Hitters)
			fresh.Pitchers = mergeLockedPlayers(fresh.Pitchers, existing.Pitchers)
		}
	}

	return backtest.WriteSnapshot(st, snapshotRoot, fresh)
}

// mergeLockedPlayers merges a date's freshly-scored player rows onto the
// previously archived ones, keeping any EXISTING row whose Locked is true
// wholesale and taking the fresh row for everyone else. A player present only
// in the existing list is kept only if it was locked (it dropped out of this
// run's scoring, e.g. no longer rostered); a player present only in the fresh
// list is taken as-is.
func mergeLockedPlayers(fresh, existing []backtest.SnapshotPlayer) []backtest.SnapshotPlayer {
	if len(existing) == 0 {
		return fresh
	}
	existingByID := make(map[string]backtest.SnapshotPlayer, len(existing))
	for _, p := range existing {
		existingByID[p.PlayerID] = p
	}

	merged := make([]backtest.SnapshotPlayer, 0, len(fresh))
	seen := make(map[string]bool, len(fresh))
	for _, p := range fresh {
		seen[p.PlayerID] = true
		if old, ok := existingByID[p.PlayerID]; ok && old.Locked {
			merged = append(merged, old)
			continue
		}
		merged = append(merged, p)
	}
	for _, old := range existing {
		if seen[old.PlayerID] {
			continue
		}
		if old.Locked {
			merged = append(merged, old)
		}
	}
	return merged
}

// buildSnapshot is the pure mapping from a day's optimizer results to the
// serializable snapshot. Beyond the projected value it records the look-back
// fields — slot occupied, locked, position eligibility, role, and whether we
// started the player — so future analysis can slice projection error along any
// of those dimensions. slotName maps a player's RosterPosition (slot pos ID) to
// its display name; benched players (no active slot) get an empty Slot.
func buildSnapshot(dr dateResult, pi projInputs, slotName map[string]string) backtest.Snapshot {
	// The unified field survives only when it can be truthful — see the
	// Snapshot doc for why a split run omits it.
	unified := ""
	if pi.HitterSystem == pi.PitcherSystem {
		unified = pi.HitterSystem
	}
	snap := backtest.Snapshot{
		Date:             dr.date.Format("2006-01-02"),
		ProjectionSystem: unified,
		HitterSystem:     pi.HitterSystem,
		PitcherSystem:    pi.PitcherSystem,
		GeneratedAt:      time.Now().UTC(),
		HittersNoData:    pi.HittersNoData,
		PitchersNoData:   pi.PitchersNoData,
		// Recorded in UTC to match GeneratedAt, which the grade-time
		// staleness comparison subtracts it from.
		HitterProjFetchedAt:  utcOrZero(pi.HitterFetchedAt),
		PitcherProjFetchedAt: utcOrZero(pi.PitcherFetchedAt),
		GSLimit:              dr.pitcherResult.GateReport.Limit,
		GSFloor:              dr.pitcherResult.GateReport.Floor,
		GSForecast:           forecastRows(dr.budget),
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

// utcOrZero normalizes a timestamp for the archive without turning the zero
// value into a real instant: time.Time{}.UTC() is still zero, but going
// through one helper keeps "zero means unknown" visible at the call site
// rather than resting on that fact.
func utcOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
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
