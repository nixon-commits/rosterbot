// internal/report/exclusions_test.go
package report

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// staleFixtureRows builds a store fixture with rows for the three
// rosterbot-c61b dates under both excluded systems (atc-ros, thebatx-ros),
// plus clean rows on the same three dates under an unaffected system
// (depthcharts-ros) and clean rows for atc-ros on an unaffected date. One row
// per (dt, system, playerID) so counts are easy to reason about.
func staleFixtureRows() []analysis.GradeRow {
	var rows []analysis.GradeRow
	staleDates := []string{"2026-08-19", "2026-08-20", "2026-08-21"}
	staleSystems := []string{"atc-ros", "thebatx-ros"}
	for _, dt := range staleDates {
		for _, sys := range staleSystems {
			// Two players per (dt, system) so a single excluded pair carries
			// more than one row -- pins that Rows counts rows, not pairs.
			rows = append(rows,
				analysis.GradeRow{Dt: dt, System: sys, PlayerID: "p1", Diff: 1},
				analysis.GradeRow{Dt: dt, System: sys, PlayerID: "p2", Diff: 2},
			)
		}
		// Clean: same date, unaffected system (the hourly Lineup job refreshes
		// depthcharts-ros in its clean overlap even inside the challenge window).
		rows = append(rows, analysis.GradeRow{Dt: dt, System: "depthcharts-ros", PlayerID: "p1", Diff: 3})
	}
	// Clean: affected system, unaffected date.
	rows = append(rows, analysis.GradeRow{Dt: "2026-08-25", System: "atc-ros", PlayerID: "p1", Diff: 4})
	return rows
}

func TestExcludeStaleGrades_DropsExactlyTheStalePairs(t *testing.T) {
	kept, excluded := excludeStaleGrades(staleFixtureRows())

	// 3 dates x 2 systems x 2 players = 12 excluded rows.
	wantExcluded := 12
	gotExcluded := 0
	for _, p := range excluded {
		gotExcluded += p.Rows
	}
	if gotExcluded != wantExcluded {
		t.Fatalf("excluded row total = %d, want %d (pairs=%+v)", gotExcluded, wantExcluded, excluded)
	}
	if len(excluded) != 6 {
		t.Fatalf("want 6 excluded (dt,system) pairs, got %d: %+v", len(excluded), excluded)
	}
	for _, p := range excluded {
		if p.Rows != 2 {
			t.Errorf("pair %s/%s: Rows=%d, want 2", p.Dt, p.System, p.Rows)
		}
		if p.Bead == "" || p.Reason == "" {
			t.Errorf("pair %s/%s missing disclosure fields: %+v", p.Dt, p.System, p)
		}
	}

	// Clean rows -- same dates under depthcharts-ros (3 rows) plus atc-ros on
	// an unaffected date (1 row) -- must survive untouched.
	wantKept := 4
	if len(kept) != wantKept {
		t.Fatalf("kept rows = %d, want %d: %+v", len(kept), wantKept, kept)
	}
	for _, r := range kept {
		if r.System == "atc-ros" && r.Dt != "2026-08-25" {
			t.Errorf("stale atc-ros row survived filtering: %+v", r)
		}
		if r.System == "thebatx-ros" {
			t.Errorf("thebatx-ros row survived filtering entirely: %+v", r)
		}
	}
}

func TestExcludeStaleGrades_NoMatchLeavesRowsAndPairsUntouched(t *testing.T) {
	rows := []analysis.GradeRow{
		{Dt: "2026-01-01", System: "depthcharts-ros", PlayerID: "p1"},
		{Dt: "2026-01-02", System: "steamer-ros", PlayerID: "p2"},
	}
	kept, excluded := excludeStaleGrades(rows)
	if len(kept) != len(rows) {
		t.Fatalf("kept = %d, want %d (no rows should match the exclusion table)", len(kept), len(rows))
	}
	if len(excluded) != 0 {
		t.Fatalf("want no disclosed pairs when nothing matched, got %+v", excluded)
	}
}

// TestAggregate_DisclosesExclusion is the end-to-end version through the
// public entry point every consumer actually calls: the Model itself must
// carry the count and the pairs, and every downstream figure (Systems list,
// per-system views, comparison ranking) must be built from the filtered rows.
func TestAggregate_DisclosesExclusion(t *testing.T) {
	seasonStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	m := Aggregate(staleFixtureRows(), gen, seasonStart)

	if m.ExcludedRows != 12 {
		t.Fatalf("Model.ExcludedRows = %d, want 12", m.ExcludedRows)
	}
	if len(m.Excluded) != 6 {
		t.Fatalf("Model.Excluded has %d pairs, want 6: %+v", len(m.Excluded), m.Excluded)
	}
	seen := map[[2]string]bool{}
	for _, p := range m.Excluded {
		seen[[2]string{p.Dt, p.System}] = true
	}
	for _, dt := range []string{"2026-08-19", "2026-08-20", "2026-08-21"} {
		for _, sys := range []string{"atc-ros", "thebatx-ros"} {
			if !seen[[2]string{dt, sys}] {
				t.Errorf("Model.Excluded missing (%s, %s)", dt, sys)
			}
		}
	}

	// thebatx-ros graded nothing but the excluded rows, so once they're
	// filtered it must vanish from the Systems list entirely -- proof the
	// exclusion runs before Systems is derived, not just before the row count.
	for _, sys := range m.Systems {
		if sys == "thebatx-ros" {
			t.Errorf("Systems still lists thebatx-ros after all its rows were excluded: %v", m.Systems)
		}
	}

	// atc-ros survives (it has one clean row on 2026-08-25) but a season
	// comparison view must not credit it with the 12 excluded rows.
	depthchartsKey := detailKey("depthcharts-ros", 0, "hitters")
	if v, ok := m.Views[depthchartsKey]; !ok || v.Scorecard.Cur.N != 3 {
		t.Fatalf("depthcharts-ros season|hitters view N = %+v, want 3 clean rows unaffected by the exclusion",
			m.Views[depthchartsKey].Scorecard.Cur)
	}
}
