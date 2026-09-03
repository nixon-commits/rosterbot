package analysis

import "testing"

// TestExcludedGrade_StalePairsFlagged pins the presence side: every one of the
// six known-stale (date, system) partitions from rosterbot-c61b must be
// flagged, on the failing path this exists to catch (a consumer that reads
// the table wrong and lets stale rows back into model input).
func TestExcludedGrade_StalePairsFlagged(t *testing.T) {
	stale := []struct{ dt, system string }{
		{"2026-08-19", "atc-ros"},
		{"2026-08-20", "atc-ros"},
		{"2026-08-21", "atc-ros"},
		{"2026-08-19", "thebatx-ros"},
		{"2026-08-20", "thebatx-ros"},
		{"2026-08-21", "thebatx-ros"},
	}
	for _, s := range stale {
		if !ExcludedGrade(s.dt, s.system) {
			t.Errorf("ExcludedGrade(%q, %q) = false, want true", s.dt, s.system)
		}
	}
}

// TestExcludedGrade_CleanPairsPass pins the absence side: a genuinely healthy
// pair -- same date under an unaffected system, same system on an unaffected
// date, or an entirely different day -- must NOT be flagged. Without this an
// over-broad match (e.g. matching on date alone) would silently drop clean
// systems too.
func TestExcludedGrade_CleanPairsPass(t *testing.T) {
	clean := []struct{ dt, system string }{
		{"2026-08-19", "depthcharts-ros"}, // same date, unaffected system (hourly refresh)
		{"2026-08-19", "steamer-ros"},     // same date, unaffected system
		{"2026-08-18", "atc-ros"},         // affected system, unaffected date (day before)
		{"2026-08-22", "atc-ros"},         // affected system, unaffected date (day after)
		{"2026-08-19", "thebatx"},         // similar but distinct system name
	}
	for _, c := range clean {
		if ExcludedGrade(c.dt, c.system) {
			t.Errorf("ExcludedGrade(%q, %q) = true, want false", c.dt, c.system)
		}
	}
}

func TestExclusions_CitesBothBeads(t *testing.T) {
	table := Exclusions()
	if len(table) != 6 {
		t.Fatalf("want 6 exclusion entries, got %d", len(table))
	}
	for _, e := range table {
		if e.Reason == "" {
			t.Errorf("entry %+v has no Reason", e)
		}
		if e.Bead == "" || !contains(e.Bead, "rosterbot-c61b") || !contains(e.Bead, "rosterbot-sagc") {
			t.Errorf("entry %+v does not cite both beads", e)
		}
	}
}

// Exclusions returns a copy: mutating it must never reach the standing table,
// or one caller's slice edit would silently change what every other caller
// sees as excluded.
func TestExclusions_ReturnsACopy(t *testing.T) {
	table := Exclusions()
	table[0].System = "mutated"
	if ExcludedGrade("2026-08-19", "mutated") {
		t.Fatal("mutating the returned slice leaked into the standing table")
	}
	if !ExcludedGrade("2026-08-19", "atc-ros") {
		t.Fatal("mutating the returned slice corrupted the standing table's original entry")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
