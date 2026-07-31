package lineupgap

import (
	"math"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// seriesRows builds `n` consecutive days ending at `end`, each losing `gap`
// points against a 100-point optimal.
func seriesRows(end time.Time, n int, gap float64) []Row {
	var out []Row
	for i := n - 1; i >= 0; i-- {
		d := end.AddDate(0, 0, -i)
		out = append(out, Row{
			Dt:         d.Format("2006-01-02"),
			ActualPts:  100 + gap,
			OptimalPts: 100,
			Gap:        gap,
			StartedN:   13,
		})
	}
	return out
}

func TestBuildModel_WindowSums(t *testing.T) {
	end := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	rows := seriesRows(end, 40, -5) // 40 days, 5 points lost per day
	m := BuildModel(rows, time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC))

	if m.LatestDate != "2026-07-30" {
		t.Errorf("LatestDate = %q, want 2026-07-30", m.LatestDate)
	}
	if len(m.Series) != 40 {
		t.Fatalf("Series has %d points, want 40", len(m.Series))
	}
	if m.Series[0].Dt >= m.Series[len(m.Series)-1].Dt {
		t.Error("Series must be ascending by date")
	}

	w7 := m.Windows["7"]
	if w7.Days != 7 {
		t.Errorf("7d Days = %d, want 7", w7.Days)
	}
	if !approx(w7.GapPts, -35) {
		t.Errorf("7d GapPts = %v, want -35", w7.GapPts)
	}
	if !approx(w7.OptimalPts, 700) {
		t.Errorf("7d OptimalPts = %v, want 700", w7.OptimalPts)
	}
	if !approx(w7.Efficiency, 665.0/700.0) {
		t.Errorf("7d Efficiency = %v, want %v", w7.Efficiency, 665.0/700.0)
	}

	// Season window ("0") spans everything.
	if s := m.Windows["0"]; s.Days != 40 || !approx(s.GapPts, -200) {
		t.Errorf("season window = %+v, want 40 days / -200 gap", s)
	}
}

func TestBuildModel_EmptyRows(t *testing.T) {
	m := BuildModel(nil, time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC))
	if m == nil {
		t.Fatal("BuildModel must return a valid empty model, not nil")
	}
	if len(m.Series) != 0 {
		t.Errorf("Series = %d points, want 0", len(m.Series))
	}
	if m.LatestDate != "" {
		t.Errorf("LatestDate = %q, want empty", m.LatestDate)
	}
	for _, k := range []string{"7", "14", "30", "0"} {
		if w, ok := m.Windows[k]; !ok || w.Days != 0 {
			t.Errorf("window %q must exist and be zeroed, got %+v ok=%v", k, w, ok)
		}
	}
}

func TestBuildModel_ZeroOptimalDoesNotDivideByZero(t *testing.T) {
	m := BuildModel([]Row{{Dt: "2026-07-30", ActualPts: 0, OptimalPts: 0}},
		time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC))
	if e := m.Windows["7"].Efficiency; math.IsNaN(e) || math.IsInf(e, 0) {
		t.Errorf("Efficiency = %v, want a finite value", e)
	}
}
