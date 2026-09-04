//go:build diag

// Package lineuprun's start-rate diagnostic.
//
// This file answers the question rosterbot-goht makes a precondition of any
// change: does a rostered SP's own active-slot start rate disperse widely
// enough around the flat 1-in-5 that buildGSForecast assumes for it to change
// a gate or floor decision?
//
// It is build-tagged `diag` and reads only from a local Fantrax cache
// directory, so it never runs in CI, never authenticates, and never touches an
// upstream. Run it with:
//
//	DIAG_CACHE_DIR=/path/to/.cache go test -tags diag -run TestDiagStartRate -v ./internal/lineuprun/
//
// The cache dir is READ-ONLY here: every open is os.Open and nothing writes.
package lineuprun

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// diagEnvelope mirrors internal/cache's on-disk envelope.
type diagEnvelope struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Data      json.RawMessage `json:"data"`
}

// diagGSSnap mirrors fantrax.playerGSSnapshot (unexported there, so it is
// redeclared rather than imported). The json tags must match gs_check.go.
type diagGSSnap struct {
	GS      int     `json:"gs"`
	FPts    float64 `json:"fpts"`
	Name    string  `json:"name"`
	MLBTeam string  `json:"mlb_team"`
	Active  bool    `json:"active"`
}

// diagRosterRow mirrors fantrax.playerYTD, which carries no json tags and so
// marshals by Go field name.
type diagRosterRow struct {
	PlayerID      string
	Name          string
	MLBTeam       string
	PosShortNames string
	StatusID      string
	FPts          float64
	GP            int
	IsPitcher     bool
}

type diagRosterSnap struct {
	Pitchers map[string]diagRosterRow `json:"pitchers"`
}

func diagRead(t *testing.T, path string, into any) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var env diagEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	if err := json.Unmarshal(env.Data, into); err != nil {
		return false
	}
	return true
}

// diagSchedule is date → set of club abbreviations that played.
type diagSchedule map[string]map[string]bool

func diagLoadSchedule(t *testing.T, dir string) diagSchedule {
	t.Helper()
	out := diagSchedule{}
	paths, _ := filepath.Glob(filepath.Join(dir, "mlb-schedule-*.json"))
	for _, p := range paths {
		base := filepath.Base(p)
		ds := strings.TrimSuffix(strings.TrimPrefix(base, "mlb-schedule-"), ".json")
		var sched struct {
			Dates []struct {
				Games []struct {
					Teams struct {
						Away struct{ Team struct{ Abbreviation string } }
						Home struct{ Team struct{ Abbreviation string } }
					}
				}
			}
		}
		if !diagRead(t, p, &sched) {
			continue
		}
		clubs := map[string]bool{}
		for _, d := range sched.Dates {
			for _, g := range d.Games {
				clubs[g.Teams.Away.Team.Abbreviation] = true
				clubs[g.Teams.Home.Team.Abbreviation] = true
			}
		}
		out[ds] = clubs
	}
	return out
}

// diagPeriodDates inverts the authoritative date→daily-period map that
// fantrax.DailyPeriodFor consults, so the harness resolves a period to its
// date exactly the way the production walk does rather than by day math.
func diagPeriodDates(t *testing.T, dir string) map[int]time.Time {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "fantrax-period-date-map-*.json"))
	out := map[int]time.Time{}
	for _, p := range paths {
		var m map[string]int
		if !diagRead(t, p, &m) {
			continue
		}
		for ds, period := range m {
			d, err := time.Parse("2006-01-02", ds)
			if err != nil {
				continue
			}
			out[period] = d.UTC()
		}
	}
	return out
}

// diagTally is one (team, pitcher) pair's conversion record.
type diagTally struct {
	Team, PlayerID, Name string
	// Opportunities are days on which the pitcher was rostered, SP-eligible,
	// neither IL nor in the minors, and his club played — the same predicate
	// buildGSForecast uses to put a pitcher in its unannounced bucket.
	Opportunities int
	// ActiveStarts are active-slot starts, derived the way
	// GetTeamPitcherStarts derives them: a positive YTD GS delta against the
	// previous period while the later period shows the pitcher active.
	ActiveStarts int
	// MLBApps are real-world appearances, from the roster-stats snapshot's GP
	// delta. That snapshot is fetched with StatsType=1 (MLB stats), so unlike
	// the pitcher-GS snapshot it sees a pitcher's outing whether or not our
	// roster had him active. For an SP an appearance is a start in all but rare
	// cases, so this is the rotation-membership rate: what the pitcher's CLUB
	// did, independent of what our lineup decided.
	MLBApps int
}

