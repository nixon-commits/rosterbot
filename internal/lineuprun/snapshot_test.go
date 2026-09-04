package lineuprun

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
)

// sysPair is the ordinary case for these tests: one system for both roles,
// real data, and no opinion about input freshness (zero = unknown, graded as
// before). Tests that care about a role split or about staleness spell out a
// projInputs literal instead.
func sysPair(system string) projInputs {
	return projInputs{HitterSystem: system, PitcherSystem: system}
}

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

	snap := buildSnapshot(dr, sysPair("depthcharts"), slotName)

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

	snap := buildSnapshot(dr, projInputs{HitterSystem: "atc-ros", PitcherSystem: "steamer-ros"}, nil)
	if snap.HitterSystem != "atc-ros" || snap.PitcherSystem != "steamer-ros" {
		t.Errorf("systems = %q/%q, want atc-ros/steamer-ros",
			snap.HitterSystem, snap.PitcherSystem)
	}
	if snap.ProjectionSystem != "" {
		t.Errorf("split run: ProjectionSystem = %q, want omitted", snap.ProjectionSystem)
	}

	snap = buildSnapshot(dr, sysPair("depthcharts-ros"), nil)
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

	snap := buildSnapshot(dr, projInputs{HitterSystem: "atc-ros", PitcherSystem: "atc-ros", HittersNoData: true}, nil)
	if !snap.HittersNoData {
		t.Error("HittersNoData = false, want true")
	}
	if snap.PitchersNoData {
		t.Error("PitchersNoData = true, want false")
	}

	snap = buildSnapshot(dr, projInputs{HitterSystem: "atc-ros", PitcherSystem: "atc-ros", PitchersNoData: true}, nil)
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

	snap := buildSnapshot(dr, sysPair("depthcharts-ros"), nil)

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

	if snap := buildSnapshot(dr, sysPair("depthcharts-ros"), nil); snap.GSLimit != 0 {
		t.Errorf("GSLimit = %d, want 0 when no budget was in force", snap.GSLimit)
	}
}

// --- writeProjectionSnapshot merge behavior (rosterbot-w79p) ---

// TestWriteProjectionSnapshot_PreservesLockedRowAcrossRewrites is the Cade
// Cavalli regression: a player locked (team game underway) at the time of an
// earlier write must keep that write's projection, deployment and status
// through every later same-date rewrite — even one that recomputes him at
// zero after an in-game injury flips him to Reserve. Without the merge, the
// later run's rescore silently overwrites the projection the optimizer
// actually used when the lineup was locked in.
func TestWriteProjectionSnapshot_PreservesLockedRowAcrossRewrites(t *testing.T) {
	st := backtest.NewFileSnapshotStore(t.TempDir())
	date := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)

	first := dateResult{
		date: date,
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{{
				Player: fantrax.Player{
					ID: "cavalli", Name: "Cade Cavalli", PosShortNames: "SP",
					Status: "Active", Locked: true,
				},
				ExpectedPts: 17.02, HasGame: true, IsStarter: true,
			}},
		},
	}
	if err := writeProjectionSnapshot(&bytes.Buffer{}, first, sysPair("depthcharts-ros"), nil, st, "snapshots"); err != nil {
		t.Fatalf("first write: %v", err)
	}

	second := dateResult{
		date: date,
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{{
				Player: fantrax.Player{
					ID: "cavalli", Name: "Cade Cavalli", PosShortNames: "SP",
					Status: "Reserve", Locked: true,
				},
				ExpectedPts: 0, HasGame: false, IsStarter: false,
			}},
		},
	}
	if err := writeProjectionSnapshot(&bytes.Buffer{}, second, sysPair("depthcharts-ros"), nil, st, "snapshots"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	snap, ok := backtest.LoadSnapshot(st, "snapshots", date)
	if !ok {
		t.Fatalf("snapshot not found")
	}
	if len(snap.Pitchers) != 1 {
		t.Fatalf("want 1 pitcher, got %d", len(snap.Pitchers))
	}
	p := snap.Pitchers[0]
	if p.ProjPtsPerGame != 17.02 {
		t.Errorf("ProjPtsPerGame = %v, want 17.02 (the locked-at-write-time value)", p.ProjPtsPerGame)
	}
	if !p.HasGame {
		t.Error("HasGame = false, want true (survived from the locked write)")
	}
	if !p.IsStarter {
		t.Error("IsStarter = false, want true (survived from the locked write)")
	}
	if p.Status != "Active" {
		t.Errorf("Status = %q, want Active (survived from the locked write)", p.Status)
	}
}

