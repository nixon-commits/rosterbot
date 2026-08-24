// Package layout is the single declaration of the STATE_BUCKET key layout:
// every prefix the bot writes, its local-filesystem equivalent, and what the
// operator should expect of it.
//
// It exists as a zero-dependency leaf because two packages need the table and
// they cannot import each other: internal/statestore builds typed stores from
// it (and imports internal/lineupapi, so lineupapi can never import statestore
// back), while internal/lineupapi serves it on GET /v1/infra. Duplicating the
// table in both is what rosterbot-9s6 spent an issue collapsing, so it lives
// here instead.
//
// The metadata beyond prefix/dir — MaxAge, Producer, Partitioned, NoBackfill —
// is here rather than in the status page because it describes the artifact
// itself, not one presentation of it.
package layout

import "time"

// Artifact describes one kind of durable or ephemeral state.
type Artifact struct {
	// Name is the human-readable label shown on the status page.
	Name string

	// S3Prefix is the key prefix under STATE_BUCKET. Always ends in "/" so a
	// listing can't match a sibling prefix by accident (e.g. "runs/" must not
	// match "runledger/").
	S3Prefix string

	// LocalDir is the filesystem equivalent used when STATE_BUCKET is unset.
	// It is the factual on-disk location, which the Infra status page lists
	// directly. Note statestore's CacheStore ignores it — its local branch
	// returns nil to mean "use cache.FileCache's own default fsStore" — so
	// naming .cache here is accurate without changing that wiring.
	LocalDir string

	// Durable marks keep-forever state. False means TTL-evicted and
	// regenerable, where age carries no health signal.
	Durable bool

	// MaxAge is how stale the newest object may be before the artifact is
	// unhealthy. Required for durable artifacts; meaningless for ephemeral ones.
	MaxAge time.Duration

	// Producer is the EventBridge schedule that writes this artifact, so a
	// stale row points at a suspect job. Empty when there is no single producer.
	Producer string

	// Partitioned marks Hive-style dt=YYYY-MM-DD partitioning, which lets the
	// status page enumerate days and detect missing ones.
	Partitioned bool

	// PerTenant marks state that belongs to ONE user rather than to the league
	// or the deployment. Those artifacts gain a user=<uid>/ segment directly
	// after S3Prefix, so one tenant's lineups, runs and reports cannot be read
	// or overwritten by another's job.
	//
	// The split is by WHO THE DATA IS ABOUT, not by who happens to produce it.
	// The Team Value Store is written by one job and describes every team in the
	// league, so it stays shared; the Lineup Gap Store is written by the same
	// job and describes one manager's bench decisions, so it does not. Getting
	// this backwards in the sharing direction is a cross-tenant leak; getting it
	// backwards the other way merely duplicates data.
	//
	// The TTL Cache is deliberately shared. Its keys already carry the team id
	// where the value is team-specific (fantrax-hitter-roster-<teamID>), and the
	// expensive entries — FanGraphs projections, Savant CSVs, the MLB schedule —
	// are identical for every tenant. Partitioning it would multiply the load on
	// those upstreams by the tenant count for no benefit.
	PerTenant bool

	// RecoveryInput names the S3Prefix of the artifact a missing day has to be
	// re-derived FROM, for artifacts whose gaps are only conditionally
	// re-runnable. Empty means unconditional: either the job can simply be run
	// again for that date, or NoBackfill already says it never can.
	//
	// It exists because "Re-runnable" was an assumption the status page stated
	// as a fact. Grades are re-derivable from a projection snapshot — but only
	// where one was captured. On 2026-08-01 and 08-02 the Shadow job failed on
	// Fantrax STALE_CLIENT and wrote none, and a snapshot records what the model
	// projected ON that day: FanGraphs rest-of-season cannot be fetched
	// retroactively, so those two days are permanently ungradeable. The page
	// listed them beside three genuinely recoverable ones under one
	// "Re-runnable" footer, which turns permanent loss into a chore that
	// silently never completes.
	//
	// Deliberately a per-day check rather than a per-artifact flag: the whole
	// distinction is that the SAME artifact has both kinds of gap at once.
	RecoveryInput string

	// NoBackfill marks an artifact whose missing days can never be recovered,
	// so a gap is permanent data loss rather than a re-runnable job. Three
	// artifacts carry it: the Team Value Store (docs/adr/0002 — HKB has no
	// history and rosters are not archived), the Trade Offer Log (Fantrax
	// keeps no record of a trade that did not happen, so an offer not
	// captured while pending is gone), and the Daily Archive (its sources
	// serve only what is current — see the Archive entry below).
	NoBackfill bool
}

