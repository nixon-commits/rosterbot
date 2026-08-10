package tradevalue

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

// player builds a side from (name, value) pairs, all priced.
func player(team string, nv ...any) Side {
	s := Side{Team: team}
	for i := 0; i < len(nv); i += 2 {
		s.Assets = append(s.Assets, Asset{
			Name:   nv[i].(string),
			Value:  nv[i+1].(int),
			Priced: true,
		})
	}
	return s
}

func approx(t *testing.T, got, want, tol float64, what string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.2f, want %.2f (+/- %.2f)", what, got, want, tol)
	}
}

// The offer that motivated the whole feature. Live pending trade
// lpe0ltlcmsdt40zv as of 2026-08-05: Intentional Balk receives Gage Jump
// (1391) and Emmet Sheehan (1234) for Kyle Harrison (2292).
//
// Raw says the 2-for-1 favors Intentional Balk by 333. Adjusted reprices the
// two-asset side at 1391 + 0.70(1234) = 2254.8 and hands it to Yordan's
// Schlong by 37. A tab that reported either number alone would state a
// confident verdict that the other method contradicts.
func TestEvaluate_LiveTwoForOne_MethodsDisagree_IsTooClose(t *testing.T) {
	ib := player("Intentional Balk", "Gage Jump", 1391, "Emmet Sheehan", 1234)
	ys := player("Yordan's Schlong", "Kyle Harrison", 2292)

	if got := ib.Raw(); got != 2625 {
		t.Errorf("Intentional Balk raw = %d, want 2625", got)
	}
	approx(t, ib.Adjusted(), 2254.8, 0.1, "Intentional Balk adjusted")
	if got := ys.Raw(); got != 2292 {
		t.Errorf("Yordan's Schlong raw = %d, want 2292", got)
	}
	// A single-asset side has no adjustment to apply.
	approx(t, ys.Adjusted(), 2292, 0.01, "Yordan's Schlong adjusted")

	v := Evaluate([]Side{ib, ys})
	if v.Status != StatusTooClose {
		t.Fatalf("Status = %q, want %q", v.Status, StatusTooClose)
	}
	if v.RawLeader != "Intentional Balk" {
		t.Errorf("RawLeader = %q, want Intentional Balk", v.RawLeader)
	}
	if v.AdjLeader != "Yordan's Schlong" {
		t.Errorf("AdjLeader = %q, want Yordan's Schlong", v.AdjLeader)
	}
	if v.FavoredTeam != "" {
		t.Errorf("FavoredTeam = %q, want empty on a too-close verdict", v.FavoredTeam)
	}
}

// Live pending trade hohgkc3cmsde2ig7: lopsided enough that both methods
// agree, so a verdict is stated.
func TestEvaluate_LiveLopsidedOffer_BothMethodsAgree(t *testing.T) {
	ib := player("Intentional Balk",
		"Lazaro Montes", 878, "Cole Carrigg", 833,
		"Heriberto Hernandez", 108, "Kyle Leahy", 79)
	ys := player("Yordan's Schlong",
		"Andrew Fischer", 774, "Kyle Harrison", 2292, "Randy Arozarena", 1006)

	v := Evaluate([]Side{ib, ys})
	if v.Status != StatusFavors {
		t.Fatalf("Status = %q, want %q", v.Status, StatusFavors)
	}
	if v.FavoredTeam != "Yordan's Schlong" {
		t.Errorf("FavoredTeam = %q, want Yordan's Schlong", v.FavoredTeam)
	}
	// Symmetric percent difference: |4072-1898| / mean(4072,1898).
	approx(t, v.RawPct, 72.83, 0.05, "RawPct")
}

// An unidentified pick makes the comparison meaningless, not merely
// imprecise: HKB prices picks 45-1419, so the missing asset could be worth
// more than everything else on its side.
func TestEvaluate_UnidentifiedPick_IsIncomplete(t *testing.T) {
	withPick := player("Intentional Balk", "Alec Bohm", 900)
	withPick.Assets = append(withPick.Assets, Asset{
		Name:   "Draft pick (unidentified)",
		IsPick: true,
	})
	other := player("BT95", "Some Guy", 1500)

	v := Evaluate([]Side{withPick, other})
	if v.Status != StatusIncomplete {
		t.Fatalf("Status = %q, want %q", v.Status, StatusIncomplete)
	}
	if v.UnpricedAssets != 1 {
		t.Errorf("UnpricedAssets = %d, want 1", v.UnpricedAssets)
	}
	if v.FavoredTeam != "" || v.RawLeader != "" || v.AdjLeader != "" {
		t.Errorf("incomplete verdict leaked a leader: %+v", v)
	}
	// The side total is still computable as a floor.
	if got := withPick.Raw(); got != 900 {
		t.Errorf("Raw = %d, want 900 (unpriced asset contributes nothing)", got)
	}
	if priced, total := withPick.Coverage(); priced != 1 || total != 2 {
		t.Errorf("Coverage = %d/%d, want 1/2", priced, total)
	}
}

