package lineupapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// These bound what one tenant can push into their own profile. The cost is
// not theirs alone: resolveCaller does a GetUser on EVERY authenticated
// request, so a bloated membership list is re-read and re-decoded on every
// single API call the deployment serves.
const (
	// maxMemberships caps the list. Fifty leagues is far past any real
	// tenant — the point is that the record cannot grow without bound.
	maxMemberships = 50

	// maxLeagueIDLen and maxTeamIDLen bound Sleeper's identifiers, which are
	// ~19-digit numeric strings, so this is deliberately generous.
	maxLeagueIDLen = 64
	maxTeamIDLen   = 64

	// maxDisplayNameLen bounds a league name.
	maxDisplayNameLen = 128
)

// handleListMemberships returns the caller's own leagues across platforms.
func (cfg Config) handleListMemberships(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	u, ok, err := cfg.Users.GetUser(r.Context(), caller.UserID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": u.AllMemberships()})
}

// handleAddMembership records one Sleeper league on the caller's own record.
//
// Sleeper ONLY, and the refusal below is the substance of this handler. A
// Fantrax membership is established by the invite and proven against Fantrax's
// own MyTeamIDs by the connect task; accepting one here would let a caller
// assert the exact fact that proof exists to establish.
//
// There is deliberately no uniqueness check ACROSS users. Sleeper league data
// is world-readable, so claiming one grants access to nothing private, and two
// tenants in the same league must both be able to add it — the opposite of the
// ErrTeamTaken rule that governs Fantrax.
func (cfg Config) handleAddMembership(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}

	var body struct {
		Platform    Platform `json:"platform"`
		LeagueID    string   `json:"league_id"`
		TeamID      string   `json:"team_id"`
		DisplayName string   `json:"display_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if body.Platform != PlatformSleeper {
		writeErr(w, http.StatusBadRequest, "only sleeper memberships can be added here")
		return
	}
	if body.LeagueID == "" {
		writeErr(w, http.StatusBadRequest, "league_id is required")
		return
	}
	// 400 for the length violations, matching the validation above: these are
	// malformed input, not a conflict with anything already stored.
	if len(body.LeagueID) > maxLeagueIDLen {
		writeErr(w, http.StatusBadRequest, "league_id is too long")
		return
	}
	if len(body.TeamID) > maxTeamIDLen {
		writeErr(w, http.StatusBadRequest, "team_id is too long")
		return
	}
	if len(body.DisplayName) > maxDisplayNameLen {
		writeErr(w, http.StatusBadRequest, "display_name is too long")
		return
	}

	errDuplicate := errors.New("duplicate membership")
	errTooMany := errors.New("membership limit reached")
	u, err := cfg.mutateUser(r.Context(), caller.UserID, func(u *User) error {
		for _, m := range u.Memberships {
			if m.Platform == body.Platform && m.LeagueID == body.LeagueID {
				return errDuplicate
			}
		}
		// Checked after the duplicate scan so that re-adding a league already
		// on a full account still reports the duplicate, which is the answer
		// that tells the caller what to do about it.
		if len(u.Memberships) >= maxMemberships {
			return errTooMany
		}
		u.Memberships = append(u.Memberships, Membership{
			Platform:    PlatformSleeper,
			LeagueID:    body.LeagueID,
			TeamID:      body.TeamID,
			DisplayName: body.DisplayName,
			// Never true. Sleeper's public API has no write endpoints, and this
			// field is what the clients trust when deciding to show an action.
			Writable: false,
			AddedAt:  time.Now().UTC(),
		})
		return nil
	})
	switch {
	case errors.Is(err, errDuplicate):
		writeErr(w, http.StatusConflict, "that league is already on your account")
		return
	// 409 like the duplicate above, not 400: the request is well formed and it
	// is the caller's own current state that refuses it. Keeping the whole
	// family on one status is what lets a client handle it in one place.
	case errors.Is(err, errTooMany):
		writeErr(w, http.StatusConflict, "you have reached the limit of "+
			strconv.Itoa(maxMemberships)+" leagues; remove one first")
		return
	case errors.Is(err, errNoSuchUser):
		writeErr(w, http.StatusNotFound, "no such user")
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "could not update memberships")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": u.AllMemberships()})
}

// handleDeleteMembership removes one Sleeper league from the caller's record.
//
// Removing an absent membership is not an error: the caller's intent is
// satisfied either way, and the alternative invites a check-then-act race —
// the same rule UserStore.DeleteCredential documents.
//
// Fantrax is refused. Removing that membership means deleting the tenant's
// proven team binding, which is an admin operation (DELETE /v1/tenants/{id}),
// not a self-service one.
func (cfg Config) handleDeleteMembership(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}

	platform := Platform(r.PathValue("platform"))
	leagueID := r.PathValue("leagueID")
	if platform != PlatformSleeper {
		writeErr(w, http.StatusBadRequest, "only sleeper memberships can be removed here")
		return
	}

	// mutateUser hands the closure the record it just read; holding onto it
	// lets the nothing-to-do path answer from that same read instead of
	// issuing a second GetUser. mutateUser returns a closure error without
	// retrying, so this is always the record the decision was made on.
	errNothingRemoved := errors.New("no matching membership")
	var read *User
	u, err := cfg.mutateUser(r.Context(), caller.UserID, func(u *User) error {
		read = u
		kept := u.Memberships[:0]
		removed := false
		for _, m := range u.Memberships {
			if m.Platform == platform && m.LeagueID == leagueID {
				removed = true
				continue
			}
			kept = append(kept, m)
		}
		if !removed {
			return errNothingRemoved
		}
		u.Memberships = kept
		return nil
	})
	switch {
	// Nothing matched, so there is nothing to write. The 200 below is
	// unchanged — removing an absent membership succeeding is the documented
	// rule — but writing an identical record would cost a real round trip and
	// bump User.Version, losing the race for any optimistic write another
	// request is holding, in exchange for nothing.
	case errors.Is(err, errNothingRemoved):
		u = read
	case errors.Is(err, errNoSuchUser):
		writeErr(w, http.StatusNotFound, "no such user")
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "could not update memberships")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": u.AllMemberships()})
}
