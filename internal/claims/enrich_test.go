package claims

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/statcast"
)

func TestEnrichSignals_TagsHitterBuyLow(t *testing.T) {
	const id = 4242
	b := &statcast.Bundle{
		HitterExp: map[int]statcast.HitterRow{
			id: {PA: 120, WOBA: 0.300, XwOBA: 0.350},
		},
		HitterSC: map[int]statcast.HitterStatcastRow{
			id: {Barrel: 12, HardHit: 45},
		},
	}
	moves := []Move{
		{Added: []SidePlayer{{Name: "Buy Low Bat", MLBAMID: id, IsPitcher: false}}},
	}

	EnrichSignals(moves, b, statcast.DefaultThresholds())

	if got := moves[0].Added[0].Signal; got != statcast.SignalBuyLow && got != statcast.SignalBoth {
		t.Errorf("expected BUY-LOW (or BOTH), got %q", got.String())
	}
}

func TestEnrichSignals_NilBundleNoop(t *testing.T) {
	moves := []Move{{Added: []SidePlayer{{Name: "x", MLBAMID: 1}}}}
	EnrichSignals(moves, nil, statcast.DefaultThresholds()) // must not panic
	if moves[0].Added[0].Signal != statcast.SignalNone {
		t.Error("nil bundle should leave SignalNone")
	}
}

func TestEnrichSignals_ZeroMLBAMIDSkipped(t *testing.T) {
	moves := []Move{{Added: []SidePlayer{{Name: "Unresolved", MLBAMID: 0}}}}
	EnrichSignals(moves, &statcast.Bundle{}, statcast.DefaultThresholds())
	if moves[0].Added[0].Signal != statcast.SignalNone {
		t.Error("player with MLBAMID 0 should be skipped, leaving SignalNone")
	}
}
