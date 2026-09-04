package lineuprun

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeGSFantrax is the four-method Fantrax surface the cascade needs. Each
// step can be failed independently so every disable path gets its own test —
// the branch ordering here is load-bearing and was previously untested
// (rosterbot-32a criterion 2).
type fakeGSFantrax struct {
	weekStart, weekEnd time.Time
	weekErr            error

	pastGS int
	gsErr  error

	// gsThrough models the walk as what it is: a per-day snapshot diff whose
	// answer depends on which days it was asked to cover, keyed by the walk's
	// last day. A fake that returns one flat number regardless of the requested
	// range cannot tell "walked through yesterday" from "walked through today",
	// which is the whole of rosterbot-cg8l. nil falls back to pastGS.
	gsThrough map[string]int
	gsFor     fantrax.ScoringPeriod

	limitMax *int
	limitMin *int
	limitErr error

	limitCalls   int
	limitsForPer fantrax.WeeklyPeriod

	// pitcherDays feeds the start-rate history read; pitcherDaysErr fails it.
	// A nil slice with a nil error is an honest "nothing settled yet", which is
	// what every pre-existing cascade test wants: no history means every SP is
	// priced at the flat rate and the forecast is unchanged.
	pitcherDays    []fantrax.PitcherDay
	pitcherDaysErr error
	pitcherDayFrom time.Time
	pitcherDayTo   time.Time
}

func (f *fakeGSFantrax) GetTeamPitcherDays(_ string, start, end, _ time.Time, _ string, _ time.Duration) ([]fantrax.PitcherDay, error) {
	f.pitcherDayFrom, f.pitcherDayTo = start, end
	return f.pitcherDays, f.pitcherDaysErr
}

func (f *fakeGSFantrax) GetMatchupWeekBounds(_, _ time.Time) (time.Time, time.Time, error) {
	return f.weekStart, f.weekEnd, f.weekErr
}

func (f *fakeGSFantrax) GetTeamGS(_, _ string, sp fantrax.ScoringPeriod, _, _ time.Time, _ int, _ bool) (int, []fantrax.PitcherStart, error) {
	f.gsFor = sp
	if f.gsThrough != nil {
		return f.gsThrough[sp.EndDate.Format("2006-01-02")], nil, f.gsErr
	}
	return f.pastGS, nil, f.gsErr
}

func (f *fakeGSFantrax) GetGSLimits(_ string, period fantrax.WeeklyPeriod) (*int, *int, error) {
	f.limitCalls++
	f.limitsForPer = period
	return f.limitMin, f.limitMax, f.limitErr
}

func intp(v int) *int { return &v }

// healthyGS is a client where every step of the cascade succeeds, so each test
// below only has to break the one step it is about.
func healthyGS() *fakeGSFantrax {
	return &fakeGSFantrax{
		weekStart: day(2026, 7, 20),
		weekEnd:   day(2026, 7, 26),
		pastGS:    4,
		// Mon-Tue produced 4 starts; Ace Arm's start today makes 5. The two
		// entries let a test assert which of them the phase actually read.
		gsThrough: map[string]int{"2026-07-24": 4, "2026-07-25": 5},
		limitMax:  intp(12),
		limitMin:  intp(10),
	}
}

func gsInputs() GSInputs {
	return GSInputs{
		TeamID:      "t1",
		Today:       day(2026, 7, 25),
		SeasonStart: day(2026, 3, 25),
		Periods: []fantrax.ScoringPeriod{
			{Number: 16, StartDate: day(2026, 7, 20), EndDate: day(2026, 7, 26)},
		},
		PitcherRoster: []fantrax.Player{
			activeSP("a", "Ace Arm", "LAD"),
			activeSP("b", "Second Arm", "NYY"),
		},
		NumPitcherSlots: 5,
		ProjPts:         func(fantrax.Player) float64 { return 10 },
	}
}

func logsJoined(d GSDecision) string { return strings.Join(d.Logs, "\n") }

