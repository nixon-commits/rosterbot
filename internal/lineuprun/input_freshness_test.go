package lineuprun

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// The snapshot is the only durable record of what a capture was built from, so
// the grade-time guard (backtest.staleInput) is worth exactly what this wiring
// is worth: if the load's FetchedAt never reaches the file, every snapshot
// reads "unknown" and the guard is permanently inert — the shape rosterbot-1jvj
// found in the GS forecast. Driving Run rather than buildSnapshot is the point.
func TestRun_SnapshotRecordsEachRolesProjectionFetchTime(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	batFetched := time.Date(2026, 8, 23, 23, 41, 0, 0, time.UTC) // three days stale
	pitFetched := time.Date(2026, 8, 26, 13, 5, 0, 0, time.UTC)  // fresh

	ft := &fakeLineupClient{
		hitters: []fantrax.Player{
			{ID: "h1", Name: "Hot Hitter", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Active", RosterPosition: "014"},
		},
		pitchers: []fantrax.Player{
			{ID: "p1", Name: "Steady Reliever", MLBTeam: "BOS", Positions: []string{"016"}, PosShortNames: "RP", Status: "Active", RosterPosition: "017"},
		},
	}
	bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
		{Name: "Hot Hitter", Team: "NYY", Proj: projections.Projection{G: 150, PA: 600, H: 170, HR: 40}},
	})
	pit := projections.NewFanGraphsPitcherSourceFromEntries([]projections.PitcherSourceEntry{
		{Name: "Steady Reliever", Team: "BOS", Proj: projections.PitcherProjection{G: 60, IP: 65, K: 70}},
	})
	sched := &fakeDateSchedule{
		fakeSchedule: fakeSchedule{
			playing: map[string]map[string]bool{
				today.Format("2006-01-02"): {"NYY": true, "BOS": true},
			},
		},
	}

	st := backtest.NewFileSnapshotStore(t.TempDir())
	var out bytes.Buffer
	opts := withFakeDeps(Options{
		Today:          today,
		HitterSystem:   "atc",
		PitcherSystem:  "atc",
		WriteSnapshots: true,
		SnapshotStore:  st,
		SnapshotRoot:   "snapshots",
		Out:            &out,
	}, bat, pit, sched)
	opts.LoadBattingProjections = func(system, _ string, _ time.Duration) (*projections.FanGraphsSource, projections.LoadResult, error) {
		return bat, projections.LoadResult{System: system, FetchedAt: batFetched}, nil
	}
	opts.LoadPitcherProjections = func(system, _ string, _ time.Duration) (*projections.FanGraphsPitcherSource, projections.LoadResult, error) {
		return pit, projections.LoadResult{System: system, FetchedAt: pitFetched}, nil
	}

	cfg := &config.Config{LeagueID: "lg1", TeamID: "team1", DryRun: true, Dates: []time.Time{today}}
	if _, err := Run(context.Background(), ft, cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	snap, ok := backtest.LoadSnapshot(st, "snapshots", today)
	if !ok {
		t.Fatalf("no snapshot archived; run output:\n%s", out.String())
	}
	if !snap.HitterProjFetchedAt.Equal(batFetched) {
		t.Errorf("HitterProjFetchedAt = %v, want %v", snap.HitterProjFetchedAt, batFetched)
	}
	if !snap.PitcherProjFetchedAt.Equal(pitFetched) {
		t.Errorf("PitcherProjFetchedAt = %v, want %v", snap.PitcherProjFetchedAt, pitFetched)
	}
	// The roles must not be crossed: the whole point is that one role can be
	// stale while the other is fresh, and a transposed pair would report the
	// healthy role's freshness for the broken one.
	if snap.HitterProjFetchedAt.Equal(snap.PitcherProjFetchedAt) {
		t.Fatalf("both roles report %v — the per-role stamps are not being threaded separately", snap.HitterProjFetchedAt)
	}
}
