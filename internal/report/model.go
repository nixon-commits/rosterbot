package report

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// Scorecard holds the current window's metrics plus the equal-length prior
// window for delta display.
type Scorecard struct {
	Cur   Metrics `json:"cur"`
	Prior Metrics `json:"prior"`
}

// View is the fully precomputed dashboard for one (window, role) pair.
type View struct {
	Window    int           `json:"window"`
	Role      string        `json:"role"`
	Scorecard Scorecard     `json:"scorecard"`
	ByPos     []PositionRow `json:"byPos"`
	Calib     []CalibPoint  `json:"calib"`
	Misses    []Miss        `json:"misses"`
	Insights  []Insight     `json:"insights"`
}

// Model is the complete payload embedded into the dashboard HTML.
type Model struct {
	GeneratedAt string                  `json:"generatedAt"`
	SeasonStart string                  `json:"seasonStart"`
	LatestDate  string                  `json:"latestDate"`
	Windows     []int                   `json:"windows"` // [7,14,30,0]; 0 = season
	Roles       []string                `json:"roles"`   // ["all","hitters","pitchers"]
	Trends      map[string][]TrendPoint `json:"trends"`  // keyed by role
	Views       map[string]View         `json:"views"`   // keyed "window|role"
}

var (
	stdWindows = []int{7, 14, 30, 0}
	stdRoles   = []string{"all", "hitters", "pitchers"}
)

func windowLabel(w int) string {
	if w <= 0 {
		return "season"
	}
	return fmt.Sprintf("%dd", w)
}

func viewKey(window int, role string) string { return fmt.Sprintf("%d|%s", window, role) }

// Aggregate builds the full embedded Model from graded rows. generatedAt stamps
// the render time; seasonStart is a display floor. Pure: no I/O.
func Aggregate(rows []analysis.GradeRow, generatedAt, seasonStart time.Time) *Model {
	latest := seasonStart
	for _, r := range rows {
		if d, err := time.Parse("2006-01-02", r.Dt); err == nil && d.After(latest) {
			latest = d
		}
	}
	m := &Model{
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		SeasonStart: seasonStart.Format("2006-01-02"),
		LatestDate:  latest.Format("2006-01-02"),
		Windows:     stdWindows,
		Roles:       stdRoles,
		Trends:      map[string][]TrendPoint{},
		Views:       map[string]View{},
	}
	for _, role := range stdRoles {
		rr := filterRole(rows, role)
		m.Trends[role] = rollingTrend(rr, 7)
		for _, w := range stdWindows {
			cur := windowRows(rr, latest, w)
			prior := priorWindowRows(rr, latest, w)
			curM := computeMetrics(cur)
			priorM := computeMetrics(prior)
			bp := byPosition(cur)
			m.Views[viewKey(w, role)] = View{
				Window:    w,
				Role:      role,
				Scorecard: Scorecard{Cur: curM, Prior: priorM},
				ByPos:     bp,
				Calib:     calibration(cur),
				Misses:    worstMisses(cur, 25),
				Insights:  generateInsights(curM, priorM, bp, windowLabel(w)),
			}
		}
	}
	return m
}
