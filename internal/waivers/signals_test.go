package waivers

import "testing"

// hitterBundle returns a bundle suitable for hitter tag tests.
func hitterBundle(season SavantHitterRow, sc SavantHitterStatcastRow, win SavantHitterRow, hasWin bool) *SavantBundle {
	b := &SavantBundle{
		HitterExp:    map[int]SavantHitterRow{},
		HitterSC:     map[int]SavantHitterStatcastRow{},
		HitterExp14d: map[int]SavantHitterRow{},
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
		SavantHitterRow{MLBAMID: 1, PA: 200, WOBA: 0.310, XwOBA: 0.360}, // gap = 0.050
		SavantHitterStatcastRow{MLBAMID: 1, Barrel: 12.0, HardHit: 46.0},
		SavantHitterRow{}, false,
	)
	sig, c := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalBuyLow {
		t.Fatalf("want BuyLow, got %v", sig)
	}
	if c.BuyLowDelta < 0.04 {
		t.Errorf("BuyLowDelta unexpected: %v", c.BuyLowDelta)
	}
}

func TestTagHitter_Hot(t *testing.T) {
	b := hitterBundle(
		SavantHitterRow{MLBAMID: 1, PA: 200, WOBA: 0.350, XwOBA: 0.360}, // gap too small
		SavantHitterStatcastRow{MLBAMID: 1, Barrel: 9.0, HardHit: 40.0}, // HH < 42 → no buy-low
		SavantHitterRow{MLBAMID: 1, PA: 25, WOBA: 0.420, XwOBA: 0.380},
		true,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalHot {
		t.Fatalf("want Hot, got %v", sig)
	}
}

func TestTagHitter_Both(t *testing.T) {
	b := hitterBundle(
		SavantHitterRow{MLBAMID: 1, PA: 200, WOBA: 0.310, XwOBA: 0.360},
		SavantHitterStatcastRow{MLBAMID: 1, Barrel: 12.0, HardHit: 46.0},
		SavantHitterRow{MLBAMID: 1, PA: 25, WOBA: 0.420, XwOBA: 0.380},
		true,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalBoth {
		t.Fatalf("want Both, got %v", sig)
	}
}

func TestTagHitter_SampleGuard(t *testing.T) {
	b := hitterBundle(
		SavantHitterRow{MLBAMID: 1, PA: 50, WOBA: 0.300, XwOBA: 0.380}, // PA below 80
		SavantHitterStatcastRow{MLBAMID: 1, Barrel: 15.0, HardHit: 48.0},
		SavantHitterRow{}, false,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None (sample guard), got %v", sig)
	}
}

func TestTagHitter_Window14dPAGuard(t *testing.T) {
	b := hitterBundle(
		SavantHitterRow{MLBAMID: 1, PA: 200, WOBA: 0.350, XwOBA: 0.360},
		SavantHitterStatcastRow{MLBAMID: 1, Barrel: 9.0, HardHit: 40.0},
		SavantHitterRow{MLBAMID: 1, PA: 10, WOBA: 0.420, XwOBA: 0.380}, // PA below 20
		true,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None (14d PA guard), got %v", sig)
	}
}

func TestTagHitter_None(t *testing.T) {
	b := hitterBundle(
		SavantHitterRow{MLBAMID: 1, PA: 200, WOBA: 0.350, XwOBA: 0.360},
		SavantHitterStatcastRow{MLBAMID: 1, Barrel: 7.0, HardHit: 38.0},
		SavantHitterRow{}, false,
	)
	sig, _ := TagHitter(b, 1, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None, got %v", sig)
	}
}

func TestTagHitter_MissingMLBAMID(t *testing.T) {
	sig, _ := TagHitter(&SavantBundle{}, 0, DefaultThresholds())
	if sig != SignalNone {
		t.Fatalf("want None for missing MLBAM ID, got %v", sig)
	}
}

func TestTagPitcher_BuyLow(t *testing.T) {
	b := &SavantBundle{
		PitcherExp: map[int]SavantPitcherRow{
			1: {MLBAMID: 1, PA: 150, ERA: 4.50, XERA: 3.20, XwOBA: 0.290},
		},
	}
	sig, c := TagPitcher(b, 1, DefaultThresholds())
	if sig != SignalBuyLow {
		t.Fatalf("want BuyLow, got %v", sig)
	}
	if c.BuyLowDelta < 1.0 {
		t.Errorf("BuyLowDelta want >= 1.0, got %v", c.BuyLowDelta)
	}
}

func TestTagPitcher_Hot(t *testing.T) {
	b := &SavantBundle{
		PitcherExp: map[int]SavantPitcherRow{
			1: {MLBAMID: 1, PA: 150, ERA: 3.30, XERA: 3.40, XwOBA: 0.310},
		},
		PitcherExp30d: map[int]SavantPitcherRow{
			1: {MLBAMID: 1, PA: 60, ERA: 2.80, XERA: 3.10, XwOBA: 0.290},
		},
	}
	sig, _ := TagPitcher(b, 1, DefaultThresholds())
	if sig != SignalHot {
		t.Fatalf("want Hot, got %v", sig)
	}
}

func TestTagPitcher_SampleGuard(t *testing.T) {
	b := &SavantBundle{
		PitcherExp: map[int]SavantPitcherRow{
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
		SavantHitterRow{MLBAMID: 1, PA: 50, WOBA: 0.330, XwOBA: 0.360}, // gap 0.030 with new threshold meets bar
		SavantHitterStatcastRow{MLBAMID: 1, Barrel: 10.0, HardHit: 44.0},
		SavantHitterRow{}, false,
	)
	sig, _ := TagHitter(b, 1, th)
	if sig != SignalBuyLow {
		t.Fatalf("want BuyLow with relaxed thresholds, got %v", sig)
	}
}
