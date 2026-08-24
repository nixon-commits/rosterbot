package s3lineup

import (
	"context"
	"fmt"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func listGrades(t *testing.T, objs map[string][]byte) (skipped, partitions []string, n int) {
	t.Helper()
	f := s3blobtest.With(objs)
	f.PageSize = 1000
	got, err := (&InfraStore{blob: f.Blob("b", "")}).ListPrefix(context.Background(), "analysis/grades/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	return got.SkippedDays, got.Partitions, got.Objects
}

func has(days []string, want string) bool {
	for _, d := range days {
		if d == want {
			return true
		}
	}
	return false
}

// The ordinary case: a day whose only object is the marker is reported skipped,
// and a day with real grades beside a stale marker is not.
func TestListPrefix_SkippedDaysNeedTheMarkerToBeAlone(t *testing.T) {
	skipped, parts, _ := listGrades(t, map[string][]byte{
		"analysis/grades/dt=2026-07-13/no-actuals.json":                      []byte("{}"),
		"analysis/grades/dt=2026-07-14/no-actuals.json":                      []byte("{}"),
		"analysis/grades/dt=2026-07-14/system=depthcharts-ros/grades.ndjson": []byte("row"),
		"analysis/grades/dt=2026-07-15/system=depthcharts-ros/grades.ndjson": []byte("row"),
	})
	if !has(skipped, "2026-07-13") {
		t.Errorf("skipped = %v, want 2026-07-13 (marker alone)", skipped)
	}
	if has(skipped, "2026-07-14") {
		t.Errorf("skipped = %v, must not include 2026-07-14 — real grades sit beside the stale marker", skipped)
	}
	if has(skipped, "2026-07-15") {
		t.Errorf("skipped = %v, must not include 2026-07-15 — no marker at all", skipped)
	}
	if len(parts) != 3 {
		t.Errorf("partitions = %v, want all three days still counted", parts)
	}
}

// A skipped day is a POSITIVE claim — "the marker is the ONLY object here" —
// and a walk that stopped early cannot support it.
//
// Within a dt= day S3 returns keys lexicographically, and "no-actuals.json"
// sorts before "system=…" (n < s), so a walk truncating between them sees the
// marker and not the grades beside it. Before this guard that day was reported
// as deliberately skipped when it had in fact been graded — a false label,
// which is the exact failure this whole feature exists to remove.
func TestListPrefix_TruncatedWalkWithholdsSkippedDays(t *testing.T) {
	objs := map[string][]byte{}
	// Filler that sorts before the target day and exhausts the budget.
	for i := 0; i < maxKeys-1; i++ {
		objs[fmt.Sprintf("analysis/grades/dt=2020-01-01/filler-%05d.json", i)] = []byte("x")
	}
	objs["analysis/grades/dt=2026-08-20/no-actuals.json"] = []byte("{}")
	objs["analysis/grades/dt=2026-08-20/system=depthcharts-ros/grades.ndjson"] = []byte("real")

	skipped, _, n := listGrades(t, objs)
	if n < maxKeys {
		t.Fatalf("fixture did not truncate: %d objects seen, need >= %d", n, maxKeys)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none — a truncated walk cannot prove any day is marker-only", skipped)
	}
}
