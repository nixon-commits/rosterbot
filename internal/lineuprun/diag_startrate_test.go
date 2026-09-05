//go:build diag

// Package lineuprun's start-rate diagnostic.
//
// This file answers the question rosterbot-goht makes a precondition of any
// change: does a rostered SP's own active-slot start rate disperse widely
// enough around the flat 1-in-5 that buildGSForecast assumes for it to change
// a gate or floor decision?
//
// EVERYTHING IT REPORTS IS PRODUCED BY THE SHIPPED CODE. The per-day rows come
// from fantrax.PitcherDayWalk — the same kernel GetTeamPitcherDays runs — the
// rates come from tallyStartRates, and the floor verdicts from
// evaluateGSFloorWith. The first version of this harness re-implemented the
// tally instead, and measured a materially different estimator from the one it
// shipped (see tallyStartRates' comment); nothing here may re-derive a number
// the production path already knows how to compute.
//
// It is build-tagged `diag` and reads only from a local Fantrax cache
// directory, so it never runs in CI, never authenticates, and never touches an
// upstream. Run it with:
//
//	DIAG_CACHE_DIR=/path/to/.cache go test -tags diag -run TestDiagStartRate -v ./internal/lineuprun/
//
// The cache dir is READ-ONLY here: every open is os.ReadFile and nothing writes.
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

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// diagEnvelope mirrors internal/cache's on-disk envelope.
type diagEnvelope struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Data      json.RawMessage `json:"data"`
}

// diagRosterRow mirrors fantrax.playerYTD, which carries no json tags and so
// marshals by Go field name. Only the two fields the roster-meta join reads
// are declared.
type diagRosterRow struct {
	StatusID      string
	PosShortNames string
}

type diagRosterSnap struct {
	Pitchers map[string]diagRosterRow `json:"pitchers"`
}

func diagRead(path string, into any) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var env diagEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	return json.Unmarshal(env.Data, into) == nil
}

// --- cache → shipped-kernel inputs -----------------------------------------

// diagCache is every frozen snapshot on disk, indexed the way the walk needs
// them: by team and daily period.
type diagCache struct {
	dir string
	// gs[team][period] and roster[team][period] are the two per-period
	// snapshots GetTeamPitcherDaysWithStatus reads.
	gs     map[string]map[int]map[string]fantrax.PitcherSnapshotRow
	roster map[string]map[int]map[string]fantrax.PitcherRosterMeta
	// playing[date][club] is the schedule, as computeStartRates builds it.
	playing map[string]map[string]bool
	// periodOf/dateOf invert the authoritative date↔daily-period map that
	// fantrax.DailyPeriodFor consults.
	periodOf map[string]int
	dateOf   map[int]time.Time

	limit, floor, slots int
}

