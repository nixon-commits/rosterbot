package waivers

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/statcast"
	"github.com/pmurley/go-fantrax/models"
)

// Candidate is one ranked free agent surfaced by the waivers report. Identity
// and roster-fit fields are owned here; the Statcast Signal and the facts behind
// it come straight from internal/statcast.
type Candidate struct {
	Name         string
	MLBTeam      string
	Position     string // e.g. "OF", "SP", "1B,3B"
	MLBAMID      int
	IsPitcher    bool
	Signal       statcast.Signal
	ProjectedFPG float64

	// Drop suggestion — the rostered player this FA would replace and the
	// projected FPG gap. Populated by buildCandidates when a roster is supplied.
	DropName string
	DropFPG  float64
	Gap      float64 // ProjectedFPG - DropFPG

	// Metrics carries the season-level and hot-window diagnostics behind Signal.
	Metrics statcast.SignalMetrics
}

// Report is the full output of one Run.
type Report struct {
	Date  time.Time
	Top   []Candidate
	Total int // total candidates that passed filters before the top-N cut
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
