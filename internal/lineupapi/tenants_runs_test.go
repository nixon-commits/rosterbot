package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// tenantsRunsFixture is tenantsFixture plus a run ledger: the same two users,
// with a caller-supplied TenantStores and ConnectionStore so a test can give
// each tenant a DIFFERENT ledger. A single shared store would answer the same
// thing for both and pass whatever the attribution did — the mistake
// perTenantConns exists to avoid one column over.
func tenantsRunsFixture(t *testing.T, tenants TenantStores, conns ConnectionStore) (http.Handler, []byte) {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	for _, u := range []*User{
		{ID: "admin1", Email: "op@e.test", Role: RoleAdmin, Status: UserActive, AutoApply: true},
		{ID: "bob", Email: "bob@e.test", Role: RoleMember, Status: UserActive},
	} {
		if err := users.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	secret := []byte("s")
	return Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t), Tenants: tenants, Connections: conns}), secret
}

// getTenants drives GET /v1/tenants as sub and decodes the typed body.
func getTenants(t *testing.T, h http.Handler, secret []byte, sub UserID) TenantsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", sub, 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/tenants as %s = %d, want 200: %s", sub, rec.Code, rec.Body)
	}
	var body TenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return body
}

func rowByID(t *testing.T, body TenantsResponse, id UserID) TenantSummary {
	t.Helper()
	for _, s := range body.Tenants {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no row for %s in %+v", id, body.Tenants)
	return TenantSummary{}
}

// TestTenants_RunSummaryIsAdminOnly re-pins the prefix gate now that this route
// carries a SECOND tenant's run history, not only their identity facts.
//
// The two assertions do different work. The 403 is what goes red under the
// mutation; the admin-side substring is what makes this test about the new
// data — without it the test passes identically on a build where the summary
// was never attached, i.e. it would be pinning a gate over nothing.
//
// MUTATION: delete "/v1/tenants" from adminOnlyRoutes (authz.go). bob then
// receives 200 carrying admin1's ledger.
func TestTenants_RunSummaryIsAdminOnly(t *testing.T) {
	h, secret := tenantsRunsFixture(t, runsPerTenant{
		"admin1": {{ID: "r-secret-9", Command: "grade", Status: "FAILED", StartedAt: nowRFC3339()}},
	}, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "bob", 0))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member GET /v1/tenants = %d, want 403 — this route now carries "+
			"every tenant's run ledger as well as their email", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET /v1/tenants = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "r-secret-9") {
		t.Fatalf("admin response carries no run ledger, so the 403 above pins an "+
			"empty gate: %s", rec.Body)
	}
}

// TestTenants_RunSummaryIsPerTenant: each row reports ITS OWN ledger.
//
// MUTATION: resolve the summary through cfg.tenantView(ctx) (the caller's view)
// instead of cfg.tenantViewOf(ctx, out[i].ID) — both rows then report the
// caller's runs, which is the mis-attribution TenantView exists to remove.
func TestTenants_RunSummaryIsPerTenant(t *testing.T) {
	h, secret := tenantsRunsFixture(t, runsPerTenant{
		"admin1": {{ID: "r-admin", Command: "optimize", Status: "SUCCESS", StartedAt: nowRFC3339()}},
		"bob":    {{ID: "r-bob", Command: "grade", Status: "FAILED", StartedAt: nowRFC3339()}},
	}, nil)

	body := getTenants(t, h, secret, "admin1")

	admin := rowByID(t, body, "admin1")
	if admin.Runs == nil || admin.Runs.Last == nil || admin.Runs.Last.ID != "r-admin" {
		t.Errorf("admin1 row runs = %+v, want last run r-admin", admin.Runs)
	}
	bob := rowByID(t, body, "bob")
	if bob.Runs == nil || bob.Runs.Last == nil || bob.Runs.Last.ID != "r-bob" {
		t.Fatalf("bob row runs = %+v, want last run r-bob", bob.Runs)
	}
	if bob.Runs.LastFailure == nil || bob.Runs.LastFailure.Command != "grade" {
		t.Errorf("bob last_failure = %+v, want grade", bob.Runs.LastFailure)
	}
}

