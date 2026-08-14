package lineupapi

import (
	"context"
	"net/http"
	"strings"
)

// Caller is the authenticated principal behind a request.
//
// There are exactly two ways to become one, and they are deliberately
// asymmetric:
//
//	a passkey session  -> a real tenant, loaded from the user store
//	the bearer token   -> an admin with NO user id, loaded from nothing
//
// THE TOKEN PATH MUST NEVER TOUCH THE USER STORE. It is the break-glass, and
// its whole value is that it keeps working when the thing it is glass over is
// broken: the migration not yet run in production, DynamoDB unreachable, or a
// bug in this very file. Route a token through a user lookup and all three
// become unrecoverable — you would need the dashboard to fix the dashboard.
//
// The cost is that a token caller has no UserID, so it cannot be used for
// anything that must be attributed to a tenant. That is the correct trade: a
// break-glass credential should be able to read and operate, not to impersonate
// a specific person.
type Caller struct {
	// UserID is empty for the bearer-token admin.
	UserID UserID

	Role Role

	// ViaToken records how this caller authenticated, so handlers that must
	// attribute an action to a person can refuse an unattributable one.
	ViaToken bool
}

// IsAdmin reports whether this caller may reach admin-only routes.
func (c Caller) IsAdmin() bool { return c.Role == RoleAdmin }

// resolveCaller identifies the requester, or returns ok=false.
//
// The token is checked FIRST and short-circuits, so a valid token never depends
// on a store read even when a session cookie is also present.
func (cfg Config) resolveCaller(ctx context.Context, r *http.Request) (Caller, bool) {
	if authorized(r, cfg.Token) {
		return Caller{Role: RoleAdmin, ViaToken: true}, true
	}

	p, err := sessionFromRequest(r, cfg.SessionSecret)
	if err != nil {
		return Caller{}, false
	}
	// A session names a user; without a store there is nothing to resolve it
	// against, so it cannot be honoured. Failing closed here is what keeps a
	// misconfigured deployment from serving every session as some default
	// principal.
	if cfg.Users == nil {
		return Caller{}, false
	}
	u, ok, err := cfg.Users.GetUser(ctx, p.Sub)
	if err != nil || !ok {
		return Caller{}, false
	}
	// Revocation (rosterbot-crq.8). The session carries the TokenVersion it was
	// minted at; incrementing the user's value invalidates every cookie issued
	// before it, for this user alone. This is the only place that comparison
	// happens, because it is the only place both halves are in hand.
	if p.Ver != u.TokenVersion {
		return Caller{}, false
	}
	if u.Status != UserActive {
		return Caller{}, false
	}
	return Caller{UserID: u.ID, Role: u.Role}, true
}

// adminOnlyRoutes are the paths a member must not reach.
//
// An ALLOWLIST of admin paths rather than a denylist of tenant ones, because
// the two failure directions are not equal: a new route accidentally treated as
// admin-only is a 403 someone reports immediately, while a new route
// accidentally treated as tenant-safe is a silent exposure nobody notices.
// Getting it wrong should be loud.
//
// GET /v1/infra is here because it reports the layout, object counts and health
// of every prefix in the state bucket — the shape of the whole deployment,
// which is operator information rather than a tenant's own data.
var adminOnlyRoutes = []string{
	"/v1/infra",
}

// leagueWideJobs are jobs a member may not launch: they act on the league or
// the deployment rather than on one team, and they cost real money on someone
// else's behalf. A member may still launch jobs scoped to their own tenant.
var leagueWideJobs = map[string]bool{
	"archive":         true,
	"recap-site":      true,
	"recap":           true,
	"version-check":   true,
	"team-values":     true,
	"gs-check":        true,
	"claims":          true,
	"transactions":    true,
	"waivers":         true,
	"projection-site": true,
	"football-values": true,
	"football-trades": true,
}

func isAdminOnlyPath(path string) bool {
	for _, p := range adminOnlyRoutes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// authorize is the single gate every non-/v1/auth route passes through.
func (cfg Config) authorize(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	caller, ok := cfg.resolveCaller(r.Context(), r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return Caller{}, false
	}
	if isAdminOnlyPath(r.URL.Path) && !caller.IsAdmin() {
		// 403, not 404: the caller is authenticated and the route exists, and
		// pretending otherwise would make a permissions problem look like a
		// broken deployment to whoever has to debug it.
		writeErr(w, http.StatusForbidden, "admin only")
		return Caller{}, false
	}
	return caller, true
}

// callerKey is the context key carrying the authorized Caller to handlers.
// Unexported and of a private type so nothing outside this package can forge
// one into a context.
type callerKeyType struct{}

var callerKey callerKeyType

func withCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey, c)
}

// CallerFrom returns the authorized caller for a request that has passed
// through Handler's gate.
//
// The zero Caller has an empty Role, so it is neither admin nor a tenant — a
// handler reached without the gate (a test wiring the mux directly, a future
// route registered outside it) gets a principal that can do nothing, rather
// than one that silently reads as an admin.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey).(Caller)
	return c
}
