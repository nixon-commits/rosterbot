package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

// The tenant management routes. All of them live under /v1/tenants/, which the
// adminOnlyRoutes prefix already gates — no handler here re-checks the role,
// and TestTenantStatus_MemberForbidden is what pins that assumption to the
// allowlist. What each handler must still do itself is refuse the specific
// request that is valid for an admin in general but wrong in particular
// (parking yourself, inviting a claimed email).

// errNoSuchUser distinguishes "the id names nobody" from a store failure, so
// handlers can answer 404 rather than 502.
var errNoSuchUser = errors.New("lineupapi: no such user")

// mutateUser re-reads and re-applies fn under PutUser's optimistic concurrency,
// following Config.mutateIdentity's rules: bounded attempts, and the mutation
// is re-applied to the freshly read record — never a stale snapshot re-written.
func (cfg Config) mutateUser(ctx context.Context, id UserID, fn func(*User) error) (*User, error) {
	for attempt := 0; attempt < 5; attempt++ {
		u, ok, err := cfg.Users.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errNoSuchUser
		}
		if err := fn(u); err != nil {
			return nil, err
		}
		err = cfg.Users.PutUser(ctx, u)
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, ErrUserConflict) {
			return nil, err
		}
	}
	return nil, ErrUserConflict
}

// handleSetTenantStatus parks or reactivates one tenant.
//
// Parking is the operator's off switch: authz refuses non-active users on
// every request and ListActive drops them from the job fan-out, so the write
// here is the whole mechanism — every reader already exists.
func (cfg Config) handleSetTenantStatus(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))

	var body struct {
		Status UserStatus `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if body.Status != UserActive && body.Status != UserParked {
		// A typo'd status written through would make the user invisible to
		// ListActive with nothing on any page saying why.
		writeErr(w, http.StatusBadRequest, "status must be \"active\" or \"parked\"")
		return
	}
	// Parking your own account with your own session is a lockout with no
	// dashboard path back (the CLI or bearer token would be the only recovery).
	// The bearer-token admin has no UserID, so it can park anyone.
	if caller := CallerFrom(r.Context()); caller.UserID != "" && caller.UserID == id && body.Status != UserActive {
		writeErr(w, http.StatusBadRequest, "cannot park your own account")
		return
	}

	u, err := cfg.mutateUser(r.Context(), id, func(u *User) error {
		u.Status = body.Status
		return nil
	})
	if errors.Is(err, errNoSuchUser) {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not update tenant")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "status": u.Status})
}

// ErrTeamRequired and ErrTeamReassignment are the two refusals SetTeam adds on
// top of the store's own ErrTeamTaken.
//
// ErrTeamReassignment is the load-bearing one. UserStore.ClaimTeam writes the
// new team's claim and updates the profile but never RELEASES the old team's
// claim, so moving a tenant between teams would leave the previous team
// recorded as theirs in the index while their profile says otherwise —
// permanently unassignable to anybody, with nothing on the Tenants page
// explaining why. Refusing keeps that impossible without pushing a release
// path into ClaimTeam, whose other implementation (ddbuser) would need the
// same change and whose only other caller is connect. It is the same trap
// UserStore.DeleteUser exists to avoid by releasing both claims.
var (
	ErrTeamRequired     = errors.New("lineupapi: a fantrax team id is required")
	ErrTeamReassignment = errors.New("lineupapi: user already holds a different fantrax team")
)

// SetTeam binds teamID to id, refusing every case that would leave the team
// claim index inconsistent.
//
// This is the single home of that rule, shared by POST /v1/tenants/{id}/team.
// cmd/user_set_team.go's setTeam is a second copy of it that predates this one
// and should be deleted in favour of this function — two copies of a guard
// whose whole job is keeping an index consistent is exactly the divergence the
// guard exists to prevent.
//
// Assignment is an ADMIN ASSERTION, not a grant: it records which team to
// prove, and connect still proves it against Fantrax's own MyTeamIDs. Naming
// the wrong team confers nothing — it makes that tenant's next connect fail
// with team_not_owned.
func SetTeam(ctx context.Context, users UserStore, id UserID, teamID string) error {
	// An empty team is precisely the state connect refuses as no_team, so
	// writing one would be a control whose only effect is recreating the wall
	// it exists to clear.
	if teamID == "" {
		return ErrTeamRequired
	}
	u, ok, err := users.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errNoSuchUser
	}
	if u.TeamID != "" && u.TeamID != teamID {
		return fmt.Errorf("%w: %s holds %s", ErrTeamReassignment, id, u.TeamID)
	}
	// ClaimTeam, never a PutUser of the field: a profile-only write would leave
	// the team unclaimed in the index and assignable to a second tenant, which
	// is the one thing ErrTeamTaken exists to prevent. Re-claiming a team the
	// user already holds is idempotent, so a double-click is a no-op.
	return users.ClaimTeam(ctx, id, teamID)
}

// handleSetTenantTeam binds a Fantrax team to a tenant — the dashboard twin of
// `rosterbot user set-team`, and the last management action that was CLI-only
// (rosterbot-yupr). Every pilot tester was invited with the invite form's
// optional team field skipped, aiming each of them at connect's no_team wall
// that nothing on this page could clear.
func (cfg Config) handleSetTenantTeam(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))

	var body struct {
		TeamID string `json:"team_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	err := SetTeam(r.Context(), cfg.Users, id, body.TeamID)
	switch {
	case err == nil:
	case errors.Is(err, ErrTeamRequired):
		writeErr(w, http.StatusBadRequest, "a fantrax team id is required (an empty team is what connect refuses as no_team)")
		return
	case errors.Is(err, errNoSuchUser):
		writeErr(w, http.StatusNotFound, "no such user")
		return
	case errors.Is(err, ErrTeamReassignment):
		// Spelled out rather than shortened: the operator's next move is to
		// release the old claim deliberately, and a bare "conflict" would send
		// them looking for the wrong problem.
		writeErr(w, http.StatusConflict, err.Error()+" — reassigning would orphan that claim. "+
			"Release it deliberately (delete and re-invite) before reassigning")
		return
	case errors.Is(err, ErrTeamTaken):
		writeErr(w, http.StatusConflict, "fantrax team already claimed")
		return
	default:
		writeErr(w, http.StatusBadGateway, "could not set tenant team")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "team_id": body.TeamID})
}

