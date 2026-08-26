package lineuprun

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// fakeLineupClient is the stateful LineupClient double behind TestRun. Its
// ApplyLineup MUTATES the rosters the getters serve, which is what lets the
// double-run test assert the Idempotency invariant end to end: the second Run
// sees the applied lineup, exactly as the real client does after
// InvalidatePeriodRosterCache.
//
// GetCurrentPeriod deliberately reports period 1: BlendSources treats the
// season as not yet underway and passes the base sources through, keeping the
// recency window (and its Fantrax reads) out of the composition under test.
type fakeLineupClient struct {
	mu       sync.Mutex
	hitters  []fantrax.Player
	pitchers []fantrax.Player

	seasonStart, seasonEnd time.Time
	period                 fantrax.DailyPeriod // DailyPeriodFor's answer for every date

	applies     []appliedLineup
	invalidated []fantrax.DailyPeriod
}

func (f *fakeLineupClient) copyOf(ps []fantrax.Player) []fantrax.Player {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fantrax.Player(nil), ps...)
}

func (f *fakeLineupClient) GetHitterRoster() ([]fantrax.Player, error) {
	return f.copyOf(f.hitters), nil
}
func (f *fakeLineupClient) GetPitcherRoster() ([]fantrax.Player, error) {
	return f.copyOf(f.pitchers), nil
}
func (f *fakeLineupClient) GetFullRoster() ([]fantrax.Player, fantrax.SlotCounts, error) {
	return append(f.copyOf(f.hitters), f.copyOf(f.pitchers)...), fantrax.SlotCounts{}, nil
}
func (f *fakeLineupClient) GetActiveSlots() ([]fantrax.Slot, error) {
	return []fantrax.Slot{{PosID: "014", PosName: "UT"}}, nil
}
func (f *fakeLineupClient) GetPitcherSlots() ([]fantrax.Slot, error) {
	return []fantrax.Slot{{PosID: "017", PosName: "P"}}, nil
}
func (f *fakeLineupClient) GetScoringWeights() (fantrax.ScoringWeights, error) {
	return fantrax.ScoringWeights{"HR": 4}, nil
}
func (f *fakeLineupClient) GetPitcherScoringWeights() (fantrax.ScoringWeights, error) {
	return fantrax.ScoringWeights{"SO": 1}, nil
}
func (f *fakeLineupClient) GetCurrentPeriod() (fantrax.DailyPeriod, error) { return 1, nil }
func (f *fakeLineupClient) GetMatchupWeekBounds(_, _ time.Time) (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, nil
}
func (f *fakeLineupClient) GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error) {
	return nil, nil, nil, nil
}
func (f *fakeLineupClient) DailyPeriodFor(_, _ time.Time) fantrax.DailyPeriod { return f.period }
func (f *fakeLineupClient) GetHitterRosterForPeriod(_ fantrax.DailyPeriod) ([]fantrax.Player, error) {
	return f.copyOf(f.hitters), nil
}
func (f *fakeLineupClient) GetPitcherRosterForPeriod(_ fantrax.DailyPeriod) ([]fantrax.Player, error) {
	return f.copyOf(f.pitchers), nil
}
func (f *fakeLineupClient) GetGSLimits(_ string, _ fantrax.WeeklyPeriod) (*int, *int, error) {
	return nil, nil, nil
}
func (f *fakeLineupClient) GetTeamGS(_, _ string, _ fantrax.ScoringPeriod, _, _ time.Time, _ int, _ bool) (int, []fantrax.PitcherStart, error) {
	return 0, nil, nil
}
func (f *fakeLineupClient) GetRecentPitcherStats(_ fantrax.DailyPeriod) (map[string]fantrax.RecentStat, error) {
	return map[string]fantrax.RecentStat{}, nil
}
func (f *fakeLineupClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	return f.seasonStart, f.seasonEnd, nil
}
func (f *fakeLineupClient) DailyFantasyPoints(_ string, _, _, _ time.Time, _ string, _ time.Duration) ([]fantrax.DayRoster, error) {
	return nil, nil
}
func (f *fakeLineupClient) MLBDailyFPts(_ []fantrax.MLBPlayerRef, _, _ time.Time) ([]fantrax.DayRoster, error) {
	return nil, nil
}

func (f *fakeLineupClient) ApplyLineup(period fantrax.DailyPeriod, active []fantrax.PlayerSlot, reserve []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = append(f.applies, appliedLineup{period: period, activate: active, bench: reserve})

	apply := func(roster []fantrax.Player) {
		for i := range roster {
			for _, ps := range active {
				if roster[i].ID == ps.PlayerID {
					roster[i].Status = "Active"
					roster[i].RosterPosition = ps.PosID
				}
			}
			for _, id := range reserve {
				if roster[i].ID == id {
					roster[i].Status = "Reserve"
					roster[i].RosterPosition = ""
				}
			}
		}
	}
	apply(f.hitters)
	apply(f.pitchers)
	return nil
}

func (f *fakeLineupClient) InvalidatePeriodRosterCache(period fantrax.DailyPeriod) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, period)
	return nil
}

