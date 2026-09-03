package lineupapi

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

// TenantRuns is the bounded run-ledger summary on one tenant's row.
//
// LastFailure is a SEPARATE field from Last, not a status on it, and that is
// the whole point: a single "last run" cell reads SUCCESS while the hourly
// optimize is fine and a daily grade fails every day — the same (command,
// user_id) blindness internal/opsalert keys its own decisions on to avoid.
// LastFailure is still just the single NEWEST failure across every command,
// though — a transient hourly blip can sit newer than, and so mask, an older
// but STILL-BROKEN weekly job. Commands below is what rosterbot-91c4 adds to
// fix that: every distinct command gets its own newest record and newest
// failure, so a still-failing Backtest cannot hide behind a Lineup run that
// has since recovered.
//
// Window is the number of records ACTUALLY read, so "no failures" is always a
// bounded claim ("none in the last W runs") and never a statement about the
// tenant's whole history. Since dates that claim: it is the oldest record in
// the window, because a record count is not a time span and the operator has to
// know which one. Window == 0 means the ledger read fine and holds nothing — a
// tenant that has never run. A NIL *TenantRuns means the ledger could not be
// read at all, which must render as unknown, exactly like Passkeys.
type TenantRuns struct {
	Last        *Run `json:"last,omitempty"`
	LastFailure *Run `json:"last_failure,omitempty"`

	// Failures counts the records in the window this page treats as failed,
	// which is NOT simply Status == "FAILED". A connect task that fails for a
	// reason only the tenant can fix exits 0 on purpose so opsalert does not
	// page the operator, so its ledger row reads SUCCESS while the connection
	// is broken (rosterbot-jg92). Counting only the exit status would put a
	// green "OK" on the operator's triage page for exactly that tenant — a
	// second surface with the same wrong green jg92 was filed against. See
	// runIsFailure.
	Failures int `json:"failures"`

	Window int `json:"window"`

	// Since is the oldest record's started_at (RFC3339), empty when Window is
	// 0. Without it "0 of last W failed" is uninterpretable — see
	// tenantRunWindow/tenantRunScanCap for how wide a claim W can be.
	Since string `json:"since,omitempty"`

	// Commands is the per-distinct-command breakdown over the SAME scanned
	// span Window/Since describe (never a wider claim than the row as a
	// whole). Order is alphabetical by Command for a stable wire shape.
	// Absent (nil) only when Window is 0 — an empty ledger has no commands to
	// report, same rule as Last/LastFailure being absent there.
	Commands []CommandRun `json:"commands,omitempty"`
}

// CommandRun is one distinct base command's own newest record and newest
// failure, scoped to the same window TenantRuns.Window/Since describe for the
// row as a whole — "no failure for this command in the window" is bounded the
// same way "no failure" is for the whole row, never a claim about history
// before Since.
//
// Command is the BASE token (e.g. "optimize" for
// "optimize --matchup --archive-projections"), the same extraction
// inFlightRun uses in jobs.go to match a RUNNING record to a job name while
// ignoring flags — grouping on the full command string would fragment one
// job into many rows the moment a manual trigger added a flag the scheduled
// run doesn't carry.
type CommandRun struct {
	Command     string `json:"command"`
	Last        *Run   `json:"last,omitempty"`
	LastFailure *Run   `json:"last_failure,omitempty"`
}