// handleSetTenantAutoApply is the operator's kill switch for one tenant's
// lineup writes: the tenant keeps their dashboard and the bot keeps proposing,
// it just stops writing to their roster. The self-serve twin is
// handleSetPreferences, which can only touch the caller's own flag.
func (cfg Config) handleSetTenantAutoApply(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))

	// A pointer so "field absent" is refused rather than read as false — the
	// same rule handleSetPreferences documents.
	var body struct {
		AutoApply *bool `json:"auto_apply"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if body.AutoApply == nil {
		writeErr(w, http.StatusBadRequest, "no auto_apply supplied")
		return
	}

	u, err := cfg.mutateUser(r.Context(), id, func(u *User) error {
		u.AutoApply = *body.AutoApply
		return nil
	})
	if errors.Is(err, errNoSuchUser) {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not update tenant")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "auto_apply": u.AutoApply})
}

// handleDeleteTenant removes a tenant entirely — the removal park cannot be.
//
// The store contract (UserStore.DeleteUser) carries the substance: profile,
// credentials and their indexes, the connection record, and the email/team
// claims RELEASED so the person can be re-invited and the team reassigned.
// The tenant's durable S3 artifacts stay behind as inert orphans, same class
// as the crq.11 cutover copies.
//
// Self-delete is refused on the self-park reasoning, one notch stronger:
// parking yourself is a lockout with a recovery, deleting yourself is a
// lockout without one. The bearer-token admin has no UserID and can delete
// anyone — it is the break-glass.
func (cfg Config) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))
	if caller := CallerFrom(r.Context()); caller.UserID != "" && caller.UserID == id {
		writeErr(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}

	// The existence check is for the 404 alone; DeleteUser itself treats an
	// absent user as success, so the race between the two is harmless.
	_, ok, err := cfg.Users.GetUser(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if err := cfg.Users.DeleteUser(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadGateway, "could not delete tenant")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// inviteResponse is the one place a minted token ever appears. Only its hash
// is stored, so this response is shown once and is not recoverable.
type inviteResponse struct {
	UserID UserID `json:"user_id"`
	Email  string `json:"email"`
	Token  string `json:"token"`

	// ExpiresAt is wiretime.Time while the Enrollment record it mirrors keeps a
	// plain time.Time. The stored record is Go-writes-Go-reads (file store and
	// DynamoDB) where RFC3339Nano round-trips exactly; this copy is read by a
	// client, and its constructor is time.Now().UTC().Add(ttl) — a nanosecond
	// count that is non-zero on every mint, so this field has never once
	// emitted the parseable shape (rosterbot-4e1j).
	ExpiresAt wiretime.Time `json:"expires_at"`
}

// defaultEnrollmentTTL matches cmd/invite.go's --ttl default, so a link minted
// from the dashboard and one minted from the CLI expire on the same schedule.
const defaultEnrollmentTTL = 72 * time.Hour

// mintEnrollmentAndRespond is the shared tail of handleTenantInvite and
// handleTenantRecovery: mint a single-use enrollment token, store it, and
// write the inviteResponse. ttlHours <= 0 defaults to defaultEnrollmentTTL,
// matching both callers' own default logic.
func (cfg Config) mintEnrollmentAndRespond(w http.ResponseWriter, r *http.Request, uid UserID, teamID, email string, ttlHours int) {
	ttl := defaultEnrollmentTTL
	if ttlHours > 0 {
		ttl = time.Duration(ttlHours) * time.Hour
	}

	token, hash, err := MintEnrollmentToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not mint enrollment token")
		return
	}
	expires := time.Now().UTC().Add(ttl)
	err = cfg.Enrollments.CreateEnrollment(r.Context(), hash, Enrollment{
		UserID: uid, TeamID: teamID, Email: email, ExpiresAt: expires,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not create enrollment link")
		return
	}
	writeJSON(w, http.StatusOK, inviteResponse{
		UserID: uid, Email: email, Token: string(token),
		ExpiresAt: wiretime.New(expires),
	})
}

// handleTenantInvite mints a new user and their single-use enrollment link —
// the dashboard twin of cmd/invite.go, producing the same record shape: member
// role, active, AutoApply false, and the email/team claims enforced at create
// time so a duplicate is a 409 while the admin is still looking at the form.
func (cfg Config) handleTenantInvite(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil || cfg.Enrollments == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}

	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		TeamID   string `json:"team_id"`
		TTLHours int    `json:"ttl_hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if body.Email == "" {
		writeErr(w, http.StatusBadRequest, "email is required")
		return
	}

	handle, err := NewWebAuthnUserID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not mint user id")
		return
	}
	uid := NewUserID(handle)
	name := body.Name
	if name == "" {
		name = body.Email
	}
	now := time.Now().UTC()
	u := &User{
		ID: uid, DisplayName: name, Email: body.Email,
		Role: RoleMember, Status: UserActive,
		AutoApply: false,
		TeamID:    body.TeamID,
		CreatedAt: now,
	}
	// The invite IS the attestation of the email address (there is no
	// verification mail), so record who vouched. The bearer-token admin has no
	// UserID and attests as nobody, which the field's omitempty makes honest.
	if caller := CallerFrom(r.Context()); caller.UserID != "" {
		u.EmailAttestedBy = caller.UserID
		u.EmailAttestedAt = now
	}
	if err := cfg.Users.CreateUser(r.Context(), u); err != nil {
		switch {
		case errors.Is(err, ErrEmailTaken):
			writeErr(w, http.StatusConflict, "email already claimed")
		case errors.Is(err, ErrTeamTaken):
			writeErr(w, http.StatusConflict, "fantrax team already claimed")
		default:
			writeErr(w, http.StatusBadGateway, "could not create user")
		}
		return
	}

	cfg.mintEnrollmentAndRespond(w, r, uid, body.TeamID, body.Email, body.TTLHours)
}