func diagLoad(t *testing.T, dir string) *diagCache {
	t.Helper()
	c := &diagCache{
		dir:      dir,
		gs:       map[string]map[int]map[string]fantrax.PitcherSnapshotRow{},
		roster:   map[string]map[int]map[string]fantrax.PitcherRosterMeta{},
		playing:  map[string]map[string]bool{},
		periodOf: map[string]int{},
		dateOf:   map[int]time.Time{},
	}

	paths, _ := filepath.Glob(filepath.Join(dir, "fantrax-period-date-map-*.json"))
	for _, p := range paths {
		var m map[string]int
		if !diagRead(p, &m) {
			continue
		}
		for ds, period := range m {
			d, err := time.Parse("2006-01-02", ds)
			if err != nil {
				continue
			}
			c.periodOf[ds] = period
			c.dateOf[period] = d.UTC()
		}
	}
	if len(c.periodOf) == 0 {
		t.Fatalf("no fantrax-period-date-map-*.json under %s — cannot resolve the daily period axis", dir)
	}

	paths, _ = filepath.Glob(filepath.Join(dir, "mlb-schedule-*.json"))
	for _, p := range paths {
		ds := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "mlb-schedule-"), ".json")
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
		if !diagRead(p, &sched) {
			continue
		}
		clubs := map[string]bool{}
		for _, d := range sched.Dates {
			for _, g := range d.Games {
				clubs[g.Teams.Away.Team.Abbreviation] = true
				clubs[g.Teams.Home.Team.Abbreviation] = true
			}
		}
		c.playing[ds] = clubs
	}

	paths, _ = filepath.Glob(filepath.Join(dir, "fantrax-pitcher-gs-*.json"))
	for _, p := range paths {
		team, period, ok := diagSplitKey(filepath.Base(p), "fantrax-pitcher-gs-")
		if !ok {
			continue
		}
		var snap map[string]fantrax.PitcherSnapshotRow
		if !diagRead(p, &snap) {
			continue
		}
		if c.gs[team] == nil {
			c.gs[team] = map[int]map[string]fantrax.PitcherSnapshotRow{}
		}
		c.gs[team][period] = snap
	}

	paths, _ = filepath.Glob(filepath.Join(dir, "fantrax-roster-stats-*.json"))
	for _, p := range paths {
		team, period, ok := diagSplitKey(filepath.Base(p), "fantrax-roster-stats-")
		if !ok {
			continue
		}
		var snap diagRosterSnap
		if !diagRead(p, &snap) {
			continue
		}
		meta := make(map[string]fantrax.PitcherRosterMeta, len(snap.Pitchers))
		for pid, row := range snap.Pitchers {
			meta[pid] = fantrax.PitcherRosterMeta{StatusID: row.StatusID, PosShortNames: row.PosShortNames}
		}
		if c.roster[team] == nil {
			c.roster[team] = map[int]map[string]fantrax.PitcherRosterMeta{}
		}
		c.roster[team][period] = meta
	}

	c.limit, c.floor = diagGSLimits(dir)
	c.slots = diagPitcherSlots(dir)
	return c
}

func diagSplitKey(base, prefix string) (team string, period int, ok bool) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".json"), "-")
	if len(parts) != 3 {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, false
	}
	return parts[0], n, true
}

func (c *diagCache) teams() []string {
	out := make([]string, 0, len(c.gs))
	for t := range c.gs {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// have reports whether every input the walk needs for that date exists.
func (c *diagCache) have(team string, d time.Time) bool {
	p, ok := c.periodOf[d.Format("2006-01-02")]
	if !ok {
		return false
	}
	if _, ok := c.gs[team][p]; !ok {
		return false
	}
	if _, ok := c.roster[team][p]; !ok {
		return false
	}
	_, ok = c.playing[d.Format("2006-01-02")]
	return ok
}

// pitcherDays runs the SHIPPED per-day derivation (fantrax.PitcherDayWalk) over
// [start, end], baselining on the day before exactly as
// GetTeamPitcherDaysWithStatus does. Returns nil if any day is missing, so a
// partially-cached window is never silently measured as a short one.
func (c *diagCache) pitcherDays(team string, start, end time.Time) []fantrax.PitcherDay {
	walk := fantrax.NewPitcherDayWalk()
	if p, ok := c.periodOf[start.AddDate(0, 0, -1).Format("2006-01-02")]; ok {
		if snap, ok := c.gs[team][p]; ok {
			walk.Baseline(snap)
		}
	}
	var out []fantrax.PitcherDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if !c.have(team, d) {
			return nil
		}
		p := c.periodOf[d.Format("2006-01-02")]
		out = append(out, walk.Day(d, c.gs[team][p], c.roster[team][p])...)
	}
	return out
}

// rates runs the SHIPPED tally over the trailing gsStartRateWindow days ending
// yesterday — the exact window computeStartRates asks for.
func (c *diagCache) rates(team string, today time.Time, p startRateParams) startRateResult {
	return c.ratesOver(team, today, gsStartRateWindow, p)
}

// ratesOver is rates with the window length as a parameter, for the window
// sweep. Everything else — the walk, the tally, the filters — is the shipped
// code either way.
func (c *diagCache) ratesOver(team string, today time.Time, window int, p startRateParams) startRateResult {
	days := c.pitcherDays(team, today.AddDate(0, 0, -window), today.AddDate(0, 0, -1))
	if days == nil {
		return startRateResult{}
	}
	return tallyStartRates(days, c.playing, p)
}

func diagGSLimits(dir string) (limit, floor int) {
	paths, _ := filepath.Glob(filepath.Join(dir, "fantrax-gs-limits-*.json"))
	sort.Strings(paths)
	for _, p := range paths {
		var lim struct {
			Min *int `json:"min"`
			Max *int `json:"max"`
		}
		if !diagRead(p, &lim) || lim.Max == nil {
			continue
		}
		limit = *lim.Max
		if lim.Min != nil {
			floor = *lim.Min
		}
	}
	return limit, floor
}

func diagPitcherSlots(dir string) int {
	paths, _ := filepath.Glob(filepath.Join(dir, "fantrax-pitcher-slots-*.json"))
	for _, p := range paths {
		var slots []struct{ PosID, PosName string }
		if diagRead(p, &slots) && len(slots) > 0 {
			return len(slots)
		}
	}
	return 6
}

func diagQuantile(sorted []float64, f float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := float64(len(sorted)-1) * f
	lo := int(i)
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(i-float64(lo))
}

func diagCacheDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("DIAG_CACHE_DIR")
	if dir == "" {
		t.Skip("set DIAG_CACHE_DIR to a Fantrax cache directory")
	}
	return dir
}

