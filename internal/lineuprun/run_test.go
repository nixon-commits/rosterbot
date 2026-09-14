package lineuprun

import (
	"bytes"
	"context"
	"errors"
	"github.com/pmurley/go-fantrax/auth_client"
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
	bracket    *auth_client.PlayoffBracket
	bracketErr error
	mu         sync.Mutex
	hitters    []fantrax.Player
	pitchers   []fantrax.Player

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

	// pitcherDayCalls counts GetTeamPitcherDaysWithStatus invocations — the
	// start-rate history walk. It exists to prove StartRateCache actually
	// dedupes that walk across a Run (see
	// TestRun_PrefilledStartRateCacheSkipsTheHistoryWalk): nothing else here
	// distinguishes "measured once" from "measured every call".
	pitcherDayCalls int
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
func (f *fakeLineupClient) GetTeamPitcherDaysWithStatus(_ string, _, _, _ time.Time, _ string, _ time.Duration) ([]fantrax.PitcherDay, error) {
	f.mu.Lock()
	f.pitcherDayCalls++
	f.mu.Unlock()
	return nil, nil
}
func (f *fakeLineupClient) GetRecentPitcherStats(_ fantrax.DailyPeriod) (map[string]fantrax.RecentStat, error) {
	return map[string]fantrax.RecentStat{}, nil
}
func (f *fakeLineupClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	return f.seasonStart, f.seasonEnd, nil
}
func (f *fakeLineupClient) GetPlayoffBracket() (*auth_client.PlayoffBracket, error) {
	if f.bracketErr != nil {
		return nil, f.bracketErr
	}
	return f.bracket, nil
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
	o.LoadBattingProjections = func(_ context.Context, system string, _ string, _ time.Duration) (*projections.FanGraphsSource, projections.LoadResult, error) {
		return bat, projections.LoadResult{System: system}, nil
	}
	o.LoadPitcherProjections = func(_ context.Context, system string, _ string, _ time.Duration) (*projections.FanGraphsPitcherSource, projections.LoadResult, error) {
		return pit, projections.LoadResult{System: system}, nil
	}
	o.FetchHandedness = func(map[string]int, string, time.Duration) (map[string]string, map[string]string, error) {
		return map[string]string{}, map[string]string{}, nil
	}
	o.LoadHKBMeta = func(context.Context, string) (map[string]lineupapi.Dynasty, error) {
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
			Today:         today,
			HitterSystem:  "depthcharts",
			PitcherSystem: "depthcharts",
			Out:           out,
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

// TestRun_UsesPerRoleProjectionSystems pins the rosterbot-5qvs split: the
// hitter and pitcher loaders each receive their own system, and the header
// names both when they differ — one system silently serving both roles is the
// regression this exists to catch.
func TestRun_UsesPerRoleProjectionSystems(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)

	ft := &fakeLineupClient{
		hitters: []fantrax.Player{
			{ID: "h1", Name: "Hot Hitter", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Active", RosterPosition: "014"},
		},
		pitchers: []fantrax.Player{
			{ID: "p1", Name: "Steady Reliever", MLBTeam: "BOS", Positions: []string{"016"}, PosShortNames: "RP", Status: "Active", RosterPosition: "017"},
		},
		seasonStart: time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		seasonEnd:   time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		period:      155,
	}
	bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
		{Name: "Hot Hitter", Team: "NYY", Proj: projections.Projection{G: 100, HR: 30}},
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
		PitcherSystem: "steamer",
		Out:           &out,
	}, bat, pit, sched)
	var gotBat, gotPit string
	origBat, origPit := opts.LoadBattingProjections, opts.LoadPitcherProjections
	opts.LoadBattingProjections = func(ctx context.Context, system, cacheDir string, ttl time.Duration) (*projections.FanGraphsSource, projections.LoadResult, error) {
		gotBat = system
		return origBat(ctx, system, cacheDir, ttl)
	}
	opts.LoadPitcherProjections = func(ctx context.Context, system, cacheDir string, ttl time.Duration) (*projections.FanGraphsPitcherSource, projections.LoadResult, error) {
		gotPit = system
		return origPit(ctx, system, cacheDir, ttl)
	}

	cfg := &config.Config{LeagueID: "lg1", TeamID: "team1", DryRun: true,
		Dates: []time.Time{today}}
	if _, err := Run(context.Background(), ft, cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotBat != "atc" || gotPit != "steamer" {
		t.Errorf("loaders received %q/%q, want atc/steamer", gotBat, gotPit)
	}
	// The run header names both systems, but it rides the progress channel
	// (stderr), not Out — the naming itself is pinned by TestDisplayNameFor.
	_ = out
}

// TestDisplayNameFor: a unified run keeps its pre-split label byte-identical
// (the golden boards and every log line depend on that); a split run must say
// which model produced which half, hitters first.
func TestDisplayNameFor(t *testing.T) {
	if got := displayNameFor("depthcharts", "depthcharts"); got != "DepthCharts" {
		t.Errorf("unified = %q, want DepthCharts", got)
	}
	if got := displayNameFor("atc", "steamer"); got != "ATC / Steamer" {
		t.Errorf("split = %q, want ATC / Steamer", got)
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
	// Thursday: three days left, which is gsFloorMaxDaysLeft — the first day
	// the 150-week replay found the projection informative.
	today := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
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

	// Fri 28 and Sat 29 nobody of ours plays at all — the empty days the alert
	// has to name. Sun 30 our one SP's club plays and has named nobody, which
	// is 0.2 estimated starts: nowhere near the six the floor still needs.
	sched := &fakeDateSchedule{
		fakeSchedule: fakeSchedule{
			playing: map[string]map[string]bool{
				today.Format("2006-01-02"): {"NYY": true, "BOS": true},
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
		Today:          today,
		HitterSystem:   "depthcharts",
		PitcherSystem:  "depthcharts",
		GSFloorMarkers: markers,
		Out:            &out,
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
	// The start-rate coverage line must reach Out on a NON-verbose run — this
	// Options has Verbose unset, exactly like the production task. It shipped
	// routed through GSDecision.Logs, which Run hands to prog.Logf, a no-op
	// unless --verbose: the first production run after the weighting deployed
	// (2026-09-05 17:00 UTC) printed the two lines above and not this one, and
	// its soft-fail WARNING would have been equally silent.
	if !strings.Contains(got, "GS start rates: ") {
		t.Fatalf("Run never printed the start-rate coverage line to Out; it is riding the verbose-only channel.\n%s", got)
	}

	// The disabled-path notice must be ABSENT here. Without this, hoisting that
	// fmt.Fprintln out of its else — a plausible refactor or merge resolution —
	// passes the whole package, and a production run with GS_TRACKING_ENABLED=true
	// that genuinely suppressed starts announces "no cap, no floor, no floor
	// alert this run" into CloudWatch and the dashboard's per-run output. An
	// operator debugging a benched ace would read that and conclude the gate is
	// off: the exact misdiagnosis rosterbot-8gmu exists to end, inverted, and
	// stated with more confidence than the silence had.
	if strings.Contains(got, "GS tracking disabled") {
		t.Fatalf("the disabled-path notice printed on a run with tracking ENABLED; "+
			"it is no longer pinned to the else branch.\n%s", got)
	}
	if !strings.Contains(got, "=== GS Floor Risk ===") {
		t.Fatalf("a week four starts into a ten-start minimum did not raise the alert.\n%s", got)
	}
	for _, day := range []string{"Fri Aug 28", "Sat Aug 29"} {
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
	// The -d3 suffix is the remaining-day component: this fixture has three days
	// left, and the key carries it so a later, worse day in the same week can
	// still page (rosterbot-bpbk).
	if len(markers.getKeys) != 1 || markers.getKeys[0] != "2026-p21-d3" {
		t.Errorf("marker store consulted with %v, want exactly [2026-p21-d3] — "+
			"Options.GSFloorMarkers is not reaching the dedup check", markers.getKeys)
	}
}

// TestRun_PrefilledStartRateCacheSkipsTheHistoryWalk pins the wiring that lets
// nothing here catch a regression: Options.StartRates only ever gets threaded
// into GSInputs.StartRates (lineuprun.go) and consulted inside ComputeGSBudget
// (gsbudget.go) — delete either line and every other test in this package
// stays green, because StartRateCache.get(measure) falls back to calling
// measure() on a nil receiver, which is exactly what "not wired" looks like
// from the outside.
//
// A cache is "pre-filled" here by calling its own get once, ahead of Run, with
// a stub measure that never touches the fake client — mirroring what
// `shadow`'s first per-system pass would have already done by the time a
// later pass reuses the same *StartRateCache. If Run is actually consulting
// that cache, its own walk must never run: pitcherDayCalls stays 0.
func TestRun_PrefilledStartRateCacheSkipsTheHistoryWalk(t *testing.T) {
	today := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
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
		usedGS: 4,
	}

	bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
		{Name: "Only Bat", Team: "NYY", Proj: projections.Projection{G: 100, HR: 20}},
	})
	pit := projections.NewFanGraphsPitcherSourceFromEntries([]projections.PitcherSourceEntry{
		{Name: "Lone Starter", Team: "BOS", Proj: projections.PitcherProjection{G: 30, IP: 180, K: 200}},
	})
	sched := &fakeDateSchedule{}

	cache := NewStartRateCache()
	// Pre-fill: measure() here is the stand-in for an earlier pass's real
	// walk. It must never be called again by the Run below.
	if _, err := cache.get(func() (startRateResult, error) {
		return startRateResult{Rate: map[string]float64{"lone starter": 0.15}}, nil
	}); err != nil {
		t.Fatalf("priming the cache: %v", err)
	}

	var out bytes.Buffer
	cfg := &config.Config{
		LeagueID: "lg1", TeamID: "team1",
		DryRun: false, AutoApply: true,
		GSTrackingEnabled: true,
		Dates:             []time.Time{today},
	}
	opts := withFakeDeps(Options{
		Today:         today,
		HitterSystem:  "depthcharts",
		PitcherSystem: "depthcharts",
		StartRates:    cache,
		Out:           &out,
	}, bat, pit, sched)

	if _, err := Run(context.Background(), ft, cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if ft.pitcherDayCalls != 0 {
		t.Errorf("pitcherDayCalls = %d, want 0 — a pre-filled StartRateCache must not "+
			"trigger a fresh history walk; either the Options.StartRates -> GSInputs.StartRates "+
			"wiring in lineuprun.go or the StartRates.get consult in gsbudget.go is not reaching "+
			"this call.\n%s", ft.pitcherDayCalls, out.String())
	}
}

// TestRun_ExplicitDateOutsideSeasonAppliesNothing pins both season-boundary
// guards on the explicit-dates path — the one the 14/day hourly job takes,
// where ResolveDates does no lookup and *OutOfSeasonError never arises
// (rosterbot-pjqw). The roster carries an obvious upgrade and the schedule
// says both clubs play, so a missing guard is not a quiet no-op: the
// optimizer finds the move and the fake records a live apply. Two positive
// controls: the final date itself, because the season's last day still has
// games and the guard must be strictly after the end; and an explicit
// in-season --dates day requested once the season is over, because the
// guards key on the day being optimized, not the wall clock — otherwise
// every single-date dry-run (the documented offseason workflow) would print
// "Season ended" until the next opener.
func TestRun_ExplicitDateOutsideSeasonAppliesNothing(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	seasonEnd := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		today     time.Time
		date      time.Time // the --dates day; zero means the bare [today] the hourly job passes
		wantApply bool
		wantLine  string
	}{
		{"day after the final date", seasonEnd.AddDate(0, 0, 1), time.Time{}, false, "Season ended 2026-09-27. No games to optimize."},
		{"day before the opener", seasonStart.AddDate(0, 0, -1), time.Time{}, false, "Season starts 2026-03-25. No games to optimize for today."},
		{"the final date itself", seasonEnd, time.Time{}, true, "Lineup applied successfully."},
		{"an explicit in-season day requested after the season ended", seasonEnd.AddDate(0, 0, 7), seasonEnd.AddDate(0, 0, -7), true, "Lineup applied successfully."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			date := tc.date
			if date.IsZero() {
				date = tc.today
			}
			ft := &fakeLineupClient{
				hitters: []fantrax.Player{
					{ID: "h1", Name: "Hot Hitter", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Reserve"},
					{ID: "h2", Name: "Cold Bat", MLBTeam: "BOS", Positions: []string{"012"}, Status: "Active", RosterPosition: "014"},
				},
				pitchers: []fantrax.Player{
					{ID: "p1", Name: "Steady Reliever", MLBTeam: "BOS", Positions: []string{"016"}, PosShortNames: "RP", Status: "Active", RosterPosition: "017"},
				},
				seasonStart: seasonStart,
				seasonEnd:   seasonEnd,
				period:      167,
			}
			bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
				{Name: "Hot Hitter", Team: "NYY", Proj: projections.Projection{G: 100, HR: 30}},
				{Name: "Cold Bat", Team: "BOS", Proj: projections.Projection{G: 100, HR: 5}},
			})
			pit := projections.NewFanGraphsPitcherSourceFromEntries([]projections.PitcherSourceEntry{
				{Name: "Steady Reliever", Team: "BOS", Proj: projections.PitcherProjection{G: 60, IP: 65, K: 70}},
			})
			sched := &fakeDateSchedule{
				fakeSchedule: fakeSchedule{
					playing: map[string]map[string]bool{
						date.Format("2006-01-02"): {"NYY": true, "BOS": true},
					},
				},
			}
			cfg := &config.Config{
				LeagueID:  "lg1",
				TeamID:    "team1",
				DryRun:    false,
				AutoApply: true,
				Dates:     []time.Time{date},
			}
			var out bytes.Buffer
			opts := withFakeDeps(Options{
				Today:         tc.today,
				HitterSystem:  "depthcharts",
				PitcherSystem: "depthcharts",
				Out:           &out,
			}, bat, pit, sched)

			if _, err := Run(context.Background(), ft, cfg, opts); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := len(ft.applies) > 0; got != tc.wantApply {
				t.Errorf("ApplyLineup called = %v, want %v; output:\n%s", got, tc.wantApply, out.String())
			}
			if !strings.Contains(out.String(), tc.wantLine) {
				t.Errorf("output lacks %q:\n%s", tc.wantLine, out.String())
			}
		})
	}
}

// A team on a bye or out of the bracket has no scoring matchup in a playoff
// round, so there is nothing to optimize: both the hourly today-only run and
// the daily --matchup pre-write stop cleanly, applying and writing nothing.
// The check is scoped to playoff periods on purpose — in the regular season
// every team has a matchup every week, so a missing row there is a Fantrax
// fault that must stay loud rather than read as a quiet week off.
func TestRun_PlayoffRoundWithoutAMatchupAppliesNothing(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	seasonEnd := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	regular := fantrax.ScoringPeriod{Number: 22, Caption: "Scoring Period 22",
		StartDate: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)}
	round2 := fantrax.ScoringPeriod{Number: 24, Caption: "Playoffs - Round 2", Playoff: true,
		StartDate: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)}
	stopLine := "No scoring matchup for this team in Playoffs - Round 2 (2026-09-14 to 2026-09-20): bye or eliminated. Nothing to optimize."

	cases := []struct {
		name        string
		today       time.Time
		periods     []fantrax.ScoringPeriod
		weekStart   time.Time // zero = the team has no matchup week containing today
		weekEnd     time.Time
		matchupFlag bool
		wantApply   bool
		wantErr     bool
		wantLine    string
	}{
		{"hourly run, playoff round, team out", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), []fantrax.ScoringPeriod{regular, round2},
			time.Time{}, time.Time{}, false, false, false, stopLine},
		{"--matchup pre-write, playoff round, team out", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), []fantrax.ScoringPeriod{regular, round2},
			time.Time{}, time.Time{}, true, false, false, stopLine},
		{"hourly run, playoff round, team alive", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), []fantrax.ScoringPeriod{regular, round2},
			round2.StartDate, round2.EndDate, false, true, false, "Lineup applied successfully."},
		{"hourly run, regular season, no matchup rows", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), []fantrax.ScoringPeriod{regular},
			time.Time{}, time.Time{}, false, true, false, "Lineup applied successfully."},
		{"--matchup pre-write, regular season, no matchup rows", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), []fantrax.ScoringPeriod{regular},
			time.Time{}, time.Time{}, true, false, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeLineupClient{
				hitters: []fantrax.Player{
					{ID: "h1", Name: "Hot Hitter", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Reserve"},
					{ID: "h2", Name: "Cold Bat", MLBTeam: "BOS", Positions: []string{"012"}, Status: "Active", RosterPosition: "014"},
				},
				pitchers: []fantrax.Player{
					{ID: "p1", Name: "Steady Reliever", MLBTeam: "BOS", Positions: []string{"016"}, PosShortNames: "RP", Status: "Active", RosterPosition: "017"},
				},
				seasonStart: seasonStart,
				seasonEnd:   seasonEnd,
				periods:     tc.periods,
				weekStart:   tc.weekStart,
				weekEnd:     tc.weekEnd,
				period:      175,
			}
			bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
				{Name: "Hot Hitter", Team: "NYY", Proj: projections.Projection{G: 100, HR: 30}},
				{Name: "Cold Bat", Team: "BOS", Proj: projections.Projection{G: 100, HR: 5}},
			})
			pit := projections.NewFanGraphsPitcherSourceFromEntries([]projections.PitcherSourceEntry{
				{Name: "Steady Reliever", Team: "BOS", Proj: projections.PitcherProjection{G: 60, IP: 65, K: 70}},
			})
			playing := map[string]map[string]bool{}
			for d := tc.today.AddDate(0, 0, -1); !d.After(tc.today.AddDate(0, 0, 7)); d = d.AddDate(0, 0, 1) {
				playing[d.Format("2006-01-02")] = map[string]bool{"NYY": true, "BOS": true}
			}
			sched := &fakeDateSchedule{fakeSchedule: fakeSchedule{playing: playing}}
			cfg := &config.Config{LeagueID: "lg1", TeamID: "team1", DryRun: false, AutoApply: true}
			if !tc.matchupFlag {
				cfg.Dates = []time.Time{tc.today}
			}
			var out bytes.Buffer
			opts := withFakeDeps(Options{
				Today:              tc.today,
				NeedsMatchupLookup: tc.matchupFlag,
				HitterSystem:       "depthcharts",
				PitcherSystem:      "depthcharts",
				Out:                &out,
			}, bat, pit, sched)

			_, err := Run(context.Background(), ft, cfg, opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Run returned nil; a regular-season week with no matchup row is a fault and must stay loud. output:\n%s", out.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := len(ft.applies) > 0; got != tc.wantApply {
				t.Errorf("ApplyLineup called = %v, want %v; output:\n%s", got, tc.wantApply, out.String())
			}
			if !strings.Contains(out.String(), tc.wantLine) {
				t.Errorf("output lacks %q:\n%s", tc.wantLine, out.String())
			}
		})
	}
}

