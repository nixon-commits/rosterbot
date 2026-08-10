//go:build corpus

// Run with: go test -tags corpus ./internal/tradeboard/ -v -run Corpus
//
// Build-tagged for the same reason as internal/tradevalue's corpus test and
// internal/s3blob's live test: it reads real artifacts from the working tree
// that CI does not have. Populate with:
//
//	go run . team-values
//
// What this covers that the unit tests cannot: Impact against a real league.
// The unit tests use three synthetic teams with round numbers, where every
// rank change is one the fixture was built to produce. Here the denominator is
// the actual 10-team league with its actual HKB values, which is the only way
// to see whether the metric moves at all on real inputs -- the rosterbot-hx5
// question.
package tradeboard

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
)

// The two offers pending against Intentional Balk on 2026-08-05, recorded from
// .cache/fantrax-pending-trades-epsb8xzlmj203yrx.json before they resolved.
// Kept verbatim because they are the only real multi-asset offers observed so
// far, and one of them is the 2-for-1 whose verdict the package adjustment
// inverts. Note the U+2019 apostrophe: it is what Fantrax actually sends.
func recordedLiveOffers() []OfferInput {
	const (
		ib = "Intentional Balk"
		ys = "Yordan’s Schlong"
	)
	return []OfferInput{
		{TradeID: "hohgkc3cmsde2ig7", Player: "Lazaro Montes", Position: "OF", From: ys, To: ib},
		{TradeID: "hohgkc3cmsde2ig7", Player: "Andrew Fischer", Position: "3B,INF", From: ib, To: ys},
		{TradeID: "hohgkc3cmsde2ig7", Player: "Kyle Harrison", Position: "SP", From: ib, To: ys},
		{TradeID: "hohgkc3cmsde2ig7", Player: "Cole Carrigg", Position: "OF", From: ys, To: ib},
		{TradeID: "hohgkc3cmsde2ig7", Player: "Heriberto Hernandez", Position: "OF", From: ys, To: ib},
		{TradeID: "hohgkc3cmsde2ig7", Player: "Randy Arozarena", Position: "OF", From: ib, To: ys},
		{TradeID: "hohgkc3cmsde2ig7", Player: "Kyle Leahy", Position: "RP", From: ys, To: ib},

		{TradeID: "lpe0ltlcmsdt40zv", Player: "Gage Jump", Position: "SP", From: ys, To: ib},
		{TradeID: "lpe0ltlcmsdt40zv", Player: "Kyle Harrison", Position: "SP", From: ib, To: ys},
		{TradeID: "lpe0ltlcmsdt40zv", Player: "Emmet Sheehan", Position: "SP", From: ys, To: ib},
	}
}

func loadValuesTable(t *testing.T) ValuesTable {
	t.Helper()
	b, err := os.ReadFile("../../.tradevalues/tradevalues-values.json")
	if err != nil {
		t.Skip("no .tradevalues table; run `go run . team-values` first")
	}
	var vt ValuesTable
	if err := json.Unmarshal(b, &vt); err != nil {
		t.Fatalf("parse values table: %v", err)
	}
	return vt
}

