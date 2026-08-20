package lineupapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPutPushDeviceHealsDuplicateTokenRows covers the lost race the
// idempotency scan cannot prevent: two concurrent registrations of one token
// both read "no existing match" and both mint an id, leaving two live rows
// that each receive every notification — forever, since a duplicate of a
// LIVE token never draws ErrDeviceGone. The public API can no longer produce
// that state, so the test seeds it directly; the next registration (which the
// client performs on every launch) must collapse it back to one row.
func TestPutPushDeviceHealsDuplicateTokenRows(t *testing.T) {
	st := NewFileUserStore(t.TempDir())
	ctx := t.Context()

	if _, err := st.PutPushDevice(ctx, "u1", PushDevice{
		Token: "tok", Environment: "sandbox", BundleID: "dev.rosterbot.app.debug",
	}); err != nil {
		t.Fatalf("first put: %v", err)
	}

	dup := PushDevice{ID: NewPushDeviceID(), Token: "tok", Environment: "sandbox", BundleID: "dev.rosterbot.app.debug"}
	data, err := json.Marshal(dup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.pushDir("u1"), pushDeviceFileName(dup.ID)), data, 0o644); err != nil {
		t.Fatalf("seed duplicate: %v", err)
	}
	if got, _ := st.PushDevices(ctx, "u1"); len(got) != 2 {
		t.Fatalf("precondition: want the seeded duplicate visible, got %d rows", len(got))
	}

	healed, err := st.PutPushDevice(ctx, "u1", PushDevice{
		Token: "tok", Environment: "sandbox", BundleID: "dev.rosterbot.app.debug",
	})
	if err != nil {
		t.Fatalf("healing put: %v", err)
	}
	got, err := st.PushDevices(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("re-registration must collapse duplicate token rows to one, got %d", len(got))
	}
	if got[0].ID != healed.ID {
		t.Fatalf("the surviving row (%s) must be the one the registration returned (%s)", got[0].ID, healed.ID)
	}
}