func (d diagTally) ActiveRate() float64 {
	if d.Opportunities == 0 {
		return 0
	}
	return float64(d.ActiveStarts) / float64(d.Opportunities)
}

func (d diagTally) MLBRate() float64 {
	if d.Opportunities == 0 {
		return 0
	}
	return float64(d.MLBApps) / float64(d.Opportunities)
}

// diagDay is one team-day of reconstructed roster facts.
type diagDay struct {
	Date time.Time
	// SPs are the pitcher ids buildGSForecast would have considered that day
	// (SP-eligible, not IL, not in the minors) whose club played.
	SPs []string
	// ActiveStarts / MLBApps are that day's realised starts among them.
	ActiveStarts int
	MLBApps      int
	// MLBStarters are the ids that made a real-world appearance — the best
	// available retrospective proxy for "his club named him".
	MLBStarters map[string]bool
	// ActiveStarters are the ids that took an active-slot start, i.e. the ones
	// that actually spent weekly GS budget.
	ActiveStarters map[string]bool
}

func diagScan(t *testing.T, dir string) (map[string]*diagTally, map[string]map[string]*diagDay) {
	t.Helper()
	sched := diagLoadSchedule(t, dir)
	periodDate := diagPeriodDates(t, dir)
	if len(periodDate) == 0 {
		t.Fatalf("no fantrax-period-date-map-*.json under %s — cannot resolve the daily period axis", dir)
	}

	// Discover teams and their available periods from the snapshot filenames.
	type key struct {
		team   string
		season string
		period int
	}
	have := map[key]bool{}
	teams := map[string]string{} // team id -> season segment
	paths, _ := filepath.Glob(filepath.Join(dir, "fantrax-pitcher-gs-*.json"))
	for _, p := range paths {
		base := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "fantrax-pitcher-gs-"), ".json")
		parts := strings.Split(base, "-")
		if len(parts) != 3 {
			continue
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil {
			continue
		}
		teams[parts[0]] = parts[1]
		have[key{parts[0], parts[1], n}] = true
	}

	tallies := map[string]*diagTally{}
	days := map[string]map[string]*diagDay{}
	for team, season := range teams {
		days[team] = map[string]*diagDay{}
		for k := range have {
			if k.team != team || !have[key{team, season, k.period - 1}] {
				continue
			}
			date, ok := periodDate[k.period]
			if !ok {
				continue
			}
			ds := date.Format("2006-01-02")
			playing, ok := sched[ds]
			if !ok {
				continue // no schedule cached for that day; skip rather than guess
			}

			var prev, cur map[string]diagGSSnap
			var roster, prevRoster diagRosterSnap
			if !diagRead(t, filepath.Join(dir, fmt.Sprintf("fantrax-pitcher-gs-%s-%s-%d.json", team, season, k.period-1)), &prev) ||
				!diagRead(t, filepath.Join(dir, fmt.Sprintf("fantrax-pitcher-gs-%s-%s-%d.json", team, season, k.period)), &cur) ||
				!diagRead(t, filepath.Join(dir, fmt.Sprintf("fantrax-roster-stats-%s-%s-%d.json", team, season, k.period)), &roster) ||
				!diagRead(t, filepath.Join(dir, fmt.Sprintf("fantrax-roster-stats-%s-%s-%d.json", team, season, k.period-1)), &prevRoster) {
				continue
			}

			day := &diagDay{Date: date, MLBStarters: map[string]bool{}, ActiveStarters: map[string]bool{}}
			for pid, snap := range cur {
				meta, ok := roster.Pitchers[pid]
				if !ok {
					continue
				}
				// SP eligibility exactly as rosterSPNames decides it: the
				// PosShortNames string carries "SP". IL (StatusID 3) and
				// minors (StatusID 9) stand in for the Player.IsInjured /
				// Player.InMinors icons the pool response carries and this
				// snapshot does not.
				if !strings.Contains(meta.PosShortNames, "SP") {
					continue
				}
				if meta.StatusID == "3" || meta.StatusID == "9" {
					continue
				}
				club := snap.MLBTeam
				if club == "" {
					club = meta.MLBTeam
				}
				if !playing[club] {
					continue
				}
				day.SPs = append(day.SPs, pid)

				tk := team + "/" + pid
				tal := tallies[tk]
				if tal == nil {
					tal = &diagTally{Team: team, PlayerID: pid, Name: snap.Name}
					tallies[tk] = tal
				}
				tal.Opportunities++

				// Real-world appearance, from the StatsType=1 roster-stats GP
				// delta. Counted independently of the GS delta below, because
				// the two snapshots answer different questions.
				if prevMeta, ok := prevRoster.Pitchers[pid]; ok && meta.GP-prevMeta.GP >= 1 {
					day.MLBApps++
					day.MLBStarters[pid] = true
					tal.MLBApps++
				}

				prv, existed := prev[pid]
				if !existed {
					// First appearance: there is no previous YTD to diff
					// against, so no start can be credited. Same reasoning as
					// DailyFantasyPoints's first-appearance zeroing.
					continue
				}
				if snap.GS-prv.GS < 1 {
					continue
				}
				// The pitcher-GS snapshot is fetched with StatsType=2 (fantasy
				// team stats), so its GS only ever advances for a start our
				// roster captured in an active slot. snap.Active is asserted
				// anyway, to mirror GetTeamPitcherStarts exactly rather than to
				// rely on that coupling.
				if snap.Active {
					day.ActiveStarts++
					day.ActiveStarters[pid] = true
					tal.ActiveStarts++
				}
			}
			sort.Strings(day.SPs)
			days[team][ds] = day
		}
	}
	return tallies, days
}

