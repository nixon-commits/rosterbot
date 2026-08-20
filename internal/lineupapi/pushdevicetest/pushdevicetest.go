// Package pushdevicetest is the shared conformance suite for
// lineupapi.PushDeviceStore, on the same reasoning as enrollmenttest and
// identitytest: the properties that matter are not expressible in the method
// signatures.
//
// Two in particular. Registration must be IDEMPOTENT ON TOKEN — the client
// re-registers on every launch because APNs tokens rotate on restore and
// reinstall, so an insert-only store grows one dead row per launch forever.
// And registration must STEAL the token from any other user holding it —
// a device whose previous owner's session cookie has already expired can
// never issue its own revocation DELETE (the iOS sign-out hook fires only on
// deliberate sign-out, never on cookie expiry), so re-registration under a
// new user is the only moment the stale record can be reaped. A store that
// merely upserts within the calling user's own items passes every
// signature-level check and leaks the previous owner's roster notifications
// to the device indefinitely.
package pushdevicetest

import (
	"context"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// deleteUserer is the optional slice of UserStore a PushDeviceStore may also
// implement. When it does, the suite additionally checks that deleting a user
// releases their tokens for clean re-registration.
type deleteUserer interface {
	DeleteUser(ctx context.Context, id lineupapi.UserID) error
}

// Run exercises the whole PushDeviceStore contract. newStore must return a
// freshly-empty store on every call.
func Run(t *testing.T, newStore func(t *testing.T) lineupapi.PushDeviceStore) {
	t.Helper()
	ctx := context.Background()

	device := func(token string) lineupapi.PushDevice {
		return lineupapi.PushDevice{
			Token:       token,
			Environment: "sandbox",
			BundleID:    "dev.rosterbot.app.debug",
			Model:       "iPhone17,1",
			CreatedAt:   "2026-08-19T00:00:00Z",
			LastSeenAt:  "2026-08-19T00:00:00Z",
		}
	}

	t.Run("RegistrationAssignsAnIDAndRoundTrips", func(t *testing.T) {
		st := newStore(t)
		stored, err := st.PutPushDevice(ctx, "u1", device("aabbcc"))
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		if stored.ID == "" {
			t.Fatal("PutPushDevice must assign an ID; the client needs it to revoke on sign-out")
		}
		got, err := st.PushDevices(ctx, "u1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("want 1 device, got %d", len(got))
		}
		d := got[0]
		if d.Token != "aabbcc" || d.Environment != "sandbox" ||
			d.BundleID != "dev.rosterbot.app.debug" || d.Model != "iPhone17,1" {
			t.Errorf("device did not round-trip: %+v", d)
		}
	})

	t.Run("ReRegisteringATokenUpdatesInPlace", func(t *testing.T) {
		st := newStore(t)
		first, err := st.PutPushDevice(ctx, "u1", device("aabbcc"))
		if err != nil {
			t.Fatalf("first put: %v", err)
		}

		// Re-registering the SAME token must update, not insert: a device
		// re-registers on every launch, so an insert-only store would grow one
		// row per launch forever.
		again := device("aabbcc")
		again.CreatedAt, again.LastSeenAt = "2026-08-20T00:00:00Z", "2026-08-20T00:00:00Z"
		second, err := st.PutPushDevice(ctx, "u1", again)
		if err != nil {
			t.Fatalf("second put: %v", err)
		}
		if second.ID != first.ID {
			t.Errorf("re-registering the same token made a new device: %q then %q", first.ID, second.ID)
		}
		if second.CreatedAt != first.CreatedAt {
			t.Errorf("CreatedAt must survive re-registration: got %q want %q", second.CreatedAt, first.CreatedAt)
		}
		if second.LastSeenAt != "2026-08-20T00:00:00Z" {
			t.Errorf("LastSeenAt must advance: got %q", second.LastSeenAt)
		}

		devices, err := st.PushDevices(ctx, "u1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(devices) != 1 {
			t.Fatalf("want 1 device after two registrations of one token, got %d", len(devices))
		}
	})

	t.Run("DevicesAreScopedToTheirUser", func(t *testing.T) {
		st := newStore(t)
		if _, err := st.PutPushDevice(ctx, "u1", device("aaa")); err != nil {
			t.Fatalf("put u1: %v", err)
		}
		if _, err := st.PutPushDevice(ctx, "u2", device("bbb")); err != nil {
			t.Fatalf("put u2: %v", err)
		}
		got, err := st.PushDevices(ctx, "u1")
		if err != nil {
			t.Fatalf("list u1: %v", err)
		}
		if len(got) != 1 || got[0].Token != "aaa" {
			t.Fatalf("u1 must see only its own device, got %+v", got)
		}
	})

	t.Run("DeleteRemovesOnlyThatDevice", func(t *testing.T) {
		st := newStore(t)
		keep, err := st.PutPushDevice(ctx, "u1", device("keep"))
		if err != nil {
			t.Fatalf("put keep: %v", err)
		}
		drop, err := st.PutPushDevice(ctx, "u1", device("drop"))
		if err != nil {
			t.Fatalf("put drop: %v", err)
		}
		if err := st.DeletePushDevice(ctx, "u1", drop.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		got, err := st.PushDevices(ctx, "u1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 || got[0].ID != keep.ID {
			t.Fatalf("want only %s to survive, got %+v", keep.ID, got)
		}
	})

	t.Run("DeletingAnAbsentDeviceIsASuccess", func(t *testing.T) {
		st := newStore(t)
		// The client revokes on sign-out and may race a theft or a prune; a
		// device that is already gone is the desired end state, not an error.
		if err := st.DeletePushDevice(ctx, "u1", "never-existed"); err != nil {
			t.Fatalf("deleting an absent device: %v", err)
		}
	})

	t.Run("RegistrationStealsTheTokenFromItsPreviousOwner", func(t *testing.T) {
		st := newStore(t)
		if _, err := st.PutPushDevice(ctx, "alice", device("shared-phone")); err != nil {
			t.Fatalf("alice registers: %v", err)
		}

		// Alice's cookie expires (no revocation fires), Bob signs in on the
		// same phone. Registration under Bob must DELETE Alice's record, not
		// sit beside it — hers is unreachable by any client action forever,
		// and while it lives she keeps receiving Bob's device's notifications.
		bob, err := st.PutPushDevice(ctx, "bob", device("shared-phone"))
		if err != nil {
			t.Fatalf("bob registers: %v", err)
		}

		aliceDevices, err := st.PushDevices(ctx, "alice")
		if err != nil {
			t.Fatalf("list alice: %v", err)
		}
		if len(aliceDevices) != 0 {
			t.Fatalf("alice's record must be stolen, not kept: %+v", aliceDevices)
		}
		bobDevices, err := st.PushDevices(ctx, "bob")
		if err != nil {
			t.Fatalf("list bob: %v", err)
		}
		if len(bobDevices) != 1 || bobDevices[0].ID != bob.ID {
			t.Fatalf("bob must own the device, got %+v", bobDevices)
		}
	})

	t.Run("TheftLeavesThePreviousOwnersOtherDevicesAlone", func(t *testing.T) {
		st := newStore(t)
		ipad, err := st.PutPushDevice(ctx, "alice", device("alices-ipad"))
		if err != nil {
			t.Fatalf("alice ipad: %v", err)
		}
		if _, err := st.PutPushDevice(ctx, "alice", device("shared-phone")); err != nil {
			t.Fatalf("alice phone: %v", err)
		}
		if _, err := st.PutPushDevice(ctx, "bob", device("shared-phone")); err != nil {
			t.Fatalf("bob steals phone: %v", err)
		}
		got, err := st.PushDevices(ctx, "alice")
		if err != nil {
			t.Fatalf("list alice: %v", err)
		}
		if len(got) != 1 || got[0].ID != ipad.ID {
			t.Fatalf("theft must take exactly the shared token's record, alice has %+v", got)
		}
	})

	t.Run("AStolenTokenCanBeStolenBack", func(t *testing.T) {
		// The steal must reassign the token's ownership, not merely delete a
		// row: when the phone changes hands again the index has to follow it.
		st := newStore(t)
		if _, err := st.PutPushDevice(ctx, "alice", device("shared-phone")); err != nil {
			t.Fatalf("alice: %v", err)
		}
		if _, err := st.PutPushDevice(ctx, "bob", device("shared-phone")); err != nil {
			t.Fatalf("bob: %v", err)
		}
		if _, err := st.PutPushDevice(ctx, "alice", device("shared-phone")); err != nil {
			t.Fatalf("alice again: %v", err)
		}
		if bob, _ := st.PushDevices(ctx, "bob"); len(bob) != 0 {
			t.Fatalf("bob must lose the device on the steal-back, has %+v", bob)
		}
		if alice, _ := st.PushDevices(ctx, "alice"); len(alice) != 1 {
			t.Fatalf("alice must own the device again, has %+v", alice)
		}
	})

	t.Run("DeletingAUserReleasesTheirTokens", func(t *testing.T) {
		st := newStore(t)
		del, ok := st.(deleteUserer)
		if !ok {
			t.Skipf("%T does not implement DeleteUser; skipping", st)
		}
		if _, err := st.PutPushDevice(ctx, "alice", device("shared-phone")); err != nil {
			t.Fatalf("alice: %v", err)
		}
		if err := del.DeleteUser(ctx, "alice"); err != nil {
			t.Fatalf("delete alice: %v", err)
		}
		if got, err := st.PushDevices(ctx, "alice"); err != nil || len(got) != 0 {
			t.Fatalf("alice's devices must be swept with her account, got %+v (err %v)", got, err)
		}
		// A later registration of the same token must land cleanly under the
		// new owner — no leftover index entry may resurrect the dead account.
		bob, err := st.PutPushDevice(ctx, "bob", device("shared-phone"))
		if err != nil {
			t.Fatalf("bob after delete: %v", err)
		}
		if got, _ := st.PushDevices(ctx, "bob"); len(got) != 1 || got[0].ID != bob.ID {
			t.Fatalf("token must be cleanly re-registrable after DeleteUser, bob has %+v", got)
		}
	})
}
