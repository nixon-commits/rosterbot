package lineuprun

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeInputsClient is the whole dependency surface LoadInputs needs — eight
// methods, no Fantrax client, no network. Each fetch can be made to fail
// independently so the partial-failure policy is assertable one branch at a
// time. hook, when set, runs at the top of every fetch (used by the
// concurrency test).
type fakeInputsClient struct {
	hitterRosterErr   error
	hitterSlotsErr    error
	hitterScoringErr  error
	pitcherRosterErr  error
	pitcherSlotsErr   error
	pitcherScoringErr error
	periodErr         error
	periodsErr        error

	hook func()
}

func (f *fakeInputsClient) call() {
	if f.hook != nil {
		f.hook()
	}
}

func (f *fakeInputsClient) GetHitterRoster() ([]fantrax.Player, error) {
	f.call()
	return []fantrax.Player{
		{ID: "h1", Name: "Star Hitter", Status: "Active"},
		{ID: "h2", Name: "Bench Bat", Status: "Reserve"},
	}, f.hitterRosterErr
}

func (f *fakeInputsClient) GetActiveSlots() ([]fantrax.Slot, error) {
	f.call()
	return []fantrax.Slot{{PosID: "012", PosName: "OF"}}, f.hitterSlotsErr
}

func (f *fakeInputsClient) GetScoringWeights() (fantrax.ScoringWeights, error) {
	f.call()
	return fantrax.ScoringWeights{"HR": 4, "R": 1}, f.hitterScoringErr
}

func (f *fakeInputsClient) GetPitcherRoster() ([]fantrax.Player, error) {
	f.call()
	return []fantrax.Player{{ID: "p1", Name: "Ace Arm", Status: "Active"}}, f.pitcherRosterErr
}

func (f *fakeInputsClient) GetPitcherSlots() ([]fantrax.Slot, error) {
	f.call()
	return []fantrax.Slot{{PosID: "020", PosName: "P"}}, f.pitcherSlotsErr
}

func (f *fakeInputsClient) GetPitcherScoringWeights() (fantrax.ScoringWeights, error) {
	f.call()
	return fantrax.ScoringWeights{"K": 2}, f.pitcherScoringErr
}

func (f *fakeInputsClient) GetCurrentPeriod() (fantrax.DailyPeriod, error) {
	f.call()
	return 113, f.periodErr
}

func (f *fakeInputsClient) GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error) {
	f.call()
	return []fantrax.ScoringPeriod{{Number: 16}}, nil, nil, f.periodsErr
}

// recordingProgress captures phase transitions and log lines in the order they
// were emitted — the ordering IS the thing under test, since concurrent
// fetching must not reorder what the terminal shows.
type recordingProgress struct {
	events []string
}

func (r *recordingProgress) Start(phase string) { r.events = append(r.events, "start:"+phase) }
func (r *recordingProgress) Done(phase, detail string) {
	r.events = append(r.events, "done:"+phase+":"+detail)
}
func (r *recordingProgress) Logf(format string, args ...any) {
	r.events = append(r.events, "log:"+fmt.Sprintf(format, args...))
}

func TestLoadInputs_ReturnsEveryFetchInOneStruct(t *testing.T) {
	got, err := LoadInputs(&fakeInputsClient{}, &recordingProgress{}, "DepthCharts RoS")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.HitterRoster) != 2 || len(got.PitcherRoster) != 1 {
		t.Errorf("rosters not populated: %d hitters, %d pitchers", len(got.HitterRoster), len(got.PitcherRoster))
	}
	if len(got.HitterSlots) != 1 || len(got.PitcherSlots) != 1 {
		t.Errorf("slots not populated: %v / %v", got.HitterSlots, got.PitcherSlots)
	}
	if len(got.HitterScoring) != 2 || len(got.PitcherScoring) != 1 {
		t.Errorf("scoring weights not populated: %v / %v", got.HitterScoring, got.PitcherScoring)
	}
	if got.CurrentPeriod != 113 {
		t.Errorf("CurrentPeriod = %d, want 113", got.CurrentPeriod)
	}
	if got.PeriodErr != nil || got.PeriodsErr != nil {
		t.Errorf("clean run should carry no soft errors: %v / %v", got.PeriodErr, got.PeriodsErr)
	}
}

// Concurrent fetching must not reorder terminal output: every log line and the
// phase completion are emitted after the group joins, in the fixed order the
// pre-phase inline code used.
func TestLoadInputs_EmitsProgressInFixedOrder(t *testing.T) {
	prog := &recordingProgress{}
	if _, err := LoadInputs(&fakeInputsClient{}, prog, "DepthCharts RoS"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{
		"start:Roster",
		"log:hitter roster: 2 hitters (1 active)",
		"log:hitter active slots: 1",
		"log:hitter scoring weights: 2 categories",
		"log:pitcher roster: 1 pitchers (1 active)",
		"log:pitcher active slots: 1",
		"log:pitcher scoring weights: 1 categories",
		"done:Roster:2 hitters (1 active) · 1 pitchers (1 active)",
		"log:current period: 113",
	}
	if len(prog.events) != len(want) {
		t.Fatalf("event count = %d, want %d:\n%s", len(prog.events), len(want), strings.Join(prog.events, "\n"))
	}
	for i := range want {
		if prog.events[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, prog.events[i], want[i])
		}
	}
}

