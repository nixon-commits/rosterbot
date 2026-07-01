package statcast

// Signal classifies why a player is worth surfacing — the Statcast Signal.
type Signal int

const (
	SignalNone Signal = iota
	SignalBuyLow
	SignalHot
	SignalBoth
)

// String returns the human-readable label used in reports.
func (s Signal) String() string {
	switch s {
	case SignalBuyLow:
		return "BUY-LOW"
	case SignalHot:
		return "HOT"
	case SignalBoth:
		return "BOTH"
	default:
		return ""
	}
}

// HotHitterMetrics captures the 14-day rolling window for hitters.
type HotHitterMetrics struct {
	Window14dWOBA  float64
	Window14dXwOBA float64
	Window14dPA    int
}

// HotPitcherMetrics captures the 30-day rolling window for pitchers.
type HotPitcherMetrics struct {
	Window30dERA  float64
	Window30dXERA float64
	Window30dTBF  int
}

// SignalMetrics carries the facts behind a Signal — the season-level and
// hot-window diagnostics a tag fired on. One of HotHitter/HotPitcher is
// populated depending on which Tag function produced it; the zero value is the
// no-signal case.
type SignalMetrics struct {
	// BuyLowDelta is the magnitude of the mispricing signal.
	// Hitter: xwOBA - wOBA (positive = good buy-low).
	// Pitcher: ERA - xERA (positive = good buy-low).
	BuyLowDelta float64

	// Season-level diagnostics.
	WOBA    float64 // hitter
	XwOBA   float64 // hitter (or pitcher)
	Barrel  float64 // hitter, percent
	HardHit float64 // hitter, percent
	ERA     float64 // pitcher
	XERA    float64 // pitcher

	// Hot-window metrics; one is populated based on the tag source.
	HotHitter  HotHitterMetrics
	HotPitcher HotPitcherMetrics
}

// TagHitter classifies a free-agent hitter using season + 14-day Statcast data.
// Returns SignalNone (and a zero SignalMetrics) when the player fails the sample
// guard or no rule fires. On a positive signal, SignalMetrics holds the facts
// behind it; the caller owns identity fields (name, team, projection).
func TagHitter(b *Bundle, mlbamID int, th Thresholds) (Signal, SignalMetrics) {
	if mlbamID == 0 || b == nil {
		return SignalNone, SignalMetrics{}
	}

	season, ok := b.HitterExp[mlbamID]
	if !ok || season.PA < th.HitterMinSeasonPA {
		return SignalNone, SignalMetrics{}
	}

	sc := b.HitterSC[mlbamID]
	win, has14 := b.HitterExp14d[mlbamID]

	buyLowGap := season.XwOBA - season.WOBA
	hasBuyLow := buyLowGap >= th.HitterBuyLowXwOBAGap &&
		sc.Barrel >= th.HitterBuyLowBarrel &&
		sc.HardHit >= th.HitterBuyLowHardHit

	hasHot := has14 && win.PA >= th.HitterMin14dPA &&
		win.WOBA >= th.HitterHot14dWOBA &&
		win.XwOBA >= th.HitterHot14dXwOBA &&
		sc.Barrel >= th.HitterHotBarrel

	sig := classify(hasBuyLow, hasHot)
	if sig == SignalNone {
		return SignalNone, SignalMetrics{}
	}

	m := SignalMetrics{
		BuyLowDelta: buyLowGap,
		WOBA:        season.WOBA,
		XwOBA:       season.XwOBA,
		Barrel:      sc.Barrel,
		HardHit:     sc.HardHit,
	}
	if has14 {
		m.HotHitter = HotHitterMetrics{
			Window14dWOBA:  win.WOBA,
			Window14dXwOBA: win.XwOBA,
			Window14dPA:    win.PA,
		}
	}
	return sig, m
}

// TagPitcher classifies a free-agent pitcher using season + 30-day xStats.
func TagPitcher(b *Bundle, mlbamID int, th Thresholds) (Signal, SignalMetrics) {
	if mlbamID == 0 || b == nil {
		return SignalNone, SignalMetrics{}
	}

	season, ok := b.PitcherExp[mlbamID]
	if !ok || season.PA < th.PitcherMinSeasonPA {
		return SignalNone, SignalMetrics{}
	}

	win, has30 := b.PitcherExp30d[mlbamID]

	eraGap := season.ERA - season.XERA
	hasBuyLow := eraGap >= th.PitcherBuyLowERAGap &&
		season.XwOBA > 0 && season.XwOBA <= th.PitcherBuyLowXwOBA

	hasHot := has30 && win.PA >= th.PitcherMin30dPA &&
		win.ERA > 0 && win.ERA <= th.PitcherHot30dERA &&
		win.XERA > 0 && win.XERA <= th.PitcherHot30dXERA

	sig := classify(hasBuyLow, hasHot)
	if sig == SignalNone {
		return SignalNone, SignalMetrics{}
	}

	m := SignalMetrics{
		BuyLowDelta: eraGap,
		WOBA:        season.WOBA,
		XwOBA:       season.XwOBA,
		ERA:         season.ERA,
		XERA:        season.XERA,
	}
	if has30 {
		m.HotPitcher = HotPitcherMetrics{
			Window30dERA:  win.ERA,
			Window30dXERA: win.XERA,
			Window30dTBF:  win.PA,
		}
	}
	return sig, m
}

func classify(buyLow, hot bool) Signal {
	switch {
	case buyLow && hot:
		return SignalBoth
	case buyLow:
		return SignalBuyLow
	case hot:
		return SignalHot
	default:
		return SignalNone
	}
}