func TestComputeGSBudget_HealthyPathBuildsBudget(t *testing.T) {
	sched := &fakeSchedule{
		locked:    map[string]map[string]bool{"2026-07-25": {"LAD": true}},
		probables: map[string]map[string]string{"2026-07-25": {"ace arm": "LAD"}},
	}
	ft := healthyGS()

	got := ComputeGSBudget(ft, sched, gsInputs())

	if got.Budget == nil {
		t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(got))
	}
	if got.Budget.Limit != 12 {
		t.Errorf("Limit = %d, want 12 (the live max, not a fallback)", got.Budget.Limit)
	}
	// The walk covers the whole week so far including today: 4 starts earlier
	// in the week plus Ace Arm's today.
	if got.Budget.Used != 5 {
		t.Errorf("Used = %d, want 5 (the walk through today)", got.Budget.Used)
	}
	if !got.Budget.WeekEnd.Equal(day(2026, 7, 26)) {
		t.Errorf("WeekEnd = %v, want the matchup week end", got.Budget.WeekEnd)
	}
	if got.Alert != nil {
		t.Errorf("healthy path must not request an alert: %+v", got.Alert)
	}
	if ft.limitsForPer != 16 {
		t.Errorf("GS limits fetched for weekly period %d, want 16", ft.limitsForPer)
	}
}

// Every disable path: nil budget, a warning line, no alert (the GS-limit
// failure is the sole exception and gets its own test below).
func TestComputeGSBudget_DisablePaths(t *testing.T) {
	cases := []struct {
		name    string
		client  func() *fakeGSFantrax
		mutate  func(*GSInputs)
		wantLog string
	}{
		{
			name:   "season start unknown",
			client: healthyGS,
			mutate: func(in *GSInputs) { in.SeasonStart = time.Time{} },
			// Silent: the failed season-range lookup upstream logged already.
			wantLog: "",
		},
		{
			name: "matchup week fetch fails",
			client: func() *fakeGSFantrax {
				f := healthyGS()
				f.weekErr = errors.New("bounds down")
				return f
			},
			wantLog: "WARNING: could not determine matchup week (bounds down) — GS limit disabled",
		},
		{
			name: "no matchup week for today",
			client: func() *fakeGSFantrax {
				f := healthyGS()
				f.weekStart = time.Time{}
				return f
			},
			wantLog: "WARNING: no matchup week found for today — GS limit disabled",
		},
		{
			name: "past-GS walk fails",
			client: func() *fakeGSFantrax {
				f := healthyGS()
				f.gsErr = errors.New("walk blew up")
				return f
			},
			wantLog: "WARNING: per-day GS walk failed (walk blew up) — GS limit disabled",
		},
		{
			name:    "periods lookup failed upstream",
			client:  healthyGS,
			mutate:  func(in *GSInputs) { in.PeriodsErr = errors.New("standings down") },
			wantLog: "WARNING: could not fetch scoring periods (standings down) — GS limit disabled",
		},
		{
			name:    "today falls in no weekly period",
			client:  healthyGS,
			mutate:  func(in *GSInputs) { in.Periods = nil },
			wantLog: "WARNING: could not resolve scoring period for today — GS limit disabled",
		},
		{
			name: "no GS max configured",
			client: func() *fakeGSFantrax {
				f := healthyGS()
				f.limitMax = nil
				return f
			},
			// Not an error and not an alert: Fantrax simply isn't tracking GS
			// for this period, so there is nothing to gate.
			wantLog: "No GS max configured by Fantrax for period 16 — GS limit disabled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := gsInputs()
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			got := ComputeGSBudget(tc.client(), &fakeSchedule{}, in)

			if got.Budget != nil {
				t.Errorf("expected the gate disabled, got budget %+v", got.Budget)
			}
			if got.Alert != nil {
				t.Errorf("this path must not alert, got %+v", got.Alert)
			}
			if tc.wantLog == "" {
				if len(got.Logs) != 0 {
					t.Errorf("expected no log line, got %v", got.Logs)
				}
				return
			}
			if logsJoined(got) != tc.wantLog {
				t.Errorf("log = %q, want %q", logsJoined(got), tc.wantLog)
			}
		})
	}
}

