package lineupapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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

// The connect budget: how many checks an account with NO PROVEN TEAM may start
// per window.
//
// Until self-serve signup, only people an admin had invited could reach
// POST /v1/connect. Now anyone can create an account, and every submission
// here is a headless login against Fantrax from this deployment's address
// whose verdict GET /v1/connect then reports — a free oracle for testing
// stolen Fantrax passwords, an ECS task per guess, and the documented route to
// Cloudflare blocking the address for every tenant. The in-flight guard bounds
// how FAST that goes; nothing bounded how MUCH.
//
// It applies while the user record names no team and stops applying the moment
// a team is proven (ClaimTeam writes it), so a working tenant re-entering a
// changed password is never counted. A team choice spends from the same
// budget: it launches the same login.
//
// KNOWN LIMIT: the count lives on the connection record, so deleting the
// account (DELETE /v1/me) and signing up again starts a fresh budget. That
// costs a new signup and a new passkey ceremony per five guesses and stays
// inside the signup cap, but it is not a hard deployment-wide ceiling.
const (
	connectBudgetAttempts = 5
	connectBudgetWindow   = 24 * time.Hour
)

// connectBudget reports how many attempts the current window has used and when
// that window opened, as of now. An expired or absent window reads as a fresh
// one opening now.
func connectBudget(cur *FantraxConnection, ok bool, now time.Time) (used int, windowStart int64) {
	if ok && cur.ConnectWindowStart != 0 &&
		now.Sub(time.Unix(cur.ConnectWindowStart, 0)) < connectBudgetWindow {
		return cur.ConnectAttempts, cur.ConnectWindowStart
	}
	return 0, now.Unix()
}

// connectBudgetRefusal writes the 429 for an account that has spent its
// budget, and reports whether it did.
func connectBudgetRefusal(w http.ResponseWriter, userTeam string, used int, windowStart int64, now time.Time) bool {
	if userTeam != "" || used < connectBudgetAttempts {
		return false
	}
	retry := time.Unix(windowStart, 0).Add(connectBudgetWindow).Sub(now)
	w.Header().Set("Retry-After", strconv.Itoa(max(int(retry.Seconds()), 1)))
	writeErr(w, http.StatusTooManyRequests,
		"too many Fantrax sign-in attempts for this account today; try again tomorrow")
	return true
}

