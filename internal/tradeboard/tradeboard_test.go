package tradeboard

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
)

const (
	me    = "Intentional Balk"
	them  = "Yordan's Schlong"
	other = "BT95"
)

var now = time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)

func hkbPlayers() []hkb.Player {
	return []hkb.Player{
		{Name: "Gage Jump", Value: 1391, Rank: 70, Age: 23.3, PitcherStats: &hkb.PitcherStats{}},
		{Name: "Emmet Sheehan", Value: 1234, Rank: 85, Age: 26.7, PitcherStats: &hkb.PitcherStats{}},
		{Name: "Kyle Harrison", Value: 2292, Rank: 40, Age: 24.9, PitcherStats: &hkb.PitcherStats{}},
		{Name: "Alec Bohm", Value: 900, Rank: 210, Age: 29.1, HitterStats: &hkb.HitterStats{}},
		{Name: "2027 Early 1st", Value: 1419, Rank: 134, AssetType: "PICK"},
		{Name: "2028 Late 3rd", Value: 45, Rank: 774, AssetType: "PICK"},
	}
}

// The live 2-for-1, end to end through the producer path.
func liveOffer() []OfferInput {
	return []OfferInput{
		{TradeID: "lpe0ltl", Player: "Gage Jump", Position: "SP", From: them, To: me},
		{TradeID: "lpe0ltl", Player: "Kyle Harrison", Position: "SP", From: me, To: them},
		{TradeID: "lpe0ltl", Player: "Emmet Sheehan", Position: "SP", From: them, To: me},
	}
}

func TestBuildOffers_PricesBothSidesAndCarriesTheVerdict(t *testing.T) {
	offers := BuildOffers(liveOffer(), hkbPlayers())
	if len(offers) != 1 {
		t.Fatalf("got %d offers, want 1", len(offers))
	}
	o := offers[0]
	if o.Verdict.Status != tradevalue.StatusTooClose {
		t.Errorf("Status = %q, want too-close", o.Verdict.Status)
	}
	if len(o.Sides) != 2 {
		t.Fatalf("got %d sides, want 2", len(o.Sides))
	}
	// Sides are sorted by team name: "Intentional Balk" < "Yordan's Schlong".
	if o.Sides[0].Team != me {
		t.Fatalf("Sides[0] = %q, want %q", o.Sides[0].Team, me)
	}
	if o.Sides[0].Raw != 2625 {
		t.Errorf("my raw = %d, want 2625", o.Sides[0].Raw)
	}
	if o.Sides[0].PricedCount != 2 || o.Sides[0].AssetCount != 2 {
		t.Errorf("my coverage = %d/%d, want 2/2", o.Sides[0].PricedCount, o.Sides[0].AssetCount)
	}
	if o.Sides[1].Raw != 2292 {
		t.Errorf("their raw = %d, want 2292", o.Sides[1].Raw)
	}
}

func TestBuildOffers_IsDeterministic(t *testing.T) {
	first := BuildOffers(liveOffer(), hkbPlayers())
	for i := 0; i < 25; i++ {
		got := BuildOffers(liveOffer(), hkbPlayers())
		if len(got) != len(first) {
			t.Fatalf("offer count moved between runs")
		}
		for j := range got {
			if got[j].TradeID != first[j].TradeID || got[j].Sides[0].Team != first[j].Sides[0].Team {
				t.Fatalf("offer ordering not stable: %+v vs %+v", first[j], got[j])
			}
		}
	}
}

func TestBuildOffers_EmptyInputYieldsNoOffersNotAnError(t *testing.T) {
	if got := BuildOffers(nil, hkbPlayers()); len(got) != 0 {
		t.Errorf("got %d offers from empty input, want 0", len(got))
	}
}

func TestMine_FiltersToMyTrades(t *testing.T) {
	inputs := append(liveOffer(),
		OfferInput{TradeID: "zzz", Player: "Alec Bohm", From: other, To: them},
		OfferInput{TradeID: "zzz", Player: "Gage Jump", From: them, To: other},
	)
	all := BuildOffers(inputs, hkbPlayers())
	if len(all) != 2 {
		t.Fatalf("got %d offers, want 2", len(all))
	}
	mine := Mine(all, me)
	if len(mine) != 1 || mine[0].TradeID != "lpe0ltl" {
		t.Errorf("Mine = %+v, want just lpe0ltl", mine)
	}
}

func teams() []teamvalue.Row {
	return []teamvalue.Row{
		{TeamID: "1", TeamName: me, PitcherMLBValue: 5000, HitterMLBValue: 3000},
		{TeamID: "2", TeamName: them, PitcherMLBValue: 4000, HitterMLBValue: 6000},
		{TeamID: "3", TeamName: other, PitcherMLBValue: 4500, HitterMLBValue: 1000},
	}
}

func pvs() []PlayerValue {
	return []PlayerValue{
		{Name: "Gage Jump", Value: 1391, IsPitcher: true, Matched: true},
		{Name: "Emmet Sheehan", Value: 1234, IsPitcher: true, Matched: true},
		{Name: "Kyle Harrison", Value: 2292, IsPitcher: true, Matched: true},
		{Name: "Alec Bohm", Value: 900, Matched: true},
	}
}

