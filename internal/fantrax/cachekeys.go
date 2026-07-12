package fantrax

// Cache-key prefixes for every cached helper in this package. Centralizing them
// makes the `<source>-<entity>[-scope...]` grammar enforceable in one place: a
// rename lands here rather than across ~24 call sites. Scope parts (teamID,
// period, leagueID, date) are appended via cache.Key at each call site.
const (
	keyAllTrades          = "fantrax-all-trades"
	keyAllTransactions    = "fantrax-all-transactions"
	keyPendingTrades      = "fantrax-pending-trades"
	keyAvailableProspects = "fantrax-available-prospects"
	keyCurrentPeriod      = "fantrax-current-period"
	keyHitterRoster       = "fantrax-hitter-roster"
	keyPitcherRoster      = "fantrax-pitcher-roster"
	keyHitterScoring      = "fantrax-hitter-scoring"
	keyPitcherScoring     = "fantrax-pitcher-scoring"
	keyHitterSlots        = "fantrax-hitter-slots"
	keyPitcherSlots       = "fantrax-pitcher-slots"
	keyMinorsRoster       = "fantrax-minors-roster"
	keyPitcherGS          = "fantrax-pitcher-gs"
	keyPlayerPool         = "fantrax-player-pool"
	keyRecentStatsPitcher = "fantrax-recent-stats-pitcher"
	keyRosterStats        = "fantrax-roster-stats"
	keySeasonRange        = "fantrax-season-range"
	keyGSLimits           = "fantrax-gs-limits"
	keyMLBGameLog         = "mlb-game-log"
)
