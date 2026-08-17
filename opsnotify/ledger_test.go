package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

const testTenant = "user=alice/"

// rec builds a stored ledger record. The key carries an inverted timestamp, so
// a SMALLER number is a NEWER run.
func rec(t *testing.T, inv, id, command, status string) (string, []byte) {
	t.Helper()
	b, err := json.Marshal(opsalert.Record{ID: id, Command: command, Status: status})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return inv + "-" + id + ".json", b
}

func readerOver(t *testing.T, objects map[string][]byte) (*ledgerReader, *s3blobtest.Fake) {
	t.Helper()
	f := s3blobtest.With(objects)
	return &ledgerReader{blob: f.Blob("b", "runledger/")}, f
}

// TestLedgerKeys_SeesTenantScopedRecords is the regression for rosterbot-3vr.
//
// layout.RunLedger is PerTenant, so every record written since the tenant
// cutover lives at runledger/user=<uid>/<key>.json. The reader skipped every
// key containing a slash — a filter that was right when it was written
// (rosterbot-432, stopping sub-objects from hiding the ledger block) and became
// a filter that excluded the entire ledger.
//
// In production this froze the notifier at 2026-08-14T17:26:37Z while jobs kept
// running: 14 false "has not run" alerts, and — far worse — Streak unable to
// observe any real failure at all.
func TestLedgerKeys_SeesTenantScopedRecords(t *testing.T) {
	k1, v1 := rec(t, "8213000000", "run-new", "optimize", "SUCCESS")
	r, _ := readerOver(t, map[string][]byte{"runledger/" + testTenant + k1: v1})

	keys, err := r.keys(context.Background(), 10)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %v, want the one tenant-scoped record; the notifier is blind", keys)
	}
	if !strings.Contains(keys[0], testTenant) {
		t.Errorf("keys[0] = %q, want the tenant-scoped key", keys[0])
	}
}

// TestLedgerKeys_OrdersAcrossPartitionsByTime is the subtle half, and the one a
// naive fix gets wrong.
//
// The "newest N" listing works because an inverted-timestamp key sorts
// lexicographically into reverse-chronological order. That holds WITHIN a
// partition and not across them: "user=" sorts after every digit, so ordering
// on the whole key puts every flat record ahead of every tenant record
// regardless of when they ran. Ordering has to be on the basename.
func TestLedgerKeys_OrdersAcrossPartitionsByTime(t *testing.T) {
	oldFlat, v1 := rec(t, "8213271619", "old", "optimize", "SUCCESS")   // 2026-08-14
	newTenant, v2 := rec(t, "8213013877", "new", "optimize", "SUCCESS") // 2026-08-17, NEWER

	r, _ := readerOver(t, map[string][]byte{
		"runledger/" + oldFlat:                v1,
		"runledger/" + testTenant + newTenant: v2,
	})

	keys, err := r.keys(context.Background(), 10)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want both records", keys)
	}
	if !strings.HasSuffix(keys[0], newTenant) {
		t.Errorf("newest = %q, want the tenant record %q — ordering on the whole key "+
			"puts flat records first no matter when they ran", keys[0], newTenant)
	}
}

// TestLedgerKeys_InterleavesTenants: with fan-out there is one partition per
// tenant, and a global "newest N" has to interleave them. Taking the newest N
// from whichever partition sorts first would report one tenant's history as the
// whole ledger.
func TestLedgerKeys_InterleavesTenants(t *testing.T) {
	a1, va1 := rec(t, "8213000001", "a-old", "optimize", "SUCCESS")
	b1, vb1 := rec(t, "8213000002", "b-older", "optimize", "SUCCESS")
	a2, va2 := rec(t, "8213000000", "a-newest", "optimize", "SUCCESS")

	r, _ := readerOver(t, map[string][]byte{
		"runledger/user=alice/" + a1: va1,
		"runledger/user=bob/" + b1:   vb1,
		"runledger/user=alice/" + a2: va2,
	})

	keys, err := r.keys(context.Background(), 10)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	want := []string{a2, a1, b1} // newest first, by inverted timestamp
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %d records", keys, len(want))
	}
	for i, w := range want {
		if !strings.HasSuffix(keys[i], w) {
			t.Errorf("keys[%d] = %q, want suffix %q", i, keys[i], w)
		}
	}
}