func diagQuantile(sorted []float64, f float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := float64(len(sorted)-1) * f
	lo := int(i)
	hi := lo + 1
	if hi > len(sorted)-1 {
		hi = len(sorted) - 1
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(i-float64(lo))
}

// TestDiagStartRateDispersion reports the per-pitcher active-slot start rate
// distribution over every settled period pair on disk.
func TestDiagStartRateDispersion(t *testing.T) {
	dir := os.Getenv("DIAG_CACHE_DIR")
	if dir == "" {
		t.Skip("set DIAG_CACHE_DIR to a Fantrax cache directory")
	}
	tallies, _ := diagScan(t, dir)

	minOpp := 20
	if v := os.Getenv("DIAG_MIN_OPP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			minOpp = n
		}
	}

	var act, mlb []float64
	kept := make([]*diagTally, 0, len(tallies))
	for _, tal := range tallies {
		if tal.Opportunities < minOpp {
			continue
		}
		kept = append(kept, tal)
		act = append(act, tal.ActiveRate())
		mlb = append(mlb, tal.MLBRate())
	}
	if len(kept) == 0 {
		t.Fatalf("no pitcher reached %d opportunities under %s", minOpp, dir)
	}
	sort.Float64s(act)
	sort.Float64s(mlb)

	mean := func(v []float64) float64 {
		var s float64
		for _, x := range v {
			s += x
		}
		return s / float64(len(v))
	}
	sd := func(v []float64) float64 {
		m := mean(v)
		var s float64
		for _, x := range v {
			s += (x - m) * (x - m)
		}
		return math.Sqrt(s / float64(len(v)))
	}
	report := func(label string, v []float64) {
		t.Logf("%-16s n=%d mean=%.3f sd=%.3f min=%.3f p10=%.3f p25=%.3f med=%.3f p75=%.3f p90=%.3f max=%.3f",
			label, len(v), mean(v), sd(v), v[0],
			diagQuantile(v, 0.10), diagQuantile(v, 0.25), diagQuantile(v, 0.50),
			diagQuantile(v, 0.75), diagQuantile(v, 0.90), v[len(v)-1])
	}
	t.Logf("pitcher-seasons with >= %d club-game-day opportunities: %d (of %d observed)", minOpp, len(kept), len(tallies))
	t.Logf("flat assumption: 1/RotationSize = %.3f", 1/RotationSize)
	report("active-slot", act)
	report("mlb-appearance", mlb)
	t.Logf("share of pitchers below 0.10 active-slot rate: %.1f%%", 100*float64(countBelow(act, 0.10))/float64(len(act)))
	t.Logf("share of pitchers above 0.30 active-slot rate: %.1f%%", 100*float64(len(act)-countBelow(act, 0.30))/float64(len(act)))

	sort.Slice(kept, func(i, j int) bool { return kept[i].ActiveRate() > kept[j].ActiveRate() })
	t.Logf("--- top 15 by active-slot rate ---")
	for _, tal := range kept[:min(15, len(kept))] {
		t.Logf("  %-26s opp=%3d act=%3d app=%3d actRate=%.3f appRate=%.3f", tal.Name, tal.Opportunities, tal.ActiveStarts, tal.MLBApps, tal.ActiveRate(), tal.MLBRate())
	}
	t.Logf("--- bottom 15 by active-slot rate ---")
	for _, tal := range kept[max(0, len(kept)-15):] {
		t.Logf("  %-26s opp=%3d act=%3d app=%3d actRate=%.3f appRate=%.3f", tal.Name, tal.Opportunities, tal.ActiveStarts, tal.MLBApps, tal.ActiveRate(), tal.MLBRate())
	}
}

