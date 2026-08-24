package lineupapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// TodayKey is the object key (storage-adapter relative) for the most recent
// APPLIED lineup — what is actually set on Fantrax. Producers also publish
// under the date string; the handler serves "today" and, separately, "preview".
const TodayKey = "today"

// PreviewKey is the object key for the most recent lineup a DRY RUN computed
// but did not apply. It exists because a dry run must not overwrite TodayKey:
// doing so would make GET /v1/lineup/today describe a roster that was never
// set, and would let one member ticking "Dry run" in the app's Functions form
// replace their real applied-lineup snapshot with a hypothetical one.
//
// Splitting the key rather than adding a flag to one key keeps GET
// /v1/lineup/today's meaning exactly as it was, so the web dashboard needs no
// change to stay truthful.
const PreviewKey = "preview"

// defaultRunsLimit caps how many runs GET /v1/runs returns by default.
const defaultRunsLimit = 25

// ObjectStore is the read side for published lineups: fetch the bytes for a key.
// ok=false means "not found" (404), err means a backend failure (502).
type ObjectStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
}

// Config wires the handler's dependencies. Lineups is required; Runs, Jobs, and
// Notifications are optional (nil -> those routes return 501, e.g. local `serve`
// has no ECS so Jobs is nil).
type Config struct {
	Token         string
	Lineups       ObjectStore
	Runs          RunStore
	Jobs          JobRunner
	Notifications NotificationStore
	Output        OutputStore
	Progress      ProgressStore
	Infra         InfraLister
	Trades        ObjectStore
	TradeValues   ObjectStore
	AvailablePool ObjectStore
	Reports       ObjectStore

	// Tenants resolves the eight stores above PER CALLER. When set it wins;
	// the flat fields then serve only `rosterbot serve` and the tests, which
	// have one tenant by construction.
	//
	// It exists because those fields were bound once at Lambda cold start from
	// ROSTERBOT_USER_ID while no route consulted who was asking, so every
	// signed-in user read the operator's data — the read half of the fan-out
	// that crq.11 left undone.
	Tenants TenantStores

	// WebAuthn passkey auth (see webauthn.go).
	Identities IdentityStore

	// Users and Enrollments back multi-tenant auth (rosterbot-crq.10). Users
	// is what a session's subject resolves against; without it every session
	// is refused, which is deliberate -- see resolveCaller.
	//
	// The bearer token path reads NEITHER, so a deployment with these nil (or
	// with DynamoDB unreachable) is still reachable by an operator holding the
	// token. That is the recovery path for every failure in this layer.
	Users       UserStore
	Enrollments EnrollmentStore

	// PushDevices is the APNs device registry behind /v1/push/devices. Nil
	// answers 501, matching the other optional stores; production wires the
	// same DynamoDB store as Users, `serve` the same file store.
	PushDevices PushDeviceStore

	// SleeperDir backs GET /v1/sleeper/leagues. Nil answers 501, matching the
	// other optional dependencies; production and `serve` both wire the
	// internal/sleeperdir adapter.
	SleeperDir SleeperDirectory

	// Connections and Sealer back POST /v1/connect. Sealer is the ENCRYPT-ONLY
	// half (see credentials.go): this config never holds an Opener, so no
	// handler can read a tenant's Fantrax password back — which is exactly why
	// verification is asynchronous rather than inline.
	Connections   ConnectionStore
	Sealer        Sealer
	WebAuthn      *webauthn.WebAuthn
	SessionSecret []byte
}

