package lineupgap

import (
	"sort"
	"strconv"
	"time"
)

// stdWindows mirrors report.stdWindows so the dashboard's existing window
// toggle indexes Model.Windows directly. 0 is the season window.
var stdWindows = []int{7, 14, 30, 0}

// Model is the payload written to gap.json for the dashboard SPA.
type Model struct {
	GeneratedAt string                   `json:"generatedAt"`
	LatestDate  string                   `json:"latestDate"`
	Windows     map[string]WindowSummary `json:"windows"` // keyed "7","14","30","0"
	Series      []Point                  `json:"series"`  // daily, ascending
}

// WindowSummary is the gap rolled up over one trailing window.
type WindowSummary struct {
	Days       int     `json:"days"`
	ActualPts  float64 `json:"actualPts"`
	OptimalPts float64 `json:"optimalPts"`
	GapPts     float64 `json:"gapPts"`
	Efficiency float64 `json:"efficiency"`
}

// Point is one day on the gap chart.
type Point struct {
	Dt         string  `json:"dt"`
	GapPts     float64 `json:"gapPts"`
	ActualPts  float64 `json:"actualPts"`
	OptimalPts float64 `json:"optimalPts"`
}

// BuildModel rolls the stored series into the precomputed windows and the daily
// chart series. Pure: no I/O.
//
// An empty input still yields a valid Model with every window present and
// zeroed, so the dashboard renders "no data yet" rather than erroring.
func BuildModel(rows []Row, generatedAt time.Time) *Model {
	sorted := make([]Row, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Dt < sorted[j].Dt })

	m := &Model{
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		Windows:     map[string]WindowSummary{},
		Series:      make([]Point, 0, len(sorted)),
	}
	for _, r := range sorted {
		m.Series = append(m.Series, Point{
			Dt: r.Dt, GapPts: r.Gap, ActualPts: r.ActualPts, OptimalPts: r.OptimalPts,
		})
	}
	if len(sorted) > 0 {
		m.LatestDate = sorted[len(sorted)-1].Dt
	}

	for _, w := range stdWindows {
		m.Windows[strconv.Itoa(w)] = summarize(sorted, m.LatestDate, w)
	}
	return m
}

// summarize sums the last `window` days ending at latest (inclusive).
// window <= 0 covers the whole series. ISO date strings sort lexicographically,
// so string comparison is correct — the same trick internal/report uses.
func summarize(rows []Row, latest string, window int) WindowSummary {
	var cutoff string
	if window > 0 && latest != "" {
		d, err := time.Parse("2006-01-02", latest)
		if err != nil {
			return WindowSummary{}
		}
		cutoff = d.AddDate(0, 0, -(window - 1)).Format("2006-01-02")
	}

	var s WindowSummary
	for _, r := range rows {
		if cutoff != "" && r.Dt < cutoff {
			continue
		}
		s.Days++
		s.ActualPts += r.ActualPts
		s.OptimalPts += r.OptimalPts
		s.GapPts += r.Gap
	}
	if s.OptimalPts != 0 {
		s.Efficiency = s.ActualPts / s.OptimalPts
	}
	return s
}
