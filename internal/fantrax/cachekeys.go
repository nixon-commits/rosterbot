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
	// keySeasonRange is versioned: the pre-v2 entry held the REGULAR-season end
	// (rosterbot-0lyz), and at stableTTL a 7-day-old wrong "season end" would
	// have kept every season-boundary guard firing through the playoffs.
	keySeasonRange   = "fantrax-season-range-v2"
	keySchedule      = "fantrax-schedule"
	keyGSLimits      = "fantrax-gs-limits"
	keyPeriodDateMap = "fantrax-period-date-map"
	keyPlayoffs      = "fantrax-playoffs"
	keyStandings     = "fantrax-standings"
	keyMLBGameLog    = "mlb-game-log"
)
