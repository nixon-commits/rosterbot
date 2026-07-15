package fantrax

// WeeklyPeriod is the weekly matchup axis: Fantrax's "Scoring Period N" from the
// getStandings SCHEDULE captions (~7 days per period, merged wider around breaks
// like the All-Star break). This is what GetGSLimits and standings-style lookups
// key on. See ScoringPeriod.Number, FindCurrentPeriod.
type WeeklyPeriod int

// DailyPeriod is the daily roster/apply axis: one number per calendar day
// (e.g. 104…110 across a week), which Fantrax never exposes as a list — only as
// "today" via GetCurrentPeriod() and as the full season dropdown parsed by the
// periodList date map. Roster/apply/GS-snapshot endpoints are keyed by this axis.
// It is a distinct type from WeeklyPeriod precisely so the two cannot be passed
// interchangeably (the rosterbot-uv6 / rosterbot-z3b bug class).
type DailyPeriod int