// diagSeason is the first and last date any measurement here may touch: the
// season opener, and the last day for which a schedule is cached. Both are
// derived rather than assumed, so a cache with a different span measures its
// own span.
func (c *diagCache) span() (first, last time.Time) {
	dates := make([]string, 0, len(c.playing))
	for ds := range c.playing {
		if _, ok := c.periodOf[ds]; ok {
			dates = append(dates, ds)
		}
	}
	sort.Strings(dates)
	if len(dates) == 0 {
		return time.Time{}, time.Time{}
	}
	first, _ = time.Parse("2006-01-02", dates[0])
	last, _ = time.Parse("2006-01-02", dates[len(dates)-1])
	return first.UTC(), last.UTC()
}

// --- dispersion -------------------------------------------------------------

// TestDiagStartRateDispersion reports the per-pitcher active-slot start rate
// distribution over the whole cached season, using the SHIPPED tally with the
// prior and the guards switched off so what it shows is the raw conversion
// each pitcher achieved.
func TestDiagStartRateDispersion(t *testing.T) {
	c := diagLoad(t, diagCacheDir(t))
	first, last := c.span()
	minOpp := 20
	if v := os.Getenv("DIAG_MIN_OPP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			minOpp = n
		}
	}

	// Raw parameters: no shrinkage, no cap, and the sample floor as the guard,
	// so tallyStartRates reports each pitcher's observed conversion directly.
	raw := startRateParams{Prior: 0, MinOpportunities: minOpp, Cap: 0}

	var rates []float64
	type row struct {
		team, name string
		opps       int
		rate       float64
	}
	var rows []row
	seen := 0
	for _, team := range c.teams() {
		days := c.diagWholeSeason(team, first, last)
		res := tallyStartRates(days, c.playing, raw)
		seen += len(res.Opportunities)
		for name, r := range res.Rate {
			rates = append(rates, r)
			rows = append(rows, row{team, name, res.Opportunities[name], r})
		}
	}
	if len(rates) == 0 {
		t.Fatalf("no pitcher reached %d opportunities under %s", minOpp, c.dir)
	}
	sort.Float64s(rates)

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

	t.Logf("window %s..%s, %d teams", first.Format("2006-01-02"), last.Format("2006-01-02"), len(c.teams()))
	t.Logf("pitcher-seasons with >= %d club-game-day opportunities: %d (of %d observed)", minOpp, len(rates), seen)
	t.Logf("flat assumption: 1/RotationSize = %.3f", 1/RotationSize)
	t.Logf("active-slot      n=%d mean=%.4f sd=%.4f min=%.4f p10=%.4f p25=%.4f med=%.4f p75=%.4f p90=%.4f max=%.4f",
		len(rates), mean(rates), sd(rates), rates[0],
		diagQuantile(rates, 0.10), diagQuantile(rates, 0.25), diagQuantile(rates, 0.50),
		diagQuantile(rates, 0.75), diagQuantile(rates, 0.90), rates[len(rates)-1])
	below := 0
	for _, r := range rates {
		if r < 0.5/RotationSize {
			below++
		}
	}
	t.Logf("share converting at under half the flat rate: %.1f%%", 100*float64(below)/float64(len(rates)))
	pctBelowFlat := 0
	for _, r := range rates {
		if r < 1/RotationSize {
			pctBelowFlat++
		}
	}
	t.Logf("share below the flat rate entirely: %.1f%%", 100*float64(pctBelowFlat)/float64(len(rates)))

	sort.Slice(rows, func(i, j int) bool { return rows[i].rate > rows[j].rate })
	t.Logf("--- top 10 by active-slot rate ---")
	for _, r := range rows[:min(10, len(rows))] {
		t.Logf("  %-26s opp=%3d rate=%.4f", r.name, r.opps, r.rate)
	}
	t.Logf("--- bottom 10 ---")
	for _, r := range rows[max(0, len(rows)-10):] {
		t.Logf("  %-26s opp=%3d rate=%.4f", r.name, r.opps, r.rate)
	}
}

