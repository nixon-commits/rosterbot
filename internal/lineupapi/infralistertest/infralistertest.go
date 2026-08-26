// Package infralistertest is the shared conformance suite for
// lineupapi.InfraLister implementations. The Infra page's verdicts are only
// as good as the listing beneath them, and the listing is computed twice —
// once over S3 for the deployed Lambda, once over the local tree for
// `serve` — from the SAME key grammar (dt= partitions, YYYY-MM-DD.<ext>
// basenames, system= sub-dimensions, marker-only skipped days). This suite
// seeds one canonical layout through each adapter's own writer and asserts
// the two listers read it identically.
//
// Deliberately OUTSIDE the contract, each for its own reason:
//   - LastModified: file mtime vs S3 put-time are different clocks; asserting
//     equality would pin an accident.
//   - Truncated: the file walker has no cap by design; the S3 cap has its own
//     dedicated tests (s3lineup's infra_truncated_test.go).
//   - Tenants: computed by the S3 lister for the deployed page; the local
//     lister does not break out user= segments today. Pinning parity there
//     would first require building it — a separate decision, not a drift.
package infralistertest

import (
	"context"
	"reflect"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Seed plants one object at prefix+remainder through the adapter's own
// storage, so the contract describes layouts, not storage mechanics.
type Seed func(remainder string, data []byte)

// Run exercises the InfraLister contract. newLister must return a
// freshly-empty lister, the artifact prefix it serves, and a Seed bound to
// that prefix.
func Run(t *testing.T, newLister func(t *testing.T) (lineupapi.InfraLister, string, Seed)) {
	t.Helper()
	ctx := context.Background()

	t.Run("EmptyPrefixListsZero", func(t *testing.T) {
		lister, prefix, _ := newLister(t)
		got, err := lister.ListPrefix(ctx, prefix)
		if err != nil {
			t.Fatalf("ListPrefix(empty): %v", err)
		}
		if got.Objects != 0 || got.Bytes != 0 || len(got.Partitions) != 0 || len(got.SkippedDays) != 0 {
			t.Fatalf("ListPrefix(empty) = %+v, want a zero listing", got)
		}
	})

	t.Run("CanonicalLayoutReadsIdentically", func(t *testing.T) {
		lister, prefix, seed := newLister(t)

		grades := []byte(`{"player":"a","err":0.5}` + "\n")
		marker := []byte(`{"reason":"no baseball"}`)
		flat := []byte(`{"snapshot":true}`)

		// A graded day whose skip marker sits BESIDE real rows (not skipped —
		// grade re-graded it later, the exact rosterbot-36r trap), a
		// marker-only day (skipped), a second graded day under another
		// system, and a flat basename-dated file.
		seed("dt=2026-08-01/system=steamer/grades.ndjson", grades)
		seed("dt=2026-08-01/"+analysis.SkipMarkerFilename, marker)
		seed("dt=2026-08-02/"+analysis.SkipMarkerFilename, marker)
		seed("dt=2026-08-03/system=atc/grades.ndjson", grades)
		seed("2026-08-04.json", flat)

		got, err := lister.ListPrefix(ctx, prefix)
		if err != nil {
			t.Fatalf("ListPrefix: %v", err)
		}
		if got.Objects != 5 {
			t.Fatalf("Objects = %d, want 5", got.Objects)
		}
		wantBytes := int64(2*len(grades) + 2*len(marker) + len(flat))
		if got.Bytes != wantBytes {
			t.Fatalf("Bytes = %d, want %d", got.Bytes, wantBytes)
		}
		wantParts := []string{"2026-08-01", "2026-08-02", "2026-08-03", "2026-08-04"}
		if !reflect.DeepEqual(got.Partitions, wantParts) {
			t.Fatalf("Partitions = %v, want %v (dt= AND basename days, sorted)", got.Partitions, wantParts)
		}
		if want := []string{"atc", "steamer"}; !reflect.DeepEqual(got.Subkeys, want) {
			t.Fatalf("Subkeys = %v, want %v", got.Subkeys, want)
		}
		if want := []string{"2026-08-02"}; !reflect.DeepEqual(got.SkippedDays, want) {
			t.Fatalf("SkippedDays = %v, want %v — a marker beside real rows must NOT count", got.SkippedDays, want)
		}
	})
}