// handleConnect accepts a tenant's Fantrax credentials, seals them, and launches
// the task that verifies them. It also accepts the answer to an ambiguous_team
// result — a bare team choice — and launches the same task for it.
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
		// TeamID is a TEAM CHOICE, valid only as the whole request: see
		// teamChoiceRefusal. It is never an assertion that rides along with
		// credentials.
		TeamID string `json:"team_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		// Deliberately does not echo the decode error: a malformed body may be
		// a truncated password, and the error text can contain the fragment.
		writeErr(w, http.StatusBadRequest, "could not read credentials")
		return
	}
	// TWO REQUEST SHAPES, never mixed. Credentials start a fresh check. A bare
	// team_id answers an ambiguous_team result: it re-runs the check on the
	// credentials already sealed on the record, with the chosen team named.
	// A body carrying both is refused rather than guessed at — honouring the
	// team beside new credentials would let a caller assert the team for a
	// Fantrax account the server has never seen.
	choosing := body.TeamID != ""
	if choosing && (body.Username != "" || body.Password != "") {
		writeErr(w, http.StatusBadRequest, "send credentials or a team choice, not both")
		return
	}
	if !choosing && (body.Username == "" || body.Password == "") {
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

	// The team on the USER record — set by an invite, or by ClaimTeam once a
	// connection has been proven. Read before the branch because both shapes
	// need it: the credentials path copies it onto the record for the connect
	// task to VERIFY, and the choice path must not override it.
	var userTeam string
	if cfg.Users != nil {
		u, uok, uerr := cfg.Users.GetUser(r.Context(), caller.UserID)
		switch {
		case uerr == nil && uok:
			userTeam = u.TeamID
		default:
			// FAILS CLOSED, for both shapes. This read used to be tolerated: an
			// unreadable record left the team empty and the connect task then
			// refused with no_team. Since discovery an empty team is no longer
			// refused — the task ADOPTS whatever single team the credentials
			// control — so tolerating a failed read here would silently turn
			// "verify the team the invite named" into "discover a team", and
			// one DynamoDB blip would skip the admin's cross-check. The caller
			// resolved through this same store a moment ago, so !uok is a
			// deletion mid-request and gets the same answer.
			writeErr(w, http.StatusBadGateway, "user directory unavailable")
			return
		}
	}

	var sealed []byte
	teamID := userTeam
	if choosing {
		if code, msg := teamChoiceRefusal(cur, ok, userTeam, body.TeamID); code != 0 {
			writeErr(w, code, msg)
			return
		}
		// THE CHOICE IS CHECKED HERE AND PROVEN THERE. It was just matched
		// against the set the connect task stored from Fantrax's own answer;
		// naming it on the record sends the next connect run down proveTeam's
		// VERIFY branch, which asks Fantrax again. Nothing is claimed on the
		// strength of this request.
		teamID = body.TeamID
	} else {
		plain, err := json.Marshal(FantraxCreds{Username: body.Username, Password: body.Password})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not prepare credentials")
			return
		}
		sealed, err = cfg.Sealer.Seal(r.Context(), caller.UserID, plain)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "could not seal credentials")
			return
		}
	}

	// The budget is checked on the record as read, and again on every re-read
	// below, for the reason the in-flight guard is: the record that won a race
	// is the one the limit must hold against.
	now := time.Now().UTC()
	used, windowStart := connectBudget(cur, ok, now)
	if connectBudgetRefusal(w, userTeam, used, windowStart, now) {
		return
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
		creds := sealed
		if choosing {
			// The credentials Fantrax accepted on the ambiguous run, carried
			// over still sealed — this handler cannot open them. Taken from the
			// record as read THIS iteration, which teamChoiceRefusal has
			// passed.
			creds = cur.CredsCiphertext
		}
		conn := &FantraxConnection{
			UserID:          caller.UserID,
			TeamID:          teamID,
			Status:          ConnPending,
			CredsCiphertext: creds,
			UpdatedAt:       time.Now().UTC(),
		}
		if userTeam == "" {
			conn.ConnectAttempts, conn.ConnectWindowStart = used+1, windowStart
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
		used, windowStart = connectBudget(cur, ok, now)
		if connectBudgetRefusal(w, userTeam, used, windowStart, now) {
			return
		}
		if choosing {
			// The record that won the race is the one the choice must be valid
			// against: a connect run may have replaced the ambiguous result.
			if code, msg := teamChoiceRefusal(cur, ok, userTeam, body.TeamID); code != 0 {
				writeErr(w, code, msg)
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
	out := connectStatusOut{
		Connected:      conn.Status == ConnVerified,
		Status:         conn.Status,
		TeamID:         conn.TeamID,
		LastError:      conn.LastError,
		LastVerifiedAt: wiretime.New(conn.LastVerifiedAt),
	}
	// Present for ONE class only, checked here as well as cleared by the
	// connect task: the list is an offer to choose, and offering it beside any
	// other state would invite a choice teamChoiceRefusal then refuses.
	if awaitingTeamChoice(conn) {
		out.OwnedTeams = conn.OwnedTeams
	}
	writeJSON(w, http.StatusOK, out)
}

// awaitingTeamChoice reports whether a record is the ambiguous_team result a
// team choice answers. One definition, shared by the status response (which
// offers the choice) and teamChoiceRefusal (which accepts it), so the two
// cannot disagree about when a choice is on the table.
func awaitingTeamChoice(c *FantraxConnection) bool {
	return c != nil && c.Status == ConnNeedsReconnect && c.LastError == ConnErrAmbiguousTeam
}

// teamChoiceRefusal decides whether a caller may name teamID as their team,
// returning the status and message to refuse with, or 0 when the choice may
// proceed.
//
// This is the ONE place a caller names their own team, and it is permitted
// only because every input the decision rests on was written by the server:
// the record's state and its OwnedTeams come from the connect task, which got
// them from Fantrax's MyTeamIDs after a login Fantrax accepted. The request
// contributes nothing but which member of that set. Even then nothing is
// claimed here — the connect task proves the choice again (see handleConnect).
func teamChoiceRefusal(cur *FantraxConnection, ok bool, userTeam, teamID string) (int, string) {
	if !ok || !awaitingTeamChoice(cur) {
		return http.StatusConflict, "there is no team choice to make; connect with your Fantrax credentials"
	}
	if userTeam != "" {
		// A team an admin named (or one already proven) is never overridden
		// from a request. Unreachable through the connect task, which only
		// reports ambiguity when no team was named — refused all the same.
		return http.StatusConflict, "this account already has a team"
	}
	if len(cur.CredsCiphertext) == 0 {
		return http.StatusConflict, "the saved sign-in is gone; connect with your Fantrax credentials"
	}
	for _, t := range cur.OwnedTeams {
		if t.ID == teamID {
			return 0, ""
		}
	}
	return http.StatusBadRequest, "that team is not one of the teams this Fantrax account controls"
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

	// OwnedTeams is the set to choose between, present only while LastError
	// is ambiguous_team and omitted otherwise. POST /v1/connect {"team_id"}
	// answers it.
	OwnedTeams []OwnedTeam `json:"owned_teams,omitempty"`
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
