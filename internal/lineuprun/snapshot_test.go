package lineuprun

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
)

// TestBuildSnapshot_MapsRichFields verifies the pure snapshot builder copies
// the projected value plus the extra look-back fields (slot, locked,
// eligibility, role, was-started) off the optimizer results.
func TestBuildSnapshot_MapsRichFields(t *testing.T) {
	slotName := map[string]string{"012": "OF", "017": "P"}
	dr := dateResult{
		date: time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC),
		hitterResult: optimizer.Result{
			Scored: []optimizer.ScoredPlayer{
				{
					Player: fantrax.Player{
						ID: "h1", Name: "Started OF", MLBTeam: "NYY",
						Positions: []string{"012", "014"}, RosterPosition: "012",
						Status: "Active", Locked: true,
					},
					ExpectedPts: 8.5, HasGame: true,
				},
				{
					Player: fantrax.Player{
						ID: "h2", Name: "Bench OF", MLBTeam: "BOS",
						Positions: []string{"012"}, RosterPosition: "", Status: "Reserve",
					},
					ExpectedPts: 4.0, HasGame: true,
				},
			},
		},
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{
				{
					Player: fantrax.Player{
						ID: "p1", Name: "Ace", MLBTeam: "LAD",
						Positions: []string{"015"}, PosShortNames: "SP",
						RosterPosition: "017", Status: "Active",
					},
					ExpectedPts: 16.0, HasGame: true, IsStarter: true,
				},
			},
		},
	}

	snap := buildSnapshot(dr, "depthcharts", "depthcharts", slotName, false, false)

	if snap.Date != "2026-05-29" {
		t.Errorf("Date = %q, want 2026-05-29", snap.Date)
	}
	if snap.ProjectionSystem != "depthcharts" {
		t.Errorf("ProjectionSystem = %q, want depthcharts", snap.ProjectionSystem)
	}
	if len(snap.Hitters) != 2 {
		t.Fatalf("want 2 hitters, got %d", len(snap.Hitters))
	}
	h := snap.Hitters[0]
	if h.PlayerID != "h1" || h.ProjPtsPerGame != 8.5 || h.Status != "Active" || h.Slot != "OF" || !h.Locked {
		t.Errorf("started hitter mapping wrong: %+v", h)
	}
	if len(h.Eligibility) != 2 || h.Eligibility[0] != "012" {
		t.Errorf("eligibility mapping wrong: %+v", h.Eligibility)
	}
	// Status, not a derived "was started" boolean: the snapshot records what the
	// roster looked like when it was written and nothing more. Deployment is
	// joined at grading time from the day's own roster (rosterbot-x3xo).
	if bench := snap.Hitters[1]; bench.Status == "Active" || bench.Slot != "" {
		t.Errorf("bench hitter should not be active or hold a slot: %+v", bench)
	}
	if len(snap.Pitchers) != 1 {
		t.Fatalf("want 1 pitcher, got %d", len(snap.Pitchers))
	}
	p := snap.Pitchers[0]
	if p.Role != "SP" || !p.IsStarter || p.Slot != "P" || !p.IsPitcher {
		t.Errorf("pitcher mapping wrong: %+v", p)
	}
}

// TestBuildSnapshot_RecordsPerRoleSystems: with the rosterbot-5qvs split a
// day's hitters and pitchers can come from different models, so the snapshot
// names both. The legacy unified field is kept when the two agree — every
// pre-split file and reader sees no change — and omitted when they differ,
// because a single field that names the hitter system would be lying about
// half the file. Nothing reads it back (verified); it is the audit trail.
func TestBuildSnapshot_RecordsPerRoleSystems(t *testing.T) {
	dr := dateResult{date: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)}

	snap := buildSnapshot(dr, "atc-ros", "steamer-ros", nil, false, false)
	if snap.HitterSystem != "atc-ros" || snap.PitcherSystem != "steamer-ros" {
		t.Errorf("systems = %q/%q, want atc-ros/steamer-ros",
			snap.HitterSystem, snap.PitcherSystem)
	}
	if snap.ProjectionSystem != "" {
		t.Errorf("split run: ProjectionSystem = %q, want omitted", snap.ProjectionSystem)
	}

	snap = buildSnapshot(dr, "depthcharts-ros", "depthcharts-ros", nil, false, false)
	if snap.ProjectionSystem != "depthcharts-ros" {
		t.Errorf("unified run: ProjectionSystem = %q, want depthcharts-ros", snap.ProjectionSystem)
	}
	if snap.HitterSystem != "depthcharts-ros" || snap.PitcherSystem != "depthcharts-ros" {
		t.Errorf("unified run systems = %q/%q, want both depthcharts-ros",
			snap.HitterSystem, snap.PitcherSystem)
	}
}

