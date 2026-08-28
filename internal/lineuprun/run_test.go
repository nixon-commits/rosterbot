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

	// GS-budget inputs. Every zero value reproduces the pre-existing
	// behaviour exactly — no week bounds, no periods, no limits — so the
	// idempotency test above is untouched by their presence.
	weekStart, weekEnd time.Time
	periods            []fantrax.ScoringPeriod
	gsMin, gsMax       *int
	usedGS             int
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
	return f.weekStart, f.weekEnd, nil
}
func (f *fakeLineupClient) GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error) {
	return f.periods, nil, nil, nil
}
func (f *fakeLineupClient) DailyPeriodFor(_, _ time.Time) fantrax.DailyPeriod { return f.period }
func (f *fakeLineupClient) GetHitterRosterForPeriod(_ fantrax.DailyPeriod) ([]fantrax.Player, error) {
	return f.copyOf(f.hitters), nil
}
func (f *fakeLineupClient) GetPitcherRosterForPeriod(_ fantrax.DailyPeriod) ([]fantrax.Player, error) {
	return f.copyOf(f.pitchers), nil
}
func (f *fakeLineupClient) GetGSLimits(_ string, _ fantrax.WeeklyPeriod) (*int, *int, error) {
	return f.gsMin, f.gsMax, nil
}
func (f *fakeLineupClient) GetTeamGS(_, _ string, _ fantrax.ScoringPeriod, _, _ time.Time, _ int, _ bool) (int, []fantrax.PitcherStart, error) {
	return f.usedGS, nil, nil
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

// TestRun_RaisesTheGSFloorAlert drives the floor alert through Run itself,
// not through reportGSFloor directly.
//
// That distinction is the whole point of this test. rosterbot-ch0s records an
// attempt at a different bug whose headline test called the new helper rather
// than the call site, so reverting the fix left the entire test file green —
// the change was never wired to anything and nothing said so. The unit tests in
// gs_floor_test.go have exactly that shape on their own, so this one asserts
// the composition: GS tracking on, a floor configured, a week projecting short,
// and the alert reaching Run's output with the marker store consulted under a
// key carrying the period ComputeGSBudget resolved.
func TestRun_RaisesTheGSFloorAlert(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	weekEnd := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	gsMin, gsMax := 10, 12

	ft := &fakeLineupClient{
		hitters: []fantrax.Player{
			{ID: "h1", Name: "Only Bat", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Active", RosterPosition: "014"},
		},
		pitchers: []fantrax.Player{
			{ID: "p1", Name: "Lone Starter", MLBTeam: "BOS", Positions: []string{"015"}, PosShortNames: "SP", Status: "Active", RosterPosition: "017"},
		},
		seasonStart: time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		seasonEnd:   time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		period:      155,

		weekStart: weekStart,
		weekEnd:   weekEnd,
		periods: []fantrax.ScoringPeriod{
			{Number: 21, StartDate: weekStart, EndDate: weekEnd},
		},
		gsMin:  &gsMin,
		gsMax:  &gsMax,
		usedGS: 4, // four banked against a minimum of ten, mid-week
	}

	bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
		{Name: "Only Bat", Team: "NYY", Proj: projections.Projection{G: 100, HR: 20}},
	})
	pit := projections.NewFanGraphsPitcherSourceFromEntries([]projections.PitcherSourceEntry{
		{Name: "Lone Starter", Team: "BOS", Proj: projections.PitcherProjection{G: 30, IP: 180, K: 200}},
	})

	// Thu 27 and Fri 28 nobody of ours plays at all — the empty days the alert
	// has to name. Sat 29 and Sun 30 our one SP's club plays and has named
	// nobody, which is 0.2 estimated starts each: nowhere near the six the
	// floor still needs.
	sched := &fakeDateSchedule{
		fakeSchedule: fakeSchedule{
			playing: map[string]map[string]bool{
				today.Format("2006-01-02"): {"NYY": true, "BOS": true},
				"2026-08-29":               {"BOS": true},
				"2026-08-30":               {"BOS": true},
			},
		},
	}

	markers := newFakeMarkers()
	cfg := &config.Config{
		LeagueID: "lg1", TeamID: "team1",
		DryRun: false, AutoApply: true,
		GSTrackingEnabled: true,
		Dates:             []time.Time{today},
	}
	var out bytes.Buffer
	opts := withFakeDeps(Options{
		Today:            today,
		ProjectionSystem: "depthcharts",
		GSFloorMarkers:   markers,
		Out:              &out,
	}, bat, pit, sched)

	if _, err := Run(context.Background(), ft, cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()

	// Matched on the COVERAGE line's own shape, not on the "gs floor check:"
	// prefix: the send-failure line carries that prefix too (the dispatcher is
	// unconfigured in tests), so the looser assertion passed even with the
	// coverage line deleted — it was not testing what its message claimed.
	if !strings.Contains(got, "gs floor check: 4/12 used, floor 10") {
		t.Fatalf("Run never printed the floor coverage line; the phase is not wired in.\n%s", got)
	}
	if !strings.Contains(got, "=== GS Floor Risk ===") {
		t.Fatalf("a week four starts into a ten-start minimum did not raise the alert.\n%s", got)
	}
	for _, day := range []string{"Thu Aug 27", "Fri Aug 28"} {
		if !strings.Contains(got, day) {
			t.Errorf("alert does not name the empty day %s — the actionable half.\n%s", day, got)
		}
	}

	// The marker key proves Period and Season survived the trip from
	// ComputeGSBudget's period lookup out to the dedup seam. A zero here would
	// still dedup consistently and would still look fine in the output, while
	// silently sharing one marker across every week of every season.
	if _, ok := markers.seen["2026-p21"]; ok {
		t.Error("marker written despite an unconfigured dispatcher: a failed send must never be marked")
	}
	if !strings.Contains(got, "2026-p21") {
		t.Errorf("the alert did not key on season+period; a zero Period/Season would share one marker "+
			"across the whole season.\n%s", got)
	}

	// And the store Options carried must be the one consulted. Without this
	// the Options field could go unthreaded, dedup would silently vanish, and
	// the only symptom would be the alert firing every hour forever — the
	// flood this repo already fixed once for the stale-cache alert.
	if len(markers.getKeys) != 1 || markers.getKeys[0] != "2026-p21" {
		t.Errorf("marker store consulted with %v, want exactly [2026-p21] — "+
			"Options.GSFloorMarkers is not reaching the dedup check", markers.getKeys)
	}
}