// handleTenantRecovery mints an enrollment link for an EXISTING user — the
// recovery half of the one primitive (enrollment.go): same token, same
// redemption, differing only in that the account already exists.
func (cfg Config) handleTenantRecovery(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil || cfg.Enrollments == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))

	// The body is optional: a bare POST means "default TTL". Only a present,
	// malformed body is refused.
	var body struct {
		TTLHours int `json:"ttl_hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	u, ok, err := cfg.Users.GetUser(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}

	cfg.mintEnrollmentAndRespond(w, r, u.ID, u.TeamID, u.Email, body.TTLHours)
}

// requireExistingTenant answers the 404 half every handler in this file
// already gives its own PathValue("id") — GetUser, not tenantViewOf, because
// tenantViewOf builds stores at a PREFIX derived from the uid string and
// succeeds for a prefix that has never been written to (an unregistered id
// reads as an empty, brand-new tenant, not as "no such tenant"). Only the user
// directory can tell the two apart.
func (cfg Config) requireExistingTenant(w http.ResponseWriter, r *http.Request, id UserID) bool {
	_, ok, err := cfg.Users.GetUser(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such user")
		return false
	}
	return true
}

// tenantConnectRun reads uid's OWN connection record for the connect verdict
// to stamp onto their runs.
//
// NEVER lastConnectRun (connectrun.go) here: that helper keys off
// CallerFrom(ctx), which on this admin route is the OPERATOR, not the tenant
// named in the path. Reusing it would stamp the operator's own connect
// verdict onto a tenant's runs — the exact per-row mis-attribution
// rosterbot-nejq's TenantView was built to remove, one join away from where
// that bead closed it. tenants.go's runSummary hits the same requirement and
// solves it the same way: read THIS tenant's connection record directly.
func (cfg Config) tenantConnectRun(ctx context.Context, uid UserID, runs []Run) *ConnectRun {
	if cfg.Connections == nil || uid == "" || !slices.ContainsFunc(runs, isConnectRun) {
		return nil
	}
	conn, ok, err := cfg.Connections.GetConnection(ctx, uid)
	if err != nil || !ok || conn == nil {
		return nil
	}
	return conn.LastConnectRun
}

// handleTenantRuns serves ONE TENANT's full run ledger — the admin drill-down
// GET /v1/tenants/{id}/runs (rosterbot-f0th). rosterbot-nejq put a BOUNDED run
// summary (last run, last failure, N of last 25) on each row of GET
// /v1/tenants because RunStore.Get scans RunLookback (200) records and must
// never run per row inside that listing; this route is the "show me" an
// operator reaches for after the summary flags a tenant, and it pays that
// cost for exactly the one tenant being examined.
//
// It mirrors handleRuns exactly (same limit param, same shape), just resolved
// against the TENANT named in the path rather than the caller.
//
// NESTED under /v1/tenants/{id}/, never a ?tenant= query param: isAdminOnlyPath
// (authz.go) matches on r.URL.Path alone, so a query string on GET /v1/runs
// would reach no gate at all and let any member read any other tenant's
// ledger. runs_tenantparam_test.go's TestRuns_IgnoresATenantQueryParam and
// tenantviewof_callers_test.go's TestNoRequestValueBecomesATenantID both
// refuse that shape; the second is what would fail if this ever moved off an
// admin-only path, because it reads real mux registrations rather than trusting
// a hand-kept list. This handler's tenantViewOf call is authorized because it
// is registered EXCLUSIVELY here, inside the /v1/tenants adminOnlyRoutes
// prefix — see tenantviewof_callers_test.go's allowedCallers, which records
// handleTenantRuns as a caller for exactly this reason.
func (cfg Config) handleTenantRuns(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))
	if !cfg.requireExistingTenant(w, r, id) {
		return
	}

	view, ok := cfg.tenantViewOf(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "could not resolve this account's data")
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
	if lr := cfg.tenantConnectRun(r.Context(), id, runs); lr != nil {
		out := slices.Clone(runs)
		for i := range out {
			applyConnectOutcome(&out[i], lr)
		}
		runs = out
	}
	writeJSON(w, http.StatusOK, RunsResponse{Runs: runs})
}

// handleTenantRun serves ONE TENANT's run detail — the admin drill-down GET
// /v1/tenants/{id}/runs/{runID} (rosterbot-iymz, following rosterbot-f0th's
// list and output routes). handleRunDetail already exists for the caller's
// own ledger; this is its per-tenant twin, giving the card the log tail, exit
// code and connect verdict a FAILED run needs that the bounded row summary on
// GET /v1/tenants deliberately does not carry.
//
// Mirrors handleRunDetail exactly, resolved against the tenant named in the
// path rather than the caller — same gating reasoning as handleTenantRuns and
// handleTenantRunOutput above: nested under /v1/tenants/{id}/, registered
// exclusively there, and tenantViewOf's call is authorized on that basis — see
// tenantviewof_callers_test.go's allowedCallers.
//
// Uses cfg.tenantConnectRun, NOT cfg.lastConnectRun: the latter keys off
// CallerFrom(ctx) (the admin caller), which would stamp the ADMIN's own
// connect verdict onto every tenant's drill-down — the exact mis-attribution
// connectrun.go's doc comment warns about and TestTenantRuns_AdminSeesTheNamedTenantsLedger's
// sibling in tenant_run_detail_test.go pins for this route.
func (cfg Config) handleTenantRun(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))
	if !cfg.requireExistingTenant(w, r, id) {
		return
	}

	view, ok := cfg.tenantViewOf(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "could not resolve this account's data")
		return
	}
	if view.Runs == nil {
		writeErr(w, http.StatusNotImplemented, "run ledger not configured")
		return
	}
	runID := r.PathValue("runID")
	detail, ok, err := view.Runs.Get(r.Context(), runID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "run ledger unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	// Copied before annotating, same as handleRunDetail: the store may hand
	// back a pointer into state it retains.
	out := *detail
	if lr := cfg.tenantConnectRun(r.Context(), id, []Run{out.Run}); lr != nil {
		applyConnectOutcome(&out.Run, lr)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTenantRunOutput serves ONE TENANT's captured stdout for one run — the
// admin drill-down GET /v1/tenants/{id}/runs/{runID}/output (rosterbot-f0th).
// Mirrors handleRunOutput exactly, resolved against the tenant named in the
// path.
//
// Same gating reasoning as handleTenantRuns above: nested under
// /v1/tenants/{id}/, registered exclusively there, and tenantViewOf's call is
// authorized on that basis — see tenantviewof_callers_test.go's
// allowedCallers.
func (cfg Config) handleTenantRunOutput(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	id := UserID(r.PathValue("id"))
	if !cfg.requireExistingTenant(w, r, id) {
		return
	}

	view, ok := cfg.tenantViewOf(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "could not resolve this account's data")
		return
	}
	if view.Output == nil {
		writeErr(w, http.StatusNotImplemented, "run output not configured")
		return
	}
	runID := r.PathValue("runID")
	data, ok, err := view.Output.GetOutput(r.Context(), runID)
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
