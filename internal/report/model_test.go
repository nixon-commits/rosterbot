// internal/report/model_test.go
package report

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func TestRollingTrend(t *testing.T) {
	rows := []analysis.GradeRow{
		{Dt: "2026-06-14", Diff: 2}, {Dt: "2026-06-15", Diff: -4},
	}
	tp := rollingTrend(rows, 7)
	if len(tp) != 2 || tp[0].Date != "2026-06-14" || tp[1].Date != "2026-06-15" {
		t.Fatalf("trend dates: %+v", tp)
	}
	// 7-day rolling on 06-15 includes both rows -> MAE = (2+4)/2 = 3
	if tp[1].MAE != 3 {
		t.Fatalf("rolling MAE: %+v", tp[1])
	}
}

func TestGenerateInsights_BiasAndImprovement(t *testing.T) {
	cur := Metrics{MAE: 3, Bias: 1.5, N: 500}
	prior := Metrics{MAE: 4, Bias: 0, N: 500}
	ins := generateInsights(cur, prior, nil, "14d")
	var sawBias, sawImprove bool
	for _, i := range ins {
		if contains(i.Text, "under-projecting") {
			sawBias = true
		}
		if contains(i.Text, "improved") {
			sawImprove = true
		}
	}
	if !sawBias || !sawImprove {
		t.Fatalf("expected bias + improvement insights, got %+v", ins)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestAggregate_KeysAndShape(t *testing.T) {
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		{Dt: "2026-06-14", PlayerID: "1", Bucket: "OF", Projected: 5, Actual: 7, Diff: 2},
		{Dt: "2026-06-15", PlayerID: "2", Bucket: "SP", IsPitcher: true, Projected: 8, Actual: 4, Diff: -4},
	}
	m := Aggregate(rows, gen, seasonStart)
	if m.LatestDate != "2026-06-15" {
		t.Fatalf("latest: %q", m.LatestDate)
	}
	if len(m.Windows) != 4 || len(m.Roles) != 3 {
		t.Fatalf("windows/roles: %+v %+v", m.Windows, m.Roles)
	}
	// 4 windows x 3 roles = 12 views
	if len(m.Views) != 12 {
		t.Fatalf("want 12 views, got %d", len(m.Views))
	}
	if _, ok := m.Views["0|all"]; !ok {
		t.Fatalf("missing season|all view; keys=%v", m.Views)
	}
	if _, ok := m.Trends["pitchers"]; !ok {
		t.Fatalf("missing pitchers trend")
	}
	// season|all should see both rows
	if m.Views["0|all"].Scorecard.Cur.N != 2 {
		t.Fatalf("season|all N: %+v", m.Views["0|all"].Scorecard.Cur)
	}
}

func TestAggregate_Empty(t *testing.T) {
	m := Aggregate(nil, time.Now(), time.Now())
	if len(m.Views) != 12 {
		t.Fatalf("want 12 (empty) views, got %d", len(m.Views))
	}
	if m.Views["7|all"].Scorecard.Cur.N != 0 {
		t.Fatalf("empty view should have N=0")
	}
}