// A stop inside the bracket says WHY, read off the bracket: the round and
// opponent a team lost to, the round it sat out, or that it was never seeded.
// When the bracket cannot be read the stop still happens on the matchup-week
// evidence alone, with the generic wording, because the wording is decoration
// on a decision the week lookup already made.
func TestRun_PlayoffStopNamesTheReason(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	seasonEnd := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	team := func(id, name string) auth_client.PlayoffSlot {
		return auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotTeam, TeamID: id, TeamName: name}
	}
	bye := auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotBye}
	bracket := &auth_client.PlayoffBracket{Rounds: []auth_client.PlayoffRound{
		{Number: 1, Caption: "Round 1", ScoringPeriod: 23,
			StartDate: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
			Matchups: []auth_client.PlayoffMatchup{
				{Home: team("pfaadt", "Pfaadt Wood Kings"), Away: bye},
				{Home: team("jimmy", "jimmydyl"), Away: team("team1", "Intentional Balk"), HomeScore: 605, AwayScore: 515, Scored: true},
			}},
		{Number: 2, Caption: "Round 2", ScoringPeriod: 24,
			StartDate: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
			Matchups: []auth_client.PlayoffMatchup{{Home: team("pfaadt", "Pfaadt Wood Kings"), Away: team("jimmy", "jimmydyl"), Scored: true}}},
	}}
	round1 := fantrax.ScoringPeriod{Number: 23, Caption: "Playoffs - Round 1", Playoff: true,
		StartDate: bracket.Rounds[0].StartDate, EndDate: bracket.Rounds[0].EndDate}
	round2 := fantrax.ScoringPeriod{Number: 24, Caption: "Playoffs - Round 2", Playoff: true,
		StartDate: bracket.Rounds[1].StartDate, EndDate: bracket.Rounds[1].EndDate}

	cases := []struct {
		name       string
		teamID     string
		today      time.Time
		periods    []fantrax.ScoringPeriod
		bracket    *auth_client.PlayoffBracket
		bracketErr error
		wantLine   string
	}{
		{"eliminated", "team1", round2.StartDate.AddDate(0, 0, 1), []fantrax.ScoringPeriod{round1, round2}, bracket, nil,
			"Eliminated in Playoffs - Round 1 (lost to jimmydyl 515-605). Nothing to optimize."},
		{"bye", "pfaadt", round1.StartDate.AddDate(0, 0, 1), []fantrax.ScoringPeriod{round1, round2}, bracket, nil,
			"On a bye in Playoffs - Round 1 (2026-09-07 to 2026-09-13). Nothing to optimize."},
		{"never seeded", "bt95", round2.StartDate.AddDate(0, 0, 1), []fantrax.ScoringPeriod{round1, round2}, bracket, nil,
			"Not in the playoff bracket (Playoffs - Round 2 is under way). Nothing to optimize."},
		{"bracket unreadable", "team1", round2.StartDate.AddDate(0, 0, 1), []fantrax.ScoringPeriod{round1, round2}, nil, errors.New("fantrax 524"),
			"No scoring matchup for this team in Playoffs - Round 2 (2026-09-14 to 2026-09-20): bye or eliminated. Nothing to optimize."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeLineupClient{
				hitters:     []fantrax.Player{{ID: "h1", Name: "Hot Hitter", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Reserve"}},
				pitchers:    []fantrax.Player{{ID: "p1", Name: "Steady Reliever", MLBTeam: "BOS", Positions: []string{"016"}, PosShortNames: "RP", Status: "Active", RosterPosition: "017"}},
				seasonStart: seasonStart,
				seasonEnd:   seasonEnd,
				periods:     tc.periods,
				bracket:     tc.bracket,
				bracketErr:  tc.bracketErr,
				period:      175,
			}
			bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{{Name: "Hot Hitter", Team: "NYY", Proj: projections.Projection{G: 100, HR: 30}}})
			pit := projections.NewFanGraphsPitcherSourceFromEntries([]projections.PitcherSourceEntry{{Name: "Steady Reliever", Team: "BOS", Proj: projections.PitcherProjection{G: 60, IP: 65, K: 70}}})
			sched := &fakeDateSchedule{fakeSchedule: fakeSchedule{playing: map[string]map[string]bool{tc.today.Format("2006-01-02"): {"NYY": true, "BOS": true}}}}
			cfg := &config.Config{LeagueID: "lg1", TeamID: tc.teamID, AutoApply: true, Dates: []time.Time{tc.today}}
			var out bytes.Buffer
			opts := withFakeDeps(Options{Today: tc.today, HitterSystem: "depthcharts", PitcherSystem: "depthcharts", Out: &out}, bat, pit, sched)

			if _, err := Run(context.Background(), ft, cfg, opts); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(ft.applies) > 0 {
				t.Errorf("ApplyLineup was called; output:\n%s", out.String())
			}
			if !strings.Contains(out.String(), tc.wantLine) {
				t.Errorf("output lacks %q:\n%s", tc.wantLine, out.String())
			}
		})
	}
}