// The one failure worth being noisy about. There is no static fallback for the
// GS max by design — Fantrax rescales it around merged periods — so a fetch
// failure must disable the gate AND request an alert, never quietly substitute
// a stale number.
func TestComputeGSBudget_LiveLimitFailureAlertsAndDoesNotFallBack(t *testing.T) {
	ft := healthyGS()
	ft.limitErr = errors.New("503 from fantrax")

	got := ComputeGSBudget(ft, &fakeSchedule{}, gsInputs())

	if got.Budget != nil {
		t.Fatalf("must not fall back to any limit, got %+v", got.Budget)
	}
	if got.Alert == nil {
		t.Fatal("a live GS-limit fetch failure must request a Pushover")
	}
	if got.Alert.Title != "optimize: GS limit fetch failed" {
		t.Errorf("alert title = %q", got.Alert.Title)
	}
	want := "optimize: live GS limit fetch failed for period 16 (503 from fantrax) — GS limit disabled"
	if got.Alert.Message != want {
		t.Errorf("alert message = %q, want %q", got.Alert.Message, want)
	}
	if logsJoined(got) != "WARNING: "+want {
		t.Errorf("log = %q, want the same text as the alert", logsJoined(got))
	}
}

// The phase decides an alert is warranted; it never sends one. That separation
// is what keeps it testable without credentials or an HTTP server, and it is
// asserted here rather than left as a convention.
func TestComputeGSBudget_ReturnsTheAlertRatherThanSendingIt(t *testing.T) {
	ft := healthyGS()
	ft.limitErr = errors.New("down")

	got := ComputeGSBudget(ft, &fakeSchedule{}, gsInputs())
	if got.Alert == nil {
		t.Fatal("expected an alert to be requested")
	}
	// GSAlert carries only the message; the credentials and the send live with
	// the caller. If a Pushover client ever appears in GSInputs, this fails to
	// compile and the reviewer gets to ask why.
	if got.Alert.Title == "" || got.Alert.Message == "" {
		t.Error("alert should carry both the title and body the caller will send")
	}
}

// A failure early in the cascade must short-circuit the rest: each step's
// result feeds the next, so continuing past one would fetch against garbage.
func TestComputeGSBudget_EarlyFailureSkipsLaterFetches(t *testing.T) {
	ft := healthyGS()
	ft.weekErr = errors.New("bounds down")

	ComputeGSBudget(ft, &fakeSchedule{}, gsInputs())

	if ft.limitCalls != 0 {
		t.Errorf("GetGSLimits called %d times after an earlier failure — the cascade should short-circuit", ft.limitCalls)
	}
}

// rosterbot-1jvj: the forecast is the last step of the cascade, and it obeys
// the same rule as every step before it — a failure disables the gate rather
// than running it on a number nobody can vouch for.
//
// The cost is bounded by cadence, not by severity: the lineup job runs hourly,
// so a statsapi blip costs one run's gate, and the next run rebuilds it.
func TestComputeGSBudget_ForecastScheduleFailureDisablesGate(t *testing.T) {
	sched := &fakeSchedule{
		locked:     map[string]map[string]bool{"2026-07-25": {"LAD": true}},
		probables:  map[string]map[string]string{"2026-07-25": {"ace arm": "LAD"}},
		playingErr: map[string]error{"2026-07-26": errors.New("statsapi timeout")},
	}

	d := ComputeGSBudget(healthyGS(), sched, gsInputs())

	if d.Budget != nil {
		t.Errorf("Budget = %+v, want nil — an unverifiable forecast must disable the gate", d.Budget)
	}
	logs := logsJoined(d)
	if !strings.Contains(logs, "statsapi timeout") || !strings.Contains(logs, "GS limit disabled") {
		t.Errorf("logs = %q, want the cause and the consequence", logs)
	}
	if d.Alert != nil {
		t.Errorf("Alert = %+v, want none — a schedule blip is not the GS-limit-fetch alert", d.Alert)
	}
}

func TestComputeGSBudget_ForecastProbablesFailureDisablesGate(t *testing.T) {
	sched := &fakeSchedule{
		locked:       map[string]map[string]bool{"2026-07-25": {"LAD": true}},
		probables:    map[string]map[string]string{"2026-07-25": {"ace arm": "LAD"}},
		probablesErr: map[string]error{"2026-07-26": errors.New("statsapi 503")},
	}

	d := ComputeGSBudget(healthyGS(), sched, gsInputs())

	if d.Budget != nil {
		t.Errorf("Budget = %+v, want nil", d.Budget)
	}
	if !strings.Contains(logsJoined(d), "statsapi 503") {
		t.Errorf("logs = %q, want the underlying cause named", logsJoined(d))
	}
}

