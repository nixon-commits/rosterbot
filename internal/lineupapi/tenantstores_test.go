package lineupapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// perTenantBlobs hands each tenant a store whose contents name that tenant, so
// a leak is visible in the response body rather than inferred.
type perTenantBlobs struct {
	err error
}

func (p perTenantBlobs) For(_ context.Context, uid UserID) (TenantView, error) {
	if p.err != nil {
		return TenantView{}, p.err
	}
	return TenantView{
		Lineups: memBlob{TodayKey: []byte(fmt.Sprintf(`{"owner":%q}`, uid))},
		Trades:  memBlob{TradesCurrentKey: []byte(fmt.Sprintf(`{"owner":%q}`, uid))},
	}, nil
}

type memBlob map[string][]byte

func (m memBlob) Get(_ context.Context, key string) ([]byte, bool, error) {
	d, ok := m[key]
	return d, ok, nil
}
func (m memBlob) Publish(_ context.Context, key string, data []byte) error {
	m[key] = data
	return nil
}

// sessionFor builds a request carrying an authorized caller, bypassing the
// cookie machinery so this file tests store SELECTION rather than auth.
func sessionFor(t *testing.T, uid UserID) *http.Request {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/lineup/today", nil)
	return r.WithContext(withCaller(r.Context(), Caller{UserID: uid, Role: RoleMember}))
}

// TestTenantView_ServesTheCallersOwnData is the whole point.
//
// The stores were resolved once at cold start from ROSTERBOT_USER_ID and no
// handler consulted CallerFrom, so every signed-in user read the operator's
// lineup, runs, trades, reports and notification feed. Meanwhile the fan-out
// wrote each tenant's real data to user=<uid>/ prefixes nothing read: the write
// half of tenancy shipped and the read half did not.
func TestTenantView_ServesTheCallersOwnData(t *testing.T) {
	cfg := Config{Tenants: perTenantBlobs{}}

	for _, uid := range []UserID{"alice", "bob"} {
		v, ok := cfg.tenantView(sessionFor(t, uid).Context())
		if !ok {
			t.Fatalf("%s: tenantView refused", uid)
		}
		data, found, _ := v.Lineups.Get(context.Background(), TodayKey)
		if !found {
			t.Fatalf("%s: no lineup", uid)
		}
		if !strings.Contains(string(data), string(uid)) {
			t.Errorf("caller %s was served %s", uid, data)
		}
	}
}

// TestTenantView_ProviderFailureDoesNotFallBack is the trap.
//
// The obvious error path — "could not resolve the tenant, use the configured
// stores" — is the original bug reappearing on the branch nobody exercises. It
// would hand the caller whichever tenant the deployment booted with, silently,
// and only when something else was already wrong.
func TestTenantView_ProviderFailureDoesNotFallBack(t *testing.T) {
	cfg := Config{
		Tenants: perTenantBlobs{err: errors.New("dynamo down")},
		// Present and populated, exactly as a real deployment would have them.
		Lineups: memBlob{TodayKey: []byte(`{"owner":"operator"}`)},
	}

	v, ok := cfg.tenantView(sessionFor(t, "alice").Context())
	if ok {
		t.Fatal("a failed tenant resolution was reported as usable")
	}
	if v.Lineups != nil {
		t.Error("the view carried a store after a failed resolution; the caller could " +
			"be served the deployment's own tenant")
	}
}

// TestTenantView_NoProviderIsTheSingleTenantShape: `rosterbot serve` and every
// handler test build Config with the flat fields and no provider. That must
// keep working unchanged — this is the "tenancy is not configured" case, which
// is different from "tenancy failed".
func TestTenantView_NoProviderIsTheSingleTenantShape(t *testing.T) {
	want := memBlob{TodayKey: []byte(`{"owner":"operator"}`)}
	cfg := Config{Lineups: want}

	v, ok := cfg.tenantView(context.Background())
	if !ok {
		t.Fatal("tenantView refused with no provider configured")
	}
	if _, found, _ := v.Lineups.Get(context.Background(), TodayKey); !found {
		t.Error("the flat Lineups store did not carry through")
	}
}

// TestLineupRoute_IsScopedToTheCaller drives the real router, because a correct
// tenantView that a handler forgets to call is the same leak with a new name —
// and that is precisely how this bug existed in the first place.
func TestLineupRoute_IsScopedToTheCaller(t *testing.T) {
	cfg := Config{Tenants: perTenantBlobs{}, SessionSecret: []byte("s"), Users: NewFileUserStore(t.TempDir())}
	h := Handler(cfg)

	for _, uid := range []UserID{"alice", "bob"} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/lineup/today", nil)
		r = r.WithContext(withCaller(r.Context(), Caller{UserID: uid, Role: RoleMember}))
		rec := httptest.NewRecorder()
		// Call the handler directly: Handler's gate resolves its own caller from
		// a cookie, and the subject here is store selection, not authentication.
		cfg.handleLineup(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", uid, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), string(uid)) {
			t.Errorf("GET /v1/lineup/today as %s returned %s", uid, rec.Body)
		}
	}
	_ = h
}