func metric(t *testing.T, imp *Impact, name string) ImpactMetric {
	t.Helper()
	for _, m := range imp.Metrics {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no %q metric in %+v", name, imp.Metrics)
	return ImpactMetric{}
}

func TestBuildImpact_MovesValueBetweenTheRightLeavesAndReRanks(t *testing.T) {
	imp := BuildImpact(me, liveOffer(), teams(), pvs())
	if imp == nil {
		t.Fatal("BuildImpact = nil, want an impact")
	}

	// I receive 1391+1234 and give up 2292: pitcher value +333.
	p := metric(t, imp, "Pitchers")
	if p.Before != 5000 || p.After != 5333 || p.Delta != 333 {
		t.Errorf("Pitchers = %d -> %d (delta %d), want 5000 -> 5333 (+333)", p.Before, p.After, p.Delta)
	}
	// I was already 1st in pitcher value and stay 1st.
	if p.RankBefore != 1 || p.RankAfter != 1 {
		t.Errorf("Pitchers rank = %d -> %d, want 1 -> 1", p.RankBefore, p.RankAfter)
	}
	if p.Teams != 3 {
		t.Errorf("Teams = %d, want 3", p.Teams)
	}

	// No hitters changed hands.
	if h := metric(t, imp, "Hitters"); h.Delta != 0 {
		t.Errorf("Hitters delta = %d, want 0", h.Delta)
	}
	if tot := metric(t, imp, "Total"); tot.Delta != 333 {
		t.Errorf("Total delta = %d, want 333", tot.Delta)
	}
}

// The rank is the reason this exists: a value-neutral trade can still move
// you in the standings for a category.
func TestBuildImpact_RankChangesWhenValueCrossesARival(t *testing.T) {
	// Give up Harrison (2292) for Bohm (900): pitcher value 5000 -> 2708,
	// dropping me from 1st to 3rd behind both rivals.
	inputs := []OfferInput{
		{TradeID: "x", Player: "Kyle Harrison", From: me, To: them},
		{TradeID: "x", Player: "Alec Bohm", From: them, To: me},
	}
	imp := BuildImpact(me, inputs, teams(), pvs())
	if imp == nil {
		t.Fatal("BuildImpact = nil")
	}
	p := metric(t, imp, "Pitchers")
	if p.After != 2708 {
		t.Errorf("Pitchers after = %d, want 2708", p.After)
	}
	if p.RankBefore != 1 || p.RankAfter != 3 {
		t.Errorf("Pitchers rank = %d -> %d, want 1 -> 3", p.RankBefore, p.RankAfter)
	}
	h := metric(t, imp, "Hitters")
	if h.Delta != 900 {
		t.Errorf("Hitters delta = %d, want +900", h.Delta)
	}
}

func TestBuildImpact_SuppressedWhenAnythingCannotBeResolved(t *testing.T) {
	cases := map[string][]OfferInput{
		"unidentified draft pick": {
			{TradeID: "x", Player: "", From: them, To: me},
			{TradeID: "x", Player: "Kyle Harrison", From: me, To: them},
		},
		"player missing from the values table": {
			{TradeID: "x", Player: "Nobody At All", From: them, To: me},
			{TradeID: "x", Player: "Kyle Harrison", From: me, To: them},
		},
		"team the values table does not know": {
			{TradeID: "x", Player: "Gage Jump", From: "Ghost Team", To: me},
			{TradeID: "x", Player: "Kyle Harrison", From: me, To: "Ghost Team"},
		},
	}
	for name, inputs := range cases {
		t.Run(name, func(t *testing.T) {
			if imp := BuildImpact(me, inputs, teams(), pvs()); imp != nil {
				t.Errorf("BuildImpact = %+v, want nil", imp)
			}
		})
	}
}

func TestBuildImpact_NilWhenIAmNotAParticipant(t *testing.T) {
	inputs := []OfferInput{
		{TradeID: "x", Player: "Gage Jump", From: them, To: other},
		{TradeID: "x", Player: "Alec Bohm", From: other, To: them},
	}
	if imp := BuildImpact("Not A Team", inputs, teams(), pvs()); imp != nil {
		t.Errorf("BuildImpact = %+v, want nil", imp)
	}
}

func TestBuildValuesTable(t *testing.T) {
	pool := []PoolPlayer{
		{Name: "Kyle Harrison", Positions: []string{"SP"}, FantasyTeamID: "1", IsPitcher: true},
		{Name: "Alec Bohm", Positions: []string{"3B"}, FantasyTeamID: "1"},
		{Name: "Unranked Nobody", Positions: []string{"OF"}, FantasyTeamID: "2"},
		{Name: "Some Free Agent", Positions: []string{"OF"}}, // no team: skipped
	}
	names := map[string]string{"1": me, "2": them}

	vt := BuildValuesTable(now, pool, hkbPlayers(), teams(), names)

	if vt.Rostered != 3 || vt.Matched != 2 {
		t.Errorf("coverage = %d/%d, want 2/3", vt.Matched, vt.Rostered)
	}
	if len(vt.Players) != 3 {
		t.Fatalf("got %d players, want 3 (free agent excluded)", len(vt.Players))
	}
	// Sorted by value descending.
	if vt.Players[0].Name != "Kyle Harrison" || vt.Players[0].Value != 2292 {
		t.Errorf("Players[0] = %+v, want Kyle Harrison at 2292", vt.Players[0])
	}
	if vt.Players[0].OwnerName != me {
		t.Errorf("OwnerName = %q, want %q", vt.Players[0].OwnerName, me)
	}
	// The unmatched player is present but flagged, not silently dropped.
	last := vt.Players[len(vt.Players)-1]
	if last.Name != "Unranked Nobody" || last.Matched || last.Value != 0 {
		t.Errorf("unmatched row = %+v, want present with Matched=false and Value=0", last)
	}
	// Picks come through as a reference list, value-sorted.
	if len(vt.Picks) != 2 || vt.Picks[0].Name != "2027 Early 1st" {
		t.Errorf("Picks = %+v, want 2 with the 1419 first", vt.Picks)
	}
}

func TestInferOutcome(t *testing.T) {
	offer := BuildOffers(liveOffer(), hkbPlayers())[0]

	exact := []ExecutedAsset{
		{TradeGroupID: "g1", Player: "Gage Jump", To: me},
		{TradeGroupID: "g1", Player: "Emmet Sheehan", To: me},
		{TradeGroupID: "g1", Player: "Kyle Harrison", To: them},
	}
	if got := InferOutcome(offer, exact); got != OutcomeAccepted {
		t.Errorf("exact match = %q, want %q", got, OutcomeAccepted)
	}

	// A different trade between the same two teams must not claim it.
	partial := []ExecutedAsset{
		{TradeGroupID: "g2", Player: "Gage Jump", To: me},
		{TradeGroupID: "g2", Player: "Kyle Harrison", To: them},
	}
	if got := InferOutcome(offer, partial); got != OutcomeNotExecuted {
		t.Errorf("partial match = %q, want %q", got, OutcomeNotExecuted)
	}
	if got := InferOutcome(offer, nil); got != OutcomeNotExecuted {
		t.Errorf("no executed trades = %q, want %q", got, OutcomeNotExecuted)
	}
}

func TestMergeLog_PreservesFirstSeenAndResolvesVanishedOffers(t *testing.T) {
	offers := BuildOffers(liveOffer(), hkbPlayers())
	earlier := now.Add(-48 * time.Hour)

	// First observation.
	day1 := MergeLog(earlier, me, nil, offers, nil)
	if len(day1) != 1 || day1[0].Outcome != OutcomeOpen {
		t.Fatalf("day1 = %+v, want one open row", day1)
	}
	if !day1[0].FirstSeen.Equal(earlier) {
		t.Errorf("FirstSeen = %v, want %v", day1[0].FirstSeen, earlier)
	}

	// Still open a day later: FirstSeen holds, LastSeen advances.
	day2 := MergeLog(now, me, day1, offers, nil)
	if !day2[0].FirstSeen.Equal(earlier) {
		t.Errorf("FirstSeen moved to %v, want %v", day2[0].FirstSeen, earlier)
	}
	if !day2[0].LastSeen.Equal(now) {
		t.Errorf("LastSeen = %v, want %v", day2[0].LastSeen, now)
	}

	// Gone, with no matching executed trade.
	day3 := MergeLog(now, me, day2, nil, nil)
	if day3[0].Outcome != OutcomeNotExecuted {
		t.Errorf("Outcome = %q, want %q", day3[0].Outcome, OutcomeNotExecuted)
	}
	// The as-of valuation survives the offer disappearing.
	if len(day3[0].Sides) != 2 || day3[0].Sides[0].Raw != 2625 {
		t.Errorf("valuation lost on resolve: %+v", day3[0].Sides)
	}

	// A resolved row does not get re-judged on later runs.
	day4 := MergeLog(now, me, day3, nil, []ExecutedAsset{
		{TradeGroupID: "g", Player: "Gage Jump", To: me},
		{TradeGroupID: "g", Player: "Emmet Sheehan", To: me},
		{TradeGroupID: "g", Player: "Kyle Harrison", To: them},
	})
	if day4[0].Outcome != OutcomeNotExecuted {
		t.Errorf("resolved outcome changed to %q; it must be decided once", day4[0].Outcome)
	}
}

func TestLogRoundTripsThroughTheStore(t *testing.T) {
	rows := MergeLog(now, me, nil, BuildOffers(liveOffer(), hkbPlayers()), nil)

	b, err := MarshalNDJSON(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := UnmarshalNDJSON(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].TradeID != "lpe0ltl" {
		t.Fatalf("round trip = %+v", got)
	}
	if got[0].Sides[0].Raw != 2625 || got[0].Verdict.Status != tradevalue.StatusTooClose {
		t.Errorf("valuation did not survive the round trip: %+v", got[0])
	}
	if want := "dt=2026-08-06/offers.ndjson"; ObjectKey(now) != want {
		t.Errorf("ObjectKey = %q, want %q", ObjectKey(now), want)
	}
}
