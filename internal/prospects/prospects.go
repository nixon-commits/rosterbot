package prospects

import "time"

// AlertKind classifies the prospect alert.
type AlertKind string

const (
	CalledUp        AlertKind = "called-up"
	Optioned        AlertKind = "optioned"
	PerformanceHot  AlertKind = "performance-hot"
	PerformanceCold AlertKind = "performance-cold"
	FreeAgentBuzz   AlertKind = "free-agent-buzz"
	UpgradeAvail    AlertKind = "upgrade-available"
)

// ProspectAlert represents a single alert about a prospect.
type ProspectAlert struct {
	Kind       AlertKind
	Priority   string // "high", "medium", "low"
	PlayerName string
	MLBTeam    string
	Position   string // "SS", "SP", etc.
	Detail     string // human-readable description
	Stats      string // optional stat line
	OnMyTeam   bool
	Rank       int // MLB Pipeline rank, 0 = unranked
	IsPitcher  bool
}

// RankedProspect is a prospect with ranking info.
type RankedProspect struct {
	Name        string
	MLBTeam     string
	MLBID       int    // MLB Stats API player ID
	Position    string // "SS", "SP", etc.
	Rank        int    // 1-100, 0 = unranked
	FV          int    // future value grade (55, 60, etc.), 0 if unavailable
	ETA         string // "2026", "2027"
	Level       string // "AAA", "AA", "A+", "A"
	IsPitcher   bool
	PctRostered float64 // Fantrax %Rostered (0-100), 0 when unavailable
}

// UpgradeCandidate represents a recommended prospect swap.
type UpgradeCandidate struct {
	Drop     RankedProspect
	Add      RankedProspect
	RankGap  int     // positive = Add is higher ranked (rank-based sources)
	PctGap   float64 // positive = Add is more rostered (Fantrax %Rostered)
	NearTerm bool    // true if Add's ETA is current or next season
}

// UpgradeSet groups upgrade candidates from a single ranking source.
type UpgradeSet struct {
	Source     string // "FanGraphs" or "Fantrax"
	Candidates []UpgradeCandidate
}

// Report is the full prospect report for a given day.
type Report struct {
	Date     time.Time
	Alerts   []ProspectAlert
	Rankings []RankedProspect // your rostered prospects, sorted by rank
	Upgrades []UpgradeSet
}

// RankingSource provides prospect ranking data.
// Implementations: MLBPipelineSource, FanGraphsRankingSource.
type RankingSource interface {
	GetTopProspects(season int) ([]RankedProspect, error)
}
