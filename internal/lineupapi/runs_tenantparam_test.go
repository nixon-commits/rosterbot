package lineupapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file is the standing refusal of rosterbot-nejq's ORIGINAL suggestion —
// "add an admin-gated ?tenant=<id> param on GET /v1/runs (and
// /v1/runs/{id}/output)". There is no such gate available to add.
//
// adminOnlyRoutes is a PATH allowlist and authz.go tests r.URL.Path, so a query
// string reaches no gate at all: the naive implementation compiles, passes
// every existing authz test (they exercise /v1/tenants, not /v1/runs), and lets
// ANY authenticated member read ANY other tenant's full run ledger, run detail
// and captured stdout. It fails open, silently, and looks correct.
//
// These tests make writing that fail instead of ship. The admin view landed on
// GET /v1/tenants instead, which is already inside the allowlist.

// runDetailPerTenant is a TenantStores giving each tenant its own run detail
// and its own captured output, so a leak is observable rather than inferred.
type runDetailPerTenant map[UserID]string

func (m runDetailPerTenant) For(_ context.Context, uid UserID) (TenantView, error) {
	id := m[uid]
	if id == "" {
		return TenantView{}, nil
	}
	return TenantView{
		Runs:   detailRuns{id: id},
		Output: outputFor{id: id},
	}, nil
}

type detailRuns struct{ id string }

func (d detailRuns) List(context.Context, int) ([]Run, error) {
	return []Run{{ID: d.id, Command: "optimize", Status: "SUCCESS", StartedAt: nowRFC3339()}}, nil
}

func (d detailRuns) Get(_ context.Context, id string) (*RunDetail, bool, error) {
	if id != d.id {
		return nil, false, nil
	}
	return &RunDetail{
		Run:     Run{ID: d.id, Command: "optimize", Status: "SUCCESS", StartedAt: nowRFC3339()},
		LogTail: "log-of-" + d.id,
	}, true, nil
}

type outputFor struct{ id string }

func (o outputFor) GetOutput(_ context.Context, runID string) ([]byte, bool, error) {
	if runID != o.id {
		return nil, false, nil
	}
	return []byte(`{"stdout":"output-of-` + o.id + `"}`), true, nil
}

// runReq builds an authorized request for one of the /v1/runs routes, with the
// {id} wildcard already resolved the way the mux would.
func runReq(target, id string, caller Caller) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if id != "" {
		r.SetPathValue("id", id)
	}
	return r.WithContext(withCaller(r.Context(), caller))
}

// TestRuns_IgnoresATenantQueryParam covers all three routes the bead named —
// the ledger, the detail (which carries the log tail) and the captured output.
//
// MUTATION, for each: in the handler, resolve the view from
// r.URL.Query().Get("tenant") when non-empty instead of cfg.requireTenantView.
// bob then reads alice's ledger / log tail / stdout and the test goes red.
func TestRuns_IgnoresATenantQueryParam(t *testing.T) {
	cfg := Config{Tenants: runDetailPerTenant{"alice": "r-alice", "bob": "r-bob"}}
	bob := Caller{UserID: "bob", Role: RoleMember}

	t.Run("list", func(t *testing.T) {
		rec := httptest.NewRecorder()
		cfg.handleRuns(rec, runReq("/v1/runs?tenant=alice", "", bob))
		assertBobNotAlice(t, rec, "r-bob", "r-alice")
	})

	t.Run("detail", func(t *testing.T) {
		// bob asks for ALICE's run id while naming her tenant. The honest
		// answer is 404: that run is not in his ledger.
		rec := httptest.NewRecorder()
		cfg.handleRunDetail(rec, runReq("/v1/runs/r-alice?tenant=alice", "r-alice", bob))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET /v1/runs/r-alice?tenant=alice as bob = %d, want 404: %s",
				rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "log-of-r-alice") {
			t.Errorf("alice's log tail leaked to bob: %s", rec.Body)
		}
		// The positive control: the route works at all for his own run, so the
		// 404 above is a refusal and not a broken fixture.
		rec = httptest.NewRecorder()
		cfg.handleRunDetail(rec, runReq("/v1/runs/r-bob", "r-bob", bob))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "log-of-r-bob") {
			t.Fatalf("bob's own run detail = %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("output", func(t *testing.T) {
		rec := httptest.NewRecorder()
		cfg.handleRunOutput(rec, runReq("/v1/runs/r-alice/output?tenant=alice", "r-alice", bob))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET /v1/runs/r-alice/output?tenant=alice as bob = %d, want 404: %s",
				rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "output-of-r-alice") {
			t.Errorf("alice's captured stdout leaked to bob: %s", rec.Body)
		}
		rec = httptest.NewRecorder()
		cfg.handleRunOutput(rec, runReq("/v1/runs/r-bob/output", "r-bob", bob))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "output-of-r-bob") {
			t.Fatalf("bob's own run output = %d %s", rec.Code, rec.Body)
		}
	})
}

func assertBobNotAlice(t *testing.T, rec *httptest.ResponseRecorder, want, forbidden string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, want) {
		t.Errorf("body does not carry the caller's own run %q, so the negative "+
			"assertion below would pass on an empty response: %s", want, body)
	}
	if strings.Contains(body, forbidden) {
		t.Errorf("?tenant= was honoured: another tenant's run %q is in the body: %s",
			forbidden, body)
	}
}
