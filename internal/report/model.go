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
//
// The detailed dashboard (Views/Trends) is computed from the detailSystem slice
// only — the bot's production projection — so its meaning is unchanged. Compare
// and CompareTrends span every captured system for the head-to-head panel.
type Model struct {
	GeneratedAt   string                             `json:"generatedAt"`
	SeasonStart   string                             `json:"seasonStart"`
	LatestDate    string                             `json:"latestDate"`
	Windows       []int                              `json:"windows"`       // [7,14,30,0]; 0 = season
	Roles         []string                           `json:"roles"`         // ["all","hitters","pitchers"]
	Systems       []string                           `json:"systems"`       // projection systems present, sorted
	DetailSystem  string                             `json:"detailSystem"`  // system feeding Views/Trends
	Trends        map[string][]TrendPoint            `json:"trends"`        // keyed "window|role"
	Views         map[string]View                    `json:"views"`         // keyed "window|role"
	Compare       map[string][]SystemScore           `json:"compare"`       // keyed "window|role" → systems ranked by MAE
	CompareTrends map[string]map[string][]TrendPoint `json:"compareTrends"` // keyed "window|role" → system → trend
}

var (
	stdWindows = []int{7, 14, 30, 0}
	stdRoles   = []string{"all", "hitters", "pitchers"}
)

// detailSystem is the projection system whose slice drives the detailed
// dashboard (scorecard, by-position, calibration, misses). It is the bot's
// production system — the same one legacy pre-migration grades are attributed
// to — so the existing views keep meaning "how accurate is what we ship".
const detailSystem = analysis.LegacySystem

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
	// Rows always carry a System when read through the Analysis Store readers
	// (legacy partitions are attributed to detailSystem there). Normalize here
	// too so any un-attributed input is treated as the production system rather
	// than silently dropped from the detail dashboard.
	rows = normalizeSystems(rows)

	latest := seasonStart
	for _, r := range rows {
		if d, err := time.Parse("2006-01-02", r.Dt); err == nil && d.After(latest) {
			latest = d
		}
	}
	m := &Model{
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339),
		SeasonStart:   seasonStart.Format("2006-01-02"),
		LatestDate:    latest.Format("2006-01-02"),
		Windows:       stdWindows,
		Roles:         stdRoles,
		Systems:       distinctSystems(rows),
		DetailSystem:  detailSystem,
		Trends:        map[string][]TrendPoint{},
		Views:         map[string]View{},
		Compare:       map[string][]SystemScore{},
		CompareTrends: map[string]map[string][]TrendPoint{},
	}

	// Detailed dashboard: the production system's slice only.
	detailRows := filterSystem(rows, detailSystem)
	for _, role := range stdRoles {
		rr := filterRole(detailRows, role)
		for _, w := range stdWindows {
			// Trend is keyed by window|role and spans the window's date range:
			// daily points over the last w days for w>0, rolling-7 over the whole
			// season for w==0. Lets the WINDOW toggle drive the chart's x-axis.
			m.Trends[viewKey(w, role)] = windowTrend(rr, latest, w)
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

	// Comparison panel: every captured system, ranked head-to-head per window×role.
	for _, role := range stdRoles {
		for _, w := range stdWindows {
			key := viewKey(w, role)
			m.Compare[key] = rankSystems(rows, m.Systems, latest, w, role)
			trends := map[string][]TrendPoint{}
			for _, sys := range m.Systems {
				sysRole := filterRole(filterSystem(rows, sys), role)
				trends[sys] = windowTrend(sysRole, latest, w)
			}
			m.CompareTrends[key] = trends
		}
	}
	return m
}