// TestWriteProjectionSnapshot_UnlockedRowsTakeTheLatestWrite is the absence
// half of the guard: it must be scoped to locked rows only, not freeze the
// whole date. A hitter unlocked in the first write whose projection
// legitimately changes in the second write must read the SECOND write's
// value, and a pitcher locked ONLY in the second write (never archived
// before) must simply take that write's value.
func TestWriteProjectionSnapshot_UnlockedRowsTakeTheLatestWrite(t *testing.T) {
	st := backtest.NewFileSnapshotStore(t.TempDir())
	date := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)

	first := dateResult{
		date: date,
		hitterResult: optimizer.Result{
			Scored: []optimizer.ScoredPlayer{{
				Player:      fantrax.Player{ID: "q", Name: "Unlocked Hitter", Status: "Active", Locked: false},
				ExpectedPts: 5.0, HasGame: true,
			}},
		},
	}
	if err := writeProjectionSnapshot(&bytes.Buffer{}, first, sysPair("depthcharts-ros"), nil, st, "snapshots"); err != nil {
		t.Fatalf("first write: %v", err)
	}

	second := dateResult{
		date: date,
		hitterResult: optimizer.Result{
			Scored: []optimizer.ScoredPlayer{{
				// Q is STILL unlocked in the second write — an ordinary
				// intraday reprojection, not a post-lock overwrite.
				Player:      fantrax.Player{ID: "q", Name: "Unlocked Hitter", Status: "Active", Locked: false},
				ExpectedPts: 9.0, HasGame: true,
			}},
		},
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{{
				// R never appeared in the first write at all; locked only here.
				Player:      fantrax.Player{ID: "r", Name: "Newly Locked", Status: "Active", Locked: true},
				ExpectedPts: 11.5, HasGame: true, IsStarter: true,
			}},
		},
	}
	if err := writeProjectionSnapshot(&bytes.Buffer{}, second, sysPair("depthcharts-ros"), nil, st, "snapshots"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	snap, ok := backtest.LoadSnapshot(st, "snapshots", date)
	if !ok {
		t.Fatalf("snapshot not found")
	}
	if len(snap.Hitters) != 1 || snap.Hitters[0].ProjPtsPerGame != 9.0 {
		t.Errorf("unlocked hitter should read the second write's value: %+v", snap.Hitters)
	}
	if len(snap.Pitchers) != 1 || snap.Pitchers[0].ProjPtsPerGame != 11.5 || !snap.Pitchers[0].IsStarter {
		t.Errorf("pitcher locked only in the second write should take that write's values: %+v", snap.Pitchers)
	}
}

// TestWriteProjectionSnapshot_MissingPriorSnapshotWritesFresh pins the base
// case: no prior snapshot for the date means a plain fresh write, with no
// warning printed (there is nothing wrong to report).
func TestWriteProjectionSnapshot_MissingPriorSnapshotWritesFresh(t *testing.T) {
	st := backtest.NewFileSnapshotStore(t.TempDir())
	date := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	dr := dateResult{
		date: date,
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{{
				Player:      fantrax.Player{ID: "p1", Name: "Ace", Status: "Active"},
				ExpectedPts: 12.0, HasGame: true, IsStarter: true,
			}},
		},
	}

	var out bytes.Buffer
	if err := writeProjectionSnapshot(&out, dr, sysPair("depthcharts-ros"), nil, st, "snapshots"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no warning on a missing prior snapshot, got: %q", out.String())
	}

	snap, ok := backtest.LoadSnapshot(st, "snapshots", date)
	if !ok || len(snap.Pitchers) != 1 || snap.Pitchers[0].ProjPtsPerGame != 12.0 {
		t.Errorf("fresh write missing or wrong: ok=%v snap=%+v", ok, snap)
	}
}

// TestWriteProjectionSnapshot_CorruptPriorSnapshotWritesFreshAndWarns pins the
// degrade-to-noise-never-to-silence behavior: an unreadable prior snapshot
// must not block the fresh write, but must print a warning rather than fail
// silently.
func TestWriteProjectionSnapshot_CorruptPriorSnapshotWritesFreshAndWarns(t *testing.T) {
	st := backtest.NewFileSnapshotStore(t.TempDir())
	date := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)

	if err := st.Put(backtest.SnapshotKey("snapshots", date.Format("2006-01-02")), []byte("not valid json")); err != nil {
		t.Fatalf("seed corrupt snapshot: %v", err)
	}

	dr := dateResult{
		date: date,
		pitcherResult: optimizer.PitcherResult{
			Scored: []optimizer.ScoredPitcher{{
				Player:      fantrax.Player{ID: "p1", Name: "Ace", Status: "Active"},
				ExpectedPts: 12.0, HasGame: true, IsStarter: true,
			}},
		},
	}

	var out bytes.Buffer
	if err := writeProjectionSnapshot(&out, dr, sysPair("depthcharts-ros"), nil, st, "snapshots"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), date.Format("2006-01-02")) {
		t.Errorf("expected a printed warning naming the date, got: %q", out.String())
	}

	snap, ok := backtest.LoadSnapshot(st, "snapshots", date)
	if !ok || len(snap.Pitchers) != 1 || snap.Pitchers[0].ProjPtsPerGame != 12.0 {
		t.Errorf("fresh write missing or wrong after corrupt prior snapshot: ok=%v snap=%+v", ok, snap)
	}
}
