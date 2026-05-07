package waivers

// TagHitter classifies a free-agent hitter using season + 14-day Statcast data.
// Returns SignalNone when the player fails the sample guard or no rule fires.
// On a positive signal, the returned Candidate is partially populated with
// signal metrics; the caller fills Name/MLBTeam/Position/ProjectedFPG.
func TagHitter(b *SavantBundle, mlbamID int, th Thresholds) (Signal, Candidate) {
	var c Candidate
	if mlbamID == 0 || b == nil {
		return SignalNone, c
	}

	season, ok := b.HitterExp[mlbamID]
	if !ok || season.PA < th.HitterMinSeasonPA {
		return SignalNone, c
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
		return SignalNone, c
	}

	c = Candidate{
		MLBAMID:     mlbamID,
		IsPitcher:   false,
		Signal:      sig,
		BuyLowDelta: buyLowGap,
		WOBA:        season.WOBA,
		XwOBA:       season.XwOBA,
		Barrel:      sc.Barrel,
		HardHit:     sc.HardHit,
	}
	if has14 {
		c.HotHitter = HotHitterMetrics{
			Window14dWOBA:  win.WOBA,
			Window14dXwOBA: win.XwOBA,
			Window14dPA:    win.PA,
		}
	}
	return sig, c
}

// TagPitcher classifies a free-agent pitcher using season + 30-day xStats.
func TagPitcher(b *SavantBundle, mlbamID int, th Thresholds) (Signal, Candidate) {
	var c Candidate
	if mlbamID == 0 || b == nil {
		return SignalNone, c
	}

	season, ok := b.PitcherExp[mlbamID]
	if !ok || season.PA < th.PitcherMinSeasonPA {
		return SignalNone, c
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
		return SignalNone, c
	}

	c = Candidate{
		MLBAMID:     mlbamID,
		IsPitcher:   true,
		Signal:      sig,
		BuyLowDelta: eraGap,
		WOBA:        season.WOBA,
		XwOBA:       season.XwOBA,
		ERA:         season.ERA,
		XERA:        season.XERA,
	}
	if has30 {
		c.HotPitcher = HotPitcherMetrics{
			Window30dERA:  win.ERA,
			Window30dXERA: win.XERA,
			Window30dTBF:  win.PA,
		}
	}
	return sig, c
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