// diagWholeSeason walks every cached day in one pass, which is what a
// season-long tally needs; the trailing-window estimator uses c.pitcherDays.
func (c *diagCache) diagWholeSeason(team string, first, last time.Time) []fantrax.PitcherDay {
	walk := fantrax.NewPitcherDayWalk()
	var out []fantrax.PitcherDay
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if !c.have(team, d) {
			continue
		}
		p := c.periodOf[d.Format("2006-01-02")]
		out = append(out, walk.Day(d, c.gs[team][p], c.roster[team][p])...)
	}
	return out
}

// --- week replay ------------------------------------------------------------

// diagWeek is one Monday-anchored team-week with everything both sweeps need.
type diagWeek struct {
	team  string
	start time.Time
	days  []time.Time
	// realised[i] is the active-slot starts our roster actually took on day i.
	realised []int
	// spsOn[i] are the normalized names of the SP-eligible, available pitchers
	// whose club played on day i — buildGSForecast's unannounced bucket in the
	// "open" announcement regime.
	spsOn [][]string
}

func (w diagWeek) totalRealised() int {
	n := 0
	for _, r := range w.realised {
		n += r
	}
	return n
}

// diagWeeks reconstructs every complete Monday-anchored team-week for which
// the whole week AND the whole trailing rate window are cached.
func diagWeeks(c *diagCache) []diagWeek {
	first, last := c.span()
	var out []diagWeek
	for _, team := range c.teams() {
		for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
			if d.Weekday() != time.Monday {
				continue
			}
			week := diagWeek{team: team, start: d}
			ok := true
			for i := 0; i < 7; i++ {
				day := d.AddDate(0, 0, i)
				if !c.have(team, day) {
					ok = false
					break
				}
				week.days = append(week.days, day)
			}
			if !ok {
				continue
			}
			// One walk over the week gives both the realised starts and the
			// per-day eligible set, from the same shipped derivation.
			rows := c.pitcherDays(team, week.days[0], week.days[6])
			if rows == nil {
				continue
			}
			week.realised = make([]int, 7)
			week.spsOn = make([][]string, 7)
			for _, r := range rows {
				i := int(r.Date.Sub(week.days[0]).Hours() / 24)
				if i < 0 || i > 6 {
					continue
				}
				if r.Started {
					week.realised[i]++
				}
				if r.FirstAppearance ||
					r.StatusID == fantrax.StatusIL || r.StatusID == fantrax.StatusMinors ||
					!strings.Contains(r.PosShortNames, "SP") ||
					!c.playing[r.Date.Format("2006-01-02")][r.MLBTeam] {
					continue
				}
				week.spsOn[i] = append(week.spsOn[i], projections.NormalizeName(r.PitcherName))
			}
			for i := range week.spsOn {
				sort.Strings(week.spsOn[i])
			}
			out = append(out, week)
		}
	}
	return out
}

// diagEstimate is the per-day estimate buildGSForecast would produce in the
// OPEN announcement regime — nobody has named anyone, so every eligible SP
// lands in the unannounced bucket. See TestDiagStartRateDecisionReplay for why
// that regime is an upper bound rather than a description of production.
func diagEstimate(sps []string, rate map[string]float64, slots int) float64 {
	var flat float64
	var sum float64
	for _, name := range sps {
		if r, ok := rate[name]; ok {
			sum += r
			continue
		}
		flat++
	}
	return math.Max(math.Min(flat/RotationSize+sum, float64(slots)), 0)
}