// Handler builds the full read/trigger API router. Every route requires
// either a valid session cookie (the everyday passkey-login path) or the
// legacy bearer token (break-glass / CLI use) — except /v1/auth/*, where each
// handler gates itself: login is open, register accepts session-or-token,
// and passkey management (list/revoke) requires a session only. Routes:
//
//	GET  /v1/lineup/today   -> precomputed lineup JSON
//	GET  /v1/runs           -> run ledger (newest first)
//	GET  /v1/runs/{id}      -> one run + log tail
//	GET  /v1/runs/{id}/progress -> live phase progress for a run
//	GET  /v1/infra          -> live state-bucket health (listed on demand)
//	GET  /v1/trades         -> pending trade offers, HKB-valued
//	GET  /v1/trades/values  -> league player/pick values table
//	GET  /v1/pool/available -> unowned players with HKB value + 30d momentum
//	GET  /v1/reports/{name} -> private dashboard report (model|gap|views)
//	GET  /v1/tenants        -> all tenants (admin only)
//	GET  /v1/me             -> the caller's own profile + Fantrax status
//	GET  /v1/sleeper/leagues -> discover a Sleeper account's leagues by username
//	GET/POST /v1/memberships          -> the caller's own leagues; add a Sleeper one
//	DELETE   /v1/memberships/{p}/{id} -> remove a Sleeper league
//	POST /v1/me/preferences -> update the caller's own auto_apply
//	POST /v1/jobs/{name}    -> launch a job (async), 202
//	POST /v1/auth/*         -> passkey login/register/logout, session mgmt
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/lineup/today", cfg.handleLineup)
	mux.HandleFunc("GET /v1/lineup/preview", cfg.handleLineupPreview)
	mux.HandleFunc("GET /v1/runs", cfg.handleRuns)
	mux.HandleFunc("GET /v1/runs/{id}", cfg.handleRunDetail)
	mux.HandleFunc("GET /v1/runs/{id}/output", cfg.handleRunOutput)
	mux.HandleFunc("GET /v1/runs/{id}/progress", cfg.handleRunProgress)
	mux.HandleFunc("GET /v1/notifications", cfg.handleNotifications)
	mux.HandleFunc("GET /v1/jobs", cfg.handleJobs)
	mux.HandleFunc("GET /v1/infra", cfg.handleInfra)
	mux.HandleFunc("GET /v1/trades", cfg.handleTrades)
	mux.HandleFunc("GET /v1/trades/values", cfg.handleTradeValues)
	mux.HandleFunc("GET /v1/pool/available", cfg.handleAvailablePool)
	mux.HandleFunc("GET /v1/reports/{name}", cfg.handleReport)
	mux.HandleFunc("POST /v1/jobs/{name}", cfg.handleJob)
	mux.HandleFunc("GET /v1/me", cfg.handleMe)
	mux.HandleFunc("GET /v1/sleeper/leagues", cfg.handleSleeperLeagues)
	mux.HandleFunc("GET /v1/memberships", cfg.handleListMemberships)
	mux.HandleFunc("POST /v1/memberships", cfg.handleAddMembership)
	mux.HandleFunc("DELETE /v1/memberships/{platform}/{leagueID}", cfg.handleDeleteMembership)
	mux.HandleFunc("GET /v1/tenants", cfg.handleTenants)
	// Tenant management (rosterbot-2twx). All under /v1/tenants/, which the
	// adminOnlyRoutes prefix gates as a unit; TestTenantStatus_MemberForbidden
	// pins that the prefix actually reaches them.
	mux.HandleFunc("POST /v1/tenants/invite", cfg.handleTenantInvite)
	mux.HandleFunc("POST /v1/tenants/{id}/status", cfg.handleSetTenantStatus)
	mux.HandleFunc("POST /v1/tenants/{id}/auto-apply", cfg.handleSetTenantAutoApply)
	mux.HandleFunc("POST /v1/tenants/{id}/recovery", cfg.handleTenantRecovery)
	mux.HandleFunc("DELETE /v1/tenants/{id}", cfg.handleDeleteTenant)
	mux.HandleFunc("POST /v1/me/preferences", cfg.handleSetPreferences)
	mux.HandleFunc("POST /v1/connect", cfg.handleConnect)
	mux.HandleFunc("GET /v1/connect", cfg.handleConnectStatus)
	// APNs device registry. Session-gated like everything else here, and each
	// handler additionally refuses the bearer token: a device registration
	// must be attributable to a person (see pushCaller).
	mux.HandleFunc("POST /v1/push/devices", cfg.handleRegisterPushDevice)
	mux.HandleFunc("GET /v1/push/devices", cfg.handleListPushDevices)
	mux.HandleFunc("DELETE /v1/push/devices/{id}", cfg.handleRevokePushDevice)

	// Auth routes gate themselves (open login, session-or-token register,
	// session-only passkey management in Task 5) instead of the blanket
	// cfg.authorize check below.
	mux.HandleFunc("POST /v1/auth/register/begin", cfg.handleAuthRegisterBegin)
	mux.HandleFunc("POST /v1/auth/register/finish", cfg.handleAuthRegisterFinish)
	mux.HandleFunc("POST /v1/auth/login/begin", cfg.handleAuthLoginBegin)
	mux.HandleFunc("POST /v1/auth/login/finish", cfg.handleAuthLoginFinish)
	mux.HandleFunc("GET /v1/auth/passkeys", cfg.handleListPasskeys)
	mux.HandleFunc("POST /v1/auth/passkeys/{id}/name", cfg.handleRenamePasskey)
	mux.HandleFunc("DELETE /v1/auth/passkeys/{id}", cfg.handleRevokePasskey)
	mux.HandleFunc("POST /v1/auth/logout", cfg.handleLogout)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			mux.ServeHTTP(w, r)
			return
		}
		caller, ok := cfg.authorize(w, r)
		if !ok {
			return // authorize already wrote the status
		}
		mux.ServeHTTP(w, r.WithContext(withCaller(r.Context(), caller)))
	})
}

func (cfg Config) handleLineup(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	serveBlob(w, r, view.Lineups, TodayKey, "lineup")
}

// handleLineupPreview serves the lineup a dry run computed without applying.
// 404 is the ordinary answer — most accounts have never run one — so the client
// treats absence as "no preview", not as an error.
func (cfg Config) handleLineupPreview(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	serveBlob(w, r, view.Lineups, PreviewKey, "lineup preview")
}

