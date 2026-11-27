package lineuprun

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// The gap these tests close. cache.GetWithStaleFallbackAt serves loadAnyAt,
// which by its own comment ignores TTL expiry, and it has NO age at which it
// refuses -- so a FanGraphs outage that outlasts the cache serves the same
// frozen copy for as long as the outage runs. FetchedAt was already threaded
// from the load into the snapshot, but only so grading could DISOWN the
// capture afterwards: no condition anywhere declined to set the lineup. The
// stale-cache alert is a push, not a refusal, and StaleAlertAfter (24h) only
// decides whether to send it.
//
// Reproduced on 2026-11-27: `optimize --dry-run --no-cache` printed four
// "stale cache: fangraphs-... (4d old)" lines and went on to score with them.
// Production never saw it because the season gate stops optimize at the root
// from 2026-09-28, which is also why it would next appear on opening day,
// 2027-03-25, setting REAL lineups off an arbitrarily old copy.
//
// The clock is opts.Today, not time.Now(): every date decision in this package
// is anchored to it, and a wall-clock ceiling would refuse every test and
// backfill run in the repo on the age of its fixture dates.

// runWithProjectionAges drives the whole of Run against an in-memory stack
// with each role's projection copy stamped at a chosen time.
func runWithProjectionAges(t *testing.T, today, batFetched, pitFetched time.Time) (string, error) {
	t.Helper()
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

	var out bytes.Buffer
	opts := withFakeDeps(Options{
		Today:         today,
		HitterSystem:  "atc",
		PitcherSystem: "atc",
		Out:           &out,
	}, bat, pit, sched)
	opts.LoadBattingProjections = func(_ context.Context, system, _ string, _ time.Duration) (*projections.FanGraphsSource, projections.LoadResult, error) {
		return bat, projections.LoadResult{System: system, FetchedAt: batFetched}, nil
	}
	opts.LoadPitcherProjections = func(_ context.Context, system, _ string, _ time.Duration) (*projections.FanGraphsPitcherSource, projections.LoadResult, error) {
		return pit, projections.LoadResult{System: system, FetchedAt: pitFetched}, nil
	}

	cfg := &config.Config{LeagueID: "lg1", TeamID: "team1", DryRun: true, Dates: []time.Time{today}}
	_, err := Run(context.Background(), ft, cfg, opts)
	return out.String(), err
}

func TestRun_RefusesALineupOnProjectionsPastTheAgeCeiling(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	_, err := runWithProjectionAges(t, today, today.Add(-73*time.Hour), today.Add(-2*time.Hour))
	if err == nil {
		t.Fatal("a copy older than the ceiling must REFUSE the run, not warn: a warning on stderr is " +
			"what the stale-cache path already did while setting the lineup anyway")
	}
	got := err.Error()
	if !strings.Contains(got, "batting") {
		t.Errorf("the error must name the offending role so the operator knows which feed is blocked: %v", err)
	}
	if strings.Contains(got, "pitching") {
		t.Errorf("the fresh role must not be named: %v", err)
	}
}

// The boundary proceeds. A ceiling that refused AT its own value would make
// "72h" mean something other than what it says, and the difference matters on
// the one run that sits exactly three refresh cycles behind.
func TestRun_ProceedsAtTheAgeCeilingExactly(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	out, err := runWithProjectionAges(t, today, today.Add(-72*time.Hour), today.Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("a copy exactly at the ceiling is inside it and must still run: %v\noutput:\n%s", err, out)
	}
}

func TestRun_RefusesOnAStalePitchingCopyToo(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	_, err := runWithProjectionAges(t, today, today.Add(-2*time.Hour), today.Add(-100*time.Hour))
	if err == nil {
		t.Fatal("the pitching role is judged on its own copy; SP projections drive the GS budget")
	}
	if !strings.Contains(err.Error(), "pitching") {
		t.Errorf("the error must name the pitching role: %v", err)
	}
}

// Zero means UNKNOWN, never ancient -- the rule LoadResult.FetchedAt's own doc
// states. The CSV fallback and the NoData stub have no upstream fetch behind
// them, and refusing a run over an absent stamp would take the whole lineup
// out on a non-event.
func TestRun_AnUndatedProjectionCopyIsNotJudgedOld(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	out, err := runWithProjectionAges(t, today, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("an undated copy cannot be called old: %v\noutput:\n%s", err, out)
	}
}

// The value is the policy, so it is pinned rather than derived: a test that
// read MaxProjectionAge to compute its own expectation would pass whatever the
// constant said.
func TestMaxProjectionAgeIsThreeRefreshCycles(t *testing.T) {
	if MaxProjectionAge != 72*time.Hour {
		t.Errorf("MaxProjectionAge = %v, want 72h", MaxProjectionAge)
	}
	if MaxProjectionAge <= projections.ProjectionCacheTTL {
		t.Error("the ceiling must be looser than one refresh cycle, or an ordinary daily blip " +
			"would stop the lineup the stale fallback exists to carry")
	}
}