// TestDiagStartRateWeeklyCalibration sweeps the estimator's three constants
// against whole-week forecast accuracy and against the count of physically
// impossible rates.
func TestDiagStartRateWeeklyCalibration(t *testing.T) {
	c := diagLoad(t, diagCacheDir(t))
	weeks := diagWeeks(c)
	if len(weeks) == 0 {
		t.Fatalf("no complete Monday-anchored team-weeks under %s", c.dir)
	}
	t.Logf("league config from cache: limit=%d floor=%d pitcher slots=%d", c.limit, c.floor, c.slots)
	t.Logf("%d complete Monday-anchored team-weeks", len(weeks))

	// The flat baseline: every eligible SP at 1/RotationSize.
	var flatBias, flatMAE, actualMean float64
	for _, w := range weeks {
		var pred float64
		for i := range w.days {
			pred += diagEstimate(w.spsOn[i], nil, c.slots)
		}
		act := float64(w.totalRealised())
		flatBias += pred - act
		flatMAE += math.Abs(pred - act)
		actualMean += act
	}
	n := float64(len(weeks))
	t.Logf("realised active-slot starts per week: mean %.2f", actualMean/n)
	t.Logf("flat 1/5: predicted bias %+.2f  MAE %.2f", flatBias/n, flatMAE/n)

	// Window sweep first, at the shipped prior/guard/cap, because the window is
	// the one constant fixed by an engineering fact (internal/schedule's 30-day
	// past-date TTL) and the measurement's job is only to say whether anything
	// is lost by staying inside it.
	shipped := shippedStartRateParams()
	t.Logf("%-8s | %8s %8s", "window", "bias", "MAE")
	for _, window := range []int{14, 21, 28, 45} {
		var bias, mae float64
		for _, w := range weeks {
			res := c.ratesOver(w.team, w.start, window, shipped)
			var pred float64
			for i := range w.days {
				pred += diagEstimate(w.spsOn[i], res.Rate, c.slots)
			}
			act := float64(w.totalRealised())
			bias += pred - act
			mae += math.Abs(pred - act)
		}
		t.Logf("%-8d | %+8.2f %8.2f", window, bias/n, mae/n)
	}

	t.Logf("%-6s %-8s %-6s | %8s %8s %8s %10s %10s", "prior", "minOpps", "cap", "bias", "MAE", "meanPred", "rates>0.25", "rates>0.20")
	for _, prior := range []float64{2, 5} {
		for _, minOpps := range []int{0, 3, 5, 8} {
			for _, capName := range []string{"none", "0.25", "0.20"} {
				capVal := 0.0
				switch capName {
				case "0.25":
					capVal = 0.25
				case "0.20":
					capVal = 0.20
				}
				p := startRateParams{Prior: prior, MinOpportunities: minOpps, Cap: capVal}

				var bias, mae, predSum float64
				over25, over20, priced := 0, 0, 0
				for _, w := range weeks {
					// Rates as known on the week's Monday: the trailing window
					// ends the day before, so nothing here reads an outcome it
					// is predicting.
					res := c.rates(w.team, w.start, p)
					for _, r := range res.Rate {
						priced++
						if r > 0.25 {
							over25++
						}
						if r > 0.20+1e-12 {
							over20++
						}
					}
					var pred float64
					for i := range w.days {
						pred += diagEstimate(w.spsOn[i], res.Rate, c.slots)
					}
					act := float64(w.totalRealised())
					bias += pred - act
					mae += math.Abs(pred - act)
					predSum += pred
				}
				t.Logf("%-6.0f %-8d %-6s | %+8.2f %8.2f %8.2f %10s %10s",
					prior, minOpps, capName, bias/n, mae/n, predSum/n,
					fmt.Sprintf("%d/%d", over25, priced), fmt.Sprintf("%d/%d", over20, priced))
			}
		}
	}
}