func loadHKB(t *testing.T) []hkb.Player {
	t.Helper()
	b, err := os.ReadFile("../../.cache/hkb-players.json")
	if err != nil {
		t.Skip("no .cache/hkb-players.json")
	}
	var env struct {
		Data []hkb.Player `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("parse hkb: %v", err)
	}
	return env.Data
}

func TestCorpus_LiveOffersAgainstTheRealLeague(t *testing.T) {
	const me = "Intentional Balk"
	vt := loadValuesTable(t)
	offers := Mine(BuildOffers(recordedLiveOffers(), loadHKB(t)), me)

	if len(offers) != 2 {
		t.Fatalf("got %d offers, want 2", len(offers))
	}
	t.Logf("league: %d teams, %d players, HKB coverage %d/%d",
		len(vt.Teams), len(vt.Players), vt.Matched, vt.Rostered)

	byTrade := map[string][]OfferInput{}
	for _, in := range recordedLiveOffers() {
		byTrade[in.TradeID] = append(byTrade[in.TradeID], in)
	}

	sawRankMove := false
	for _, o := range offers {
		t.Logf("--- %s: %s", o.TradeID, o.Verdict.Status)
		for _, s := range o.Sides {
			t.Logf("    %-20s raw %6d  adj %7.0f  (%d/%d priced)",
				s.Team, s.Raw, s.Adjusted, s.PricedCount, s.AssetCount)
		}

		imp, note := BuildImpact(me, byTrade[o.TradeID], vt.Teams, vt.Players)
		if imp == nil {
			// Legitimate against a recorded offer: these were captured on
			// 2026-08-05 and an asset has since been dropped to free agency
			// (Heriberto Hernandez), so it prices from HKB but sits on no
			// roster. What must never happen is silent suppression.
			if note == "" {
				t.Errorf("%s: Impact = nil with no reason given", o.TradeID)
			}
			t.Logf("    impact unavailable: %s", note)
			continue
		}
		if note != "" {
			t.Errorf("%s: got both an impact and a note %q", o.TradeID, note)
		}
		for _, m := range imp.Metrics {
			t.Logf("    %-9s %7d -> %-7d (%+6d)  rank %2d -> %2d of %d",
				m.Name, m.Before, m.After, m.Delta, m.RankBefore, m.RankAfter, m.Teams)
			if m.RankBefore != m.RankAfter {
				sawRankMove = true
			}
			if m.Teams != len(vt.Teams) {
				t.Errorf("%s/%s: Teams = %d, want %d", o.TradeID, m.Name, m.Teams, len(vt.Teams))
			}
			if m.After-m.Before != m.Delta {
				t.Errorf("%s/%s: Delta %d does not match After-Before %d", o.TradeID, m.Name, m.Delta, m.After-m.Before)
			}
		}
	}

	// The 2-for-1 is the case the whole verdict rule exists for.
	var twoForOne Offer
	for _, o := range offers {
		if o.TradeID == "lpe0ltlcmsdt40zv" {
			twoForOne = o
		}
	}
	if twoForOne.Verdict.Status != tradevalue.StatusTooClose {
		t.Errorf("the 2-for-1 reads %q against live values, want too-close; "+
			"if HKB values have moved enough to settle it, that is fine but worth knowing",
			twoForOne.Verdict.Status)
	}

	// rosterbot-hx5: a metric that cannot move is not a measurement. Rank is
	// the whole reason Impact exists over a bare value delta.
	if !sawRankMove {
		t.Errorf("no metric changed rank on either real offer — Impact may be reporting a constant")
	}
}

func TestCorpus_ValuesTableShape(t *testing.T) {
	vt := loadValuesTable(t)

	if vt.Rostered == 0 {
		t.Fatal("values table has no rostered players")
	}
	cover := float64(vt.Matched) / float64(vt.Rostered)
	t.Logf("HKB join coverage: %d/%d (%.1f%%)", vt.Matched, vt.Rostered, cover*100)
	if cover < 0.90 {
		t.Errorf("HKB join coverage %.1f%% — below 90%%, the name join has likely regressed", cover*100)
	}
	if len(vt.Picks) == 0 {
		t.Error("no pick assets in the values table; HKB ships 18")
	}
	// Every team in the league should have players attributed to it.
	owners := map[string]int{}
	for _, p := range vt.Players {
		owners[p.OwnerName]++
	}
	if len(owners) != len(vt.Teams) {
		t.Errorf("players attributed to %d owners but %d teams exist: %v", len(owners), len(vt.Teams), owners)
	}
	if _, ok := owners[""]; ok {
		t.Error("some rostered players have no owner name")
	}
	// Unmatched rows must be present-and-flagged, never silently dropped.
	if vt.Matched < vt.Rostered {
		found := false
		for _, p := range vt.Players {
			if !p.Matched {
				found = true
				if p.Value != 0 {
					t.Errorf("unmatched player %q carries value %d, want 0", p.Name, p.Value)
				}
			}
		}
		if !found {
			t.Error("Matched < Rostered but no row is flagged unmatched")
		}
	}
	if vt.GeneratedAt.IsZero() || vt.GeneratedAt.After(time.Now().Add(time.Hour)) {
		t.Errorf("GeneratedAt = %v, implausible", vt.GeneratedAt)
	}
}
