package analysis

// Exclusion is one (date, system) grade partition withheld from model input
// at READ TIME. The partition's rows stay physically in the Analysis Store --
// this table is a disclosure list a report-building consumer consults, not a
// rewrite of the store. See ExcludedGrade and docs/analytics-stores.md.
type Exclusion struct {
	// Dt is the graded date, YYYY-MM-DD, matching GradeRow.Dt.
	Dt string
	// System is the projection system, matching the object key's system=
	// segment and GradeRow.System.
	System string
	// Reason is prose for whoever reads the disclosure -- printed on the CLI
	// and carried into the dashboard JSON.
	Reason string
	// Bead cites the issue(s) that diagnosed and decided the exclusion.
	Bead string
}

// excludedGrades is the standing exclusion table.
//
// It should not need to grow. Since rosterbot-shku the snapshot records each
// role's projection fetch time (backtest.Snapshot.HitterProjFetchedAt /
// PitcherProjFetchedAt) and RunProjectionAnalysis refuses to grade a capture
// whose input was already older than backtest.StaleInputAfter when it was
// written, so a future Cloudflare window produces no rows to exclude rather
// than rows to disown. This table remains the only remedy for the six
// historical partitions below, which were written before that guard existed.
//
// Add an entry here -- never mutate or delete a row in the Analysis Store --
// whenever a (date, system) partition is known to compare stale model input
// against fresh actuals. Rewriting or marking the store itself needs its own
// per-(day,system) primitive that this table deliberately does not add (see
// rosterbot-c61b); this is the cheaper, reversible fix: exclude and disclose
// at read time, leave the bytes alone.
//
// These six entries (rosterbot-c61b): the Shadow job ran at 23:40 UTC, inside
// the FanGraphs Cloudflare interactive-challenge window documented in
// rosterbot-sagc (holds roughly 17:00-03:00 UTC). atc-ros and thebatx-ros are
// fetched ONLY by Shadow -- nothing else in the pipeline touches those two
// systems -- so every one of these three nights served
// projections.fetchJSON's stale-cache fallback instead of a fresh capture.
// Verified in S3 (measured 2026-08-21): cache/fangraphs-{bat,pit}-ratcdc.json
// and cache/fangraphs-{bat,pit}-rthebatx.json both last-modified
// 2026-08-18T23:41Z -- three days stale on all three dates below.
// depthcharts-ros is also fetched hourly by the Lineup job (a narrow clean
// overlap most days, ~14:00-16:00 UTC), so its snapshot on these same three
// dates is NOT excluded; steamer-ros is Shadow-only like the two excluded
// systems but was not observed stale on these nights and stays in.
var excludedGrades = []Exclusion{
	{
		Dt: "2026-08-19", System: "atc-ros",
		Reason: "Shadow capture served FanGraphs' stale-cache fallback (last fetched 2026-08-18T23:41Z) during the Cloudflare challenge window",
		Bead:   "rosterbot-c61b, rosterbot-sagc",
	},
	{
		Dt: "2026-08-20", System: "atc-ros",
		Reason: "Shadow capture served FanGraphs' stale-cache fallback (last fetched 2026-08-18T23:41Z) during the Cloudflare challenge window",
		Bead:   "rosterbot-c61b, rosterbot-sagc",
	},
	{
		Dt: "2026-08-21", System: "atc-ros",
		Reason: "Shadow capture served FanGraphs' stale-cache fallback (last fetched 2026-08-18T23:41Z) during the Cloudflare challenge window",
		Bead:   "rosterbot-c61b, rosterbot-sagc",
	},
	{
		Dt: "2026-08-19", System: "thebatx-ros",
		Reason: "Shadow capture served FanGraphs' stale-cache fallback (last fetched 2026-08-18T23:41Z) during the Cloudflare challenge window",
		Bead:   "rosterbot-c61b, rosterbot-sagc",
	},
	{
		Dt: "2026-08-20", System: "thebatx-ros",
		Reason: "Shadow capture served FanGraphs' stale-cache fallback (last fetched 2026-08-18T23:41Z) during the Cloudflare challenge window",
		Bead:   "rosterbot-c61b, rosterbot-sagc",
	},
	{
		Dt: "2026-08-21", System: "thebatx-ros",
		Reason: "Shadow capture served FanGraphs' stale-cache fallback (last fetched 2026-08-18T23:41Z) during the Cloudflare challenge window",
		Bead:   "rosterbot-c61b, rosterbot-sagc",
	},
}

// ExcludedGrade reports whether the (dt, system) grade partition is a known
// stale-input capture that must be withheld from model input at read time.
//
// dt is the YYYY-MM-DD graded date (GradeRow.Dt); system is the projection
// system (GradeRow.System). The rows themselves are untouched in the store --
// this only says whether a consumer building model input should use them.
func ExcludedGrade(dt, system string) bool {
	for _, e := range excludedGrades {
		if e.Dt == dt && e.System == system {
			return true
		}
	}
	return false
}

// Exclusions returns a copy of the standing exclusion table, for a consumer
// that needs to disclose which (date, system) pairs it withheld and why
// (internal/report.Aggregate does this on the Model it returns).
//
// A copy, not the live slice: nothing here should let a caller mutate the
// standing table through an exported getter.
func Exclusions() []Exclusion {
	return append([]Exclusion(nil), excludedGrades...)
}