// A player HKB has never heard of blocks the verdict the same way a pick
// does -- the reason is different, the consequence is identical.
func TestEvaluate_HKBNameMiss_IsIncomplete(t *testing.T) {
	a := Side{Team: "A", Assets: []Asset{
		{Name: "Known Player", Value: 500, Priced: true},
		{Name: "Unranked Nobody", Priced: false},
	}}
	b := player("B", "Other Guy", 400)

	if v := Evaluate([]Side{a, b}); v.Status != StatusIncomplete {
		t.Fatalf("Status = %q, want %q", v.Status, StatusIncomplete)
	}
}

// internal/transactions models sides as [2]TradeSide and drops the third
// team's assets outright. This package must not inherit that.
func TestEvaluate_ThreeSidedTrade_NoSideIsDropped(t *testing.T) {
	sides := []Side{
		player("A", "p1", 100),
		player("B", "p2", 3000),
		player("C", "p3", 200),
	}
	v := Evaluate(sides)
	if v.Status != StatusFavors {
		t.Fatalf("Status = %q, want %q", v.Status, StatusFavors)
	}
	if v.FavoredTeam != "B" {
		t.Errorf("FavoredTeam = %q, want B", v.FavoredTeam)
	}
	// Runner-up is C (200), not A -- proving the third side was ranked and
	// not silently discarded.
	approx(t, v.RawPct, math.Abs(3000-200)/((3000+200)/2)*100, 0.01, "RawPct")
}

// The decay is fitted to this exact observation from HKB's calculator; if
// the constant is ever changed, this test is the record of what it was
// calibrated against.
func TestAdjusted_ReproducesTheHKBObservationTheDecayWasFittedTo(t *testing.T) {
	s := player("T", "Ritchie", 589, "Rodriguez-Cruz", 540, "McGreevy", 366)
	if got := s.Raw(); got != 1495 {
		t.Fatalf("Raw = %d, want 1495", got)
	}
	// HKB reported 1144 for this package. 0.2% residual.
	approx(t, s.Adjusted(), 1144, 5, "Adjusted")
}

// HKB reports "a 91.6% value difference" for 3079 against 1144. Matching
// their formula matters because these numbers get read side by side.
func TestPctDiff_MatchesHKBsReportedFigure(t *testing.T) {
	approx(t, pctDiff(3079, 1144), 91.6, 0.1, "pctDiff(3079,1144)")
}

func TestEvaluate_FewerThanTwoSides_IsIncomplete(t *testing.T) {
	for _, sides := range [][]Side{nil, {player("A", "p", 100)}} {
		if v := Evaluate(sides); v.Status != StatusIncomplete {
			t.Errorf("Evaluate(%d sides) = %q, want %q", len(sides), v.Status, StatusIncomplete)
		}
	}
}

// Equal-value sides must resolve the same way on every run -- the same
// stability requirement as the optimizer's player-ID tiebreaker.
func TestEvaluate_TiedSides_AreStable(t *testing.T) {
	sides := []Side{player("Zebra", "p1", 500), player("Aardvark", "p2", 500)}
	first := Evaluate(sides)
	for i := 0; i < 20; i++ {
		if got := Evaluate(sides); got != first {
			t.Fatalf("verdict not stable: %+v then %+v", first, got)
		}
	}
	if first.RawLeader != "Aardvark" {
		t.Errorf("tie broke to %q, want Aardvark (lexical)", first.RawLeader)
	}
}

func TestNewAsset(t *testing.T) {
	lookup := hkb.BuildLookup([]hkb.Player{
		{Name: "Kyle Harrison", Value: 2292, Rank: 40},
		{Name: "José Ramírez", Value: 3000, Rank: 10},
	})

	t.Run("empty name is an unidentified pick", func(t *testing.T) {
		a := NewAsset("", "", lookup)
		if !a.IsPick || a.Priced {
			t.Errorf("got %+v, want IsPick and !Priced", a)
		}
	})
	t.Run("known player is priced", func(t *testing.T) {
		a := NewAsset("Kyle Harrison", "SP", lookup)
		if !a.Priced || a.Value != 2292 {
			t.Errorf("got %+v, want priced at 2292", a)
		}
	})
	t.Run("diacritics join through Normalize", func(t *testing.T) {
		a := NewAsset("Jose Ramirez", "3B", lookup)
		if !a.Priced || a.Value != 3000 {
			t.Errorf("got %+v, want priced at 3000", a)
		}
	})
	t.Run("unknown player is unpriced", func(t *testing.T) {
		a := NewAsset("Nobody At All", "OF", lookup)
		if a.Priced || a.IsPick {
			t.Errorf("got %+v, want !Priced and !IsPick", a)
		}
	})
}