// TestLedgerKeys_KeepsPreMigrationHistory: the cutover COPIED rather than moved,
// so months of flat records are real history. A fix that only read tenant
// partitions would throw them away.
func TestLedgerKeys_KeepsPreMigrationHistory(t *testing.T) {
	flat, v1 := rec(t, "8213500000", "historic", "optimize", "SUCCESS")
	r, _ := readerOver(t, map[string][]byte{"runledger/" + flat: v1})

	keys, err := r.keys(context.Background(), 10)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %v, want the pre-migration flat record", keys)
	}
}

// TestLedgerKeys_IgnoresNonRecords preserves what the slash filter was actually
// protecting (rosterbot-432): objects that merely share the prefix must not be
// mistaken for ledger records.
//
// The discriminator is now the record's SHAPE rather than its depth, which is
// strictly better — it excludes a stray object at the top level too, which the
// slash test never did.
func TestLedgerKeys_IgnoresNonRecords(t *testing.T) {
	good, v := rec(t, "8213000000", "real", "optimize", "SUCCESS")
	r, _ := readerOver(t, map[string][]byte{
		"runledger/" + good:                        v,
		"runledger/index.json":                     []byte("{}"),
		"runledger/notes.txt":                      []byte("x"),
		"runledger/" + testTenant + "summary.json": []byte("{}"),
		"runledger/user=alice/nested/deep.json":    []byte("{}"),
	})

	keys, err := r.keys(context.Background(), 10)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 1 || !strings.HasSuffix(keys[0], good) {
		t.Fatalf("keys = %v, want only the one real record", keys)
	}
}

// TestLedgerKeys_RespectsTheLimit keeps the fetch bounded. The listing has to
// see every key to order globally, but the expensive part — one GetObject per
// record — must still stop at the window.
func TestLedgerKeys_RespectsTheLimit(t *testing.T) {
	objects := map[string][]byte{}
	for _, inv := range []string{"8213000000", "8213000001", "8213000002", "8213000003"} {
		k, v := rec(t, inv, "id"+inv, "optimize", "SUCCESS")
		objects["runledger/"+testTenant+k] = v
	}
	r, _ := readerOver(t, objects)

	keys, err := r.keys(context.Background(), 2)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want 2", keys)
	}
	if !strings.Contains(keys[0], "8213000000") || !strings.Contains(keys[1], "8213000001") {
		t.Errorf("keys = %v, want the two NEWEST", keys)
	}
}

// TestLedgerKeys_PagesThroughEveryPartition: a single-page listing is the
// rosterbot-432 bug class, and it is worse now — with partitions, one page can
// easily be filled entirely by one tenant, hiding every other tenant's records
// behind a continuation token.
func TestLedgerKeys_PagesThroughEveryPartition(t *testing.T) {
	objects := map[string][]byte{}
	for i := 0; i < 30; i++ {
		k, v := rec(t, "82130000"+string(rune('0'+i/10))+string(rune('0'+i%10)), "alice", "optimize", "SUCCESS")
		objects["runledger/user=alice/"+k] = v
	}
	// Strictly newer than every alice key (which run 8213000000..8213000029),
	// so this asserts paging rather than a basename tie-break.
	newest, v := rec(t, "8212999999", "bob-newest", "optimize", "SUCCESS")
	objects["runledger/user=bob/"+newest] = v

	f := s3blobtest.With(objects)
	f.PageSize = 5
	r := &ledgerReader{blob: f.Blob("b", "runledger/")}

	keys, err := r.keys(context.Background(), 1)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if f.ListCalls < 2 {
		t.Fatalf("ListCalls = %d; the walk did not page", f.ListCalls)
	}
	if len(keys) != 1 || !strings.HasSuffix(keys[0], newest) {
		t.Fatalf("keys = %v, want bob's newest record, which sits on a later page", keys)
	}
}

// TestIsLedgerRecord pins the shape discriminator directly. The format is
// lineupapi.RunKey's fmt.Sprintf("%010d-%s", inv, id) plus ".json"; the
// zero-padding is what makes lexicographic order equal chronological order.
func TestIsLedgerRecord(t *testing.T) {
	for _, tc := range []struct {
		base string
		want bool
	}{
		{"8213271619-7b654f41cc4d4cb8a4085e182a8c1f25.json", true},
		{"8218268822-local-20260617211937.json", true},
		{"index.json", false},
		{"summary.json", false},
		{"notes.txt", false},
		{"8213271619.json", false}, // no id segment
		{"-abc.json", false},       // no timestamp
		{"abc-def.json", false},    // timestamp is not numeric
	} {
		if got := isLedgerRecord(tc.base); got != tc.want {
			t.Errorf("isLedgerRecord(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}