// tenantRunWindow is the size of the FIRST, cheap listing runSummary reads for
// a tenant. It is a COST BUDGET, deliberately not tied to defaultRunsLimit,
// which is a page size answering a different question.
//
// Cost per tenant is exactly one ListObjectsV2 plus at most this many
// GetObject calls: s3lineup's flatKeys stops the walk once the limit is
// collected and readNewest fetches only the keys it kept. That one-page bound
// holds because the run-ledger prefix is FLAT — s3lineup writes one
// <key>.json per run and per-run output lives under a separate artifact
// (layout.RunOutput) — and flatKeys skips sub-objects without counting them,
// so a future sub-object under runledger/ would silently unbound the walk.
// It must never reach RunStore.Get, which scans RunLookback records.
//
// THIS ALONE USED TO BE THE WHOLE WINDOW, AND THAT WAS THE BUG (rosterbot-91c4).
// Counted from infra/infra.go's schedule table and perTenantJobs set: Lineup
// is cron(0 14-23,0-3 * * ? *) = 14 runs a day, plus Grade, Shadow, Prospects
// and ProjectionReports once each = 18 records a day for a member, and more
// for the operator, whose prefix also receives every league-wide singleton.
// 25 records is about 1.4 days for a member (25/18), so the WEEKLY Backtest
// job (cron(0 12 ? * MON *)) used to be pushed out of a single-read window by
// the 24 records that follow it — about 32 hours (24/18 days) after it runs,
// i.e. by Tuesday evening — and reported "0 of last 25 failed" for the rest of
// the week rather than the still-broken FAILED it should have shown.
//
// runSummary now reads this cheap window FIRST and escalates to
// tenantRunScanCap only when it comes back full AND tenantKnownCommands isn't
// fully covered yet — see both. A tenant whose commands are all already
// represented (the common case, once this week's Backtest has run) never
// pays for the wider read, so this stays the ONLY listing for most requests;
// widening it further unconditionally is the cost tradeoff tenantRunScanCap's
// doc explains rather than takes for granted.
const tenantRunWindow = 25

// tenantRunScanCap is the size of the SECOND, escalated listing runSummary
// reads for a tenant whose first tenantRunWindow records did not carry a
// fresh record for every name in tenantKnownCommands — almost always because
// the weekly Backtest run sits further back than the daily/hourly noise in
// front of it (see tenantRunWindow). 250 is comfortably more than a week even
// at the OPERATOR's higher combined cadence (per-tenant jobs plus every
// league-wide singleton, easily 25-30 records a day), so the escalated read
// is sized to the tenant who actually needs the width, not the member whose
// six commands usually show up inside the cheap read alone.
//
// THIS IS PAID ONLY BY THE TENANT THAT NEEDS IT, and paid AT MOST ONCE:
// runSummary escalates exactly one time, never loops, so the worst case per
// tenant is tenantRunWindow + tenantRunScanCap GetObject calls (275), not an
// unbounded climb. tenantRunBudget's context deadline still bounds the wall
// clock either way — a slow escalated read degrades that row to unknown
// (RunsBudgetExpired) rather than costing the page, the same as every other
// failure in this file.
const tenantRunScanCap = 250

// tenantKnownCommands names the base commands infra/infra.go's perTenantJobs
// set schedules once per tenant (Lineup→optimize, Grade→grade,
// Backtest→backtest, Shadow→shadow, Prospects→prospects,
// ProjectionReports→projection-site). infra/ is a separate Go module (its own
// go.mod) this package cannot import, so this is a deliberate, documented
// duplication — the same shape as internal/backtest.standardRotationSize
// citing internal/lineuprun.RotationSize — except nothing here can cross-check
// it automatically, since the two sides live in modules that cannot import
// each other.
//
// IT IS AN EARLY-EXIT HINT, NEVER A CORRECTNESS BOUND. runSummary keeps
// whatever it actually read either way, and a command absent from this set
// (a manual trigger, an operator-only league-wide singleton) still gets its
// own Commands entry — this set only decides whether the cheap tenantRunWindow
// read is enough or whether the tenant pays for the wider tenantRunScanCap
// read. The two ways it can drift from perTenantJobs fail in opposite
// directions, and neither is a wrong answer. A job ADDED to perTenantJobs
// without an entry here is simply not waited for: the cheap read can look
// complete while that job's weekly record sits further back, and its Commands
// entry appears only once it falls inside whichever read ran — the pre-91c4
// blindness, confined to the one unlisted job. A name KEPT here after its job
// is removed from perTenantJobs can never be covered, so every tenant pays the
// escalated read on every page load — a cost, visibly, never a wrong answer.
// The operator escalates more often than a member for the ordinary reason:
// their prefix also receives every league-wide singleton, so the weekly
// Backtest falls out of the cheap 25 sooner — expected, and accepted, per
// tenantRunScanCap's sizing.
var tenantKnownCommands = map[string]bool{
	"optimize": true, "grade": true, "backtest": true,
	"shadow": true, "prospects": true, "projection-site": true,
}