// withFakeDeps populates ALL FIVE dependency fields. Never hand-assemble a
// partial set in a test: a forgotten field silently resolves to the real,
// network-hitting dependency (see the Options doc).
func withFakeDeps(o Options, bat *projections.FanGraphsSource, pit *projections.FanGraphsPitcherSource, sched ScheduleClient) Options {
	o.Schedule = sched
	o.LoadBattingProjections = func(system string, _ string, _ time.Duration) (*projections.FanGraphsSource, projections.LoadResult, error) {
		return bat, projections.LoadResult{System: system}, nil
	}
	o.LoadPitcherProjections = func(system string, _ string, _ time.Duration) (*projections.FanGraphsPitcherSource, projections.LoadResult, error) {
		return pit, projections.LoadResult{System: system}, nil
	}
	o.FetchHandedness = func(map[string]int, string, time.Duration) (map[string]string, map[string]string, error) {
		return map[string]string{}, map[string]string{}, nil
	}
	o.LoadHKBMeta = func(string) (map[string]lineupapi.Dynasty, error) {
		return map[string]lineupapi.Dynasty{}, nil
	}
	return o
}

// TestRun_SecondRunAppliesNothing drives the WHOLE composition — every phase,
// through Run itself — against an in-memory stack, twice. The first pass must
// find and apply the obvious upgrade; the second pass, seeing the applied
// lineup, must conclude "No changes needed" and apply nothing. This automates
// the Idempotency verification CLAUDE.md previously prescribed as a manual
// build-two-binaries-and-diff ritual.
func TestRun_SecondRunAppliesNothing(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)

	ft := &fakeLineupClient{
		hitters: []fantrax.Player{
			{ID: "h1", Name: "Hot Hitter", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Reserve"},
			{ID: "h2", Name: "Cold Bat", MLBTeam: "BOS", Positions: []string{"012"}, Status: "Active", RosterPosition: "014"},
		},
		pitchers: []fantrax.Player{
			{ID: "p1", Name: "Steady Reliever", MLBTeam: "BOS", Positions: []string{"016"}, PosShortNames: "RP", Status: "Active", RosterPosition: "017"},
		},
		seasonStart: time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		seasonEnd:   time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		period:      155,
	}

	bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
		{Name: "Hot Hitter", Team: "NYY", Proj: projections.Projection{G: 100, HR: 30}}, // 1.2 pts/G at HR:4
		{Name: "Cold Bat", Team: "BOS", Proj: projections.Projection{G: 100, HR: 5}},    // 0.2 pts/G
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

	cfg := &config.Config{
		LeagueID:  "lg1",
		TeamID:    "team1",
		DryRun:    false,
		AutoApply: true,
		Dates:     []time.Time{today},
	}
	newOpts := func(out *bytes.Buffer) Options {
		return withFakeDeps(Options{
			Today:            today,
			ProjectionSystem: "depthcharts",
			Out:              out,
		}, bat, pit, sched)
	}

	// --- First pass: the upgrade is found and applied. ---
	var out1 bytes.Buffer
	res, err := Run(context.Background(), ft, cfg, newOpts(&out1))
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if res.HittersNoData || res.PitchersNoData {
		t.Fatalf("Result = %+v, want no NoData flags — the injected loaders supplied data", res)
	}
	if !strings.Contains(out1.String(), "Changes (") {
		t.Fatalf("first run printed no planned-moves block; output:\n%s", out1.String())
	}
	if !strings.Contains(out1.String(), "Lineup applied successfully.") {
		t.Fatalf("first run did not apply; output:\n%s", out1.String())
	}
	if len(ft.applies) != 1 {
		t.Fatalf("ApplyLineup calls = %d, want 1", len(ft.applies))
	}
	got := ft.applies[0]
	if got.period != 155 {
		t.Fatalf("applied period = %d, want 155 (DailyPeriodFor's answer)", got.period)
	}
	activatedH1 := false
	for _, ps := range got.activate {
		if ps.PlayerID == "h1" && ps.PosID == "014" {
			activatedH1 = true
		}
	}
	if !activatedH1 {
		t.Fatalf("apply payload %+v does not activate h1 into UT", got.activate)
	}
	benchedH2 := false
	for _, id := range got.bench {
		if id == "h2" {
			benchedH2 = true
		}
	}
	if !benchedH2 {
		t.Fatalf("apply payload bench=%v does not bench h2", got.bench)
	}
	if len(ft.invalidated) != 1 || ft.invalidated[0] != 155 {
		t.Fatalf("InvalidatePeriodRosterCache calls = %v, want [155]", ft.invalidated)
	}

	// --- Second pass: same fakes, post-apply state. Idempotency. ---
	var out2 bytes.Buffer
	if _, err := Run(context.Background(), ft, cfg, newOpts(&out2)); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if !strings.Contains(out2.String(), "No changes needed.") {
		t.Fatalf("second run proposed changes; the composition is not idempotent. Output:\n%s", out2.String())
	}
	if len(ft.applies) != 1 {
		t.Fatalf("second run applied a lineup: ApplyLineup calls = %d, want still 1", len(ft.applies))
	}
}
