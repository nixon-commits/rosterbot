package tradevalue

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// observation is one row of testdata/hkb_calculator_2026-09-04.csv: a package
// typed into HKB's public calculator with the opposing side left empty, and the
// two numbers the page rendered back.
type observation struct {
	ID         int
	LeagueSize int
	Values     []int
	Adjustment int // the "Package adjustment" line, as a positive deduction
	Total      int // the "Total value" line
	Label      string
}

func (o observation) raw() int {
	sum := 0
	for _, v := range o.Values {
		sum += v
	}
	return sum
}

func (o observation) side() Side {
	s := Side{Team: fmt.Sprintf("obs%d", o.ID)}
	for i, v := range o.Values {
		s.Assets = append(s.Assets, Asset{Name: fmt.Sprintf("a%d", i), Value: v, Priced: true})
	}
	return s
}

const observationFile = "testdata/hkb_calculator_2026-09-04.csv"

func loadObservations(t *testing.T) []observation {
	t.Helper()
	f, err := os.Open(observationFile)
	if err != nil {
		t.Fatalf("open %s: %v", observationFile, err)
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	r.Comment = '#'
	r.FieldsPerRecord = 6
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v", observationFile, err)
	}

	num := func(s string) int {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Fatalf("parse %q in %s: %v", s, observationFile, err)
		}
		return n
	}

	obs := make([]observation, 0, len(rows))
	for _, row := range rows {
		o := observation{
			ID:         num(row[0]),
			LeagueSize: num(row[1]),
			Adjustment: num(row[3]),
			Total:      num(row[4]),
			Label:      row[5],
		}
		for _, v := range strings.Split(row[2], "|") {
			o.Values = append(o.Values, num(v))
		}
		obs = append(obs, o)
	}
	if len(obs) < 12 {
		t.Fatalf("only %d observations loaded; the bead asks for at least 12", len(obs))
	}
	return obs
}

// The capture is internally consistent before it is used to judge anything:
// HKB's own two rendered numbers must reconcile against the asset values that
// were on screen beside them. A row that fails this was mis-transcribed, and
// pinning a model against a mis-transcribed row is worse than not pinning it.
func TestHKBObservations_AreInternallyConsistent(t *testing.T) {
	for _, o := range loadObservations(t) {
		if got := o.raw() - o.Adjustment; got != o.Total {
			t.Errorf("obs %d (%s): raw %d - adjustment %d = %d, but the page showed total %d",
				o.ID, o.Label, o.raw(), o.Adjustment, got, o.Total)
		}
	}
}

// TestAdjusted_MatchesEveryHKBObservation is the measurement rosterbot-492
// asked for, and it is an EXACT pin rather than a tolerance.
//
// HKB floors its deduction (`Math.floor(raw - adjusted)`) and shows
// `raw - deduction`, so the rendered total is exactly ceil(adjusted) for a
// non-integral adjusted value and adjusted itself when it is integral. Both
// collapse to: the rendered total sits in [adjusted, adjusted+1). A tolerance
// of "within 2%" would let a wrong curve through on the low-value rows, where
// 2% is smaller than the rounding this expresses exactly.
func TestAdjusted_MatchesEveryHKBObservation(t *testing.T) {
	for _, o := range loadObservations(t) {
		adj := o.side().AdjustedForLeague(o.LeagueSize)
		gap := float64(o.Total) - adj
		if gap < 0 || gap >= 1 {
			t.Errorf("obs %d (%s, %d assets, league %d): model %.2f, HKB showed %d (rounding gap %.2f, want [0,1))",
				o.ID, o.Label, len(o.Values), o.LeagueSize, adj, o.Total, gap)
		}
	}
}

// Adjusted() with no league argument must agree with the 12-team observations,
// since that is the league size every caller in this repo gets by default.
func TestAdjusted_DefaultLeagueSizeMatchesTheTwelveTeamObservations(t *testing.T) {
	seen := 0
	for _, o := range loadObservations(t) {
		if o.LeagueSize != DefaultLeagueSize {
			continue
		}
		seen++
		adj := o.side().Adjusted()
		if gap := float64(o.Total) - adj; gap < 0 || gap >= 1 {
			t.Errorf("obs %d (%s): Adjusted() = %.2f, HKB showed %d (gap %.2f)", o.ID, o.Label, adj, o.Total, gap)
		}
	}
	if seen < 10 {
		t.Fatalf("only %d observations at the default league size; the pin is too thin", seen)
	}
}