// tenantRunConcurrency bounds the fan-out. Same shape as opsnotify's ledger
// reader: a semaphore, not errgroup, because every failure here is soft.
const tenantRunConcurrency = 8

// tenantRunBudget caps the whole enrichment inside the request. This page
// carries park, delete and the recovery-link control — the controls an operator
// needs DURING an incident — so a slow ledger must cost the run column (rows
// read "unknown") and never the page.
//
// The cost it bounds is larger than the listing itself on a cold start:
// resolving a tenant's ledger builds that tenant's whole TenantView, which is
// nine S3-backed stores whose constructors each resolve AWS credentials
// (lambda/tenantstores.go). Those are cached per uid for the life of the Lambda
// instance, so it is the FIRST admin page load after a cold start that pays
// 9xN credential resolutions, eight tenants at a time, and every later load
// pays one listing per tenant. 4s of a 10s function timeout leaves the identity
// reads above and the response itself their own headroom; if a cohort ever
// outgrows it, the budget expiring is visible on the page (RunsBudgetExpired)
// rather than silent.
const tenantRunBudget = 4 * time.Second

// TenantSummary is one tenant's row on the admin Tenants tab.
//
// It answers "what is failing, for whom" across N tenants without reading the
// ledger by hand, which is the question a pilot with other people's teams
// generates constantly.
type TenantSummary struct {
	ID          UserID     `json:"id"`
	DisplayName string     `json:"display_name,omitempty"`
	Email       string     `json:"email,omitempty"`
	Role        Role       `json:"role"`
	Status      UserStatus `json:"status"`

	// AutoApply is on the row because it is the difference between a bot that
	// proposes and one that writes to somebody's team. An operator scanning
	// this page is entitled to see, at a glance, whose rosters this deployment
	// is actually touching.
	AutoApply bool `json:"auto_apply"`

	TeamID string `json:"team_id,omitempty"`

	// Passkeys counts this tenant's registered credentials. Zero is the signal
	// the operator scans for — invited but never registered. nil means the
	// credential store could not answer, which must render as unknown rather
	// than as zero: a masked error that prints 0 would tell the operator to
	// re-invite someone whose account is fine.
	Passkeys *int `json:"passkeys,omitempty"`

	// Runs is the bounded run-ledger summary. NIL means the ledger could not be
	// resolved or read, and follows Passkeys' rule exactly: unknown is not
	// zero. A nil summary rendered as "never run" would send the operator to
	// re-invite somebody whose jobs are running fine.
	Runs *TenantRuns `json:"runs,omitempty"`

	// ConnStatus is empty when the tenant has never connected Fantrax, which is
	// different from a failed connection and is shown differently.
	ConnStatus ConnStatus `json:"conn_status,omitempty"`
	ConnError  string     `json:"conn_error,omitempty"`

	// LastVerifiedAt is wiretime.Time for the reason spelled out on
	// FantraxStatus.LastVerifiedAt (me.go): the same stored value, fed by the
	// connect task's time.Now(), reaching the same client (rosterbot-4e1j).
	LastVerifiedAt wiretime.Time `json:"last_verified_at,omitempty"`
}