// Each of the six roster/slot/scoring reads is load-bearing: a failure aborts
// the run rather than producing a lineup from incomplete inputs. The error text
// is asserted because it is what an operator reads out of a failed Fargate task.
func TestLoadInputs_EachRosterFetchIsFatal(t *testing.T) {
	boom := errors.New("upstream down")
	cases := []struct {
		name    string
		set     func(*fakeInputsClient)
		wantMsg string
	}{
		{"hitter roster", func(f *fakeInputsClient) { f.hitterRosterErr = boom }, "get hitter roster: upstream down"},
		{"hitter slots", func(f *fakeInputsClient) { f.hitterSlotsErr = boom }, "get hitter slots: upstream down"},
		{"hitter scoring", func(f *fakeInputsClient) { f.hitterScoringErr = boom }, "get hitter scoring: upstream down"},
		{"pitcher roster", func(f *fakeInputsClient) { f.pitcherRosterErr = boom }, "get pitcher roster: upstream down"},
		{"pitcher slots", func(f *fakeInputsClient) { f.pitcherSlotsErr = boom }, "get pitcher slots: upstream down"},
		{"pitcher scoring", func(f *fakeInputsClient) { f.pitcherScoringErr = boom }, "get pitcher scoring: upstream down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeInputsClient{}
			tc.set(ft)
			got, err := LoadInputs(ft, &recordingProgress{}, "DepthCharts RoS")
			if err == nil {
				t.Fatalf("expected a fatal error for %s", tc.name)
			}
			if !errors.Is(err, boom) {
				t.Errorf("error does not wrap the cause: %v", err)
			}
			if err.Error() != tc.wantMsg {
				t.Errorf("error = %q, want %q", err.Error(), tc.wantMsg)
			}
			if got.HitterRoster != nil || got.PitcherRoster != nil {
				t.Error("a fatal failure must not return half-populated Inputs")
			}
		})
	}
}

// Concurrency must not make the reported error depend on which request lost the
// race. With several fetches failing at once, the caller sees the same one the
// sequential code would have reported — the first in declaration order.
func TestLoadInputs_FatalErrorPrecedenceIsDeterministic(t *testing.T) {
	ft := &fakeInputsClient{
		pitcherScoringErr: errors.New("last"),
		hitterSlotsErr:    errors.New("second"),
		pitcherRosterErr:  errors.New("fourth"),
	}
	_, err := LoadInputs(ft, &recordingProgress{}, "DepthCharts RoS")
	if err == nil || err.Error() != "get hitter slots: second" {
		t.Errorf("error = %v, want the earliest failure in sequential order (hitter slots)", err)
	}
}

// The two period lookups are soft: each disables one downstream behaviour that
// has a defined degraded mode, so they ride along on Inputs instead of aborting.
func TestLoadInputs_PeriodLookupFailuresAreSoft(t *testing.T) {
	periodBoom := errors.New("no current period")
	periodsBoom := errors.New("standings down")
	ft := &fakeInputsClient{periodErr: periodBoom, periodsErr: periodsBoom}
	prog := &recordingProgress{}

	got, err := LoadInputs(ft, prog, "DepthCharts RoS")
	if err != nil {
		t.Fatalf("period lookups must not abort the run, got %v", err)
	}
	if !errors.Is(got.PeriodErr, periodBoom) {
		t.Errorf("PeriodErr = %v, want the cause carried on Inputs", got.PeriodErr)
	}
	if !errors.Is(got.PeriodsErr, periodsBoom) {
		t.Errorf("PeriodsErr = %v, want the cause carried on Inputs", got.PeriodsErr)
	}
	if len(got.HitterRoster) == 0 {
		t.Error("the rest of the inputs should still be populated")
	}

	joined := strings.Join(prog.events, "\n")
	for _, want := range []string{
		"WARNING: could not get current period (no current period) — using DepthCharts RoS only",
		"WARNING: could not fetch weekly scoring periods (standings down) — GS-budget gate disabled",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing degraded-mode warning %q in:\n%s", want, joined)
		}
	}
}

// The fetches genuinely run concurrently, not just "could". Every fetch parks
// on a barrier that only opens once all eight have arrived; a sequential
// implementation never opens it and this times out.
func TestLoadInputs_FetchesRunConcurrently(t *testing.T) {
	const nFetches = 8
	var (
		mu      sync.Mutex
		arrived int
		opened  = make(chan struct{})
		once    sync.Once
	)
	ft := &fakeInputsClient{hook: func() {
		mu.Lock()
		arrived++
		full := arrived == nFetches
		mu.Unlock()
		if full {
			once.Do(func() { close(opened) })
		}
		select {
		case <-opened:
		case <-time.After(5 * time.Second):
		}
	}}

	done := make(chan error, 1)
	go func() {
		_, err := LoadInputs(ft, &recordingProgress{}, "DepthCharts RoS")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LoadInputs did not finish — the fetches are running sequentially")
	}

	select {
	case <-opened:
	default:
		t.Error("barrier never opened: fewer than eight fetches were in flight at once")
	}
}
