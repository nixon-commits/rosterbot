package claims

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/waivers"
)

func TestEnrichSignals_TagsHitterBuyLow(t *testing.T) {
	const id = 4242
	b := &waivers.SavantBundle{
		HitterExp: map[int]waivers.SavantHitterRow{
			id: {PA: 120, WOBA: 0.300, XwOBA: 0.350},
		},
		HitterSC: map[int]waivers.SavantHitterStatcastRow{
			id: {Barrel: 12, HardHit: 45},
		},
	}
	moves := []Move{
		{Added: []SidePlayer{{Name: "Buy Low Bat", MLBAMID: id, IsPitcher: false}}},
	}

	EnrichSignals(moves, b, waivers.DefaultThresholds())

	if got := moves[0].Added[0].Signal; got != waivers.SignalBuyLow && got != waivers.SignalBoth {
		t.Errorf("expected BUY-LOW (or BOTH), got %q", got.String())
	}
}

func TestEnrichSignals_NilBundleNoop(t *testing.T) {
	moves := []Move{{Added: []SidePlayer{{Name: "x", MLBAMID: 1}}}}
	EnrichSignals(moves, nil, waivers.DefaultThresholds()) // must not panic
	if moves[0].Added[0].Signal != waivers.SignalNone {
		t.Error("nil bundle should leave SignalNone")
	}
}

func TestEnrichSignals_ZeroMLBAMIDSkipped(t *testing.T) {
	moves := []Move{{Added: []SidePlayer{{Name: "Unresolved", MLBAMID: 0}}}}
	EnrichSignals(moves, &waivers.SavantBundle{}, waivers.DefaultThresholds())
	if moves[0].Added[0].Signal != waivers.SignalNone {
		t.Error("player with MLBAMID 0 should be skipped, leaving SignalNone")
	}
}