// TenantsResponse is the admin tab's payload.
type TenantsResponse struct {
	Tenants []TenantSummary `json:"tenants"`

	// RunsBudgetExpired reports that the run-summary enrichment ran out of
	// tenantRunBudget, so some rows carry no summary for that reason rather
	// than because their ledger is broken.
	//
	// It exists because the per-row "?" alone cannot tell those two apart, and
	// the page-wide case is the one with a different response: one unknown row
	// means look at that tenant, every row unknown means look at the page.
	// Degrade to noise, never to silence.
	RunsBudgetExpired bool `json:"runs_budget_expired,omitempty"`
}

// handleTenants lists every tenant for an operator.
//
// ADMIN-ONLY, and enforced by adminOnlyRoutes rather than by a check here. That
// list is an ALLOWLIST, so a route absent from it is reachable by every member
// — this endpoint exposes every tenant's email, team and connection state, so
// its entry there is the load-bearing part of this file, not this function.
//
// Each row carries a BOUNDED run summary (tenantRunWindow records, sometimes
// escalated to tenantRunScanCap — see both), resolved through the same
// per-tenant view GET /v1/runs serves, so the operator can see which tenant is
// failing. Until rosterbot-nejq this comment claimed that "an operator who
// needs a tenant's runs has a place to look" — there was no such place, GET
// /v1/runs serves the CALLER's ledger only, and babysitting the pilot cohort
// ran entirely on Pushover.
//
// It is HERE rather than as a ?tenant= param on GET /v1/runs because
// isAdminOnlyPath matches on r.URL.Path (authz.go): a query string reaches no
// gate, so that version would have let any member read any tenant's ledger and
// per-run output while compiling clean and passing every existing authz test.
// This route is already inside the allowlist, so the summary inherits the gate
// as a unit — TestTenants_RunSummaryIsAdminOnly pins that, and
// TestRuns_IgnoresATenantQueryParam pins the refusal at the trap's own address.
//
// The cost the old comment was right to worry about is bounded rather than
// avoided: one listing (occasionally two, see tenantRunScanCap) plus at most
// tenantRunWindow+tenantRunScanCap object reads per tenant, fanned out at
// tenantRunConcurrency under tenantRunBudget, every failure soft.
func (cfg Config) handleTenants(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	// ListUsers, not ListActive: this page is the only reactivate control, so
	// a parked tenant must stay on it. The fan-out's listing rightly differs.
	users, err := cfg.Users.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}

	out := make([]TenantSummary, 0, len(users))
	// stamps[i] is out[i]'s connect verdict, carried out of the connection read
	// below so runSummary can label its rows without a second GetConnection per
	// tenant. Parallel to out by index, and both are built in one pass.
	stamps := make([]*ConnectRun, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		s := TenantSummary{
			ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
			Role: u.Role, Status: u.Status, AutoApply: u.AutoApply, TeamID: u.TeamID,
		}
		// Soft, like the connection read below: a credentials-store hiccup
		// leaves the count nil ("unknown"), never zero — zero is a real value
		// meaning "invited but never registered", and conflating the two would
		// tell the operator to re-invite someone whose account is fine.
		if creds, cerr := cfg.Users.Credentials(r.Context(), u.ID); cerr == nil {
			n := len(creds)
			s.Passkeys = &n
		}
		// Soft: a connection-store hiccup must not blank the whole page. A row
		// with no connection state reads as "unknown", which is honest, where a
		// 502 for the entire list would hide the tenants that are fine.
		var stamp *ConnectRun
		if cfg.Connections != nil {
			if conn, ok, cerr := cfg.Connections.GetConnection(r.Context(), u.ID); cerr == nil && ok {
				s.ConnStatus, s.ConnError = conn.Status, conn.LastError
				s.LastVerifiedAt = wiretime.New(conn.LastVerifiedAt)
				if s.TeamID == "" {
					s.TeamID = conn.TeamID
				}
				stamp = conn.LastConnectRun
			}
		}
		out = append(out, s)
		stamps = append(stamps, stamp)
	}
	expired := cfg.fillRunSummaries(r.Context(), out, stamps)
	// Stable order so the page does not reshuffle between polls. Safe after the
	// enrichment because each row's summary was resolved from that row's own
	// ID; sort.Slice swaps whole structs, so the pairing cannot come apart.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	writeJSON(w, http.StatusOK, TenantsResponse{Tenants: out, RunsBudgetExpired: expired})
}

