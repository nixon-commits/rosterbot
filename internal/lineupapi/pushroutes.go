package lineupapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// maxPushTokenLen bounds the stored token. An APNs device token is 64 hex
// characters today and 100+ under some configurations; this is a sanity bound
// against a client posting an unbounded body into durable storage.
const maxPushTokenLen = 512

type registerDeviceIn struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	BundleID    string `json:"bundle_id"`
	Model       string `json:"model"`
}

// pushDeviceOut is the listing shape. It deliberately omits Token: this view
// exists so a person can identify and revoke their own devices, and the raw
// token is a delivery credential, not an identifier for humans.
type pushDeviceOut struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	BundleID    string `json:"bundle_id"`
	Model       string `json:"model"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

// pushCaller resolves the tenant a push route acts for, refusing the two
// callers that have none: the operator bearer token (authenticated, but no
// UserID by construction — handleConnect's precedent) and a handler reached
// without the gate. There is genuinely no account a device could attach to.
func (cfg Config) pushCaller(w http.ResponseWriter, r *http.Request) (UserID, bool) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden,
			"push registration requires a passkey session; the API token authenticates an operator, not a person")
		return "", false
	}
	if cfg.PushDevices == nil {
		writeErr(w, http.StatusNotImplemented, "push device store not configured")
		return "", false
	}
	return caller.UserID, true
}

func (cfg Config) handleRegisterPushDevice(w http.ResponseWriter, r *http.Request) {
	uid, ok := cfg.pushCaller(w, r)
	if !ok {
		return
	}
	var in registerDeviceIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Token == "" || len(in.Token) > maxPushTokenLen {
		writeErr(w, http.StatusBadRequest, "invalid token")
		return
	}
	// Only sandbox and production exist, and the value routes the send (see
	// PushDevice): storing anything else is a device the sender cannot reach,
	// surfacing later as silence rather than as this 400.
	if in.Environment != "sandbox" && in.Environment != "production" {
		writeErr(w, http.StatusBadRequest, "environment must be sandbox or production")
		return
	}
	if in.BundleID == "" {
		writeErr(w, http.StatusBadRequest, "bundle_id is required")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	stored, err := cfg.PushDevices.PutPushDevice(r.Context(), uid, PushDevice{
		Token:       in.Token,
		Environment: in.Environment,
		BundleID:    in.BundleID,
		Model:       in.Model,
		CreatedAt:   now,
		LastSeenAt:  now,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "device store unavailable")
		return
	}
	// The id is the whole point of the response: the client persists it and
	// sends it back to DELETE on sign-out.
	writeJSON(w, http.StatusOK, map[string]any{"id": stored.ID})
}

func (cfg Config) handleListPushDevices(w http.ResponseWriter, r *http.Request) {
	uid, ok := cfg.pushCaller(w, r)
	if !ok {
		return
	}
	devices, err := cfg.PushDevices.PushDevices(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "device store unavailable")
		return
	}
	out := []pushDeviceOut{}
	for _, d := range devices {
		out = append(out, pushDeviceOut{
			ID: d.ID, Environment: d.Environment, BundleID: d.BundleID,
			Model: d.Model, CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (cfg Config) handleRevokePushDevice(w http.ResponseWriter, r *http.Request) {
	uid, ok := cfg.pushCaller(w, r)
	if !ok {
		return
	}
	// Scoped to the caller by construction: the store key is (their user id,
	// this device id), so a caller supplying someone else's id deletes
	// nothing — and an absent device is a success, which is also what makes
	// the client's revoke-on-sign-out safe to race a theft or a prune.
	if err := cfg.PushDevices.DeletePushDevice(r.Context(), uid, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
