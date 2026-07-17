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
// lineup. Producers also publish under the date string; the handler only ever
// serves "today".
const TodayKey = "today"

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

	// WebAuthn passkey auth (see webauthn.go).
	Identities    IdentityStore
	WebAuthn      *webauthn.WebAuthn
	SessionSecret []byte
}

// Handler builds the full read/trigger API router. Every route requires the
// bearer token. Routes:
//
//	GET  /v1/lineup/today   -> precomputed lineup JSON
//	GET  /v1/runs           -> run ledger (newest first)
//	GET  /v1/runs/{id}      -> one run + log tail
//	POST /v1/jobs/{name}    -> launch a job (async), 202
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/lineup/today", cfg.handleLineup)
	mux.HandleFunc("GET /v1/runs", cfg.handleRuns)
	mux.HandleFunc("GET /v1/runs/{id}", cfg.handleRunDetail)
	mux.HandleFunc("GET /v1/runs/{id}/output", cfg.handleRunOutput)
	mux.HandleFunc("GET /v1/notifications", cfg.handleNotifications)
	mux.HandleFunc("GET /v1/jobs", cfg.handleJobs)
	mux.HandleFunc("POST /v1/jobs/{name}", cfg.handleJob)

	// Auth routes gate themselves (open login, session-or-token register,
	// session-only passkey management in Task 5) instead of the blanket
	// isAuthed check below.
	mux.HandleFunc("POST /v1/auth/register/begin", cfg.handleAuthRegisterBegin)
	mux.HandleFunc("POST /v1/auth/register/finish", cfg.handleAuthRegisterFinish)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			mux.ServeHTTP(w, r)
			return
		}
		if !isAuthed(r, cfg) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// isAuthed reports whether the request is authenticated by either a valid
// session cookie (the everyday passkey-login path) or the legacy bearer
// token (break-glass / CLI use).
func isAuthed(r *http.Request, cfg Config) bool {
	return hasValidSession(r, cfg.SessionSecret) || authorized(r, cfg.Token)
}

func (cfg Config) handleLineup(w http.ResponseWriter, r *http.Request) {
	if cfg.Lineups == nil {
		writeErr(w, http.StatusNotImplemented, "lineup store not configured")
		return
	}
	data, ok, err := cfg.Lineups.Get(r.Context(), TodayKey)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "lineup store unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no lineup available yet")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (cfg Config) handleRuns(w http.ResponseWriter, r *http.Request) {
	if cfg.Runs == nil {
		writeErr(w, http.StatusNotImplemented, "run ledger not configured")
		return
	}
	limit := defaultRunsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	runs, err := cfg.Runs.List(r.Context(), limit)
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
	if cfg.Runs == nil {
		writeErr(w, http.StatusNotImplemented, "run ledger not configured")
		return
	}
	id := r.PathValue("id")
	detail, ok, err := cfg.Runs.Get(r.Context(), id)
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
	if cfg.Output == nil {
		writeErr(w, http.StatusNotImplemented, "run output not configured")
		return
	}
	id := r.PathValue("id")
	data, ok, err := cfg.Output.GetOutput(r.Context(), id)
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

func (cfg Config) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if cfg.Notifications == nil {
		writeErr(w, http.StatusNotImplemented, "activity feed not configured")
		return
	}
	limit := defaultRunsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	notifs, err := cfg.Notifications.List(r.Context(), limit)
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
	if cfg.Jobs == nil {
		writeErr(w, http.StatusNotImplemented, "job runner not configured")
		return
	}
	name := r.PathValue("name")

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
	if run := inFlightRun(r.Context(), cfg.Runs, name, time.Now()); run != nil {
		writeErr(w, http.StatusConflict, "job "+name+" is already running (run "+run.ID+", started "+run.StartedAt+")")
		return
	}
	id, runErr := cfg.Jobs.Run(r.Context(), args)
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

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
