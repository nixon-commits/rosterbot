package statcast

// Thresholds collects every tunable signal-classification knob.
// Tests pass overrides; production uses DefaultThresholds().
type Thresholds struct {
	HitterMinSeasonPA  int
	HitterMin14dPA     int
	PitcherMinSeasonPA int // TBF, since the pitcher endpoint uses pa
	PitcherMin30dPA    int

	HitterBuyLowXwOBAGap float64
	HitterBuyLowBarrel   float64
	HitterBuyLowHardHit  float64

	HitterHot14dWOBA  float64
	HitterHot14dXwOBA float64
	HitterHotBarrel   float64

	PitcherBuyLowERAGap float64
	PitcherBuyLowXwOBA  float64

	PitcherHot30dERA  float64
	PitcherHot30dXERA float64
}

// DefaultThresholds returns production defaults documented in the plan.
func DefaultThresholds() Thresholds {
	return Thresholds{
		HitterMinSeasonPA:  80,
		HitterMin14dPA:     20,
		PitcherMinSeasonPA: 100, // ~25 IP * 4 TBF/IP
		PitcherMin30dPA:    50,  // ~12 IP * 4 TBF/IP

		HitterBuyLowXwOBAGap: 0.030,
		HitterBuyLowBarrel:   9.0,
		HitterBuyLowHardHit:  42.0,

		HitterHot14dWOBA:  0.380,
		HitterHot14dXwOBA: 0.360,
		HitterHotBarrel:   8.0,

		PitcherBuyLowERAGap: 1.00,
		PitcherBuyLowXwOBA:  0.310,

		PitcherHot30dERA:  3.20,
		PitcherHot30dXERA: 3.50,
	}
}