// TestDiagStartRateDecisionReplay counts the gate and floor decisions the
// weighting flips relative to the flat divisor.
//
// The weighting only ever moves the ESTIMATE, so the replay holds today's
// starters and the future confirmed starts fixed and varies nothing else. What
// a club had announced at forecast time is not recoverable from the cache, so
// this runs in the OPEN regime — no future club has named anyone — which is the
// regime the weighting can move at all and therefore an UPPER BOUND on its
// effect. It is NOT what production sees: MLB trickles probables out days ahead
// (rosterbot-1jvj), so in production a share of each day's clubs are already
// announced and contribute a confirmed start the weighting never touches. The
// closed regime — every club announced — is identical between flat and weighted
// by construction, so the true effect lies between zero and what is printed
// here. The earlier version of this comment promised a computed "named"
// control; it never computed one, and the honest statement is this bound.
func TestDiagStartRateDecisionReplay(t *testing.T) {
	c := diagLoad(t, diagCacheDir(t))
	weeks := diagWeeks(c)
	if len(weeks) == 0 {
		t.Fatalf("no complete Monday-anchored team-weeks under %s", c.dir)
	}
	p := shippedStartRateParams()
	t.Logf("shipped estimator: window %d prior %.0f minOpps %d cap %.2f",
		gsStartRateWindow, p.Prior, p.MinOpportunities, p.Cap)

	var evaluated, gateFlips, floorFlips int
	var flatSum, wtSum float64
	var priced, flat int
	for _, w := range weeks {
		used := 0
		for i := range w.days {
			if i == 6 {
				break
			}
			evaluated++
			res := c.rates(w.team, w.days[i], p)

			var flatEst, wtEst float64
			for j := i + 1; j < 7; j++ {
				flatEst += diagEstimate(w.spsOn[j], nil, c.slots)
				wtEst += diagEstimate(w.spsOn[j], res.Rate, c.slots)
				for _, name := range w.spsOn[j] {
					if _, ok := res.Rate[name]; ok {
						priced++
					} else {
						flat++
					}
				}
			}
			flatSum += flatEst
			wtSum += wtEst

			todayStarters := w.realised[i]
			remaining := max(c.limit-used, 0)
			const eps = 1e-9
			if gf, gw := float64(remaining)+eps < float64(todayStarters)+flatEst,
				float64(remaining)+eps < float64(todayStarters)+wtEst; gf != gw {
				gateFlips++
			}

			daysLeft := 6 - i
			ff := diagFloorFires(c, used, todayStarters, daysLeft, w, i, nil)
			fw := diagFloorFires(c, used, todayStarters, daysLeft, w, i, res.Rate)
			if ff != fw {
				floorFlips++
			}
			used += w.realised[i]
		}
	}
	t.Logf("%d in-week decision points over %d team-weeks", evaluated, len(weeks))
	t.Logf("pitcher-days priced from history: %d; fell back to flat: %d (%.1f%%)",
		priced, flat, 100*float64(flat)/math.Max(float64(priced+flat), 1))
	t.Logf("mean remaining-week estimate — flat %.2f, weighted %.2f (%.1f%% of flat)",
		flatSum/float64(evaluated), wtSum/float64(evaluated), 100*wtSum/math.Max(flatSum, 1e-9))
	t.Logf("UPPER BOUND on decision flips (open regime; production sits between this and zero):")
	t.Logf("  gate  %d (%.1f%%)", gateFlips, 100*float64(gateFlips)/float64(evaluated))
	t.Logf("  floor %d (%.1f%%)", floorFlips, 100*float64(floorFlips)/float64(evaluated))
}

// diagFloorFires builds the same *optimizer.GSBudget the floor trigger reads and
// asks the SHIPPED rule.
func diagFloorFires(c *diagCache, used, todayStarters, daysLeft int, w diagWeek, i int, rate map[string]float64) bool {
	return diagFloorFiresWith(c, used, todayStarters, daysLeft, w, i, rate, shippedGSFloorParams())
}

func diagFloorFiresWith(c *diagCache, used, todayStarters, daysLeft int, w diagWeek, i int, rate map[string]float64, gp gsFloorParams) bool {
	b := &optimizer.GSBudget{
		Limit: c.limit, Floor: c.floor, Used: used,
		Today: w.days[i], WeekEnd: w.days[6],
		TodayUnsettled: todayStarters,
	}
	for j := i + 1; j < 7; j++ {
		b.Forecast = append(b.Forecast, optimizer.DayForecast{
			Date:      w.days[j],
			Estimated: diagEstimate(w.spsOn[j], rate, c.slots),
		})
	}
	_ = daysLeft
	return evaluateGSFloorWith(b, gp).Fires
}