func (cfg Config) handleRuns(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	if view.Runs == nil {
		writeErr(w, http.StatusNotImplemented, "run ledger not configured")
		return
	}
	limit := defaultRunsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	runs, err := view.Runs.List(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "run ledger unavailable")
		return
	}
	if runs == nil {
		runs = []Run{}
	}
	writeJSON(w, http.StatusOK, RunsResponse{Runs: runs})
}

func (cfg Config) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	if view.Runs == nil {
		writeErr(w, http.StatusNotImplemented, "run ledger not configured")
		return
	}
	id := r.PathValue("id")
	detail, ok, err := view.Runs.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "run ledger unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (cfg Config) handleRunOutput(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	if view.Output == nil {
		writeErr(w, http.StatusNotImplemented, "run output not configured")
		return
	}
	id := r.PathValue("id")
	data, ok, err := view.Output.GetOutput(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "run output unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no output for run")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleRunProgress serves the raw stored progress bytes for a run — an
// internal/progress.Snapshot (phase/pct/phases) written by the progress
// recorder. The bytes are passed through untouched (never decoded here), so
// progress.Snapshot is the single source of truth for the wire shape.
func (cfg Config) handleRunProgress(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	if view.Progress == nil {
		writeErr(w, http.StatusNotImplemented, "run progress not configured")
		return
	}
	id := r.PathValue("id")
	data, ok, err := view.Progress.GetProgress(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "run progress unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no progress for run")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (cfg Config) handleNotifications(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	if view.Notifications == nil {
		writeErr(w, http.StatusNotImplemented, "activity feed not configured")
		return
	}
	limit := defaultRunsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	notifs, err := view.Notifications.List(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "activity feed unavailable")
		return
	}
	if notifs == nil {
		notifs = []Notification{}
	}
	writeJSON(w, http.StatusOK, NotificationsResponse{Notifications: notifs})
}

// handleJobs returns the job schema (GET /v1/jobs) so the app can render forms.
// Static — available even when Jobs (the runner) isn't wired.
func (cfg Config) handleJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, JobsResponse{Jobs: JobSpecList()})
}

func (cfg Config) handleJob(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// AUTHORIZATION BEFORE CONFIGURATION. League-wide jobs act on the league or
	// the deployment rather than on one team, and every run costs real money on
	// someone else's behalf, so only an admin may launch them; a member may
	// still launch jobs scoped to their own tenant. The check lives here rather
	// than in the path allowlist because it depends on the job NAME, which is a
	// path value and not a prefix.
	//
	// It deliberately precedes the nil-runner check below. Ordering them the
	// other way answers "job runner not configured" to a caller who was never
	// entitled to ask — a small disclosure on its own, and the wrong habit in a
	// handler whose job is to decide who may spend money.
	if leagueWideJobs[name] && !CallerFrom(r.Context()).IsAdmin() {
		writeErr(w, http.StatusForbidden, "job "+name+" is league-wide; admin only")
		return
	}

	if cfg.Jobs == nil {
		writeErr(w, http.StatusNotImplemented, "job runner not configured")
		return
	}

	// Optional JSON body { "params": { ... } }. An empty/absent body means
	// "use defaults"; a malformed body just yields no params (defaults too).
	var body struct {
		Params map[string]string `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	args, ok, err := BuildJobArgs(name, body.Params)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown job: "+name+" (valid: "+strings.Join(JobNames(), ", ")+")")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// view.Runs, not cfg.Runs: the duplicate guard has to ask about THIS
	// caller's runs. Reading the configured store was harmless only while every
	// launch was operator-scoped — with per-tenant launches it would let one
	// tenant's run block another's, and hide a member's run from the guard
	// entirely.
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	if run := inFlightRun(r.Context(), view.Runs, name, time.Now()); run != nil {
		writeErr(w, http.StatusConflict, "job "+name+" is already running (run "+run.ID+", started "+run.StartedAt+")")
		return
	}
	id, runErr := cfg.Jobs.Run(r.Context(), CallerFrom(r.Context()).UserID, args)
	if runErr != nil {
		writeErr(w, http.StatusBadGateway, "could not start job")
		return
	}
	writeJSON(w, http.StatusAccepted, JobResponse{
		ID:      id,
		Command: commandString(args),
		Status:  "RUNNING",
	})
}

// authorized reports whether the request carries the expected bearer token.
// A constant-time compare avoids leaking the token via response timing; an
// empty server token (misconfiguration) rejects everything.
func authorized(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := strings.TrimPrefix(h, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr must go through the encoder, not string concatenation. msg is not
// always a literal: handleJobRun passes BuildJobArgs' error straight through,
// and that error interpolates the caller's own param value (jobs.go's
// "invalid period: %s"). A value containing a double quote used to escape the
// string it was pasted into — at best a malformed body the dashboard cannot
// parse, at worst arbitrary extra keys injected into the error object
// (rosterbot-w200).
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, struct {
		Error string `json:"error"`
	}{msg})
}