// The league-size term is not decoration: the SAME package must reprice as the
// setting moves, and in the documented direction ("larger leagues reduce the
// penalty because depth is more valuable"). Without this, a model that ignored
// leagueSize entirely would still pass the 12-team rows.
func TestAdjustedForLeague_LargerLeaguesDiscountLess(t *testing.T) {
	// The sweep rows are exactly the ones carrying this package, captured at
	// every league size the picker offers. Matching on the asset list rather
	// than on "three assets" keeps an unrelated three-asset row (obs 12) from
	// wandering into the monotonicity chain.
	sweep := []int{10000, 9758, 8556}
	sameAssets := func(vals []int) bool {
		if len(vals) != len(sweep) {
			return false
		}
		for i := range vals {
			if vals[i] != sweep[i] {
				return false
			}
		}
		return true
	}

	type point struct {
		size int
		adj  float64
	}
	var pts []point
	for _, o := range loadObservations(t) {
		if !sameAssets(o.Values) {
			continue
		}
		pts = append(pts, point{o.LeagueSize, o.side().AdjustedForLeague(o.LeagueSize)})
	}
	if len(pts) < 5 {
		t.Fatalf("only %d league sizes swept; need the picker range", len(pts))
	}
	// The capture file is ordered by observation id, not by league size.
	sort.Slice(pts, func(i, j int) bool { return pts[i].size < pts[j].size })

	for i := 1; i < len(pts); i++ {
		if pts[i].adj <= pts[i-1].adj {
			t.Errorf("league %d priced the same package at %.2f, not above the %d-team %.2f",
				pts[i].size, pts[i].adj, pts[i-1].size, pts[i-1].adj)
		}
	}
}

// The discount ladder itself, stated once so a future edit has to argue with
// numbers rather than with prose.
//
// The 0.78 ceiling IS observed -- observation 14 is 24 assets deep, so its
// 23rd and 24th assets are capped. The max(0, ...) floor on the base rate is
// NOT observable: the base only goes negative above a 48-team league (at
// exactly 48 it merely reaches zero on its own, so the clamp isn't exercised
// until 49+), and HKB's picker stops at 30 (out-of-range URL values fall back
// to 8). It is carried because HKB's own client-side code carries it, and is
// marked as such.
func TestPackageDiscount_LadderIsPinned(t *testing.T) {
	cases := []struct {
		k, league int
		want      float64
		why       string
	}{
		{0, 12, 0.00, "the best asset is never discounted"},
		{1, 12, 0.38, "12-team base 0.36 plus one step"},
		{2, 12, 0.40, ""},
		{3, 12, 0.42, ""},
		{1, 8, 0.42, "smaller leagues discount harder"},
		{1, 30, 0.20, "larger leagues discount less"},
		{21, 12, 0.78, "exactly at the ceiling"},
		{22, 12, 0.78, "clamped by the ceiling"},
		{40, 12, 0.78, "still clamped"},
		{1, 48, 0.02, "base reaches zero here; the clamp does not yet bind"},
		{1, 60, 0.02, "base goes negative here; the clamp binds -- read from HKB's code, not observable in their UI"},
	}
	for _, c := range cases {
		if got := PackageDiscount(c.k, c.league); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("PackageDiscount(%d, %d) = %.4f, want %.4f %s", c.k, c.league, got, c.want, c.why)
		}
	}
}

// Adjusted must not depend on the order assets arrive in -- the same
// idempotency requirement the optimizer's player-ID tiebreaker exists for.
func TestAdjusted_IsOrderIndependent(t *testing.T) {
	ascending := player("T", "c", 300, "b", 900, "a", 1500)
	descending := player("T", "a", 1500, "b", 900, "c", 300)
	approx(t, ascending.Adjusted(), descending.Adjusted(), 1e-9, "order independence")
}