// TestDiagGSFloorSweep sweeps the floor trigger's credit and upper day bound
// against the weeks that actually finished under the minimum.
//
// GROUND TRUTH: a team-week is POSITIVE when its realised active-slot starts
// finished strictly below the league floor. That is the event the alert exists
// to give notice of, and it is the only one recoverable from frozen snapshots.
//
// A week counts as ALERTED if the rule fires on any in-window day of it, since
// the marker keys on (season, weekly period) and so one week raises at most one
// alert however many days would have triggered.
func TestDiagGSFloorSweep(t *testing.T) {
	c := diagLoad(t, diagCacheDir(t))
	weeks := diagWeeks(c)
	if len(weeks) == 0 {
		t.Fatalf("no complete Monday-anchored team-weeks under %s", c.dir)
	}
	p := shippedStartRateParams()
	// Two ground truths, because Period 20 — one of the bead's two designated
	// cases — finished EXACTLY on the floor and so is not a violation under the
	// strict reading. "under" is the event the alert names; "at or under" is
	// the event a manager cares about, since a week that lands on the floor is
	// one rained-out turn from missing it. Reporting both is what keeps the
	// choice of constants from quietly depending on which was meant.
	strict, atOrUnder := 0, 0
	for _, w := range weeks {
		if w.totalRealised() < c.floor {
			strict++
		}
		if w.totalRealised() <= c.floor {
			atOrUnder++
		}
	}
	t.Logf("league config from cache: limit=%d floor=%d pitcher slots=%d", c.limit, c.floor, c.slots)
	t.Logf("%d team-weeks: %d finished UNDER the %d-start floor (%.1f%%), %d finished AT OR UNDER it (%.1f%%)",
		len(weeks), strict, c.floor, 100*float64(strict)/float64(len(weeks)),
		atOrUnder, 100*float64(atOrUnder)/float64(len(weeks)))

	report := func(label string, rateFor func(w diagWeek, day time.Time) map[string]float64, gp gsFloorParams) {
		alerts, tp, tpAt := 0, 0, 0
		for _, w := range weeks {
			fired := false
			used := 0
			for i := 0; i < 6; i++ {
				if diagFloorFiresWith(c, used, w.realised[i], 6-i, w, i, rateFor(w, w.days[i]), gp) {
					fired = true
				}
				used += w.realised[i]
			}
			if fired {
				alerts++
				if w.totalRealised() < c.floor {
					tp++
				}
				if w.totalRealised() <= c.floor {
					tpAt++
				}
			}
		}
		div := func(a, b int) float64 {
			if b == 0 {
				return 0
			}
			return float64(a) / float64(b)
		}
		t.Logf("%-34s alerts=%3d/%d | under: TP=%3d P=%.3f R=%.3f | at-or-under: TP=%3d P=%.3f R=%.3f",
			label, alerts, len(weeks),
			tp, div(tp, alerts), div(tp, strict),
			tpAt, div(tpAt, alerts), div(tpAt, atOrUnder))
	}

	flatRate := func(diagWeek, time.Time) map[string]float64 { return nil }
	wtRate := func(w diagWeek, d time.Time) map[string]float64 { return c.rates(w.team, d, p).Rate }

	// The baseline is the rule as it stood BEFORE this change — the flat
	// estimate at credit 0.8 and a four-day window — spelled out rather than
	// read from the shipped constants, which this sweep is what moved. Reading
	// them would make the comparison rewrite itself every time the answer
	// changed, which is a baseline that cannot report a regression.
	prev := gsFloorParams{EstimateCredit: 0.8, MinDaysLeft: 2, MaxDaysLeft: 4}
	t.Logf("--- baseline: the rule before this change (flat 1/5, credit 0.8, max 4) ---")
	report("flat credit=0.8 max=4", flatRate, prev)

	t.Logf("--- corrected estimator, credit x maxDaysLeft ---")
	for _, credit := range []float64{0.6, 0.7, 0.8, 0.9, 1.0} {
		for _, maxDays := range []int{3, 4, 5} {
			gp := gsFloorParams{EstimateCredit: credit, MinDaysLeft: gsFloorMinDaysLeft, MaxDaysLeft: maxDays}
			report(fmt.Sprintf("weighted credit=%.1f max=%d", credit, maxDays), wtRate, gp)
		}
	}
}
