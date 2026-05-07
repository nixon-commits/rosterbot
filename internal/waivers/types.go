package waivers

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/models"
)

// Signal classifies a waiver candidate's reason for surfacing.
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

// Candidate is one ranked free agent surfaced by the waivers report.
type Candidate struct {
	Name         string
	MLBTeam      string
	Position     string // e.g. "OF", "SP", "1B,3B"
	MLBAMID      int
	IsPitcher    bool
	Signal       Signal
	ProjectedFPG float64

	// Drop suggestion — the rostered player this FA would replace and the
	// projected FPG gap. Populated by buildCandidates when a roster is supplied.
	DropName string
	DropFPG  float64
	Gap      float64 // ProjectedFPG - DropFPG

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

	// Hot-window metrics; one is populated based on IsPitcher.
	HotHitter  HotHitterMetrics
	HotPitcher HotPitcherMetrics
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

// Report is the full output of one Run.
type Report struct {
	Date  time.Time
	Top   []Candidate
	Total int // total candidates that passed filters before the top-N cut
}

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

// SavantHitterRow holds parsed data from the hitter expected-statistics CSV.
type SavantHitterRow struct {
	MLBAMID int
	PA      int
	WOBA    float64
	XwOBA   float64
}

// SavantHitterStatcastRow holds quality-of-contact metrics from the Statcast CSV.
type SavantHitterStatcastRow struct {
	MLBAMID   int
	Barrel    float64 // percent (e.g. 12.4)
	HardHit   float64 // percent
	SweetSpot float64 // percent
}

// SavantPitcherRow holds parsed data from the pitcher expected-statistics CSV.
type SavantPitcherRow struct {
	MLBAMID int
	PA      int // TBF — total batters faced (the pitcher endpoint uses "pa")
	ERA     float64
	XERA    float64
	WOBA    float64
	XwOBA   float64
}

// SavantBundle aggregates every Savant data slice keyed by MLBAM ID.
// Any slice may be nil if its fetch failed; signal tagging handles missing data.
type SavantBundle struct {
	HitterExp     map[int]SavantHitterRow
	HitterSC      map[int]SavantHitterStatcastRow
	HitterExp14d  map[int]SavantHitterRow
	PitcherExp    map[int]SavantPitcherRow
	PitcherExp30d map[int]SavantPitcherRow
}

// FantraxClient is the narrow subset of *fantrax.Client used by Run, isolated
// for testability (mirrors transactions.TradeClient).
type FantraxClient interface {
	GetFullPlayerPool() ([]models.PoolPlayer, error)
	GetScoringWeights() (fantrax.ScoringWeights, error)
	GetPitcherScoringWeights() (fantrax.ScoringWeights, error)
	GetHitterRoster() ([]fantrax.Player, error)
	GetPitcherRoster() ([]fantrax.Player, error)
}

// Options govern a single Run invocation.
type Options struct {
	TopN             int
	Positions        []string // optional position filter (e.g. "OF", "SP"); empty = all
	NoCache          bool
	DryRun           bool
	PushoverUserKey  string
	PushoverAPIToken string
	CacheDir         string // defaults to ".cache" if empty
}
