package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
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
	UserID    UserID    `json:"user_id"`
	Email     string    `json:"email"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
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
		UserID: uid, Email: email, Token: string(token), ExpiresAt: expires,
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