// runIsFailure reports whether this page should count a record as a failure.
//
// It is NOT `Status == "FAILED"`. cmd/connect.go exits 0 when a connect fails
// for a reason only the tenant can fix, so opsalert does not page the operator
// for something the operator cannot do anything about — which means the ledger
// row for a broken connection reads SUCCESS (rosterbot-jg92). Reading the exit
// status alone here would paint that tenant green on the one page the operator
// uses to find what is failing, which is the same wrong green jg92 was filed
// against, on a second surface.
//
// A nil Connect is never read as success: it means no verdict could be
// attributed to this row (not a connect run, or no stamp), and the exit status
// then stands on its own.
func runIsFailure(r Run) bool {
	if r.Connect != nil && r.Connect.Verdict == ConnectVerdictFailed {
		return true
	}
	return r.Status == "FAILED"
}

// runSummary reads one tenant's newest ledger records.
//
// It resolves the store through tenantViewOf, so the column shows EXACTLY what
// that tenant's own GET /v1/runs returns — one resolution rule, not a second
// notion of "their runs" that could drift from the first. tenantViewOf refuses
// when no Tenants provider is configured, which is what keeps the flat
// single-tenant shape (`rosterbot serve`) from labelling one process-wide
// ledger as every tenant's history.
//
// It does NOT filter on Run.UserID. Attribution here is by PREFIX
// (layout.RunLedger is PerTenant), and the pre-fan-out records the crq.11
// backfill relocated under the operator's segment legitimately carry an empty
// user_id — empty is an ordinary tenant value, not a wildcard, so filtering
// would silently drop the operator's own history.
//
// stamp is that tenant's own connect verdict, already read by handleTenants.
// applyConnectOutcome matches on the run id, so a stamp for a different run is
// ignored rather than mislabelling one.
//
// THE READ IS ONE LISTING, OR TWO (rosterbot-91c4): tenantRunWindow's cheap
// read is used as-is when it already covers every name in
// tenantKnownCommands, or when it came back short of tenantRunWindow (the
// tenant's whole ledger fit inside it, so there is nothing further back to
// find). Otherwise a SECOND, wider read at tenantRunScanCap replaces it —
// never more than one escalation, so the worst case is bounded, not a loop.
// This is what stops a low-frequency job (the weekly Backtest) from being
// pushed out of view by a high-frequency one (the hourly Lineup optimize)
// while keeping the common, fully-covered case at the original single cheap
// read.
//
// NIL MEANS "COULD NOT READ", never "nothing to report".
func (cfg Config) runSummary(ctx context.Context, uid UserID, stamp *ConnectRun) *TenantRuns {
	view, ok := cfg.tenantViewOf(ctx, uid)
	if !ok || view.Runs == nil {
		return nil
	}
	recs, err := view.Runs.List(ctx, tenantRunWindow)
	if err != nil {
		return nil
	}
	out := summarizeRuns(recs, stamp)
	if len(recs) >= tenantRunWindow && !coversKnownCommands(out.Commands) {
		if wider, werr := view.Runs.List(ctx, tenantRunScanCap); werr == nil {
			out = summarizeRuns(wider, stamp)
		}
		// A failed escalation is not fatal: out already holds the cheap read's
		// summary, which is still a true (if possibly incomplete) bounded
		// claim — degrade to a narrower answer, never to nothing, matching
		// every other soft failure on this page.
	}
	return out
}

