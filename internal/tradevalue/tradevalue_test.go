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
// two-asset side at 1391 + 0.62(1234) = 2156.08 and hands it to Yordan's
// Schlong. A tab that reported either number alone would state a confident
// verdict that the other method contradicts.
//
// The adjusted figure moved with rosterbot-492 -- the measured ladder keeps
// 0.62 of the second asset where the old unmeasured 0.70 kept more, so the
// margin widened from 37 to 136. The VERDICT is unchanged, which is the point:
// the two methods still name opposite winners, and this trade was always
// too-close-to-call rather than close because of where 0.70 happened to sit.
func TestEvaluate_LiveTwoForOne_MethodsDisagree_IsTooClose(t *testing.T) {
	ib := player("Intentional Balk", "Gage Jump", 1391, "Emmet Sheehan", 1234)
	ys := player("Yordan's Schlong", "Kyle Harrison", 2292)

	if got := ib.Raw(); got != 2625 {
		t.Errorf("Intentional Balk raw = %d, want 2625", got)
	}
	approx(t, ib.Adjusted(), 2156.08, 0.01, "Intentional Balk adjusted")
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

// The single observation the old geometric decay was fitted to, kept as the
// historical record. The measured ladder still lands on it -- 589 + 0.62(540) +
// 0.60(366) = 1143.4 against HKB's reported 1144 -- which is exactly why one
// point could not adjudicate anything: both curves pass through it, and they
// diverge by 38% elsewhere. The observation set that CAN adjudicate is
// testdata/hkb_calculator_2026-09-04.csv (see hkb_calibration_test.go).
func TestAdjusted_StillReproducesTheOriginalSingleObservation(t *testing.T) {
	s := player("T", "Ritchie", 589, "Rodriguez-Cruz", 540, "McGreevy", 366)
	if got := s.Raw(); got != 1495 {
		t.Fatalf("Raw = %d, want 1495", got)
	}
	approx(t, s.Adjusted(), 1143.4, 0.01, "Adjusted")
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
// stability requirement as the optimizer's player-ID tiebreaker -- but that
// stability must not escape as a verdict (rosterbot-h688).
//
// This test previously asserted `RawLeader == "Aardvark"`, i.e. that the
// lexical tiebreak WAS the answer, which pinned the bug as intended behaviour.
// A symmetric 1-for-1 swap of equally valued players rendered as
// "favors Aardvark (+0%)" in both the dashboard and the Pushover trade alert.
func TestEvaluate_TiedSides_AreNotAVerdict(t *testing.T) {
	sides := []Side{player("Zebra", "p1", 500), player("Aardvark", "p2", 500)}
	first := Evaluate(sides)
	for i := 0; i < 20; i++ {
		if got := Evaluate(sides); got != first {
			t.Fatalf("verdict not stable: %+v then %+v", first, got)
		}
	}
	if first.Status != StatusDeadEven {
		t.Fatalf("Status = %q, want %q", first.Status, StatusDeadEven)
	}
	if first.FavoredTeam != "" || first.RawLeader != "" || first.AdjLeader != "" {
		t.Errorf("verdict named a team on a tie: %+v", first)
	}
	// Nothing failed to price, so the view must not blame an asset.
	if first.UnpricedAssets != 0 {
		t.Errorf("UnpricedAssets = %d, want 0", first.UnpricedAssets)
	}
}

// Every side worth zero reaches StatusDeadEven by the same route as any other
// tie -- all priced, all equal -- which is why this package needs no separate
// zero guard of the kind internal/dynasty carries.
func TestEvaluate_AllSidesWorthZero_IsNotAVerdict(t *testing.T) {
	v := Evaluate([]Side{player("Alpha", "p1", 0), player("Beta", "p2", 0)})
	if v.Status != StatusDeadEven {
		t.Fatalf("Status = %q, want %q", v.Status, StatusDeadEven)
	}
	if v.FavoredTeam != "" {
		t.Errorf("FavoredTeam = %q, want empty", v.FavoredTeam)
	}
}

// A raw tie that the adjusted method DOES separate is a disagreement about
// whether there is a winner at all, and must read too-close.
//
// Before rosterbot-h688 this was decided by luck: raw's lexical tiebreak
// produced a leader, and the verdict depended on whether that leader happened
// to coincide with the adjusted winner. Alpha sorts first and also wins on
// adjusted value here, so the old code returned StatusFavors at raw 0.0% --
// the losing side of the coin toss the bead's own measurement never saw.
func TestEvaluate_RawTieSeparatedByAdjusted_IsTooClose(t *testing.T) {
	// Both sides total 600 raw. Alpha carries it in one asset, Beta splits it
	// across two, so the package discount separates them: 600 vs
	// 300 + 300(0.62) = 486.
	alpha := Side{Team: "Alpha", Assets: []Asset{{Name: "a1", Value: 600, Priced: true}}}
	beta := Side{Team: "Beta", Assets: []Asset{
		{Name: "b1", Value: 300, Priced: true},
		{Name: "b2", Value: 300, Priced: true},
	}}
	v := Evaluate([]Side{alpha, beta})
	if v.Status != StatusTooClose {
		t.Fatalf("Status = %q, want %q (raw is level, adjusted is not)", v.Status, StatusTooClose)
	}
	if v.FavoredTeam != "" {
		t.Errorf("FavoredTeam = %q, want empty", v.FavoredTeam)
	}
	if v.RawLeader != "" {
		t.Errorf("RawLeader = %q, want empty — raw did not separate the sides", v.RawLeader)
	}
	if v.AdjLeader != "Alpha" {
		t.Errorf("AdjLeader = %q, want Alpha", v.AdjLeader)
	}
}

// A tie between the LEADING sides is still a tie when a third, lower side
// exists — no side leads, so no winner can be named. The package is n-sided
// throughout (this league has only ever traded two-sided, but internal/dynasty
// grades Sleeper trades where three rosters are ordinary), so the verdict must
// be about the leaders rather than about "both sides".
//
// The copy this drives is deliberately count-neutral for the same reason:
// "the leading sides price exactly level" is true at any N, where "both sides"
// asserts a two-sided trade the verdict does not know it has.
func TestEvaluate_ThreeSides_TopTwoLevel_IsDeadEven(t *testing.T) {
	v := Evaluate([]Side{
		player("Alpha", "p1", 1000),
		player("Beta", "p2", 1000),
		player("Gamma", "p3", 10),
	})
	if v.Status != StatusDeadEven {
		t.Fatalf("Status = %q, want %q — the two leading sides are level, so none leads", v.Status, StatusDeadEven)
	}
	if v.FavoredTeam != "" {
		t.Errorf("FavoredTeam = %q, want empty", v.FavoredTeam)
	}
}

// A clear leader over two level runners-up is a real verdict: the leader is
// unique, which is the only thing that has to be true to name one.
func TestEvaluate_ThreeSides_ClearLeaderOverLevelRunnersUp_Favors(t *testing.T) {
	v := Evaluate([]Side{
		player("Alpha", "p1", 2000),
		player("Beta", "p2", 500),
		player("Gamma", "p3", 500),
	})
	if v.Status != StatusFavors || v.FavoredTeam != "Alpha" {
		t.Fatalf("verdict = %+v, want favors Alpha", v)
	}
}

// The tie guard must not swallow a real, merely-narrow verdict.
func TestEvaluate_NearTie_StillFavorsTheLeader(t *testing.T) {
	v := Evaluate([]Side{player("Alpha", "p1", 501), player("Beta", "p2", 500)})
	if v.Status != StatusFavors || v.FavoredTeam != "Alpha" {
		t.Fatalf("verdict = %+v, want favors Alpha", v)
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

func testPickPlayers() []hkb.Player {
	return []hkb.Player{
		{Name: "2027 Early 1st", AssetType: "PICK", Value: 1456},
		{Name: "2027 Mid 1st", AssetType: "PICK", Value: 821},
		{Name: "2027 Late 1st", AssetType: "PICK", Value: 778},
	}
}

// rosterbot-uc3: a pick still tied to a team's future draft slot (Fantrax
// gave us a year, round and the original-owner team, no resolved number)
// prices at the average of whatever tiers HKB currently lists for that
// year+round, and is flagged Estimated so it reads differently from a
// direct name match.
func TestNewDraftPickAsset_StillProjected_PricesAtTheTierAverage(t *testing.T) {
	a := NewDraftPickAsset(2027, 1, 0, "Houston Swang and Bang", testPickPlayers())
	wantAvg := (1456 + 821 + 778) / 3
	if !a.Priced || !a.IsPick || !a.Estimated {
		t.Fatalf("got %+v, want Priced, IsPick and Estimated all true", a)
	}
	if a.Value != wantAvg {
		t.Errorf("Value = %d, want %d", a.Value, wantAvg)
	}
	if a.Name != "2027 1st Round Pick (Houston Swang and Bang)" {
		t.Errorf("Name = %q", a.Name)
	}
}

// A resolved slot (the draft order is already known) carries no team
// ambiguity, but by the same token HKB has stopped listing it as an
// upcoming PICK asset -- PickAverage finds zero tiers and the asset stays
// unpriced, same as an unidentified pick did before this recovery existed.
func TestNewDraftPickAsset_ResolvedSlot_NoLongerListedByHKB_StaysUnpriced(t *testing.T) {
	a := NewDraftPickAsset(2026, 1, 6, "", testPickPlayers())
	if a.Priced || a.Estimated {
		t.Fatalf("got %+v, want !Priced and !Estimated", a)
	}
	if !a.IsPick {
		t.Error("want IsPick true even when unpriced")
	}
	if a.Name != "2026 1st Round Pick (Pick 6)" {
		t.Errorf("Name = %q", a.Name)
	}
}

// An asset can be both IsPick and Priced now, which used to be impossible --
// pin that a caller checking IsPick alone (e.g. formatting code) still sees
// Value/Priced reflect the estimate rather than always reading as 0/false.
func TestNewDraftPickAsset_FeedsIntoEvaluateLikeAnyPricedAsset(t *testing.T) {
	pick := NewDraftPickAsset(2027, 2, 0, "Some Team", testPickPlayers())
	if pick.Priced {
		t.Fatalf("fixture has no 2027 2nd tiers, expected this pick to stay unpriced for the test setup")
	}
	// Re-derive with a round that IS covered so the fed-in Evaluate actually
	// exercises the priced path.
	pick = NewDraftPickAsset(2027, 1, 0, "Some Team", testPickPlayers())
	side := Side{Team: "A", Assets: []Asset{pick}}
	other := Side{Team: "B", Assets: []Asset{{Name: "Player", Value: 100, Priced: true}}}

	v := Evaluate([]Side{side, other})
	if v.Status == StatusIncomplete {
		t.Errorf("Status = incomplete, want a verdict now that the pick is priced: %+v", v)
	}
}