// TestComputeGSBudget_CarriesTheLiveFloor pins rosterbot-dpm's first step: the
// minimum reaches the budget instead of being dropped at the call site.
//
// GetGSLimits has always returned both bounds; ComputeGSBudget discarded the
// first with `_`, so nothing downstream could represent the floor even in
// principle. Our team finished on the league minimum in three of four scoring
// periods while the only component that knew the floor existed (gs-check)
// reported it the day AFTER the period closed.
func TestComputeGSBudget_CarriesTheLiveFloor(t *testing.T) {
	d := ComputeGSBudget(healthyGS(), &fakeSchedule{}, gsInputs())
	if d.Budget == nil {
		t.Fatal("budget should be built")
	}
	if d.Budget.Floor != 10 {
		t.Errorf("Floor = %d, want 10 — the live minimum must reach the budget", d.Budget.Floor)
	}
	if d.Budget.Limit != 12 {
		t.Errorf("Limit = %d, want 12", d.Budget.Limit)
	}
}

// TestComputeGSBudget_MissingFloorDoesNotDisableTheGate pins the asymmetry
// between the two bounds. A missing MAXIMUM disables the gate — there is
// nothing to gate against. A missing MINIMUM is an ordinary league
// configuration: there is simply no floor to protect, and the ceiling side
// must keep working exactly as before.
func TestComputeGSBudget_MissingFloorDoesNotDisableTheGate(t *testing.T) {
	f := healthyGS()
	f.limitMin = nil
	d := ComputeGSBudget(f, &fakeSchedule{}, gsInputs())
	if d.Budget == nil {
		t.Fatal("a missing GS minimum must NOT disable the gate")
	}
	if d.Budget.Floor != 0 {
		t.Errorf("Floor = %d, want 0", d.Budget.Floor)
	}
	if d.Budget.NeedForFloor() != 0 {
		t.Errorf("NeedForFloor() = %d, want 0 with no floor configured", d.Budget.NeedForFloor())
	}
}

// lockedSP is an SP whose game is already in progress or final. Fantrax locks
// him at first pitch, which is the same boundary its YTD GS settlement crosses
// — so a locked probable's start is (or is about to be) in Used and must not
// be counted a second time.
func lockedSP(id, name, team string) fantrax.Player {
	p := activeSP(id, name, team)
	p.Locked = true
	return p
}

// Today sits in a seam: buildGSForecast starts TOMORROW and Used only reflects
// a day once Fantrax settles its YTD GS deltas that evening. Between the two,
// today's confirmed starters are counted nowhere, which is what made the floor
// alert's shortfall spike every morning (rosterbot-ogtq). The budget has to
// carry that count so the alert can net it off.
func TestComputeGSBudget_CountsTodaysUnlockedProbablesAsUnsettled(t *testing.T) {
	sched := &fakeSchedule{
		probables: map[string]map[string]string{
			"2026-07-25": {"ace arm": "LAD", "second arm": "NYY"},
		},
	}

	got := ComputeGSBudget(healthyGS(), sched, gsInputs())

	if got.Budget == nil {
		t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(got))
	}
	if got.Budget.TodayUnsettled != 2 {
		t.Errorf("TodayUnsettled = %d, want 2 — both of today's probables are unlocked, so neither is in Used yet",
			got.Budget.TodayUnsettled)
	}
}

// The lock is the settlement boundary. Once a start is under way Fantrax will
// credit it to Used on its own, so counting it here too would net the same
// start off the floor twice and quiet an alert that should have fired — the
// false NEGATIVE this alert can least afford.
func TestComputeGSBudget_LockedProbablesAreAlreadySettled(t *testing.T) {
	sched := &fakeSchedule{
		probables: map[string]map[string]string{
			"2026-07-25": {"ace arm": "LAD", "second arm": "NYY"},
		},
	}
	in := gsInputs()
	in.PitcherRoster = []fantrax.Player{
		lockedSP("a", "Ace Arm", "LAD"),
		activeSP("b", "Second Arm", "NYY"),
	}

	got := ComputeGSBudget(healthyGS(), sched, in)

	if got.Budget == nil {
		t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(got))
	}
	if got.Budget.TodayUnsettled != 1 {
		t.Errorf("TodayUnsettled = %d, want 1 — Ace Arm is locked, so his start is Used's to count",
			got.Budget.TodayUnsettled)
	}
}