// TestBuildSnapshot_SetsNoDataFlags verifies the two load-result availability
// flags pass straight through onto the serialized snapshot, independently per
// role, so a later backtest run can tell a data outage from a real projection.
func TestBuildSnapshot_SetsNoDataFlags(t *testing.T) {
	dr := dateResult{date: time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)}

	snap := buildSnapshot(dr, "atc-ros", "atc-ros", nil, true, false)
	if !snap.HittersNoData {
		t.Error("HittersNoData = false, want true")
	}
	if snap.PitchersNoData {
		t.Error("PitchersNoData = true, want false")
	}

	snap = buildSnapshot(dr, "atc-ros", "atc-ros", nil, false, true)
	if snap.HittersNoData {
		t.Error("HittersNoData = true, want false")
	}
	if !snap.PitchersNoData {
		t.Error("PitchersNoData = false, want true")
	}
}

// TestBuildSnapshot_RecordsStatusAndGSCap pins the two fields the roster-shape
// report reads. Status is what separates an IL/Minors player from a healthy
// benched one — without it an injury reads as a structural surplus. GSLimit is
// recorded per day rather than fetched once at report time because Fantrax
// rescales the cap for merged periods, and because the gate runs for today's
// date only (optimize_dates.go nils the budget for every other date), so a
// --matchup pre-write correctly carries no cap at all.
func TestBuildSnapshot_RecordsStatusAndGSCap(t *testing.T) {
	dr := dateResult{
		date: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
		hitterResult: optimizer.Result{
			Scored: []optimizer.ScoredPlayer{
				{
					Player: fantrax.Player{
						ID: "h1", Name: "Healthy Bench", Status: "Reserve",
					},
					ExpectedPts: 4.0, HasGame: true,
				},
				{
					Player: fantrax.Player{
						ID: "h2", Name: "Hurt Bat", Status: "Injured Reserve",
					},
					ExpectedPts: 9.0, HasGame: true,
				},
			},
		},
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{
				{
					Player: fantrax.Player{
						ID: "p1", Name: "Ace", PosShortNames: "SP", Status: "Active",
					},
					ExpectedPts: 16.0, HasGame: true, IsStarter: true,
				},
			},
			GateReport: optimizer.GSGateReport{Limit: 12, Used: 9, Remaining: 3},
		},
	}

	snap := buildSnapshot(dr, "depthcharts-ros", "depthcharts-ros", nil, false, false)

	if snap.GSLimit != 12 {
		t.Errorf("GSLimit = %d, want 12", snap.GSLimit)
	}
	if got := snap.Hitters[0].Status; got != "Reserve" {
		t.Errorf("hitter[0].Status = %q, want Reserve", got)
	}
	if got := snap.Hitters[1].Status; got != "Injured Reserve" {
		t.Errorf("hitter[1].Status = %q, want Injured Reserve", got)
	}
	if got := snap.Pitchers[0].Status; got != "Active" {
		t.Errorf("pitcher[0].Status = %q, want Active", got)
	}
}

// TestBuildSnapshot_NoGateLeavesCapUnset pins that a date optimized without a
// GS budget in force records no cap rather than a misleading zero-that-means-
// twelve. optimize_dates.go nils the budget for every non-today date, so this
// is the ordinary state of a --matchup pre-write.
func TestBuildSnapshot_NoGateLeavesCapUnset(t *testing.T) {
	dr := dateResult{
		date: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{
				{
					Player:      fantrax.Player{ID: "p1", PosShortNames: "SP", Status: "Active"},
					ExpectedPts: 10.0, HasGame: true,
				},
			},
		},
	}

	if snap := buildSnapshot(dr, "depthcharts-ros", "depthcharts-ros", nil, false, false); snap.GSLimit != 0 {
		t.Errorf("GSLimit = %d, want 0 when no budget was in force", snap.GSLimit)
	}
}