// summarizeRuns builds a TenantRuns from one already-fetched, newest-first
// batch of records. The overall Last/LastFailure/Failures/Window/Since are
// unchanged from the pre-rosterbot-91c4 shape; Commands is the per-command
// breakdown that bead adds — see TenantRuns' doc for why the overall
// LastFailure alone cannot be trusted to surface a still-broken low-frequency
// job.
func summarizeRuns(recs []Run, stamp *ConnectRun) *TenantRuns {
	out := &TenantRuns{Window: len(recs)}
	byCommand := map[string]*CommandRun{}
	for i := range recs {
		// Copied before stamping: a RunStore may hand back a slice it retains,
		// and applyConnectOutcome writes to the value it is given.
		r := recs[i]
		applyConnectOutcome(&r, stamp)
		// Newest first (RunKey's inverted-timestamp prefix), so the first
		// record of each kind wins. RUNNING counts as Last — the question "did
		// anything run" is opsalert.Overdue's, and a launched run answers it
		// whatever it later becomes.
		if out.Last == nil {
			out.Last = &r
		}
		out.Since = r.StartedAt
		failed := runIsFailure(r)
		if failed {
			out.Failures++
			if out.LastFailure == nil {
				out.LastFailure = &r
			}
		}

		base := commandBase(r.Command)
		cr, seen := byCommand[base]
		if !seen {
			cr = &CommandRun{Command: base}
			byCommand[base] = cr
		}
		// Same newest-first-wins rule as the overall fields above, scoped to
		// this one command.
		if cr.Last == nil {
			cr.Last = &r
		}
		if failed && cr.LastFailure == nil {
			cr.LastFailure = &r
		}
	}
	if len(byCommand) > 0 {
		out.Commands = make([]CommandRun, 0, len(byCommand))
		for _, cr := range byCommand {
			out.Commands = append(out.Commands, *cr)
		}
		// Alphabetical, not scan order: scan order depends on which command
		// happened to run most recently, which would reshuffle the row on
		// every poll for no reason a reader could see.
		sort.Slice(out.Commands, func(i, j int) bool {
			return out.Commands[i].Command < out.Commands[j].Command
		})
	}
	return out
}

// commandBase returns the CLI subcommand token a ledger record's Command
// string starts with (e.g. "optimize" for
// "optimize --matchup --archive-projections") — the same extraction
// inFlightRun uses in jobs.go to match a RUNNING record to a job name while
// ignoring flags. Grouping on the full command string instead would fragment
// one job into several rows the moment a manual trigger added a flag the
// scheduled run doesn't carry (e.g. "optimize --dates 2026-09-03").
func commandBase(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// coversKnownCommands reports whether commands already carries an entry for
// every name in tenantKnownCommands — see tenantRunScanCap for what this
// gates.
func coversKnownCommands(commands []CommandRun) bool {
	seen := make(map[string]bool, len(commands))
	for _, c := range commands {
		seen[c.Command] = true
	}
	for name := range tenantKnownCommands {
		if !seen[name] {
			return false
		}
	}
	return true
}

// fillRunSummaries populates out[i].Runs concurrently under its own budget, and
// reports whether that budget expired.
//
// Every row failure is soft — a row degrades to an unknown summary, never the
// page — but the budget expiring is reported to the caller AND logged, because
// "this tenant's ledger is broken" and "the page ran out of time" produce the
// same "?" and need different responses.
func (cfg Config) fillRunSummaries(ctx context.Context, out []TenantSummary, stamps []*ConnectRun) bool {
	if len(out) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, tenantRunBudget)
	defer cancel()

	var wg sync.WaitGroup
	sem := make(chan struct{}, tenantRunConcurrency)
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var stamp *ConnectRun
			if i < len(stamps) {
				stamp = stamps[i]
			}
			out[i].Runs = cfg.runSummary(ctx, out[i].ID, stamp)
		}(i)
	}
	wg.Wait()

	if ctx.Err() != nil {
		log.Printf("tenants: run summaries hit the %s budget; some rows report no runs "+
			"for that reason rather than a broken ledger", tenantRunBudget)
		return true
	}
	return false
}