func countBelow(sorted []float64, x float64) int {
	n := 0
	for _, v := range sorted {
		if v < x {
			n++
		}
	}
	return n
}

// TestDiagStartRateDecisionReplay replays each completed matchup week's
// forecast under the flat 1/5 and under per-pitcher weighting, and counts the
// gate and floor decisions that would have flipped.
//
// The weighting only ever moves the ESTIMATE, so the replay holds today's
// starters and the future confirmed starts fixed and varies nothing else. Two
// announcement regimes bracket the answer, because what a club had announced
// at forecast time is not recoverable from the cache:
//
//	"open"  — no future club has named anyone yet. Every rostered SP whose
//	          club plays lands in the estimate. This is the regime the
//	          weighting can move at all, and the upper bound on its effect.
//	"named" — every club that played had named someone. Our actual MLB
//	          starters are confirmed and the estimate is empty, so flat and
//	          weighted are identical by construction. Reported as the control.
func TestDiagStartRateDecisionReplay(t *testing.T) {
	dir := os.Getenv("DIAG_CACHE_DIR")
	if dir == "" {
		t.Skip("set DIAG_CACHE_DIR to a Fantrax cache directory")
	}
	_, days := diagScan(t, dir)

	// League configuration, read from the cached per-period GS limits rather
	// than assumed. Fantrax rescales both bounds for merged multi-week
	// periods, so a constant would be wrong exactly where it matters.
	limit, floor, ok := diagGSLimits(t, dir)
	if !ok {
		t.Fatalf("no fantrax-gs-limits-*.json under %s", dir)
	}
	slots := diagPitcherSlots(t, dir)
	t.Logf("league config from cache: limit=%d floor=%d pitcher slots=%d", limit, floor, slots)
	t.Logf("weighting: trailing %d days, prior %.0f pseudo-opportunities at %.3f", diagRateWindow, diagRatePrior, 1/RotationSize)

	type flip struct {
		team    string
		weekEnd time.Time
		what    string
		detail  string
	}
	var flips []flip
	var weeks, evaluated int
	var flatSum, wtSum float64
	var withHistory, withoutHistory int
	var flatWeekSum, wtWeekSum, actualWeekSum, flatWeekErr, wtWeekErr float64

	teams := make([]string, 0, len(days))
	for team := range days {
		teams = append(teams, team)
	}
	sort.Strings(teams)

	for _, team := range teams {
		byDate := days[team]
		dates := make([]string, 0, len(byDate))
		for ds := range byDate {
			dates = append(dates, ds)
		}
		sort.Strings(dates)
		for _, weekStart := range dates {
			d, _ := time.Parse("2006-01-02", weekStart)
			if d.Weekday() != time.Monday {
				continue
			}
			week := make([]*diagDay, 0, 7)
			complete := true
			for i := 0; i < 7; i++ {
				ds := d.AddDate(0, 0, i).Format("2006-01-02")
				day, ok := byDate[ds]
				if !ok {
					complete = false
					break
				}
				week = append(week, day)
			}
			if !complete {
				continue
			}
			weeks++

			// Calibration: forecast the WHOLE week from its Monday and compare
			// against what the week actually spent. The gate's estimate is a
			// prediction of budget consumption, so the only honest test of the
			// divisor is whether it predicts.
			var flatWeek, wtWeek, weekActual float64
			for _, fd := range week {
				var sum float64
				for _, pid := range fd.SPs {
					r, _ := diagTrailingRate(byDate, pid, week[0].Date)
					sum += r
				}
				flatWeek += math.Max(math.Min(float64(len(fd.SPs))/RotationSize, float64(slots)), 0)
				wtWeek += math.Max(math.Min(sum, float64(slots)), 0)
				weekActual += float64(fd.ActiveStarts)
			}
			flatWeekSum += flatWeek
			wtWeekSum += wtWeek
			actualWeekSum += weekActual
			flatWeekErr += math.Abs(flatWeek - weekActual)
			wtWeekErr += math.Abs(wtWeek - weekActual)

			used := 0
			for i, today := range week {
				remainingDays := week[i+1:]
				if len(remainingDays) == 0 {
					break
				}
				evaluated++

				// Reconstructed as the DAYTIME run sees it, which is when the
				// gate acts: Used holds the days Fantrax has settled, and
				// today's starts are still carried separately as today's
				// probables (GSBudget.TodayUnsettled's seam). Adding today to
				// Used and to todayStarters at once would double-count it.
				todayStarters := len(today.ActiveStarters)

				var flatEst, wtEst float64
				for _, fd := range remainingDays {
					n := float64(len(fd.SPs))
					var sum float64
					for _, pid := range fd.SPs {
						r, ok := diagTrailingRate(byDate, pid, today.Date)
						if ok {
							withHistory++
						} else {
							withoutHistory++
						}
						sum += r
					}
					capSlots := float64(slots)
					flatEst += math.Max(math.Min(n/RotationSize, capSlots), 0)
					wtEst += math.Max(math.Min(sum, capSlots), 0)
				}
				flatSum += flatEst
				wtSum += wtEst

				remaining := limit - used
				if remaining < 0 {
					remaining = 0
				}
				const eps = 1e-9
				gateFlat := float64(remaining)+eps < float64(todayStarters)+flatEst
				gateWt := float64(remaining)+eps < float64(todayStarters)+wtEst
				if gateFlat != gateWt {
					flips = append(flips, flip{team, week[6].Date, "gate",
						fmt.Sprintf("day %s remaining=%d today=%d flatEst=%.2f wtEst=%.2f (flat fires=%v, weighted fires=%v)",
							today.Date.Format("2006-01-02"), remaining, todayStarters, flatEst, wtEst, gateFlat, gateWt)})
				}

				need := floor - used - todayStarters
				if need < 0 {
					need = 0
				}
				daysLeft := len(remainingDays)
				floorWindow := need > 0 && daysLeft >= gsFloorMinDaysLeft && daysLeft <= gsFloorMaxDaysLeft
				floorFlat := floorWindow && float64(need)-gsFloorEstimateCredit*flatEst > gsFloorEps
				floorWt := floorWindow && float64(need)-gsFloorEstimateCredit*wtEst > gsFloorEps
				if floorFlat != floorWt {
					flips = append(flips, flip{team, week[6].Date, "floor",
						fmt.Sprintf("day %s need=%d flatSupply=%.2f wtSupply=%.2f (flat fires=%v, weighted fires=%v)",
							today.Date.Format("2006-01-02"), need, gsFloorEstimateCredit*flatEst, gsFloorEstimateCredit*wtEst, floorFlat, floorWt)})
				}

				used += today.ActiveStarts
			}
		}
	}

	t.Logf("replayed %d complete Monday-anchored team-weeks, %d in-week decision points", weeks, evaluated)
	t.Logf("whole-week calibration (forecast from Monday vs realised active-slot starts):")
	t.Logf("  actual mean=%.2f  flat 1/5 mean=%.2f (bias %+.2f, MAE %.2f)  weighted mean=%.2f (bias %+.2f, MAE %.2f)",
		actualWeekSum/float64(max(weeks, 1)),
		flatWeekSum/float64(max(weeks, 1)), (flatWeekSum-actualWeekSum)/float64(max(weeks, 1)), flatWeekErr/float64(max(weeks, 1)),
		wtWeekSum/float64(max(weeks, 1)), (wtWeekSum-actualWeekSum)/float64(max(weeks, 1)), wtWeekErr/float64(max(weeks, 1)))
	t.Logf("pitcher-days priced from history: %d; fell back to the flat rate: %d (%.1f%%)",
		withHistory, withoutHistory, 100*float64(withoutHistory)/math.Max(float64(withHistory+withoutHistory), 1))
	t.Logf("mean remaining-week estimate under flat 1/5: %.2f   under per-pitcher weighting: %.2f (%.1f%% of flat)",
		flatSum/float64(max(evaluated, 1)), wtSum/float64(max(evaluated, 1)), 100*wtSum/math.Max(flatSum, 1e-9))
	byWhat := map[string]int{}
	for _, f := range flips {
		byWhat[f.what]++
	}
	t.Logf("decision flips: %d total — gate=%d (%.1f%% of decision points), floor=%d (%.1f%%)",
		len(flips), byWhat["gate"], 100*float64(byWhat["gate"])/float64(max(evaluated, 1)),
		byWhat["floor"], 100*float64(byWhat["floor"])/float64(max(evaluated, 1)))
	for i, f := range flips {
		if i >= 40 {
			t.Logf("  ... %d more", len(flips)-40)
			break
		}
		t.Logf("  [%s] team=%s week ending %s: %s", f.what, f.team, f.weekEnd.Format("2006-01-02"), f.detail)
	}
}

