package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// nonStoreFields are the lineupapi.Config fields that buildStores deliberately
// does NOT set, because main owns them: they come from SSM, from ECS, or from
// the WebAuthn RP config rather than from the state bucket.
//
// This is an allowlist rather than a denylist on purpose. A new *store* field
// added to Config fails TestBuildStores_WiresEveryStoreField immediately, which
// is the point. A new non-store field fails it too, and the author has to come
// here and say so explicitly — a deliberate act, rather than a silent default
// that lets the next field go unwired the way Trades and TradeValues did.
var nonStoreFields = map[string]string{
	"Token":         "bearer token, from SSM",
	"Jobs":          "ECS runner, needs an AWS SDK client",
	"WebAuthn":      "RP config, built from SSM-published RP_ID/RP_ORIGIN",
	"SessionSecret": "HMAC secret, from SSM",
}

// TestBuildStores_WiresEveryStoreField is the regression guard for the class of
// bug rosterbot-gi2 was: Trades and TradeValues were declared on Config, routed
// in handler.go, and granted IAM in infra.go, but never assigned in the Lambda's
// Config literal. handleTrades/handleTradeValues return 501 "not configured" on
// a nil store, so GET /v1/trades and /v1/trades/values answered 501 in
// production from the day the Trades tab shipped — and nothing anywhere failed.
// cmd/serve.go DID wire them, so local development looked perfectly healthy.
//
// A test naming Trades and TradeValues specifically would only pin the bug we
// already found. Reflecting over the struct pins the bug we have not found yet:
// the next store field someone adds to Config and forgets here.
func TestBuildStores_WiresEveryStoreField(t *testing.T) {
	// LoadDefaultConfig reads the ambient AWS environment. Pin a region so the
	// result does not depend on the developer's or CI's config, and so a
	// missing region can never turn this guard into a silent skip.
	t.Setenv("AWS_REGION", "us-west-1")
	// buildStores refuses to start without a tenant directory: a session names
	// a user, and with nowhere to resolve it the Lambda would reject every
	// passkey login. Failing at construction is louder than failing per-request.
	t.Setenv("IDENTITY_TABLE", "test-identity-table")

	cfg, err := buildStores(context.Background(), "test-bucket")
	if err != nil {
		t.Fatalf("buildStores: %v", err)
	}

	v := reflect.ValueOf(cfg)
	typ := v.Type()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if reason, ok := nonStoreFields[name]; ok {
			t.Logf("skipping %s (%s)", name, reason)
			continue
		}
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Interface, reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func:
			if f.IsNil() {
				t.Errorf("lineupapi.Config.%s is nil after buildStores — every route "+
					"backed by it will answer 501 %q in production. Wire it in "+
					"buildStores, or add it to nonStoreFields with a reason if main "+
					"owns it.", name, "not configured")
			}
		default:
			t.Errorf("lineupapi.Config.%s has unexpected kind %s; this guard only "+
				"understands nilable fields. Add it to nonStoreFields or extend "+
				"the switch.", name, f.Kind())
		}
	}
}

// TestBuildStores_TradesAndTradeValuesAreDistinct pins the specific thing the
// reflective guard above cannot see: that the two Trades artifacts read from
// two different prefixes. layout.go keeps them separate deliberately — they
// have different producers (Lineup hourly vs TeamValues daily) and different
// cadences, and one prefix would judge both by whichever object is newest,
// hiding a dead daily producer behind a healthy hourly one. Wiring both to the
// same store would satisfy the nil check and silently reintroduce exactly that.
func TestBuildStores_TradesAndTradeValuesAreDistinct(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-1")
	// buildStores refuses to start without a tenant directory: a session names
	// a user, and with nowhere to resolve it the Lambda would reject every
	// passkey login. Failing at construction is louder than failing per-request.
	t.Setenv("IDENTITY_TABLE", "test-identity-table")

	cfg, err := buildStores(context.Background(), "test-bucket")
	if err != nil {
		t.Fatalf("buildStores: %v", err)
	}
	if cfg.Trades == (lineupapi.ObjectStore)(nil) || cfg.TradeValues == (lineupapi.ObjectStore)(nil) {
		t.Fatal("Trades and TradeValues must both be wired")
	}
	if reflect.ValueOf(cfg.Trades).Pointer() == reflect.ValueOf(cfg.TradeValues).Pointer() {
		t.Error("Trades and TradeValues point at the same store; they must read " +
			"from separate prefixes (layout.Trades vs layout.TradeValues)")
	}
}
