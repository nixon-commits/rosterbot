package lineupapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

// connectInFlightWindow is how long a pending verification blocks a second
// submission. Long enough to cover a slow chromedp login with headroom; short
// enough that a crashed connect task (which leaves the record pending forever)
// does not lock the tenant out of retrying.
const connectInFlightWindow = 10 * time.Minute

// connectInterruptedCooldown throttles retries after a ConnInterrupted result.
//
// ConnInterrupted deliberately does NOT block resubmission the way ConnPending
// does — the tenant is told to try again, and there is no task in flight to
// wait for. But "try again" against a Fantrax outage is an invitation to hold
// the button down, and every submission drives a full chromedp login, which is
// the documented trigger for Fantrax lockout and Cloudflare bot-blocking. One
// minute is what the tenant-facing copy already promises, in both
// connectFailureMessage and settings.js's FAILURE_COPY.
const connectInterruptedCooldown = time.Minute

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

	// The record is read at its version before anything is sealed, and the
	// write below is conditioned on that version (rosterbot-wm9g). A store
	// read failure used to proceed to the write; it now refuses. A record that
	// could not be read has an unknown version, and the only write the handler
	// could then make asserts that no record exists — which the version
	// refuses over a real record anyway. Failing the request is the honest
	// form of that refusal: the tenant retries in a moment instead of getting
	// a 502 from a write that was never going to be allowed.
	cur, ok, gerr := cfg.Connections.GetConnection(r.Context(), caller.UserID)
	if gerr != nil {
		writeErr(w, http.StatusBadGateway, "connection store unavailable")
		return
	}
	if ok {
		if msg := connectInFlightRefusal(cur); msg != "" {
			writeErr(w, http.StatusConflict, msg)
			return
		}
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

	// Written BEFORE the task is launched. If the launch fails the record shows
	// pending with no run, which is recoverable by retrying; the reverse order
	// could leave a task looking for credentials that were never stored.
	//
	// The write REPLACES the record wholesale — fresh credentials, pending —
	// at the version that was read, so a writer landing in between (a session
	// ladder storing a refreshed cookie, a connect task recording a verdict)
	// surfaces as ErrConnectionConflict rather than being overwritten unseen.
	// This is the one connection writer that may re-apply after a lost race,
	// because its write does not depend on the old record beyond the in-flight
	// guard — and that guard is exactly why the retry RE-READS rather than
	// retrying blind: it has to run against the record that won. A connect
	// task landing "verified" in the window is fine to replace; a second
	// submission landing "pending" is not (see ConnectionStore).
	for attempt := 1; ; attempt++ {
		conn := &FantraxConnection{
			UserID:          caller.UserID,
			TeamID:          teamID,
			Status:          ConnPending,
			CredsCiphertext: sealed,
			UpdatedAt:       time.Now().UTC(),
		}
		if ok {
			conn.Version = cur.Version
		}
		err := cfg.Connections.PutConnection(r.Context(), conn)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrConnectionConflict) {
			writeErr(w, http.StatusBadGateway, "could not store credentials")
			return
		}
		if attempt >= connectPutAttempts {
			// Bounded, like mutateUser. A record that moves on every read is
			// real contention, and the answer is a 409 the client can act on,
			// not a 502 that reads as an outage.
			writeErr(w, http.StatusConflict, "the connection changed while this request was running; try again")
			return
		}
		cur, ok, gerr = cfg.Connections.GetConnection(r.Context(), caller.UserID)
		if gerr != nil {
			writeErr(w, http.StatusBadGateway, "connection store unavailable")
			return
		}
		if ok {
			if msg := connectInFlightRefusal(cur); msg != "" {
				writeErr(w, http.StatusConflict, msg)
				return
			}
		}
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
	writeJSON(w, http.StatusOK, connectStatusOut{
		Connected:      conn.Status == ConnVerified,
		Status:         conn.Status,
		TeamID:         conn.TeamID,
		LastError:      conn.LastError,
		LastVerifiedAt: wiretime.New(conn.LastVerifiedAt),
	})
}

// connectStatusOut is GET /v1/connect's body for a tenant that has a connection
// record. The no-record case above stays an anonymous {"connected": false},
// because that response genuinely has one field.
//
// It is a NAMED TYPE purely so the wire surface is reachable by reflection.
// This response was an inline map[string]any handing out conn.LastVerifiedAt —
// a raw time.Time — and TestWireTypes_CarryNoRawTimeTime structurally cannot
// see inside a map literal: there is no type to walk. A response body that only
// exists as an expression is a response body no guard can cover, which is the
// whole reason this one survived a survey that named every other site
// (rosterbot-4e1j). Key ORDER changes from the map's alphabetical sort to
// declaration order here; JSON objects are unordered and both clients read by
// key, so that is not a contract change.
type connectStatusOut struct {
	Connected      bool          `json:"connected"`
	Status         ConnStatus    `json:"status"`
	TeamID         string        `json:"team_id"`
	LastError      string        `json:"last_error"`
	LastVerifiedAt wiretime.Time `json:"last_verified_at"`
}

// connectPutAttempts bounds handleConnect's re-read-and-retry on a lost
// version race, the same bound Config.mutateUser and mutateIdentity use.
const connectPutAttempts = 5

// connectInFlightRefusal is the one-verification-at-a-time guard, returning
// the 409 message for a record that must not be replaced yet, or "" when a
// new submission may proceed. It is a function rather than inline because
// handleConnect runs it TWICE — on the first read and again on every re-read
// after a lost race — and two hand-copies of a three-branch guard are how the
// branches drift.
//
// Each accepted submission launches a chromedp login against the tenant's
// real Fantrax account, so a double-click must not stack parallel logins —
// that is how a tester's first minutes trip Fantrax's bot defences. The
// window bounds the pending guard: a connect task that crashed leaves the
// record pending forever, and refusing past the window would lock the tenant
// out of retrying.
func connectInFlightRefusal(cur *FantraxConnection) string {
	switch {
	case cur.Status == ConnPending && time.Since(cur.UpdatedAt) < connectInFlightWindow:
		return "a verification is already running; wait for it to finish"
	case cur.Status == ConnInterrupted && time.Since(cur.UpdatedAt) < connectInterruptedCooldown:
		return "the last check reached Fantrax but did not finish; try again in a minute"
	case cur.Status == ConnCheckFailed && time.Since(cur.UpdatedAt) < connectInterruptedCooldown:
		// ConnCheckFailed gets the SAME cooldown as ConnInterrupted, not
		// ConnPending's open-ended block: like ConnInterrupted, there is no
		// task in flight to wait for, and "try again" against a fault on our
		// own side is the same invitation to hold the button down — every
		// submission drives a full chromedp login regardless of why the
		// PREVIOUS one never reached Fantrax (rosterbot-spb9).
		return "the last check could not run because of a problem on our side; try again in a minute"
	}
	return ""
}
