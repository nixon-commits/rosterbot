package lineupapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// connectInFlightWindow is how long a pending verification blocks a second
// submission. Long enough to cover a slow chromedp login with headroom; short
// enough that a crashed connect task (which leaves the record pending forever)
// does not lock the tenant out of retrying.
const connectInFlightWindow = 10 * time.Minute

// handleConnect accepts a tenant's Fantrax credentials, seals them, and launches
// the task that verifies them.
//
// THE CALLER MUST BE A SESSION, never the bearer token. A token caller has no
// UserID by construction (see Caller), so there is no account it could be
// connecting — and allowing it would mean a break-glass credential could attach
// a Fantrax login to somebody else's tenant. The check below is therefore not a
// policy choice that could be relaxed; there is genuinely nothing to connect.
//
// The credentials are sealed and never read back here. This handler holds a
// Sealer and no Opener, mirroring IAM that grants kms:Encrypt without
// kms:Decrypt, which is exactly why verification is asynchronous: the API
// cannot check a password it cannot read.
func (cfg Config) handleConnect(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden,
			"connect requires a passkey session; the API token authenticates an operator, not a person")
		return
	}
	if cfg.Sealer == nil || cfg.Connections == nil || cfg.Jobs == nil {
		writeErr(w, http.StatusNotImplemented, "connect is not configured")
		return
	}

	// Bounded read. The body carries a password, so it must never be logged,
	// and it must not be allowed to be arbitrarily large either.
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		// Deliberately does not echo the decode error: a malformed body may be
		// a truncated password, and the error text can contain the fragment.
		writeErr(w, http.StatusBadRequest, "could not read credentials")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password are both required")
		return
	}

	// One verification at a time. Each accepted submission launches a chromedp
	// login against the tenant's real Fantrax account, so a double-click must
	// not stack parallel logins — that is how a tester's first minutes trip
	// Fantrax's bot defences. The window bounds the guard: a connect task that
	// crashed leaves the record pending forever, and refusing past the window
	// would lock the tenant out of retrying. A store read failure proceeds —
	// the Put below hits the same store and fails loudly.
	if cur, ok, gerr := cfg.Connections.GetConnection(r.Context(), caller.UserID); gerr == nil && ok &&
		cur.Status == ConnPending && time.Since(cur.UpdatedAt) < connectInFlightWindow {
		writeErr(w, http.StatusConflict, "a verification is already running; wait for it to finish")
		return
	}

	plain, err := json.Marshal(FantraxCreds{Username: body.Username, Password: body.Password})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not prepare credentials")
		return
	}
	sealed, err := cfg.Sealer.Seal(r.Context(), caller.UserID, plain)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not seal credentials")
		return
	}

	// The team comes from the user's own record — set by the invite — not from
	// the request. A caller naming their own team would be asserting the thing
	// the connect task exists to prove.
	var teamID string
	if cfg.Users != nil {
		if u, ok, err := cfg.Users.GetUser(r.Context(), caller.UserID); err == nil && ok {
			teamID = u.TeamID
		}
	}

	conn := &FantraxConnection{
		UserID:          caller.UserID,
		TeamID:          teamID,
		Status:          ConnPending,
		CredsCiphertext: sealed,
		UpdatedAt:       time.Now().UTC(),
	}
	// Written BEFORE the task is launched. If the launch fails the record shows
	// pending with no run, which is recoverable by retrying; the reverse order
	// could leave a task looking for credentials that were never stored.
	if err := cfg.Connections.PutConnection(r.Context(), conn); err != nil {
		writeErr(w, http.StatusBadGateway, "could not store credentials")
		return
	}

	// The caller goes in BOTH places, and they are not redundant. --user tells
	// the connect command whose credentials to decrypt and verify; the caller
	// argument sets the task's ROSTERBOT_USER_ID, which decides the S3 prefixes
	// it writes under. Without the second, a tenant's freshly minted FX_RM
	// cookie cache lands in the OPERATOR's session/ prefix — the precise
	// cross-tenant credential leak cmd/sync_tenant_test.go exists to prevent,
	// arriving through the one flow whose entire job is handling somebody
	// else's credentials.
	id, err := cfg.Jobs.Run(r.Context(), caller.UserID,
		[]string{"connect", "--user", string(caller.UserID)})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not start verification")
		return
	}
	// The run id is how the browser follows this: GET /v1/runs/{id}/progress
	// already exists, so the connect flow needs no polling endpoint of its own.
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": id, "status": string(ConnPending)})
}

// handleConnectStatus reports where a tenant's connection stands, without ever
// touching the ciphertext.
func (cfg Config) handleConnectStatus(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "connect status requires a passkey session")
		return
	}
	if cfg.Connections == nil {
		writeErr(w, http.StatusNotImplemented, "connect is not configured")
		return
	}
	conn, ok, err := cfg.Connections.GetConnection(r.Context(), caller.UserID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "connection store unavailable")
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	// Only the fields a user needs. The ciphertexts are not serialized here even
	// though they are opaque — an endpoint that returns them invites someone to
	// build a client that stores them.
	writeJSON(w, http.StatusOK, map[string]any{
		"connected":        conn.Status == ConnVerified,
		"status":           conn.Status,
		"team_id":          conn.TeamID,
		"last_error":       conn.LastError,
		"last_verified_at": conn.LastVerifiedAt,
	})
}
