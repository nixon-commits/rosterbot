package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Self-serve signup (rosterbot-0kjf).
//
// POST /v1/signup is the one route that creates a tenant for a caller nobody
// has identified. Design: RosterbotApp
// docs/superpowers/specs/2026-09-02-self-serve-signup-design.md (D1, D2, D6,
// D7) and its 2026-11-18 plan, which is where the abuse limits below were
// decided.
//
// WHAT AN ACCOUNT MADE HERE CAN DO is bounded elsewhere and was already true
// of every freshly invited user: it is UserActive with no team and no
// connection record, so AuthorizeRun denies at its first check and the
// dispatch fan-out skips it. The admission gate is the team claim at connect
// (ClaimTeam / ErrTeamTaken), proven against a live Fantrax login.
//
// WHAT THIS FILE BOUNDS is how many such accounts can exist. The route was
// specified with no limit for a family TestFlight; a public App Store listing
// changed that. Three rules, all keyed on one predicate (unprovenSignup) so
// they cannot drift apart and can never touch an account an admin made:
//
//   - a CAP on unproven signup accounts, deployment-wide;
//   - a CLEANUP, run inside the request, of signup accounts abandoned before
//     a passkey existed (signupNoPasskeyTTL) or never connected to Fantrax
//     (signupNeverConnectedTTL);
//   - a repeat signup REPLACES an attempt that never got a passkey, so a
//     person who cancels the passkey sheet is not locked out of their own
//     address.
//
// The cap is SOFT: two concurrent requests can both pass at cap-1. It bounds
// table growth to "about the cap", which is all it is for; it is not a
// security boundary. An attacker can hold the cap full and block signup until
// the cleanup frees it. That trade (no growth, short denial) was chosen over
// no limit.
//
// ACCEPTED RISK: 409 tells a caller that an address has an account. There is
// no mail infrastructure to do better, and the address is unattested anyway.

const (
	signupPath = "/v1/signup"

	defaultSignupMaxUnproven = 50

	// signupNoPasskeyTTL: the passkey ceremony follows signup within seconds,
	// so an account with no credential an hour later was abandoned.
	signupNoPasskeyTTL = time.Hour

	// signupNeverConnectedTTL: a passkey but no connection record at all — the
	// Fantrax form was never submitted.
	signupNeverConnectedTTL = 7 * 24 * time.Hour

	// signupEnrollmentTTL is deliberately far shorter than an invite link's 72
	// hours: this token is returned to the caller and spent at once.
	signupEnrollmentTTL = 15 * time.Minute

	signupMaxEmailBytes = 254
	signupMaxNameRunes  = 80
)

// signupNow is the clock, a variable so the cleanup's age rules are testable.
var signupNow = time.Now

// signupOut is the 201 body. A named type so the wire-shape guards can reach
// it by reflection. The token appears here and nowhere else; only its hash is
// stored.
type signupOut struct {
	UserID          UserID `json:"user_id"`
	EnrollmentToken string `json:"enrollment_token"`
}

// unprovenSignup is the ONE predicate the cap, the cleanup and replacement
// share: an account signup created that holds no proven team. An invited
// account never matches (Origin is empty), and neither does a signed-up
// account once ClaimTeam has bound its team.
func unprovenSignup(u *User) bool {
	return u != nil && u.Origin == UserOriginSignup && u.TeamID == ""
}

