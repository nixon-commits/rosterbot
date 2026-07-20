package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

var errFakeList = errors.New("fake list error")

// fakeStore is an in-memory ObjectStore for handler tests.
type fakeStore struct {
	data map[string][]byte
	err  error
}

func (f fakeStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	d, ok := f.data[key]
	return d, ok, nil
}

// fakeInputs is the synthetic "optimizer output" the contract is asserted
// against: two active hitters (one benched in the real MLB lineup), one active
// SP, and one reserve hitter on the bench.
func fakeInputs() Inputs {
	return Inputs{
		Date:     "2026-06-17",
		LeagueID: "LG1",
		TeamID:   "TM1",
		HitterSlots: []fantrax.Slot{
			{PosID: "001", PosName: "C"},
			{PosID: "002", PosName: "1B"},
		},
		PitcherSlots: []fantrax.Slot{
			{PosID: "015", PosName: "SP"},
		},
		Hitters: []optimizer.ScoredPlayer{
			{Player: fantrax.Player{ID: "h1", Name: "Adley Rutschman", MLBTeam: "BAL", Positions: []string{"001"}, RosterPosition: "001", Status: "Active"}, ExpectedPts: 3.4, HasGame: true},
			{Player: fantrax.Player{ID: "h2", Name: "Vlad Guerrero", MLBTeam: "TOR", Positions: []string{"002"}, RosterPosition: "002", Status: "Active"}, ExpectedPts: 5.25, HasGame: false},
			{Player: fantrax.Player{ID: "h3", Name: "Bench Guy", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Reserve"}, ExpectedPts: 2.0, HasGame: true},
		},
		Pitchers: []optimizer.ScoredPitcher{
			{Player: fantrax.Player{ID: "p1", Name: "Corbin Burnes", MLBTeam: "ARI", Positions: []string{"015"}, PosShortNames: "SP", RosterPosition: "015", Status: "Active"}, ExpectedPts: 12.1, HasGame: true, IsStarter: true},
		},
		BenchedToday: map[string]bool{projections.NormalizeName("Vlad Guerrero"): true},
	}
}

// wantJSON is the exact wire contract the iOS client is pinned to.
const wantJSON = `{
  "date": "2026-06-17",
  "league_id": "LG1",
  "team_id": "TM1",
  "slots": [
    {
      "slot": "C",
      "player": {
        "id": "h1",
        "name": "Adley Rutschman",
        "team": "BAL",
        "pos": [
          "C"
        ],
        "proj": 3.4,
        "status": "OK"
      }
    },
    {
      "slot": "1B",
      "player": {
        "id": "h2",
        "name": "Vlad Guerrero",
        "team": "TOR",
        "pos": [
          "1B"
        ],
        "proj": 5.25,
        "status": "BENCHED"
      }
    },
    {
      "slot": "SP",
      "player": {
        "id": "p1",
        "name": "Corbin Burnes",
        "team": "ARI",
        "pos": [
          "SP"
        ],
        "proj": 12.1,
        "status": "OK"
      }
    },
    {
      "slot": "BN",
      "player": {
        "id": "h3",
        "name": "Bench Guy",
        "team": "NYY",
        "pos": [
          "OF"
        ],
        "proj": 2,
        "status": "OK"
      }
    }
  ],
  "projected_points": 15.5,
  "warnings": [
    "Vlad Guerrero benched in real lineup"
  ]
}`

func TestBuildContract(t *testing.T) {
	got, err := Marshal(Build(fakeInputs()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != wantJSON {
		t.Fatalf("contract mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantJSON)
	}
}

func TestHandlerServesPublishedBytes(t *testing.T) {
	body, _ := Marshal(Build(fakeInputs()))
	h := Handler(Config{Token: "secret-token", Lineups: fakeStore{data: map[string][]byte{TodayKey: body}}})

	req := httptest.NewRequest(http.MethodGet, "/v1/lineup/today", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if rec.Body.String() != wantJSON {
		t.Fatalf("body mismatch:\n--- got ---\n%s\n--- want ---\n%s", rec.Body.String(), wantJSON)
	}
}

func TestHandlerAuth(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Lineups: fakeStore{data: map[string][]byte{TodayKey: []byte("{}")}}})

	cases := []struct {
		name, header string
		want         int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer nope", http.StatusUnauthorized},
		{"malformed", "secret-token", http.StatusUnauthorized},
		{"valid", "Bearer secret-token", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/lineup/today", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestHandlerNotFoundWhenNoLineup(t *testing.T) {
	h := Handler(Config{Token: "t", Lineups: fakeStore{data: map[string][]byte{}}})
	req := httptest.NewRequest(http.MethodGet, "/v1/lineup/today", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// --- runs + jobs routes ---

type fakeRuns struct {
	runs   []Run
	detail *RunDetail
	err    error
}

func (f fakeRuns) List(_ context.Context, limit int) ([]Run, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.runs) > limit {
		return f.runs[:limit], nil
	}
	return f.runs, nil
}

func (f fakeRuns) Get(_ context.Context, id string) (*RunDetail, bool, error) {
	if f.detail != nil && f.detail.ID == id {
		return f.detail, true, nil
	}
	return nil, false, nil
}

type fakeJobs struct {
	lastCommand []string
	id          string
	err         error
}

func (f *fakeJobs) Run(_ context.Context, command []string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.lastCommand = command
	return f.id, nil
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRunsList(t *testing.T) {
	ec := 0
	h := Handler(Config{Token: "t", Runs: fakeRuns{runs: []Run{
		{ID: "a", Command: "optimize --matchup", Status: "SUCCESS", ExitCode: &ec, StartedAt: "2026-06-17T19:00:00Z", EndedAt: "2026-06-17T19:00:30Z", Trigger: "schedule"},
	}}})
	rec := do(h, http.MethodGet, "/v1/runs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got RunsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Runs) != 1 || got.Runs[0].ID != "a" || got.Runs[0].Status != "SUCCESS" {
		t.Fatalf("unexpected runs: %+v", got.Runs)
	}
}

func TestRunDetailNotFound(t *testing.T) {
	h := Handler(Config{Token: "t", Runs: fakeRuns{}})
	if rec := do(h, http.MethodGet, "/v1/runs/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestJobTriggerMapsCommand(t *testing.T) {
	jobs := &fakeJobs{id: "task-123"}
	h := Handler(Config{Token: "t", Jobs: jobs})

	rec := do(h, http.MethodPost, "/v1/jobs/optimize")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	var got JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "task-123" || got.Command != "optimize --matchup" || got.Status != "RUNNING" {
		t.Fatalf("unexpected job response: %+v", got)
	}
	if strings.Join(jobs.lastCommand, " ") != "optimize --matchup" {
		t.Fatalf("runner got command %v", jobs.lastCommand)
	}
}

func TestBuildJobArgs(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   string
		errs   bool
	}{
		{"optimize", nil, "optimize --matchup", false},
		{"optimize", map[string]string{"period": "all"}, "optimize --dates all", false},
		{"optimize", map[string]string{"period": "custom", "dates": "2026-04-01:2026-04-07"}, "optimize --dates 2026-04-01:2026-04-07", false},
		{"optimize", map[string]string{"period": "custom", "dates": "garbage"}, "", true},
		{"optimize", map[string]string{"projections": "steamer", "dry_run": "true"}, "optimize --matchup --projections steamer --dry-run", false},
		{"optimize", map[string]string{"projections": "evil"}, "", true},
		{"waivers", map[string]string{"top": "25", "positions": "OF,SP"}, "waivers --top 25 --positions OF,SP", false},
		{"waivers", map[string]string{"top": "999"}, "", true},
		{"waivers", map[string]string{"positions": "--apply"}, "", true}, // flag-injection blocked
		{"backtest", map[string]string{"recency_experiment": "true"}, "backtest --recency-experiment", false},
	}
	for _, tc := range cases {
		args, ok, err := BuildJobArgs(tc.name, tc.params)
		if !ok {
			t.Errorf("%s %v: unknown job", tc.name, tc.params)
			continue
		}
		if tc.errs {
			if err == nil {
				t.Errorf("%s %v: expected validation error, got %v", tc.name, tc.params, args)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %v: unexpected error %v", tc.name, tc.params, err)
			continue
		}
		if got := strings.Join(args, " "); got != tc.want {
			t.Errorf("%s %v = %q, want %q", tc.name, tc.params, got, tc.want)
		}
	}
}

func TestJobsSchemaEndpoint(t *testing.T) {
	h := Handler(Config{Token: "t"}) // schema is static, no Jobs runner needed
	rec := do(h, http.MethodGet, "/v1/jobs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got JobsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Jobs) != 9 {
		t.Fatalf("want 9 jobs, got %d", len(got.Jobs))
	}
}

func TestJobTriggerUnknown(t *testing.T) {
	h := Handler(Config{Token: "t", Jobs: &fakeJobs{}})
	if rec := do(h, http.MethodPost, "/v1/jobs/nonsense"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// fakeOutput is an in-memory OutputStore for handler tests.
type fakeOutput struct {
	data map[string][]byte
	err  error
}

func (f fakeOutput) GetOutput(_ context.Context, runID string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	d, ok := f.data[runID]
	return d, ok, nil
}

func TestRunOutputRoundTripPerJob(t *testing.T) {
	samples := map[string][]byte{}
	add := func(id, jobType string, data any) {
		b, err := MarshalOutput(jobType, data)
		if err != nil {
			t.Fatalf("marshal %s: %v", jobType, err)
		}
		samples[id] = b
	}
	add("r-prospects", "prospects", ProspectsResult{Alerts: []ProspectAlertOut{{Name: "A", Team: "BAL", Kind: "called-up", Priority: "high", Detail: "promoted"}}})
	add("r-waivers", "waivers", WaiversResult{Picks: []WaiverPickOut{{Name: "B", Team: "NYY", Pos: "OF", Signal: "HOT", ProjectedFPG: 4.1, Rank: 1}}, Total: 1})
	add("r-claims", "claims", ClaimsResult{Claims: []ClaimOut{{Team: "T", ClaimType: "FA", Added: "C", NetValue: 3}}})
	add("r-transactions", "transactions", TransactionsResult{Trades: []TradeOut{{Teams: []string{"a", "b"}, ProcessedAt: "2026-06-20T00:00:00Z"}}})
	add("r-gs-check", "gs-check", GSCheckResult{Violations: []GSViolationOut{{Team: "X", Kind: "over", Used: 6, Limit: 5, OverBy: 1}}})
	add("r-backtest", "backtest", BacktestResult{Start: "2026-06-08", End: "2026-06-14", Days: []BacktestDayOut{{Date: "2026-06-08", Actual: 40, Optimal: 42, Gap: -2}}})
	add("r-grade", "grade", GradeResult{Dates: []string{"2026-06-19"}, RowsWritten: 12})

	h := Handler(Config{Token: "t", Output: fakeOutput{data: samples}})
	for id, want := range samples {
		rec := do(h, http.MethodGet, "/v1/runs/"+id+"/output")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", id, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s: content-type = %q", id, ct)
		}
		if rec.Body.String() != string(want) {
			t.Fatalf("%s: body mismatch", id)
		}
	}
}

func TestRunOutputNotFound(t *testing.T) {
	h := Handler(Config{Token: "t", Output: fakeOutput{data: map[string][]byte{}}})
	if rec := do(h, http.MethodGet, "/v1/runs/missing/output"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRunOutputNotImplementedWhenNil(t *testing.T) {
	h := Handler(Config{Token: "t"}) // no Output store wired
	if rec := do(h, http.MethodGet, "/v1/runs/x/output"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

type fakeNotifs struct{ notifs []Notification }

func (f fakeNotifs) List(_ context.Context, limit int) ([]Notification, error) {
	if len(f.notifs) > limit {
		return f.notifs[:limit], nil
	}
	return f.notifs, nil
}

func TestNotificationsList(t *testing.T) {
	h := Handler(Config{Token: "t", Notifications: fakeNotifs{notifs: []Notification{
		{ID: "1", Kind: "lineup", Title: "Lineup applied", Message: "2 changes", CreatedAt: "2026-06-17T21:00:41Z", RunID: "abc"},
	}}})
	rec := do(h, http.MethodGet, "/v1/notifications")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got NotificationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Notifications) != 1 || got.Notifications[0].Kind != "lineup" {
		t.Fatalf("unexpected notifications: %+v", got.Notifications)
	}
}

func TestKindFromTitle(t *testing.T) {
	cases := map[string]string{
		"Fantrax Lineup":             "lineup",
		"Waiver Claims":              "claims",
		"Trade Alert":                "transactions",
		"RosterBot: Prospect Alerts": "prospects",
		"Fantrax GS Alert":           "gs-check",
		"Something else":             "alert",
	}
	for title, want := range cases {
		if got := KindFromTitle(title); got != want {
			t.Errorf("KindFromTitle(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestRunsNotImplementedWhenNil(t *testing.T) {
	h := Handler(Config{Token: "t"}) // no Runs/Jobs wired (local serve)
	if rec := do(h, http.MethodGet, "/v1/runs"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/v1/jobs/optimize"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestHandleRunProgress(t *testing.T) {
	dir := t.TempDir()
	ps := NewFileProgressStore(dir)
	_ = ps.PutProgress(context.Background(), "run123", []byte(`{"phase":"Roster","pct":10,"status":"running"}`))

	h := Handler(Config{Token: "t", Progress: ps})

	// present
	rec := do(h, http.MethodGet, "/v1/runs/run123/progress")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"phase":"Roster"`) {
		t.Fatalf("present: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("present: content-type = %q, want application/json", ct)
	}
	// missing => 404
	rec = do(h, http.MethodGet, "/v1/runs/nope/progress")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing: code=%d", rec.Code)
	}
	// not configured => 501
	h2 := Handler(Config{Token: "t"})
	rec = do(h2, http.MethodGet, "/v1/runs/x/progress")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("nil store: code=%d", rec.Code)
	}
}

// --- inFlightRun (rosterbot-744: manual-trigger race guard) ---

func TestInFlightRun_MatchesRunningSameJobName(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	runs := fakeRuns{runs: []Run{
		{ID: "r1", Command: "claims --dry-run", Status: "RUNNING", StartedAt: now.Add(-5 * time.Minute).Format(time.RFC3339)},
	}}
	got := inFlightRun(context.Background(), runs, "claims", now)
	if got == nil || got.ID != "r1" {
		t.Fatalf("expected in-flight run r1, got %+v", got)
	}
}

func TestInFlightRun_IgnoresDifferentJobName(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	runs := fakeRuns{runs: []Run{
		{ID: "r1", Command: "waivers --top 15", Status: "RUNNING", StartedAt: now.Add(-1 * time.Minute).Format(time.RFC3339)},
	}}
	if got := inFlightRun(context.Background(), runs, "claims", now); got != nil {
		t.Errorf("expected nil for different job name, got %+v", got)
	}
}

func TestInFlightRun_IgnoresFinishedRun(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	runs := fakeRuns{runs: []Run{
		{ID: "r1", Command: "claims --dry-run", Status: "SUCCESS", StartedAt: now.Add(-1 * time.Minute).Format(time.RFC3339)},
	}}
	if got := inFlightRun(context.Background(), runs, "claims", now); got != nil {
		t.Errorf("expected nil for a finished run, got %+v", got)
	}
}

// TestInFlightRun_IgnoresStaleRunningEntry is the regression test for the
// crash-recovery edge case: entrypoint.sh only flips a run to SUCCESS/FAILED
// after the bot command exits, so a hard crash mid-run leaves the ledger
// entry RUNNING forever. Without a staleness bound, one crashed task would
// permanently block manual triggers for that job name.
func TestInFlightRun_IgnoresStaleRunningEntry(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	runs := fakeRuns{runs: []Run{
		{ID: "stuck", Command: "claims --dry-run", Status: "RUNNING", StartedAt: now.Add(-3 * time.Hour).Format(time.RFC3339)},
	}}
	if got := inFlightRun(context.Background(), runs, "claims", now); got != nil {
		t.Errorf("expected nil for a stale RUNNING entry beyond maxJobDuration, got %+v", got)
	}
}

func TestInFlightRun_NilRunStoreSkipsCheck(t *testing.T) {
	if got := inFlightRun(context.Background(), nil, "claims", time.Now()); got != nil {
		t.Errorf("expected nil with no RunStore configured, got %+v", got)
	}
}

func TestInFlightRun_ListErrorFailsOpen(t *testing.T) {
	runs := fakeRuns{err: errFakeList}
	if got := inFlightRun(context.Background(), runs, "claims", time.Now()); got != nil {
		t.Errorf("expected nil (fail-open) on a RunStore list error, got %+v", got)
	}
}

// --- POST /v1/jobs/{name} in-flight rejection (rosterbot-744) ---

func TestJobTrigger_RejectsWhenAlreadyRunning(t *testing.T) {
	now := time.Now().UTC()
	runs := fakeRuns{runs: []Run{
		{ID: "sched-1", Command: "claims", Status: "RUNNING", StartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339), Trigger: "schedule"},
	}}
	jobs := &fakeJobs{id: "should-not-launch"}
	h := Handler(Config{Token: "t", Runs: runs, Jobs: jobs})

	rec := do(h, http.MethodPost, "/v1/jobs/claims")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if jobs.lastCommand != nil {
		t.Errorf("expected job runner NOT to be invoked while a run is in-flight, got command %v", jobs.lastCommand)
	}
}

func TestJobTrigger_AllowsWhenPriorRunFinished(t *testing.T) {
	now := time.Now().UTC()
	runs := fakeRuns{runs: []Run{
		{ID: "sched-1", Command: "claims", Status: "SUCCESS", StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)},
	}}
	jobs := &fakeJobs{id: "task-456"}
	h := Handler(Config{Token: "t", Runs: runs, Jobs: jobs})

	rec := do(h, http.MethodPost, "/v1/jobs/claims")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if strings.Join(jobs.lastCommand, " ") != "claims" {
		t.Errorf("expected job runner invoked with 'claims', got %v", jobs.lastCommand)
	}
}