// diagRateWindow / diagRateMinOpp mirror the trailing-window estimator the
// production weighting would use: only days strictly BEFORE the run day count,
// so the replay never reads an outcome it is predicting.
// diagRatePrior is the strength, in pseudo-opportunities at the flat rate, of
// the shrinkage prior. Zero opportunities therefore return exactly the flat
// rate, and a short sample is pulled toward it rather than believed outright.
// The defaults are the SHIPPED constants, so a bare run reports what
// production actually does; the env knobs exist to sweep alternatives.
var (
	diagRateWindow = diagEnvInt("DIAG_RATE_WINDOW", gsStartRateWindow)
	diagRateMinOpp = diagEnvInt("DIAG_RATE_MIN_OPP", 0)
	diagRatePrior  = float64(diagEnvInt("DIAG_RATE_PRIOR", int(gsStartRatePrior)))
)

func diagEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func diagTrailingRate(byDate map[string]*diagDay, pid string, asOf time.Time) (float64, bool) {
	opp, starts := 0, 0
	for i := 1; i <= diagRateWindow; i++ {
		day, ok := byDate[asOf.AddDate(0, 0, -i).Format("2006-01-02")]
		if !ok {
			continue
		}
		found := false
		for _, id := range day.SPs {
			if id == pid {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		opp++
		if day.ActiveStarters[pid] {
			starts++
		}
	}
	if opp == 0 || opp < diagRateMinOpp {
		return 1 / RotationSize, false
	}
	return (float64(starts) + diagRatePrior/RotationSize) / (float64(opp) + diagRatePrior), true
}

func diagGSLimits(t *testing.T, dir string) (limit, floor int, ok bool) {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "fantrax-gs-limits-*.json"))
	sort.Strings(paths)
	for _, p := range paths {
		var lim struct {
			Min *int `json:"min"`
			Max *int `json:"max"`
		}
		if !diagRead(t, p, &lim) || lim.Max == nil {
			continue
		}
		limit = *lim.Max
		if lim.Min != nil {
			floor = *lim.Min
		}
		ok = true
	}
	return limit, floor, ok
}

func diagPitcherSlots(t *testing.T, dir string) int {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "fantrax-pitcher-slots-*.json"))
	for _, p := range paths {
		var slots []struct{ PosID, PosName string }
		if diagRead(t, p, &slots) && len(slots) > 0 {
			return len(slots)
		}
	}
	return 6
}