// TestTenants_ANewerSuccessDoesNotHideAnOlderFailure is the doctrinal one.
//
// A column that collapsed to the newest record's status would report a tenant
// whose daily grade fails every day as healthy, because the hourly optimize
// runs 14 times a day and is always the newest record. That is the
// (command, user_id) blindness internal/opsalert keys its own decisions on to
// avoid, one level up.
//
// MUTATION: set LastFailure only when recs[0] is FAILED (i.e. collapse the
// column to "last run status").
func TestTenants_ANewerSuccessDoesNotHideAnOlderFailure(t *testing.T) {
	h, secret := tenantsRunsFixture(t, runsPerTenant{
		"bob": {
			{ID: "r3", Command: "optimize", Status: "SUCCESS", StartedAt: "2026-08-30T12:00:00Z"},
			{ID: "r2", Command: "grade", Status: "FAILED", StartedAt: "2026-08-30T11:00:00Z"},
			{ID: "r1", Command: "optimize", Status: "SUCCESS", StartedAt: "2026-08-30T10:00:00Z"},
		},
	}, nil)

	bob := rowByID(t, getTenants(t, h, secret, "admin1"), "bob")
	if bob.Runs == nil {
		t.Fatal("bob has no run summary")
	}
	if bob.Runs.Last == nil || bob.Runs.Last.Command != "optimize" || bob.Runs.Last.Status != "SUCCESS" {
		t.Errorf("last = %+v, want the newest record (optimize/SUCCESS)", bob.Runs.Last)
	}
	if bob.Runs.LastFailure == nil || bob.Runs.LastFailure.Command != "grade" {
		t.Errorf("last_failure = %+v, want the older failing grade — a healthy hourly "+
			"job must not hide a daily one that fails every day", bob.Runs.LastFailure)
	}
	if bob.Runs.Failures != 1 || bob.Runs.Window != 3 {
		t.Errorf("failures/window = %d/%d, want 1/3", bob.Runs.Failures, bob.Runs.Window)
	}
	// Since dates the bounded claim: "1 of last 3" is meaningless without the
	// span those 3 cover.
	if bob.Runs.Since != "2026-08-30T10:00:00Z" {
		t.Errorf("since = %q, want the OLDEST record's started_at", bob.Runs.Since)
	}
}

// TestTenants_RunningCountsAsARunAndIsNotAFailure.
//
// MUTATION A: skip RUNNING records when choosing Last — Last becomes the older
// SUCCESS, and a tenant mid-run reads as though nothing has launched since.
// MUTATION B: count RUNNING toward Failures.
func TestTenants_RunningCountsAsARunAndIsNotAFailure(t *testing.T) {
	h, secret := tenantsRunsFixture(t, runsPerTenant{
		"bob": {
			{ID: "r2", Command: "optimize", Status: "RUNNING", StartedAt: "2026-08-30T12:00:00Z"},
			{ID: "r1", Command: "optimize", Status: "SUCCESS", StartedAt: "2026-08-30T11:00:00Z"},
		},
	}, nil)

	bob := rowByID(t, getTenants(t, h, secret, "admin1"), "bob")
	if bob.Runs == nil || bob.Runs.Last == nil || bob.Runs.Last.Status != "RUNNING" {
		t.Fatalf("last = %+v, want the RUNNING record", bob.Runs)
	}
	if bob.Runs.LastFailure != nil || bob.Runs.Failures != 0 {
		t.Errorf("RUNNING counted as a failure: last_failure=%+v failures=%d",
			bob.Runs.LastFailure, bob.Runs.Failures)
	}
	if bob.Runs.Window != 2 {
		t.Errorf("window = %d, want 2", bob.Runs.Window)
	}
}

// failingRuns is a RunStore whose listing errors.
type failingRuns struct{}

func (failingRuns) List(context.Context, int) ([]Run, error) {
	return nil, errors.New("ledger unavailable")
}
func (failingRuns) Get(context.Context, string) (*RunDetail, bool, error) { return nil, false, nil }

// mixedRuns is a TenantStores handing one tenant a broken ledger.
type mixedRuns map[UserID]RunStore

