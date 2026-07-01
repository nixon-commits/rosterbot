package statcast

import "testing"

// hitterBundle returns a bundle suitable for hitter tag tests.
func hitterBundle(season HitterRow, sc HitterStatcastRow, win HitterRow, hasWin bool) *Bundle {
	b := &Bundle{
		HitterExp:    map[int]HitterRow{},
		HitterSC:     map[int]HitterStatcastRow{},
		HitterExp14d: map[int]HitterRow{},
	}
	if season.MLBAMID > 0 {
		b.HitterExp[season.MLBAMID] = season
	}
	if sc.MLBAMID > 0 {
		b.HitterSC[sc.MLBAMID] = sc
	}
	if hasWin && win.MLBAMID > 0 {
		b.HitterExp14d[win.MLBAMID] = win
	}
	return b
}

func TestTagHitter_BuyLow(t *testing.T) {
	b := hitterBundle(
		HitterRow{MLBAMID: 1, PA: 200, WOBA: 0.310, XwOBA: 0.360}, // gap = 0.050
		HitterStatcastRow{MLBAMID: 1, Barrel: 12.0, HardHit: 46.0},
		HitterRow{}, false,
	)
	sig, m := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalBuyLow {
		t.Fatalf("want BuyLow, got %v", sig)
	}
	if m.BuyLowDelta < 0.04 {
		t.Errorf("BuyLowDelta unexpected: %v", m.BuyLowDelta)
	}
}

func TestTagHitter_Hot(t *testing.T) {
	b := hitterBundle(
		HitterRow{MLBAMID: 1, PA: 200, WOBA: 0.350, XwOBA: 0.360}, // gap too small
		HitterStatcastRow{MLBAMID: 1, Barrel: 9.0, HardHit: 40.0}, // HH < 42 → no buy-low
		HitterRow{MLBAMID: 1, PA: 25, WOBA: 0.420, XwOBA: 0.380},
		true,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalHot {
		t.Fatalf("want Hot, got %v", sig)
	}
}

func TestTagHitter_Both(t *testing.T) {
	b := hitterBundle(
		HitterRow{MLBAMID: 1, PA: 200, WOBA: 0.310, XwOBA: 0.360},
		HitterStatcastRow{MLBAMID: 1, Barrel: 12.0, HardHit: 46.0},
		HitterRow{MLBAMID: 1, PA: 25, WOBA: 0.420, XwOBA: 0.380},
		true,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalBoth {
		t.Fatalf("want Both, got %v", sig)
	}
}

func TestTagHitter_SampleGuard(t *testing.T) {
	b := hitterBundle(
		HitterRow{MLBAMID: 1, PA: 50, WOBA: 0.300, XwOBA: 0.380}, // PA below 80
		HitterStatcastRow{MLBAMID: 1, Barrel: 15.0, HardHit: 48.0},
		HitterRow{}, false,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None (sample guard), got %v", sig)
	}
}

func TestTagHitter_Window14dPAGuard(t *testing.T) {
	b := hitterBundle(
		HitterRow{MLBAMID: 1, PA: 200, WOBA: 0.350, XwOBA: 0.360},
		HitterStatcastRow{MLBAMID: 1, Barrel: 9.0, HardHit: 40.0},
		HitterRow{MLBAMID: 1, PA: 10, WOBA: 0.420, XwOBA: 0.380}, // PA below 20
		true,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None (14d PA guard), got %v", sig)
	}
}

func TestTagHitter_None(t *testing.T) {
	b := hitterBundle(
		HitterRow{MLBAMID: 1, PA: 200, WOBA: 0.350, XwOBA: 0.360},
		HitterStatcastRow{MLBAMID: 1, Barrel: 7.0, HardHit: 38.0},
		HitterRow{}, false,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None, got %v", sig)
	}
}

func TestTagHitter_MissingMLBAMID(t *testing.T) {
	sig, _ := TagHitter(&Bundle{}, 0, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None for missing MLBAM ID, got %v", sig)
	}
}

func TestTagPitcher_BuyLow(t *testing.T) {
	b := &Bundle{
		PitcherExp: map[int]PitcherRow{
			1: {MLBAMID: 1, PA: 150, ERA: 4.50, XERA: 3.20, XwOBA: 0.290},
		},
	}
	sig, m := TagPitcher(b, 1, DefaultThresholds())
	if sig != SignalBuyLow {
		t.Fatalf("want BuyLow, got %v", sig)
	}
	if m.BuyLowDelta < 1.0 {
		t.Errorf("BuyLowDelta want >= 1.0, got %v", m.BuyLowDelta)
	}
}

func TestTagPitcher_Hot(t *testing.T) {
	b := &Bundle{
		PitcherExp: map[int]PitcherRow{
			1: {MLBAMID: 1, PA: 150, ERA: 3.30, XERA: 3.40, XwOBA: 0.310},
		},
		PitcherExp30d: map[int]PitcherRow{
			1: {MLBAMID: 1, PA: 60, ERA: 2.80, XERA: 3.10, XwOBA: 0.290},
		},
	}
	sig, _ := TagPitcher(b, 1, DefaultThresholds())
	if sig != SignalHot {
		t.Fatalf("want Hot, got %v", sig)
	}
}

func TestTagPitcher_SampleGuard(t *testing.T) {
	b := &Bundle{
		PitcherExp: map[int]PitcherRow{
			1: {MLBAMID: 1, PA: 50, ERA: 5.00, XERA: 3.00, XwOBA: 0.290},
		},
	}
	sig, _ := TagPitcher(b, 1, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None (sample guard), got %v", sig)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		buyLow, hot bool
		want        Signal
	}{
		{false, false, SignalNone},
		{true, false, SignalBuyLow},
		{false, true, SignalHot},
		{true, true, SignalBoth},
	}
	for _, tc := range cases {
		if got := classify(tc.buyLow, tc.hot); got != tc.want {
			t.Errorf("classify(%v,%v): want %v, got %v", tc.buyLow, tc.hot, tc.want, got)
		}
	}
}

func TestThresholdOverride(t *testing.T) {
	// Loosen thresholds, expect more signals.
	th := DefaultThresholds()
	th.HitterMinSeasonPA = 30
	th.HitterBuyLowXwOBAGap = 0.020
	b := hitterBundle(
		HitterRow{MLBAMID: 1, PA: 50, WOBA: 0.330, XwOBA: 0.360}, // gap 0.030 with new threshold meets bar
		HitterStatcastRow{MLBAMID: 1, Barrel: 10.0, HardHit: 44.0},
		HitterRow{}, false,
	)
	sig, _ := TagHitter(b, 1, th)
	if sig != SignalBuyLow {
		t.Fatalf("want BuyLow with relaxed thresholds, got %v", sig)
	}
}
