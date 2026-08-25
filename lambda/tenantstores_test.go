package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// TestTenantStores_WiresEveryTenantViewField is the sibling of
// TestBuildStores_WiresEveryStoreField, and it exists because that guard did
// its job and then the code moved out from under it.
//
// buildStores composes the FLAT lineupapi.Config fields. Those are read only
// when cfg.Tenants is nil (lineupapi.Config.tenantView) — the single-tenant
// shape `rosterbot serve` and the handler tests use. A real request goes
// through cfg.Tenants.For, which lands in tenantStores.build and assembles a
// TenantView instead. So there are now TWO places a store gets wired, the older
// guard covers one of them, and the flat assignment is dead code on the
// production path.
//
// That is exactly how AvailablePool shipped broken: lambda/main.go set
// cfg.AvailablePool, buildStores' guard went green, `rosterbot serve` served
// the payload correctly through the real handler and route — and GET
// /v1/pool/available answered 501 "available player pool store not configured"
// on a real device, because build never set the field. Same class as
// rosterbot-gi2 (Trades and TradeValues), one refactor later, with a passing
// regression test six inches away.
//
// Reflecting over TenantView pins the next one rather than this one.
func TestTenantStores_WiresEveryTenantViewField(t *testing.T) {
	// s3lineup.New resolves AWS config, which reads the ambient environment.
	// Pin a region so the result cannot depend on the developer's or CI's
	// config, and so a missing region can never turn this guard into a silent
	// skip. Same reasoning as the buildStores guard.
	t.Setenv("AWS_REGION", "us-west-1")

	ts := newTenantStores("test-bucket", lineupapi.UserID("default-tenant"))

	view, err := ts.build(context.Background(), lineupapi.UserID("some-tenant"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	v := reflect.ValueOf(view)
	typ := v.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func:
			if f.IsNil() {
				t.Errorf("lineupapi.TenantView.%s is nil after tenantStores.build — "+
					"every route backed by it answers 501 %q for real callers, while "+
					"`rosterbot serve` and the handler tests stay green because they "+
					"read the flat Config fields instead. Wire it in build.",
					name, "not configured")
			}
		default:
			t.Errorf("lineupapi.TenantView.%s has unexpected kind %s; this guard only "+
				"understands nilable fields. Extend the switch.", name, f.Kind())
		}
	}
}

// TestTenantStores_EveryTenantViewFieldReadsItsOwnPrefix pins what the nil
// check above cannot see: that no two stores were wired to the same prefix.
//
// A copy-paste that points AvailablePool at layout.TradeValues satisfies the
// reflective guard perfectly — the field is non-nil — and then serves the
// wrong artifact, or judges a dead producer healthy because a different
// artifact under the same prefix is fresh. The same reasoning as
// TestBuildStores_TradesAndTradeValuesAreDistinct, generalised so it covers
// every pair rather than the one pair that already bit.
func TestTenantStores_EveryTenantViewFieldReadsItsOwnPrefix(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-1")

	ts := newTenantStores("test-bucket", lineupapi.UserID("default-tenant"))

	view, err := ts.build(context.Background(), lineupapi.UserID("some-tenant"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	v := reflect.ValueOf(view)
	typ := v.Type()
	seen := map[uintptr]string{}
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		f := v.Field(i)
		if f.Kind() != reflect.Interface || f.IsNil() {
			continue // the guard above reports nils; nothing to compare here
		}
		ptr := reflect.ValueOf(f.Interface()).Pointer()
		if prev, dup := seen[ptr]; dup {
			t.Errorf("TenantView.%s and TenantView.%s are the same store instance; "+
				"each must read from its own layout prefix", prev, name)
			continue
		}
		seen[ptr] = name
	}
}