func (m mixedRuns) For(_ context.Context, uid UserID) (TenantView, error) {
	return TenantView{Runs: m[uid]}, nil
}

// TestTenants_LedgerErrorReadsAsUnknownNotAsZero — absence of evidence is not
// evidence. Decoded as map[string]any on purpose: the property is the WIRE
// ABSENCE of the key, which a typed decode into *TenantRuns cannot distinguish
// from a zero-valued one.
//
// MUTATION: return &TenantRuns{} instead of nil on the List error path. bob's
// row then serialises "runs":{"failures":0,"window":0}, which the UI renders as
// "Never run" — a read failure reported to the operator as a fact about the
// tenant.
func TestTenants_LedgerErrorReadsAsUnknownNotAsZero(t *testing.T) {
	h, secret := tenantsRunsFixture(t, mixedRuns{
		"admin1": staticRuns{{ID: "r-admin", Command: "optimize", Status: "SUCCESS", StartedAt: nowRFC3339()}},
		"bob":    failingRuns{},
	}, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — one broken ledger must not blank the page: %s",
			rec.Code, rec.Body)
	}

	var raw struct {
		Tenants []map[string]any `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	for _, row := range raw.Tenants {
		_, present := row["runs"]
		switch row["id"] {
		case "bob":
			if present {
				t.Errorf("bob's row carries a runs key (%v) despite the ledger read "+
					"failing; unknown must be absent, not zero", row["runs"])
			}
		case "admin1":
			if !present {
				t.Error("admin1's row has no runs key, so the bob assertion above " +
					"would pass on a build that never attached a summary at all")
			}
		}
	}
}

// budgetRuns records what the listing was asked for.
type budgetRuns struct {
	mu     sync.Mutex
	limits []int
	gets   int
}

// List returns BOTH a success and a failure. A success-only window would leave
// the failure branch unexecuted, and the zero-Get assertion below would then
// pass against a build that fetches a log tail for every failing row — which is
// the expensive mistake it exists to catch. Verified by mutation.
func (b *budgetRuns) List(_ context.Context, limit int) ([]Run, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limits = append(b.limits, limit)
	return []Run{
		{ID: "r2", Command: "optimize", Status: "SUCCESS", StartedAt: "2026-08-30T12:00:00Z"},
		{ID: "r1", Command: "grade", Status: "FAILED", StartedAt: "2026-08-30T11:00:00Z"},
	}, nil
}

func (b *budgetRuns) Get(context.Context, string) (*RunDetail, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets++
	return nil, false, nil
}

type sharedRuns struct{ store RunStore }

func (s sharedRuns) For(context.Context, UserID) (TenantView, error) {
	return TenantView{Runs: s.store}, nil
}

// TestTenants_RunSummaryIsBounded pins the two costs this column must not pay,
// for the COMMON case: a tenant whose ledger is small enough that its records
// come back short of tenantRunWindow, so runSummary has nothing further back
// to look for and never escalates. TestTenants_RunSummaryEscalationIsBounded
// below covers the OTHER case — a full first read that still doesn't cover
// tenantKnownCommands — and pins that escalation is capped at exactly one
// extra call, never a loop.
//
// An unbounded List makes s3lineup's flatKeys walk the ENTIRE runledger prefix
// instead of stopping at the window, and any call to RunStore.Get is a
// RunLookback-record scan (200 GetObject) per row inside a 10s Lambda.
//
// MUTATION A: pass 0 as the limit. MUTATION B: call view.Runs.Get to fetch a
// log tail for the failing row.
func TestTenants_RunSummaryIsBounded(t *testing.T) {
	store := &budgetRuns{}
	h, secret := tenantsRunsFixture(t, sharedRuns{store}, nil)

	body := getTenants(t, h, secret, "admin1")
	if len(body.Tenants) != 2 {
		t.Fatalf("got %d rows, want 2", len(body.Tenants))
	}

	if len(store.limits) != 2 {
		t.Errorf("List called %d times for 2 tenants, want exactly one listing each: %v",
			len(store.limits), store.limits)
	}
	for _, l := range store.limits {
		if l != tenantRunWindow {
			t.Errorf("List limit %d, want tenantRunWindow (%d) — 0 or negative makes "+
				"flatKeys walk the whole prefix", l, tenantRunWindow)
		}
	}
	if tenantRunWindow <= 0 {
		t.Fatalf("tenantRunWindow = %d; the assertion above is vacuous unless it is "+
			"a real positive bound", tenantRunWindow)
	}
	if store.gets != 0 {
		t.Errorf("RunStore.Get called %d times; it scans RunLookback (%d) records per "+
			"call, which this listing must never pay per row", store.gets, RunLookback)
	}
}

// TestTenants_NoLedgerConfiguredLeavesRunsUnknown covers the shape `rosterbot
// serve` actually runs in: NO Tenants provider AND a real, non-nil Runs store
// (cmd/serve.go sets lineupapi.NewFileRunStore and never sets Tenants).
//
// This is the fixture that matters. A fixture with no run store at all would
// yield an absent key from `view.Runs == nil` and pass identically whether or
// not the tenancy decision is correct — a test that cannot fail for the reason
// it was written.
//
// MUTATION: give tenantViewOf back the flat-Config branch tenantView has
// (return the flat fields when cfg.Tenants == nil). Every row then reports
// r-flat — one process-wide ledger labelled as N different tenants' history,
// which is the mis-attribution TenantView was created to remove.
func TestTenants_NoLedgerConfiguredLeavesRunsUnknown(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	for _, u := range []*User{
		{ID: "admin1", Email: "op@e.test", Role: RoleAdmin, Status: UserActive},
		{ID: "bob", Email: "bob@e.test", Role: RoleMember, Status: UserActive},
	} {
		if err := users.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t),
		// The serve shape: a real ledger, no tenancy.
		Runs: staticRuns{{ID: "r-flat", Command: "optimize", Status: "SUCCESS", StartedAt: nowRFC3339()}},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "r-flat") {
		t.Fatalf("the flat single-tenant ledger was attributed to a tenant row: %s",
			rec.Body)
	}

	var raw struct {
		Tenants []map[string]any `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Tenants) != 2 {
		t.Fatalf("got %d rows, want 2", len(raw.Tenants))
	}
	for _, row := range raw.Tenants {
		if _, present := row["runs"]; present {
			t.Errorf("row %v carries a runs summary with no tenancy configured; the "+
				"flat store belongs to whoever the process booted as, not to this row",
				row["id"])
		}
	}
}

// TestTenants_AFailedConnectIsNotOK — the jg92 half.
//
// cmd/connect.go exits 0 when a connect fails for a reason only the tenant can
// fix, so the ledger row for a broken connection reads SUCCESS. A summary that
// counted only the exit status would paint that tenant green on the one page
// the operator uses to find what is failing — the same wrong green rosterbot-jg92
// was filed against, on a second surface.
//
// MUTATION: make runIsFailure `return r.Status == "FAILED"`.
func TestTenants_AFailedConnectIsNotOK(t *testing.T) {
	h, secret := tenantsRunsFixture(t,
		runsPerTenant{"bob": {{
			ID: "r-connect", Command: "connect", Status: "SUCCESS",
			StartedAt: "2026-08-30T12:00:00Z",
		}}},
		perTenantConns{conns: map[UserID]*FantraxConnection{
			"bob": {UserID: "bob", Status: ConnVerified, LastConnectRun: &ConnectRun{
				RunID: "r-connect", Verdict: ConnectVerdictFailed, LastError: "bot_challenge",
			}},
		}},
	)

	bob := rowByID(t, getTenants(t, h, secret, "admin1"), "bob")
	if bob.Runs == nil {
		t.Fatal("bob has no run summary")
	}
	if bob.Runs.Failures != 1 || bob.Runs.LastFailure == nil {
		t.Fatalf("a connect run whose own verdict is 'failed' was counted as OK: %+v",
			bob.Runs)
	}
	if bob.Runs.LastFailure.Connect == nil ||
		bob.Runs.LastFailure.Connect.Verdict != ConnectVerdictFailed {
		t.Errorf("last_failure carries no connect verdict (%+v); the renderer needs it "+
			"to label the row as a failed connection rather than a failed job",
			bob.Runs.LastFailure)
	}
}

// TestTenants_ConnectVerdictIsTheRowTenantsNot the caller's.
//
// The stamp is attributed by run id, and each row's stamp comes from that row's
// own connection record. Reusing lastConnectRun (which reads
// CallerFrom(ctx).UserID) would have put the OPERATOR's verdict on every
// tester's row — the mis-attribution connectrun.go's doc comment warns whoever
// adds a cross-tenant runs view about.
//
// MUTATION: pass the caller's stamp to every row (e.g. read stamps[0] for all
// i, or resolve via cfg.lastConnectRun) — bob's healthy connect run then
// inherits admin1's failed verdict.
func TestTenants_ConnectVerdictIsTheRowTenantsNotTheCallers(t *testing.T) {
	h, secret := tenantsRunsFixture(t,
		runsPerTenant{
			"admin1": {{ID: "r-op", Command: "connect", Status: "SUCCESS", StartedAt: "2026-08-30T12:00:00Z"}},
			"bob":    {{ID: "r-bob", Command: "connect", Status: "SUCCESS", StartedAt: "2026-08-30T12:00:00Z"}},
		},
		perTenantConns{conns: map[UserID]*FantraxConnection{
			"admin1": {UserID: "admin1", LastConnectRun: &ConnectRun{
				RunID: "r-op", Verdict: ConnectVerdictFailed, LastError: "bot_challenge"}},
			"bob": {UserID: "bob", LastConnectRun: &ConnectRun{
				RunID: "r-bob", Verdict: ConnectVerdictVerified}},
		}},
	)

	body := getTenants(t, h, secret, "admin1")

	op := rowByID(t, body, "admin1")
	if op.Runs == nil || op.Runs.Failures != 1 {
		t.Errorf("admin1 (the caller, whose own connect failed) = %+v, want 1 failure", op.Runs)
	}
	bob := rowByID(t, body, "bob")
	if bob.Runs == nil {
		t.Fatal("bob has no run summary")
	}
	if bob.Runs.Failures != 0 || bob.Runs.LastFailure != nil {
		t.Errorf("bob's verified connect inherited the caller's failed verdict: %+v", bob.Runs)
	}
	if bob.Runs.Last == nil || bob.Runs.Last.Connect == nil ||
		bob.Runs.Last.Connect.Verdict != ConnectVerdictVerified {
		t.Errorf("bob's row carries no verdict of its own (%+v), so the assertion above "+
			"would pass on a build that stamped nothing at all", bob.Runs.Last)
	}
}

// limitedRuns is a RunStore whose List genuinely ENFORCES limit — returning
// the newest `limit` of its backing records — unlike staticRuns/budgetRuns
// above, which return everything (or a fixed set) regardless of what was
// asked. A fake that never truncates cannot reproduce the window bug this
// file exists to fix: `all` must already be supplied newest-first, matching
// what a real store's RunKey ordering guarantees.
type limitedRuns struct {
	mu  sync.Mutex
	all []Run
}

func (r *limitedRuns) List(_ context.Context, limit int) ([]Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit >= len(r.all) {
		return append([]Run(nil), r.all...), nil
	}
	return append([]Run(nil), r.all[:limit]...), nil
}

func (r *limitedRuns) Get(context.Context, string) (*RunDetail, bool, error) {
	return nil, false, nil
}

// commandEntry finds one command's entry in a TenantRuns.Commands breakdown,
// or nil if that command never appeared in the scanned window.
func commandEntry(commands []CommandRun, name string) *CommandRun {
	for i := range commands {
		if commands[i].Command == name {
			return &commands[i]
		}
	}
	return nil
}

// TestTenants_WeeklyFailureSurvivesHourlyNoise is rosterbot-91c4 itself: a
// single tenantRunWindow-sized read must not let hourly Lineup noise push a
// WEEKLY Backtest failure out of view for most of the week.
//
// Uses limitedRuns rather than runsPerTenant/staticRuns specifically because
// those ignore the requested limit — against them this test would pass on
// the unfixed code too, pinning nothing.
//
// MUTATION: revert runSummary to the single, non-escalating
// `summarizeRuns(recs)` where recs = view.Runs.List(ctx, tenantRunWindow) with
// no follow-up read. The 30 newer "optimize" records fill the whole window
// and the older "backtest" FAILED record is never read at all.
func TestTenants_WeeklyFailureSurvivesHourlyNoise(t *testing.T) {
	newest := time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC) // a Monday evening
	var all []Run
	// 30 hourly Lineup records — comfortably more than tenantRunWindow (25) —
	// newest first, one per hour back from "now".
	for i := 0; i < 30; i++ {
		at := newest.Add(-time.Duration(i) * time.Hour)
		all = append(all, Run{
			ID:        fmt.Sprintf("lineup-%02d", i),
			Command:   "optimize --matchup --archive-projections",
			Status:    "SUCCESS",
			StartedAt: at.Format(time.RFC3339),
		})
	}
	// The weekly Backtest, older than every one of the 30 above, and FAILED.
	all = append(all, Run{
		ID:        "backtest-1",
		Command:   "backtest",
		Status:    "FAILED",
		StartedAt: newest.Add(-8 * 24 * time.Hour).Format(time.RFC3339),
	})

	h, secret := tenantsRunsFixture(t, mixedRuns{"bob": &limitedRuns{all: all}}, nil)

	bob := rowByID(t, getTenants(t, h, secret, "admin1"), "bob")
	if bob.Runs == nil {
		t.Fatal("bob has no run summary")
	}
	if bob.Runs.LastFailure == nil || bob.Runs.LastFailure.ID != "backtest-1" {
		t.Fatalf("the weekly Backtest failure was pushed out of view by 30 newer "+
			"hourly optimize records: runs=%+v", bob.Runs)
	}
}

// TestTenants_CommandsBreakdownKeepsEachCommandsOwnFailureVisible is the
// masking half of rosterbot-91c4: the row-level LastFailure is, by design,
// just the SINGLE newest failure across every command — so a newer, healthy
// job's failure hides an older, STILL-BROKEN one there. Commands must not.
//
// MUTATION: collapse summarizeRuns' per-command tracking so cr.LastFailure is
// only ever set from the SAME record that set out.LastFailure (i.e. share one
// pointer across commands) — backtest's own entry would then read nil.
func TestTenants_CommandsBreakdownKeepsEachCommandsOwnFailureVisible(t *testing.T) {
	h, secret := tenantsRunsFixture(t, runsPerTenant{
		"bob": {
			{ID: "r-lineup-fail", Command: "optimize --matchup --archive-projections",
				Status: "FAILED", StartedAt: "2026-08-31T23:00:00Z"},
			{ID: "r-backtest-fail", Command: "backtest",
				Status: "FAILED", StartedAt: "2026-08-24T12:00:00Z"},
		},
	}, nil)

	bob := rowByID(t, getTenants(t, h, secret, "admin1"), "bob")
	if bob.Runs == nil {
		t.Fatal("bob has no run summary")
	}
	// Row-level LastFailure is unchanged from before rosterbot-91c4: the
	// single newest failure, here optimize.
	if bob.Runs.LastFailure == nil || bob.Runs.LastFailure.ID != "r-lineup-fail" {
		t.Fatalf("row-level last_failure = %+v, want the newest failure (optimize)",
			bob.Runs.LastFailure)
	}
	bt := commandEntry(bob.Runs.Commands, "backtest")
	if bt == nil || bt.LastFailure == nil || bt.LastFailure.ID != "r-backtest-fail" {
		t.Fatalf("commands = %+v, want a backtest entry whose OWN last_failure is "+
			"r-backtest-fail, not masked by the newer optimize failure", bob.Runs.Commands)
	}
	opt := commandEntry(bob.Runs.Commands, "optimize")
	if opt == nil || opt.LastFailure == nil || opt.LastFailure.ID != "r-lineup-fail" {
		t.Fatalf("commands = %+v, want an optimize entry whose own last_failure is "+
			"r-lineup-fail", bob.Runs.Commands)
	}
}

// escalatingRuns simulates a ledger with AT LEAST as many records as asked
// for, every one of them "optimize" — so coversKnownCommands can never be
// satisfied (backtest/grade/shadow/prospects/projection-site never appear)
// and runSummary MUST escalate, exactly once, never more.
type escalatingRuns struct {
	mu     sync.Mutex
	limits []int
	gets   int
}

func (r *escalatingRuns) List(_ context.Context, limit int) ([]Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limits = append(r.limits, limit)
	out := make([]Run, limit)
	for i := range out {
		out[i] = Run{
			ID: fmt.Sprintf("r%d-%d", limit, i), Command: "optimize",
			Status: "SUCCESS", StartedAt: nowRFC3339(),
		}
	}
	return out, nil
}

func (r *escalatingRuns) Get(context.Context, string) (*RunDetail, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gets++
	return nil, false, nil
}

// TestTenants_RunSummaryEscalationIsBounded is the escalation-path sibling of
// TestTenants_RunSummaryIsBounded: a tenant whose known command set is NEVER
// satisfied must still cost exactly two List calls — tenantRunWindow then
// tenantRunScanCap — never an unbounded climb, and never a RunStore.Get.
//
// MUTATION A: drop the `len(recs) >= tenantRunWindow` / coverage guard so
// runSummary always escalates even when nothing more could be found — this
// test's fixture would still only see 2 calls (it always returns `limit`
// records, so the guard's other half never trips it), so a stronger canary
// for that half lives in TestTenants_RunSummaryIsBounded's fixture ending
// short of the window. MUTATION B: loop the escalation (e.g. retry at an ever
// -larger limit until coverage or a huge cap) — store.limits grows past 2.
func TestTenants_RunSummaryEscalationIsBounded(t *testing.T) {
	store := &escalatingRuns{}
	h, secret := tenantsRunsFixture(t, mixedRuns{"bob": store}, nil)

	body := getTenants(t, h, secret, "admin1")
	bob := rowByID(t, body, "bob")
	if bob.Runs == nil {
		t.Fatal("bob has no run summary")
	}

	if len(store.limits) != 2 {
		t.Fatalf("List called %d times, want exactly 2 (the cheap read, then one "+
			"escalation): %v", len(store.limits), store.limits)
	}
	if store.limits[0] != tenantRunWindow || store.limits[1] != tenantRunScanCap {
		t.Errorf("List limits = %v, want [%d %d]", store.limits, tenantRunWindow, tenantRunScanCap)
	}
	if store.gets != 0 {
		t.Errorf("RunStore.Get called %d times; escalation must stay inside List, "+
			"never reach the RunLookback-record Get scan", store.gets)
	}
}

// TestCoversKnownCommands_IsByName pins that the escalation gate checks NAMES,
// not a count: six distinct commands that are not the known six must not read
// as covered, and the known six minus any one must not either. A count-based
// check would let an operator ledger full of league-wide singletons look
// complete while the weekly Backtest was still out of view — the exact
// blindness the escalation exists to remove.
func TestCoversKnownCommands_IsByName(t *testing.T) {
	entries := func(names ...string) []CommandRun {
		out := make([]CommandRun, 0, len(names))
		for _, n := range names {
			out = append(out, CommandRun{Command: n})
		}
		return out
	}
	known := make([]string, 0, len(tenantKnownCommands))
	for name := range tenantKnownCommands {
		known = append(known, name)
	}
	if !coversKnownCommands(entries(known...)) {
		t.Fatal("every known command present must read as covered")
	}
	if !coversKnownCommands(entries(append([]string{"gs-check", "waivers"}, known...)...)) {
		t.Fatal("extra, unknown commands must not un-cover a complete set")
	}
	if coversKnownCommands(entries("a", "b", "c", "d", "e", "f")) {
		t.Fatal("six unrelated commands read as covered: the check is counting, not naming")
	}
	for i := range known {
		missingOne := append(append([]string{}, known[:i]...), known[i+1:]...)
		if coversKnownCommands(entries(missingOne...)) {
			t.Errorf("covered without %q: a still-unseen weekly job would never trigger the wider read", known[i])
		}
	}
	if coversKnownCommands(nil) {
		t.Fatal("an empty breakdown must not read as covered")
	}
}
