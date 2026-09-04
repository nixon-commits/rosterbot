package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// stubUserLookup is a one-method fake for userLookup — resolveTenantLabel's
// only dependency once IDENTITY_TABLE and the operator comparison are out of
// the way.
type stubUserLookup struct {
	user *lineupapi.User
	ok   bool
	err  error
}

func (s stubUserLookup) GetUser(context.Context, lineupapi.UserID) (*lineupapi.User, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return s.user, s.ok, nil
}

func TestTenantLabelFrom_ReturnsDisplayName(t *testing.T) {
	dir := stubUserLookup{user: &lineupapi.User{DisplayName: "Jon's Team"}, ok: true}
	got := tenantLabelFrom(context.Background(), dir, "u1")
	if got != "Jon's Team" {
		t.Errorf("got %q, want %q", got, "Jon's Team")
	}
}

func TestTenantLabelFrom_EmptyOnLookupFailure(t *testing.T) {
	dir := stubUserLookup{err: errors.New("dynamodb blip")}
	got := tenantLabelFrom(context.Background(), dir, "u1")
	if got != "" {
		t.Errorf("a lookup error must not surface a guessed label, got %q", got)
	}
}

func TestTenantLabelFrom_EmptyWhenRecordMissing(t *testing.T) {
	dir := stubUserLookup{ok: false}
	got := tenantLabelFrom(context.Background(), dir, "u1")
	if got != "" {
		t.Errorf("a missing record must not surface a guessed label, got %q", got)
	}
}

func TestTenantLabelFrom_EmptyWhenNoDisplayName(t *testing.T) {
	dir := stubUserLookup{user: &lineupapi.User{DisplayName: ""}, ok: true}
	got := tenantLabelFrom(context.Background(), dir, "u1")
	if got != "" {
		t.Errorf("an empty DisplayName has nothing usable to tag with, got %q", got)
	}
}

// --- resolveTenantLabel: the early-return cases it must never reach a store for ---

func TestResolveTenantLabel_EmptyForSingleTenant(t *testing.T) {
	// uid == "" is the single-tenant/local-dev case: no tenant to tag, and no
	// IDENTITY_TABLE env is set here either, so a bug that skipped the uid
	// check would still be caught by the table check below it — set the table
	// too, to isolate the uid=="" branch specifically.
	t.Setenv("IDENTITY_TABLE", "some-table")
	t.Setenv("OPERATOR_USER_ID", "")

	got := resolveTenantLabel(context.Background(), "")
	if got != "" {
		t.Errorf("an empty uid must resolve to an untagged push, got %q", got)
	}
}

func TestResolveTenantLabel_EmptyForTheOperatorsOwnTenant(t *testing.T) {
	t.Setenv("IDENTITY_TABLE", "some-table")
	t.Setenv("OPERATOR_USER_ID", "operator-uid")

	got := resolveTenantLabel(context.Background(), "operator-uid")
	if got != "" {
		t.Errorf("the operator's own tenant must not be tagged (noise on their own pushes), got %q", got)
	}
}

func TestResolveTenantLabel_EmptyWhenIdentityTableUnset(t *testing.T) {
	t.Setenv("IDENTITY_TABLE", "")
	t.Setenv("OPERATOR_USER_ID", "")

	got := resolveTenantLabel(context.Background(), "some-tenant")
	if got != "" {
		t.Errorf("with no directory to consult the push must stay untagged, got %q", got)
	}
}
