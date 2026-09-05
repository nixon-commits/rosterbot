package lineupapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// memBlobs is an ObjectStore over a map: key -> stored bytes.
type memBlobs map[string][]byte

func (m memBlobs) Get(_ context.Context, key string) ([]byte, bool, error) {
	b, ok := m[key]
	return b, ok, nil
}

// failingUsers is a user directory whose every read fails — a DynamoDB outage.
// Embedding the interface leaves the other methods nil, which is fine: the
// route under test must never reach them.
type failingUsers struct{ UserStore }

func (failingUsers) GetUser(context.Context, UserID) (*User, bool, error) {
	return nil, false, errUserDirectoryDown
}

var errUserDirectoryDown = errors.New("user directory down")

func rosterReq(t *testing.T, cfg Config, caller Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/roster/values", nil)
	req = req.WithContext(withCaller(req.Context(), caller))
	rec := httptest.NewRecorder()
	cfg.handleRosterValues(rec, req)
	return rec
}

func rosterUsers(t *testing.T, users ...*User) *FileUserStore {
	t.Helper()
	store := NewFileUserStore(t.TempDir())
	for _, u := range users {
		if err := store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	return store
}

// The artifact holds every team; the route's whole job is choosing the
// caller's. Serving the wrong key here is the cross-tenant read this package
// exists to prevent, dressed up as a feature.
func TestRosterValues_ServesTheCallersOwnTeam(t *testing.T) {
	cfg := Config{
		RosterValues: memBlobs{
			"team-7": []byte(`{"team_id":"team-7"}`),
			"team-9": []byte(`{"team_id":"team-9"}`),
		},
		Users: rosterUsers(t, &User{ID: "alice", TeamID: "team-7", Status: UserActive}),
	}
	rec := rosterReq(t, cfg, Caller{UserID: "alice", Role: RoleMember})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"team-7"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "team-9") {
		t.Fatalf("served another team's roster: %s", rec.Body)
	}
}

// Same chain as handleMe: the user record first, the connection second.
func TestRosterValues_FallsBackToTheConnectionsTeam(t *testing.T) {
	cfg := Config{
		RosterValues: memBlobs{"team-7": []byte(`{}`)},
		Users:        rosterUsers(t, &User{ID: "alice", Status: UserActive}),
		Connections:  &memConnections{conn: &FantraxConnection{UserID: "alice", TeamID: "team-7"}},
	}
	if rec := rosterReq(t, cfg, Caller{UserID: "alice"}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// The bearer token has no UserID by construction. It resolves through the
// deployment's default tenant, exactly as tenantStores.For does for stores.
func TestRosterValues_BearerResolvesThroughTheDefaultTenant(t *testing.T) {
	cfg := Config{
		RosterValues:  memBlobs{"team-7": []byte(`{}`)},
		Users:         rosterUsers(t, &User{ID: "operator", TeamID: "team-7", Status: UserActive}),
		DefaultTenant: "operator",
	}
	if rec := rosterReq(t, cfg, Caller{Role: RoleAdmin, ViaToken: true}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Local `serve` has no user directory worth the name; FANTRAX_TEAM_ID is the
// last resort that keeps the curl-and-Simulator workflow answering.
func TestRosterValues_BearerFallsBackToTheDeploymentTeam(t *testing.T) {
	cfg := Config{RosterValues: memBlobs{"team-7": []byte(`{}`)}, DefaultTeamID: "team-7"}
	if rec := rosterReq(t, cfg, Caller{Role: RoleAdmin, ViaToken: true}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A member who has not connected Fantrax has no team, so no roster. That is
// the ordinary day-one state, and 404 is what the client maps to its empty
// state — anything else would show a Retry button for a non-fault.
func TestRosterValues_NoTeamIsAnOrdinary404(t *testing.T) {
	cfg := Config{
		RosterValues: memBlobs{"team-7": []byte(`{}`)},
		Users:        rosterUsers(t, &User{ID: "newbie", Status: UserActive}),
	}
	rec := rosterReq(t, cfg, Caller{UserID: "newbie"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no Fantrax team") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A store that is not wired is a deployment defect (501), and a team whose
// object the job has not written yet is the ordinary pre-first-run state
// (404) — the same split serveBlob gives every other artifact.
func TestRosterValues_StoreNilIs501AndMissingBlobIs404(t *testing.T) {
	if rec := rosterReq(t, Config{DefaultTeamID: "team-7"}, Caller{ViaToken: true}); rec.Code != http.StatusNotImplemented {
		t.Errorf("nil store: %d %s", rec.Code, rec.Body)
	}
	cfg := Config{RosterValues: memBlobs{}, DefaultTeamID: "team-7"}
	if rec := rosterReq(t, cfg, Caller{ViaToken: true}); rec.Code != http.StatusNotFound {
		t.Errorf("missing blob: %d %s", rec.Code, rec.Body)
	}
}

// A user-directory outage must not be laundered into "no team" — that would
// answer a transient with the empty state and hide the outage.
func TestRosterValues_UserStoreErrorIs502NotEmpty(t *testing.T) {
	cfg := Config{
		RosterValues: memBlobs{"team-7": []byte(`{}`)},
		Users:        failingUsers{},
	}
	rec := rosterReq(t, cfg, Caller{UserID: "alice"})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