// Day is one day's tolerance, the natural unit for daily-cadence artifacts.
const Day = 24 * time.Hour

// The table. Keep S3Prefix values in sync with cmd/sync.go's statePairs and
// with the CDK's bucket policy.
var (
	Cache = Artifact{Name: "TTL Cache", S3Prefix: "cache/", LocalDir: ".cache", Durable: false}
	// Analysis grades a day by joining that day's actuals against the projection
	// snapshot the Shadow job captured for it, so a gap is re-runnable exactly
	// when backtest/ still holds that day's capture — see RecoveryInput.
	Analysis   = Artifact{Name: "Analysis Store", S3Prefix: "analysis/grades/", LocalDir: ".analysis", Durable: true, MaxAge: 2 * Day, Producer: "Grade", Partitioned: true, PerTenant: true, RecoveryInput: "backtest/"}
	TeamValues = Artifact{Name: "Team Value Store", S3Prefix: "analysis/team-values/", LocalDir: ".teamvalue", Durable: true, MaxAge: 2 * Day, Producer: "TeamValues", Partitioned: true, NoBackfill: true}
	// FootballValues is per-player grain (unlike TeamValues' per-team
	// aggregate), so unlike the Team Value Store it is NOT NoBackfill: both
	// the Sleeper roster snapshot and the StatsGuy bundle are re-fetchable,
	// so a missed day is re-runnable rather than permanently lost.
	FootballValues = Artifact{Name: "Dynasty Value Store", S3Prefix: "analysis/football-values/", LocalDir: ".footballvalue", Durable: true, MaxAge: 2 * Day, Producer: "FootballValues", Partitioned: true}
	LineupGaps     = Artifact{Name: "Lineup Gap Store", S3Prefix: "analysis/lineup-gaps/", LocalDir: ".lineupgap", Durable: true, MaxAge: 2 * Day, Producer: "Grade", Partitioned: true, PerTenant: true}
	// The Daily Archive is NoBackfill for the same reason the Team Value Store
	// is, one level upstream: it captures point-in-time reads of feeds that
	// publish no history. HKB serves current rankings, FanGraphs serves the
	// current rest-of-season projection and the current prospect board, and
	// three of the five Savant CSVs are season-to-date as of the moment of the
	// fetch. None of those has a historical replay endpoint, so a day nobody
	// archived is a day that no longer exists anywhere — which is precisely why
	// this artifact exists at all (rosterbot-0l31).
	//
	// The corollary is enforced in cmd/archive.go: `archive --date <past>` is
	// refused rather than treated as a backfill, because the sources would fetch
	// TODAY'S data and write it under a historical partition. That fills the gap
	// on this page while making the archive silently wrong, which is strictly
	// worse than the gap.
	Archive      = Artifact{Name: "Daily Archive", S3Prefix: "archive/", LocalDir: ".archive", Durable: true, MaxAge: 2 * Day, Producer: "Archive", Partitioned: true, NoBackfill: true}
	Backtest     = Artifact{Name: "Projection Snapshots", S3Prefix: "backtest/", LocalDir: ".backtest", Durable: true, MaxAge: 2 * Day, Producer: "Lineup", PerTenant: true}
	RunLedger    = Artifact{Name: "Run Ledger", S3Prefix: "runledger/", LocalDir: ".lineup/runs", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup", PerTenant: true}
	RunOutput    = Artifact{Name: "Run Output", S3Prefix: "runs/", LocalDir: ".lineup/outputs", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup", PerTenant: true}
	Notification = Artifact{Name: "Notifications", S3Prefix: "notifications/", LocalDir: ".lineup/notifications", Durable: true, MaxAge: 7 * Day, Producer: "", PerTenant: true}
	Lineup       = Artifact{Name: "Published Lineup", S3Prefix: "lineup/", LocalDir: ".lineup", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup", PerTenant: true}
	Claims       = Artifact{Name: "Claims Ledger", S3Prefix: "claims/", LocalDir: ".waivers", Durable: true, MaxAge: 3 * Day, Producer: "Claims"}

	// The three Trades-tab artifacts. They are separate prefixes rather than
	// one, because they have different producers and different cadences and a
	// single prefix would judge all of them by whichever object is newest —
	// hiding a dead daily producer behind a healthy hourly one.
	//
	// Trades' 14h tolerance is not its 1h cadence: the Lineup schedule runs
	// 14:00-03:00 UTC, so an 11h overnight quiet period is legitimate. This is
	// the same reasoning as infra.go's hourlyGap (13h), plus slack.
	Trades      = Artifact{Name: "Pending Offers", S3Prefix: "trades/", LocalDir: ".trades", Durable: true, MaxAge: 14 * time.Hour, Producer: "Lineup", PerTenant: true}
	TradeValues = Artifact{Name: "Trade Values Table", S3Prefix: "tradevalues/", LocalDir: ".tradevalues", Durable: true, MaxAge: 26 * time.Hour, Producer: "TeamValues"}
	// AvailablePool backs the app's Pickups screen: every unowned player joined
	// onto HKB value and 30-day momentum. Produced by TeamValues rather than a
	// schedule of its own, because that job already fetches both inputs — the
	// 10.7 MB player pool and the HKB rankings — and a separate schedule would
	// re-fetch the pool for no freshness gain. Same reasoning, and the same
	// 26h tolerance, as TradeValues beside it.
	//
	// NOT PerTenant, matching TradeValues: who is unowned is a property of the
	// league, not of the manager asking. If this deployment ever spans more
	// than one Fantrax league, both artifacts need a league segment together.
	AvailablePool = Artifact{Name: "Available Player Pool", S3Prefix: "pool/", LocalDir: ".pool", Durable: true, MaxAge: 26 * time.Hour, Producer: "TeamValues"}

	TradeOffers = Artifact{Name: "Trade Offer Log", S3Prefix: "analysis/trade-offers/", LocalDir: ".tradeoffers", Durable: true, MaxAge: 2 * Day, Producer: "Lineup", Partitioned: true, NoBackfill: true, PerTenant: true}

	// Reports holds the three private dashboard artifacts — model.json,
	// gap.json, views.json — that projection-site used to publish to the
	// dashboard bucket's report/ prefix, where CloudFront's default behavior
	// served them to anyone with the distribution domain (rosterbot-crq.3).
	// They live in the state bucket, which no distribution fronts, and reach
	// the SPA through the passkey-gated GET /v1/reports/{name}. value.json,
	// football.json and football-trades.json stay on the public prefix: those
	// are league-wide standings and completed trades every manager can already
	// read off Fantrax and Sleeper, not one manager's performance.
	//
	// One prefix for all three, unlike the Trades pair: same producer, same
	// daily schedule, so the newest object's age is an honest reading for the
	// set. The residual cost is that one of the three soft-failing in
	// projection-site while the others succeed does not move this row.
	Reports = Artifact{Name: "Private Dashboard Reports", S3Prefix: "reports/", LocalDir: ".reports", Durable: true, MaxAge: 26 * time.Hour, Producer: "ProjectionReports", PerTenant: true}

	Session = Artifact{Name: "Fantrax Session", S3Prefix: "session/", LocalDir: ".fantrax-cache", Durable: true, MaxAge: 7 * Day, Producer: "", PerTenant: true}

	// Progress shares the runs/ prefix with RunOutput; it is not a separate
	// listing target, so it is deliberately absent from All().
	Progress = Artifact{Name: "Run Progress", S3Prefix: "runs/", LocalDir: ".lineup/progress", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup", PerTenant: true}

	// TenantRoster is the active tenant list the fan-out dispatcher publishes
	// so the ops notifier can assert on a tenant that has NEVER run
	// (rosterbot-crq.4). Overdue otherwise derives its tenant set from the
	// ledger, which can only ever surface a tenant that ran and then stopped.
	//
	// Deliberately NOT PerTenant: it is the list OF tenants, so scoping it to
	// one would be self-defeating.
	//
	// It is declared here rather than as a string literal in each module
	// because the writer (lambda/dispatch) and the reader (opsnotify) are
	// separate Go modules with no compiler link between them — the precise
	// arrangement that let the run ledger's readers and writers drift apart and
	// blind alerting for three days (rosterbot-3vr). One declaration, two
	// importers, no restatement.
	TenantRoster = Artifact{Name: "Tenant Roster", S3Prefix: "tenants/", LocalDir: ".lineup/tenants", Durable: true, MaxAge: 26 * time.Hour, Producer: "Lineup"}

	// FootballTrades holds one dedup marker object per Sleeper trade
	// transaction_id (check -> send -> mark, rosterbot-chs). Deliberately no
	// MaxAge and absent from All(), like Progress: markers are written only
	// when a trade happens, so a quiet league writes nothing for weeks and
	// any age check would misread the normal case as stale.
	//
	// The marker's whole body is the rendered verdict string, which is why it
	// could never back a GUI: no teams, no assets, no per-side values, no date.
	// FootballTradeLog below is the artifact that can, and it nests UNDER this
	// prefix rather than beside it — safe because a BlobStore only ever Gets and
	// Publishes an exact key and never lists, so the log's dt= objects are
	// invisible to the marker store.
	FootballTrades = Artifact{Name: "Football Trade Markers", S3Prefix: "football/trades/", LocalDir: ".football/trades", Durable: true, Producer: "FootballTrades"}

	// FootballTradeLog is the durable record of every graded football trade:
	// per-side teams and assets, each priced in all four StatsGuy formats AS OF
	// THE MOMENT IT WAS GRADED, plus the verdict.
	//
	// Deliberately NOT NoBackfill, and deliberately not fully recoverable
	// either — the honest statement is that the two halves differ. Sleeper
	// retains completed trades indefinitely, so a missing day's trade IDENTITY
	// can always be re-derived; StatsGuy publishes no history, so its GRADE
	// cannot. `football-trades --relog` rebuilds a missing row at today's
	// prices and marks it regraded rather than pretending otherwise.
	//
	// No MaxAge and absent from All(), for the same reason as FootballTrades: a
	// partition is written only on a poll that graded something, so a league
	// with no trades this week writes nothing and an age check would read the
	// normal case as stale. Partitioned is set anyway — it describes the key
	// layout, and only All() members are gap-scanned.
	FootballTradeLog = Artifact{Name: "Football Trade Log", S3Prefix: "football/trades/log/", LocalDir: ".football/trades/log", Durable: true, Producer: "FootballTrades", Partitioned: true}

	// ILStarts holds one dedup marker per (player, start date) for the
	// "IL-slotted player has an announced start" alert. Same shape and same
	// reasoning as FootballTrades: no MaxAge and absent from All(), because a
	// marker exists only when the condition fires. A healthy roster writes
	// nothing all season, so an age check would read the normal case as stale.
	ILStarts = Artifact{Name: "IL Start Markers", S3Prefix: "alerts/il-starts/", LocalDir: ".alerts/il-starts", Durable: true, Producer: "Lineup", PerTenant: true}

	// StaleCacheAlerts holds one dedup marker per cache key for the "serving a
	// stale copy" alert, so a standing upstream outage reports itself once
	// rather than once per key per run. Same no-MaxAge, absent-from-All()
	// shape as ILStarts: a marker exists only when the condition fires, so an
	// age check would read the healthy case as stale.
	//
	// NOT PerTenant, for exactly the reason layout.Cache is not: the cache it
	// guards is a league-wide singleton fetched once per day, so a per-tenant
	// marker would let N tenants each re-announce the one shared outage — the
	// per-(command, tenant) split that opsalert NEEDS is precisely wrong here.
	//
	// Producer is empty because there genuinely is not one: any job that loads
	// projections can be the run that first serves a stale copy.
	StaleCacheAlerts = Artifact{Name: "Stale Cache Markers", S3Prefix: "alerts/stale-cache/", LocalDir: ".alerts/stale-cache", Durable: true}
)

// All returns every artifact worth listing, in the order the status page shows
// them: the two unbackfillable/queryable stores first, then the rest.
//
// Progress is excluded — it shares runs/ with RunOutput, so listing it would
// double-count the same objects.
func All() []Artifact {
	return []Artifact{
		TeamValues, TradeOffers, Analysis, LineupGaps, FootballValues, Archive, Backtest,
		Lineup, Trades, TradeValues, AvailablePool, Reports, RunLedger, RunOutput, Notification, Claims,
		Session, Cache, TenantRoster,
	}
}

// PrefixFor returns this artifact's S3 key prefix for one tenant.
//
// The composition lives here, in the zero-dependency leaf that already owns the
// key layout, because there are two independent consumers: internal/statestore
// (which builds the producers' stores) and the API Lambda (which builds the
// readers). A copy in each is how they drift — and the failure of drift here is
// silent and total: producers writing to user=<uid>/ while readers look at the
// un-segmented prefix means a dashboard that shows nothing while every job
// reports success.
//
// An empty tenant returns the original prefix unchanged. That is a real mode,
// not a missing value: it is where every object sits before the cutover.
func (a Artifact) PrefixFor(tenant string) string {
	if !a.PerTenant || tenant == "" {
		return a.S3Prefix
	}
	return a.S3Prefix + "user=" + tenant + "/"
}

// LocalDirFor is the filesystem equivalent, kept in step so `serve` and a
// deployed task disagree about nothing but the backend.
func (a Artifact) LocalDirFor(tenant string) string {
	if !a.PerTenant || tenant == "" || a.LocalDir == "" {
		return a.LocalDir
	}
	return a.LocalDir + "/user=" + tenant
}