func (cfg Config) handleSignup(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil || cfg.Enrollments == nil {
		writeErr(w, http.StatusNotImplemented, "signup is not configured")
		return
	}

	var body struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	email, ok := normalizeSignupEmail(body.Email)
	if !ok {
		writeErr(w, http.StatusBadRequest, "a valid email address is required")
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if utf8.RuneCountInString(name) > signupMaxNameRunes {
		writeErr(w, http.StatusBadRequest, "display name is too long")
		return
	}
	if name == "" {
		name = email
	}

	ctx := r.Context()
	now := signupNow().UTC()

	unproven, err := cfg.sweepSignups(ctx, email, now)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	limit := cfg.SignupMaxUnproven
	if limit <= 0 {
		limit = defaultSignupMaxUnproven
	}
	if unproven >= limit {
		// Counted, not silent: this line is the only signal that the cap is
		// being held full.
		log.Printf("lineupapi: signup refused, %d unproven signup accounts at cap %d", unproven, limit)
		w.Header().Set("Retry-After", "3600")
		writeErr(w, http.StatusTooManyRequests, "signup is busy; try again later")
		return
	}

	handle, err := NewWebAuthnUserID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not mint user id")
		return
	}
	uid := NewUserID(handle)
	// EmailAttestedBy/At stay zero: nobody vouched for this address, and the
	// fields' omitempty is what keeps that honest (spec D7). TeamID stays
	// empty: nobody names a team here, connect discovers it.
	u := &User{
		ID: uid, DisplayName: name, Email: email,
		Role: RoleMember, Status: UserActive,
		AutoApply: false,
		Origin:    UserOriginSignup,
		CreatedAt: now,
	}
	if err := cfg.Users.CreateUser(ctx, u); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			writeErr(w, http.StatusConflict, "an account with that email already exists")
			return
		}
		writeErr(w, http.StatusBadGateway, "could not create account")
		return
	}

	token, hash, err := MintEnrollmentToken()
	if err == nil {
		err = cfg.Enrollments.CreateEnrollment(ctx, hash, Enrollment{
			UserID: uid, Email: email, ExpiresAt: now.Add(signupEnrollmentTTL),
		})
	}
	if err != nil {
		// The account exists and nothing can enroll a passkey for it. Remove it
		// rather than leave the address claimed until the cleanup; if that
		// fails too, the no-passkey rule frees it within the hour, and a retry
		// with the same address replaces it sooner.
		if derr := cfg.Users.DeleteUser(ctx, uid); derr != nil {
			log.Printf("lineupapi: signup could not remove user %q after a failed enrollment mint: %v", uid, derr)
		}
		writeErr(w, http.StatusBadGateway, "could not start enrollment")
		return
	}
	writeJSON(w, http.StatusCreated, signupOut{UserID: uid, EnrollmentToken: string(token)})
}

// normalizeSignupEmail trims, lower-cases and validates an address. It accepts
// the bare addr-spec only: "Name <a@b.c>" parses as a valid mailbox, and
// storing that would put a display name inside the uniqueness key.
func normalizeSignupEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > signupMaxEmailBytes {
		return "", false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return "", false
	}
	// net/mail accepts "a@localhost"; a public signup wants a dotted domain.
	at := strings.LastIndexByte(email, '@')
	if domain := email[at+1:]; !strings.Contains(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return email, true
}

// sweepSignups deletes abandoned signup accounts and reports how many unproven
// signup accounts remain — the number the cap is checked against.
//
// An account is deleted when it matches unprovenSignup AND one of:
//
//   - it has no passkey and is older than signupNoPasskeyTTL;
//   - it has no passkey and holds the address this request is signing up with
//     (the repeat signup; age does not matter, the ceremony never finished);
//   - it has a passkey, no connection record, and is older than
//     signupNeverConnectedTTL.
//
// EVERY UNCERTAINTY KEEPS THE ACCOUNT. A credential read that fails, a
// connection read that fails, an unconfigured connection store, a zero
// CreatedAt, a delete that fails: each leaves the row in place and counted.
// Deleting on a guess would remove a real person's account for a DynamoDB
// blip; keeping on a guess costs one slot under the cap.
func (cfg Config) sweepSignups(ctx context.Context, email string, now time.Time) (int, error) {
	users, err := cfg.Users.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	remaining := 0
	for _, u := range users {
		if !unprovenSignup(u) {
			continue
		}
		if cfg.signupAbandoned(ctx, u, email, now) {
			if err := cfg.Users.DeleteUser(ctx, u.ID); err == nil {
				continue
			} else {
				log.Printf("lineupapi: signup cleanup could not delete user %q: %v", u.ID, err)
			}
		}
		remaining++
	}
	return remaining, nil
}

func (cfg Config) signupAbandoned(ctx context.Context, u *User, email string, now time.Time) bool {
	creds, err := cfg.Users.Credentials(ctx, u.ID)
	if err != nil {
		return false
	}
	if len(creds) == 0 {
		if strings.EqualFold(u.Email, email) {
			return true
		}
		return !u.CreatedAt.IsZero() && now.Sub(u.CreatedAt) > signupNoPasskeyTTL
	}
	if cfg.Connections == nil || u.CreatedAt.IsZero() || now.Sub(u.CreatedAt) <= signupNeverConnectedTTL {
		return false
	}
	_, ok, err := cfg.Connections.GetConnection(ctx, u.ID)
	return err == nil && !ok
}
