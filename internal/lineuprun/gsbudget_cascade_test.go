package lineuprun

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeGSFantrax is the three-method Fantrax surface the cascade needs. Each
// step can be failed independently so every disable path gets its own test —
// the branch ordering here is load-bearing and was previously untested
// (rosterbot-32a criterion 2).
type fakeGSFantrax struct {
	weekStart, weekEnd time.Time
	weekErr            error

	pastGS int
	gsErr  error

	limitMax *int
	limitErr error

	limitCalls   int
	limitsForPer fantrax.WeeklyPeriod
}

func (f *fakeGSFantrax) GetMatchupWeekBounds(_, _ time.Time) (time.Time, time.Time, error) {
	return f.weekStart, f.weekEnd, f.weekErr
}

func (f *fakeGSFantrax) GetTeamGS(_, _ string, _ fantrax.ScoringPeriod, _, _ time.Time, _ int, _ bool) (int, []fantrax.PitcherStart, error) {
	return f.pastGS, nil, f.gsErr
}

func (f *fakeGSFantrax) GetGSLimits(_ string, period fantrax.WeeklyPeriod) (*int, *int, error) {
	f.limitCalls++
	f.limitsForPer = period
	return nil, f.limitMax, f.limitErr
}

func intp(v int) *int { return &v }

// healthyGS is a client where every step of the cascade succeeds, so each test
// below only has to break the one step it is about.
func healthyGS() *fakeGSFantrax {
	return &fakeGSFantrax{
		weekStart: day(2026, 7, 20),
		weekEnd:   day(2026, 7, 26),
		pastGS:    4,
		limitMax:  intp(12),
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
	// 4 from the past-GS walk + 1 locked-and-probable SP today.
	if got.Budget.Used != 5 {
		t.Errorf("Used = %d, want 5 (4 past + 1 started today)", got.Budget.Used)
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

// The schedule lookups that convert today's starts into "used" are best-effort,
// but only together: a partial view undercounts, which would let the gate
// approve a start the roster cannot afford.
func TestComputeGSBudget_PartialScheduleViewDoesNotCountTodayStarts(t *testing.T) {
	sched := &failingLockedSchedule{fakeSchedule{
		probables: map[string]map[string]string{"2026-07-25": {"ace arm": "LAD"}},
	}}

	got := ComputeGSBudget(healthyGS(), sched, gsInputs())

	if got.Budget == nil {
		t.Fatal("a schedule hiccup should degrade the count, not disable the gate")
	}
	if got.Budget.Used != 4 {
		t.Errorf("Used = %d, want 4 (past GS only — today's starts are unknowable)", got.Budget.Used)
	}
}

type failingLockedSchedule struct{ fakeSchedule }

func (f *failingLockedSchedule) LockedTeams(time.Time) (map[string]bool, error) {
	return nil, errors.New("locked teams unavailable")
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
