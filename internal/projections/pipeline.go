package projections

// HitterPipelineDetail holds the full adjustment pipeline for a single hitter.
type HitterPipelineDetail struct {
	PlayerName string
	PlayerID   string
	MLBTeam    string

	// Stage 1: Base projection
	BasePtsPerGame float64

	// Stage 2: Blend
	BlendedPtsPerGame float64
	BlendDelta        float64
	HasRecent         bool
	BaseWt            float64
	RecentFPG         float64
	GamesPlayed       int

	// Stage 3: Matchup — platoon + pitcher quality applied together
	PlatoonMult      float64
	PlatoonFavorable *bool // nil=unknown, true=favorable, false=unfavorable
	QualityMult      float64
	OpposingPitcher  string
	OpposingFIP      float64
	LeagueAvgFIP     float64

	// Final
	FinalPtsPerGame float64
	PlatoonDelta    float64
	QualityDelta    float64
}

// PitcherPipelineDetail holds the full adjustment pipeline for a single pitcher.
type PitcherPipelineDetail struct {
	PlayerName string
	PlayerID   string
	MLBTeam    string

	// Role
	Role string // "SP" or "RP"

	// Stage 1: Base projection (from active projection system)
	BasePtsPerGame float64

	// Stage 2: Blend (base + recent YTD)
	BlendedPtsPerGame float64
	BlendDelta        float64
	HasRecent         bool
	BaseWt            float64
	RecentFPG         float64
	GamesPlayed       int

	// Stage 3: GS Gate (SP only, today only)
	WasGated  bool
	GateDelta float64

	// Final
	FinalPtsPerGame float64
}
