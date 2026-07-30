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

	// NoBackfill marks an artifact whose missing days can never be recovered,
	// so a gap is permanent data loss rather than a re-runnable job. Only the
	// Team Value Store carries this today (docs/adr/0002).
	NoBackfill bool
}

// Day is one day's tolerance, the natural unit for daily-cadence artifacts.
const Day = 24 * time.Hour

// The table. Keep S3Prefix values in sync with cmd/sync.go's statePairs and
// with the CDK's bucket policy.
var (
	Cache        = Artifact{Name: "TTL Cache", S3Prefix: "cache/", LocalDir: ".cache", Durable: false}
	Analysis     = Artifact{Name: "Analysis Store", S3Prefix: "analysis/grades/", LocalDir: ".analysis", Durable: true, MaxAge: 2 * Day, Producer: "Grade", Partitioned: true}
	TeamValues   = Artifact{Name: "Team Value Store", S3Prefix: "analysis/team-values/", LocalDir: ".teamvalue", Durable: true, MaxAge: 2 * Day, Producer: "TeamValues", Partitioned: true, NoBackfill: true}
	LineupGaps   = Artifact{Name: "Lineup Gap Store", S3Prefix: "analysis/lineup-gaps/", LocalDir: ".lineupgap", Durable: true, MaxAge: 2 * Day, Producer: "Grade", Partitioned: true}
	Archive      = Artifact{Name: "Daily Archive", S3Prefix: "archive/", LocalDir: ".archive", Durable: true, MaxAge: 2 * Day, Producer: "Archive", Partitioned: true}
	Backtest     = Artifact{Name: "Projection Snapshots", S3Prefix: "backtest/", LocalDir: ".backtest", Durable: true, MaxAge: 2 * Day, Producer: "Lineup"}
	RunLedger    = Artifact{Name: "Run Ledger", S3Prefix: "runledger/", LocalDir: ".lineup/runs", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup"}
	RunOutput    = Artifact{Name: "Run Output", S3Prefix: "runs/", LocalDir: ".lineup/outputs", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup"}
	Notification = Artifact{Name: "Notifications", S3Prefix: "notifications/", LocalDir: ".lineup/notifications", Durable: true, MaxAge: 7 * Day, Producer: ""}
	Lineup       = Artifact{Name: "Published Lineup", S3Prefix: "lineup/", LocalDir: ".lineup", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup"}
	Claims       = Artifact{Name: "Claims Ledger", S3Prefix: "claims/", LocalDir: ".waivers", Durable: true, MaxAge: 3 * Day, Producer: "Claims"}
	Session      = Artifact{Name: "Fantrax Session", S3Prefix: "session/", LocalDir: ".fantrax-cache", Durable: true, MaxAge: 7 * Day, Producer: ""}

	// Progress shares the runs/ prefix with RunOutput; it is not a separate
	// listing target, so it is deliberately absent from All().
	Progress = Artifact{Name: "Run Progress", S3Prefix: "runs/", LocalDir: ".lineup/progress", Durable: true, MaxAge: 6 * time.Hour, Producer: "Lineup"}
)

// All returns every artifact worth listing, in the order the status page shows
// them: the two unbackfillable/queryable stores first, then the rest.
//
// Progress is excluded — it shares runs/ with RunOutput, so listing it would
// double-count the same objects.
func All() []Artifact {
	return []Artifact{
		TeamValues, Analysis, LineupGaps, Archive, Backtest,
		Lineup, RunLedger, RunOutput, Notification, Claims, Session,
		Cache,
	}
}
