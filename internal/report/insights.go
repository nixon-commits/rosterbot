package report

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// Insight is one auto-generated plain-language callout.
type Insight struct {
	Severity string `json:"severity"` // "info" | "warn"
	Text     string `json:"text"`
}

// TrendPoint is a rolling-window accuracy reading for one date.
type TrendPoint struct {
	Date string  `json:"date"`
	MAE  float64 `json:"mae"`
	Bias float64 `json:"bias"`
}

// rollingTrend computes a trailing roll-day MAE/bias for each date that has
// grades, ascending. O(D*R) — fine for a season of small data.
func rollingTrend(rows []analysis.GradeRow, roll int) []TrendPoint {
	dates := map[string]bool{}
	for _, r := range rows {
		dates[r.Dt] = true
	}
	ds := make([]string, 0, len(dates))
	for d := range dates {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	var out []TrendPoint
	for _, d := range ds {
		dt, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		lo := dt.AddDate(0, 0, -(roll - 1)).Format("2006-01-02")
		var win []analysis.GradeRow
		for _, r := range rows {
			if r.Dt >= lo && r.Dt <= d {
				win = append(win, r)
			}
		}
		m := computeMetrics(win)
		out = append(out, TrendPoint{Date: d, MAE: m.MAE, Bias: m.Bias})
	}
	return out
}

// generateInsights derives plain-language callouts from a window's aggregates.
// Thresholds are intentionally simple and centralized here for easy tuning.
func generateInsights(cur, prior Metrics, byPos []PositionRow, windowLabel string) []Insight {
	var out []Insight

	if math.Abs(cur.Bias) >= 0.5 {
		sev := "info"
		if math.Abs(cur.Bias) >= 1.0 {
			sev = "warn"
		}
		dir := "over-projecting"
		if cur.Bias > 0 {
			dir = "under-projecting"
		}
		out = append(out, Insight{sev, fmt.Sprintf("Overall bias %+.1f over %s — systematically %s.", cur.Bias, windowLabel, dir)})
	}

	var weak, strong *PositionRow
	for i := range byPos {
		p := &byPos[i]
		if p.N < 20 {
			continue
		}
		if weak == nil || p.MAE > weak.MAE {
			weak = p
		}
		if strong == nil || p.MAE < strong.MAE {
			strong = p
		}
	}
	if weak != nil {
		out = append(out, Insight{"info", fmt.Sprintf("Weakest position: %s (MAE %.1f).", weak.Bucket, weak.MAE)})
	}
	if strong != nil && (weak == nil || strong.Bucket != weak.Bucket) {
		out = append(out, Insight{"info", fmt.Sprintf("Best-calibrated: %s (MAE %.1f).", strong.Bucket, strong.MAE)})
	}

	for _, p := range byPos {
		if p.N >= 30 && math.Abs(p.Bias) >= 1.0 {
			dir := "over"
			if p.Bias > 0 {
				dir = "under"
			}
			out = append(out, Insight{"warn", fmt.Sprintf("%s bias %+.1f — %s-projecting %s.", p.Bucket, p.Bias, dir, p.Bucket)})
		}
	}

	if prior.N > 0 && prior.MAE > 0 {
		change := (cur.MAE - prior.MAE) / prior.MAE
		if change <= -0.05 {
			out = append(out, Insight{"info", fmt.Sprintf("Accuracy improved %.0f%% vs the prior %s.", -change*100, windowLabel)})
		} else if change >= 0.05 {
			out = append(out, Insight{"warn", fmt.Sprintf("Accuracy degraded %.0f%% vs the prior %s.", change*100, windowLabel)})
		}
	}

	if cur.N > 0 && cur.N < 200 {
		out = append(out, Insight{"info", fmt.Sprintf("Thin sample (%d player-days) — interpret with caution.", cur.N)})
	}
	return out
}
