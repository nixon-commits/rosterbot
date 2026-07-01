package claims

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/statcast"
	"github.com/pmurley/go-fantrax/models"
)

// ClaimsClient is the subset of *fantrax.Client the claims report needs.
type ClaimsClient interface {
	GetRecentTransactions(since time.Time) ([]models.Transaction, error)
}

// SidePlayer is one player on one side of a move (added or dropped) with HKB value.
type SidePlayer struct {
	Name     string
	Position string
	Value    int  // HKB value (0 if unranked)
	Ranked   bool // found in HKB
	Rank     int  // HKB overall rank
	Trend30D int  // HKB 30-day value change
	Level    string
	Prospect bool

	// Stats — at most one populated.
	IsPitcher bool
	HasStats  bool
	OPS       float64
	ERA       float64
	WHIP      float64

	// Enrichment (added players only).
	MLBAMID      int
	Signal       statcast.Signal
	ProjectedFPG float64 // 0 = unavailable
}

// Move is one waiver/FA transaction set: a team adds a player, usually dropping one.
type Move struct {
	TxID          string
	TeamName      string
	TeamID        string
	ClaimType     string // "FA" or "WW"
	ProcessedDate time.Time
	BidAmount     string // raw, may be empty
	Priority      string // raw, may be empty
	Added         []SidePlayer
	Dropped       []SidePlayer
}

// NetValue is added HKB value minus dropped HKB value.
func (m Move) NetValue() int {
	var net int
	for _, p := range m.Added {
		net += p.Value
	}
	for _, p := range m.Dropped {
		net -= p.Value
	}
	return net
}

// Options configures a claims run.
type Options struct {
	CacheDir         string // defaults to ".cache"
	DryRun           bool
	NoSignals        bool
	Since            time.Time // zero = use cursor
	DropsMin         int       // notable-drops HKB threshold
	PushoverUserKey  string
	PushoverAPIToken string
	LedgerDir        string       // defaults to ".waivers/claims"
	CursorPath       string       // defaults to ".cache/last-claims.json"
	HKBPlayers       []hkb.Player // optional injection for tests; when non-nil, skips the hkb.GetPlayers fetch
}