// A statsapi blip on TODAY's probables must not disable the budget, and must
// not invent a credit either. Degrading to zero reproduces the pre-fix
// shortfall exactly: louder than the truth, never quieter.
func TestComputeGSBudget_TodaysProbablesFailureDegradesToNoCredit(t *testing.T) {
	sched := &fakeSchedule{
		probablesErr: map[string]error{"2026-07-25": errors.New("statsapi 503")},
	}

	got := ComputeGSBudget(healthyGS(), sched, gsInputs())

	if got.Budget == nil {
		t.Fatalf("a probables blip for today must not disable the gate. logs:\n%s", logsJoined(got))
	}
	if got.Budget.TodayUnsettled != 0 {
		t.Errorf("TodayUnsettled = %d, want 0 when today's probables are unreadable", got.Budget.TodayUnsettled)
	}
	if !strings.Contains(logsJoined(got), "today's probables") {
		t.Errorf("the degrade must be visible in the logs, got:\n%s", logsJoined(got))
	}
}

// A benched pitcher's real-world start never reaches Used — GetTeamGS counts
// only active-slot GS deltas (internal/fantrax/pitcher_starts.go:84-85), so a
// reserve SP confirmed as today's probable is never going to settle into the
// week's used count no matter how certain MLB is about him pitching.
// todayUnsettledStarts must not credit him, or the shortfall reads low on a
// day the roster actually left that start on the bench — the false NEGATIVE
// the design explicitly rejects (rosterbot-ogtq lens A).
func TestComputeGSBudget_ReserveConfirmedStarterIsNotCredited(t *testing.T) {
	sched := &fakeSchedule{
		probables: map[string]map[string]string{
			"2026-07-25": {"ace arm": "LAD", "second arm": "NYY"},
		},
	}
	in := gsInputs()
	in.PitcherRoster = []fantrax.Player{
		activeSP("a", "Ace Arm", "LAD"),
		reserveSP("b", "Second Arm", "NYY"),
	}

	got := ComputeGSBudget(healthyGS(), sched, in)

	if got.Budget == nil {
		t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(got))
	}
	if got.Budget.TodayUnsettled != 1 {
		t.Errorf("TodayUnsettled = %d, want 1 — Second Arm is benched, so his start cannot reach Used",
			got.Budget.TodayUnsettled)
	}
}

// todayUnsettledStarts must never credit more starts than the roster has
// active pitcher slots to hold them in, mirroring buildGSForecast's own cap
// ("Cap at active P slots, keeping the highest-value probables",
// gsbudget.go ~325-333) — a team can only actually start as many pitchers as
// it has active P slots, so an uncapped count would over-credit on any
// roster carrying more confirmed-starter-eligible SPs than P slots
// (rosterbot-ogtq lens B).
func TestComputeGSBudget_TodayUnsettledCapsAtPitcherSlots(t *testing.T) {
	sched := &fakeSchedule{
		probables: map[string]map[string]string{
			"2026-07-25": {"ace arm": "LAD", "second arm": "NYY", "third arm": "BOS"},
		},
	}
	in := gsInputs()
	in.NumPitcherSlots = 2
	in.PitcherRoster = []fantrax.Player{
		activeSP("a", "Ace Arm", "LAD"),
		activeSP("b", "Second Arm", "NYY"),
		activeSP("c", "Third Arm", "BOS"),
	}

	got := ComputeGSBudget(healthyGS(), sched, in)

	if got.Budget == nil {
		t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(got))
	}
	if got.Budget.TodayUnsettled != 2 {
		t.Errorf("TodayUnsettled = %d, want 2 — capped at NumPitcherSlots even though 3 active SPs are confirmed",
			got.Budget.TodayUnsettled)
	}
}
