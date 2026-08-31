package lineupapi

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

// Health is a per-artifact verdict, ordered from best to worst.
type Health string

const (
	HealthOK      Health = "ok"      // fresh, or ephemeral (age carries no signal)
	HealthGap     Health = "gap"     // a day is missing and cannot be recovered
	HealthStale   Health = "stale"   // newest object is older than the artifact's MaxAge
	HealthMissing Health = "missing" // durable prefix is empty — nothing has ever landed
	HealthUnknown Health = "unknown" // the listing itself failed
)

// PrefixListing is the raw result of enumerating one prefix. The adapter does
// the S3 call; every judgement about what the numbers mean is made here, so the
// health rules are testable without AWS.
type PrefixListing struct {
	Objects      int       `json:"objects"`
	Bytes        int64     `json:"bytes"`
	LastModified time.Time `json:"last_modified"`

	// Truncated is true when the walk hit its object cap and stopped before
	// enumerating the whole prefix. Every field above and below it is then a
	// statement about the PART of the prefix that was read, not the prefix —
	// and not merely approximate: Objects/Bytes are floors, but LastModified is
	// the newest object SEEN, so a prefix with fresh objects past the cut can
	// read stale, and Partitions/the sub-dimension values can omit the actual
	// newest day entirely. A listing that never truncates (the local
	// FileInfraStore has no cap) leaves this false.
	Truncated bool `json:"truncated,omitempty"`

	// Partitions holds the days this prefix has data for (YYYY-MM-DD), read
	// either from a Hive dt= segment or from a bare YYYY-MM-DD.json basename.
	// Empty for prefixes that carry no date in their keys.
	//
	// Both encodings land in one field because both answer the same question —
	// which days exist here — and the second one has a consumer: the projection
	// snapshots under backtest/ are named by filename, not partitioned, and are
	// what decides whether an Analysis Store gap can be re-graded at all.
	// Only artifacts marked Partitioned are gap-scanned, so filling this for a
	// flat prefix adds a fact without changing any verdict.
	Partitions []string `json:"partitions,omitempty"`

	// SkippedDays holds the days under this prefix whose ONLY object is a
	// deliberate-skip marker (internal/analysis.SkipMarkerFilename) — a day the
	// producer ran and judged ungradeable, as opposed to one it never reached.
	// Always a subset of Partitions: the marker registers its dt= day, which is
	// what closed the false All-Star-break gaps in rosterbot-u9u, and is
	// precisely why the day then became indistinguishable from a graded one.
	//
	// "Only object" is the whole predicate, and the second half is not
	// belt-and-braces. cmd/grade.go writes the marker unconditionally on this
	// run's judgement, analysis.Writer has no delete counterpart anywhere in
	// the tree, and the default grade window re-grades the trailing 3 days — so
	// a day marked skipped on Monday can receive real graded rows on Tuesday
	// with the marker still sitting beside them. Keying on the marker's
	// presence alone would report such a day as deliberately skipped forever.
	SkippedDays []string `json:"skipped_days,omitempty"`

	// Subkeys names the second-level dimension where one exists — the four
	// projection systems under analysis/grades/, the archive's per-source
	// directories. A missing entry here is the "one shadow system quietly
	// stopped" case that no error would otherwise surface.
	Subkeys []string `json:"subkeys,omitempty"`

	// Tenants breaks the listing down by user= segment, for artifacts the
	// layout marks PerTenant. Empty for shared artifacts and for a bucket with
	// no tenant segments yet.
	//
	// Judging a per-tenant artifact on the aggregate above is worse than
	// useless: LastModified is the newest object across ALL tenants, so one
	// tenant whose jobs still run makes the row read healthy while eleven
	// others have written nothing for a week. Partitions has the same defect —
	// the dt= values are unioned, so a day missing for one tenant is hidden by
	// any other tenant having it. That is the rosterbot-ys8 blindness (a dead
	// producer is indistinguishable from a live one) reproduced per tenant,
	// which is exactly what this page exists to catch.
	Tenants map[string]TenantListing `json:"tenants,omitempty"`
}

// TenantListing is one tenant's slice of a per-tenant prefix.
type TenantListing struct {
	Objects      int       `json:"objects"`
	LastModified time.Time `json:"last_modified"`
	Partitions   []string  `json:"partitions,omitempty"`
	// SkippedDays is this tenant's marker-only days, computed per tenant for
	// the same reason Partitions is: a day skipped for one tenant and graded
	// for another is exactly the difference the union would hide.
	SkippedDays []string `json:"skipped_days,omitempty"`
}

// InfraLister enumerates one prefix of the state bucket. Implemented by
// s3lineup for the deployed Lambda; nil in local `serve`, where GET /v1/infra
// returns 501 like the other optional routes.
type InfraLister interface {
	ListPrefix(ctx context.Context, prefix string) (PrefixListing, error)
}

// ArtifactStatus is one row of the status page.
type ArtifactStatus struct {
	Name        string `json:"name"`
	Prefix      string `json:"prefix"`
	Health      Health `json:"health"`
	Durable     bool   `json:"durable"`
	Producer    string `json:"producer,omitempty"`
	NoBackfill  bool   `json:"no_backfill,omitempty"`
	Partitioned bool   `json:"partitioned,omitempty"`

	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`

	// LastModified is wiretime.Time even though PrefixListing.LastModified —
	// the value it is copied from — stays a real time.Time. That split is the
	// point: the listing's timestamp comes off S3 object metadata and is
	// arithmetic input (artifactHealth subtracts it from now), while this copy
	// is display, read by a client. Converting the listing instead would push a
	// wire concern into both InfraLister implementations and truncate the
	// operand of the staleness comparison; converting HERE, at the response
	// boundary, is the shape rosterbot-4e1j prescribes for a timestamp that is
	// genuinely a time.Time upstream.
	LastModified  wiretime.Time `json:"last_modified,omitempty"`
	AgeSeconds    float64       `json:"age_seconds,omitempty"`
	MaxAgeSeconds float64       `json:"max_age_seconds,omitempty"`

	// Truncated mirrors PrefixListing.Truncated: the listing this row is built
	// from stopped before enumerating the whole prefix. Health is forced to
	// HealthUnknown for a Durable row when this is set — the same treatment a
	// failed listing already gets — because a truncated walk cannot support a
	// freshness verdict (LastModified is only the newest object SEEN). Every
	// partition-derived field (LatestPartition, Partitions, Gaps, LostGaps,
	// Skipped) is withheld rather than computed from a partial read: a missing
	// day and an unlisted day are not the same fact, and findGaps cannot tell
	// them apart.
	Truncated bool `json:"truncated,omitempty"`

	LatestPartition string   `json:"latest_partition,omitempty"`
	Partitions      int      `json:"partitions,omitempty"`
	Gaps            []string `json:"gaps,omitempty"`

	// Skipped is the subset of the partitions that exist only as a
	// deliberate-skip marker. It is reported beside Gaps rather than folded
	// into them because it is the opposite finding: a gap is a day nobody
	// covered, a skipped day is one the producer covered and correctly found
	// nothing to record.
	//
	// Like LostGaps it deliberately does NOT move Health. A skipped day is
	// correct by construction — there was no fantasy-relevant baseball — so
	// colouring the row for it would train the reader to ignore the page,
	// which is the standing argument findGaps makes for not scanning past
	// yesterday.
	Skipped []string `json:"skipped,omitempty"`

	// LostGaps is the subset of Gaps that cannot be re-run, because the
	// artifact's declared RecoveryInput has nothing for that day. It is a
	// positive finding, never an inference from silence: if the input's listing
	// fails we know nothing and claim nothing, and that failure is visible on
	// this same page as the input artifact's own HealthUnknown row.
	LostGaps []string `json:"lost_gaps,omitempty"`

	// Tenants is how many tenants were found under a per-tenant prefix, and
	// WorstTenant names the one that produced this row's health. Reporting the
	// worst rather than the aggregate is the whole point: a page that averages
	// tenants reports green during a single tenant's outage.
	Tenants     int      `json:"tenants,omitempty"`
	WorstTenant string   `json:"worst_tenant,omitempty"`
	Subkeys     []string `json:"subkeys,omitempty"`

	// OrphanTenants counts user= segments belonging to tenants that no longer
	// exist. Their objects are still in the aggregate above — they occupy the
	// bucket either way — but they carry no health signal, because no producer
	// will ever write to them again.
	//
	// Reported rather than silently dropped: a segment excluded from the
	// verdict and named nowhere is indistinguishable from one that was never
	// there, and "these bytes belong to nobody" is worth an operator's glance
	// even though it is not a fault.
	OrphanTenants int `json:"orphan_tenants,omitempty"`

	// DormantTenants counts user= segments belonging to tenants who still
	// exist but whom the fan-out deliberately never launches: lambda/dispatch
	// skips any tenant whose Fantrax connection is not Usable(), so an invited
	// member who never completed connect has no producer either. Their slice
	// cannot refresh, so judging it left the row permanently red for a person
	// who has done nothing wrong (rosterbot-5lkj).
	//
	// Counted apart from OrphanTenants rather than folded in, because the two
	// prompt different actions. An orphan needs none — the account is gone. A
	// dormant tenant is a person waiting on a reconnect, and collapsing them
	// would file that person under a label meaning "ignore this".
	DormantTenants int `json:"dormant_tenants,omitempty"`

	Error string `json:"error,omitempty"`
}

// InfraStatus is the whole page.
//
// GeneratedAt is always the moment of the request: this endpoint lists S3 live
// rather than serving a precomputed file. That is the point — a status page
// built from a scheduled artifact would go stale in exactly the situation it
// exists to detect, and could report "all healthy" while the job that writes it
// is the thing that died.
type InfraStatus struct {
	// wiretime.Time, not time.Time: `now` here is always the request moment
	// (handleInfra passes time.Now()), so a raw field emits a variable-length
	// RFC3339Nano fraction on every single response — the live half of
	// rosterbot-4e1j rather than the latent half.
	GeneratedAt wiretime.Time    `json:"generated_at"`
	Artifacts   []ArtifactStatus `json:"artifacts"`
}

// artifactHealth judges one listing against the artifact's expectations.
//
// Ephemeral artifacts are always OK: cache/ is TTL-evicted by design, so both
// an old object and an empty prefix (a cold start) are normal.
func artifactHealth(a layout.Artifact, l PrefixListing, now time.Time) Health {
	if !a.Durable {
		return HealthOK
	}
	if l.Objects == 0 {
		return HealthMissing
	}
	if a.MaxAge > 0 && !l.LastModified.IsZero() && now.Sub(l.LastModified) > a.MaxAge {
		return HealthStale
	}
	return HealthOK
}

const partitionLayout = "2006-01-02"

// findGaps returns the dt= days absent from a partitioned series, scanning from
// its earliest partition up to YESTERDAY. Today is excluded deliberately: the
// producers run mid-morning UTC, so "today isn't written yet" is normal for
// most of the day and would otherwise show as a permanent-looking gap every
// morning — the kind of false alarm that trains you to ignore the page.
func findGaps(partitions []string, now time.Time) []string {
	if len(partitions) < 2 {
		return nil
	}
	days := make([]string, len(partitions))
	copy(days, partitions)
	sort.Strings(days)

	first, err := time.ParseInLocation(partitionLayout, days[0], time.UTC)
	if err != nil {
		return nil
	}
	last, err := time.ParseInLocation(partitionLayout, days[len(days)-1], time.UTC)
	if err != nil {
		return nil
	}
	// Never scan past yesterday, even if a partition is somehow dated ahead.
	yesterday := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if last.After(yesterday) {
		last = yesterday
	}

	have := make(map[string]bool, len(days))
	for _, d := range days {
		have[d] = true
	}
	var gaps []string
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if key := d.Format(partitionLayout); !have[key] {
			gaps = append(gaps, key)
		}
	}
	return gaps
}

// prefixCache lists each prefix at most once per request, so an artifact that
// is both its own row and another's RecoveryInput costs one listing, not two.
type prefixCache struct {
	lister InfraLister
	seen   map[string]PrefixListing
	errs   map[string]error
}

func newPrefixCache(l InfraLister) *prefixCache {
	return &prefixCache{lister: l, seen: map[string]PrefixListing{}, errs: map[string]error{}}
}

func (p *prefixCache) get(ctx context.Context, prefix string) (PrefixListing, error) {
	if l, ok := p.seen[prefix]; ok {
		return l, p.errs[prefix]
	}
	l, err := p.lister.ListPrefix(ctx, prefix)
	p.seen[prefix], p.errs[prefix] = l, err
	return l, err
}

// recovery answers "does the recovery input still hold this day?".
//
// The zero value answers yes to everything, and that asymmetry is the point: an
// artifact with no declared input, or one whose input could not be listed, must
// never have days written off as unrecoverable. Loss is reported only from a
// listing that SUCCEEDED and did not contain the day — a positive finding, not
// an inference from silence.
type recovery struct {
	known   bool
	days    map[string]bool
	tenants map[string]map[string]bool
}

func (r recovery) holds(uid, day string) bool {
	if !r.known {
		return true
	}
	// Before the per-tenant cutover the input carries no user= segments at all,
	// so its aggregate is the only reading available and is the right one.
	if uid != "" && len(r.tenants) > 0 {
		return r.tenants[uid][day]
	}
	return r.days[day]
}

func recoveryFor(ctx context.Context, list *prefixCache, a layout.Artifact) recovery {
	if a.RecoveryInput == "" {
		return recovery{}
	}
	l, err := list.get(ctx, a.RecoveryInput)
	if err != nil {
		return recovery{}
	}
	r := recovery{known: true, days: make(map[string]bool, len(l.Partitions))}
	for _, d := range l.Partitions {
		r.days[d] = true
	}
	if len(l.Tenants) > 0 {
		r.tenants = make(map[string]map[string]bool, len(l.Tenants))
		for uid, t := range l.Tenants {
			days := make(map[string]bool, len(t.Partitions))
			for _, d := range t.Partitions {
				days[d] = true
			}
			r.tenants[uid] = days
		}
	}
	return r
}

// tenantFilter decides which user= segments a per-tenant artifact is judged on.
//
// It exists because DeleteUser deliberately leaves a tenant's durable S3
// artifacts behind and calls them inert (see UserStore.DeleteUser). They are not
// inert here: this page derives its tenant set from the segments present in the
// listing, so a deleted tenant's frozen slice went on being judged against
// MaxAge by a producer that will never run again — a red row no action could
// ever clear, which is precisely what the LostGaps rule below refuses to create.
//
// It carries a second set for the same reason reached by another route
// (rosterbot-5lkj): a tenant who still EXISTS but whose Fantrax connection is
// not Usable() is one lambda/dispatch refuses to launch, so nothing will write
// their slice either. Judging existence answered the wrong question — the one
// that matters is whether a producer is expected to run — and the row went red
// for a real member whose only fault was not having reconnected yet.
//
// The zero value judges everyone, and that asymmetry is the whole safety
// property — with one deliberate exception, stated here because the rule that
// follows is otherwise read as absolute: a PARKED tenant is marked dormant even
// with no connection store wired at all. Parking is knowable from the user
// record alone, so treating it as unknown would leave local `serve` judging a
// tenant the scheduler demonstrably skips. Every CONNECTION-derived exemption
// does obey the rule. Both sets arrive over the network; a nil store, a failed read or an
// empty answer must fall back to judging every segment, because a filter that
// narrowed on a blind read would drop a REAL tenant whose producer had died —
// the rosterbot-ys8 blindness this page exists to catch. A false alarm costs
// attention; a missing one costs the outage. Same direction, and the same
// reasoning, as the recovery zero value above.
type tenantFilter struct{ known, dormant map[string]bool }

// live reports whether this tenant still exists. An unknown live set answers yes
// to everything.
func (f tenantFilter) live(uid string) bool {
	if len(f.known) == 0 {
		return true
	}
	return f.known[uid]
}

// split partitions a listing's tenants into the ones worth judging, plus counts
// of the two kinds of segment that carry no health signal.
//
// A segment is counted once. Orphan is tested first, so a tenant who is both
// deleted and unlaunchable lands there: the counts exist to prompt actions, and
// there is nobody to chase for a reconnect once the account is gone.
func (f tenantFilter) split(all map[string]TenantListing) (judged map[string]TenantListing, orphans, dormant int) {
	if len(f.known) == 0 {
		return all, 0, 0
	}
	judged = make(map[string]TenantListing, len(all))
	for uid, t := range all {
		switch {
		case !f.live(uid):
			orphans++
		case f.dormant[uid]:
			dormant++
		default:
			judged[uid] = t
		}
	}
	return judged, orphans, dormant
}

// SchedulerSkips reports whether the fan-out will decline to launch a scheduled
// job for this tenant. hasConn is GetConnection's found flag; conn may be nil.
//
// Exported, and living here rather than in lambda/dispatch, because it is the
// ONE definition two packages need and neither can own: lambda/ is a separate
// module that imports this one, so the dependency runs only that way. The Infra
// page must exempt exactly the tenants the fan-out skips — a tenant nothing
// runs for has no producer, so judging their frozen slice against MaxAge yields
// a red row no action can clear (rosterbot-5lkj, and rosterbot-1oai before it).
//
// It exists because the two sides HAD drifted while looking identical. The
// launch gate is a conjunction — ListActive, which filters on Status, and then
// Usable() — and the first implementation of the Infra half read only the
// connection call. That left `parked AND ConnVerified` judged, which is not a
// corner case: the Tenants tab's park action writes u.Status alone and never
// touches the connection record, so parking a tester reintroduced the exact
// permanent red this exemption removes. Restating a two-part predicate in two
// packages is what made a half-copy possible.
//
// Be precise about what this guarantees, because the honest claim is narrower
// than "one definition": lambda/dispatch does NOT call this function. It still
// expresses the gate inline, as ListActive plus its own Usable() check, since
// the status half is a server-side store filter there rather than a per-user
// test. What binds the two is lambda/dispatch's contract test, which drives the
// real dispatcher against this predicate across the whole matrix. One function
// PLUS that test is the mechanism; the function alone would not be.
//
// Deliberately NOT a blindness test. A caller who could not READ the connection
// must not reach this: dispatch launches anyway (its task's AuthorizeRun is the
// authority and fails loudly) and the Infra page judges anyway (excusing on a
// blind read would blank its ability to report a real outage). Those two
// fail-open directions are opposite and both correct, so folding them in here
// would force one of them to be wrong. This answers only the question it can:
// given what the stores actually said, does the scheduler run for this tenant?
func SchedulerSkips(u *User, conn *FantraxConnection, hasConn bool) bool {
	// The empty id is dispatch's own third refusal, and it belongs here for the
	// same reason the status half does: leaving it out is a half-copy, which is
	// the failure this function exists to end. It cannot currently reach an
	// Infra row — PrefixFor("") is the un-segmented legacy path, so such a
	// tenant never produces a user= segment to judge — but "the two predicates
	// agree except on a case that happens to be unreachable" is an invariant
	// that holds by luck, and the contract test's whole claim is that they
	// agree across the matrix.
	if u == nil || u.ID == "" || u.Status != UserActive {
		return true
	}
	return !hasConn || !conn.Usable()
}

// liveTenants reads the tenant directory the Infra page judges against.
//
// ListUsers, not ListActive or the fan-out's tenants/active.json: those carry
// only tenants a job should RUN for, so a real member who is parked or
// needs_reconnect would vanish from the page entirely — hiding somebody who
// exists and is broken. The admin directory answers the question actually being
// asked here ("does this tenant still exist?"), which is the same reason
// GET /v1/tenants lists through it.
//
// Dormancy is then read from the SAME ConnectionStore lambda/dispatch gates the
// launch on, through the same Usable() call. Restating it from a status field
// would be a second definition of "will a job run for this tenant", and a second
// definition drifts from the one that actually decides with nothing to catch it:
// the symptom is a row that is red or green for a reason no code states.
//
// Every failure returns the zero filter, which judges everyone.
func (cfg Config) liveTenants(ctx context.Context) tenantFilter {
	if cfg.Users == nil {
		return tenantFilter{}
	}
	users, err := cfg.Users.ListUsers(ctx)
	if err != nil || len(users) == 0 {
		return tenantFilter{}
	}
	known := make(map[string]bool, len(users))
	dormant := make(map[string]bool)
	for _, u := range users {
		uid := string(u.ID)
		known[uid] = true

		// The connection half is consulted only when a store can actually
		// answer it. With no store wired (local `serve`) or a read that failed,
		// the tenant is handed to the predicate as connected, so that ONLY the
		// status half can mark them dormant: not knowing must never exempt, or
		// a DynamoDB blip silently excuses every tenant at once and blanks this
		// page's ability to report a real outage — indistinguishable from
		// health. Dispatch is permissive on the same read in its own terms — it
		// launches anyway — so the two are less opposed than each erring toward
		// doing its own job regardless, which is why SchedulerSkips declines to
		// answer that case at all.
		//
		// Parking needs no store at all, and that asymmetry is load-bearing:
		// gating it behind a connection read would leave the nil-store case
		// judging a tenant the scheduler demonstrably skips.
		conn, hasConn := &FantraxConnection{Status: ConnVerified}, true
		if cfg.Connections != nil {
			if c, found, cerr := cfg.Connections.GetConnection(ctx, u.ID); cerr == nil {
				conn, hasConn = c, found
			}
		}
		if SchedulerSkips(u, conn, hasConn) {
			dormant[uid] = true
		}
	}
	return tenantFilter{known: known, dormant: dormant}
}

// buildStatus lists every artifact and judges it. A listing failure is confined
// to its own row (HealthUnknown + the message) so one broken prefix cannot
// blank the page — the same soft-fail posture the rest of the API takes.
func buildStatus(ctx context.Context, lister InfraLister, artifacts []layout.Artifact, now time.Time) InfraStatus {
	return buildStatusFor(ctx, lister, artifacts, now, tenantFilter{})
}

// buildStatusFor is buildStatus with an explicit tenant filter. Split out rather
// than threaded through every caller because the zero filter IS the old
// behaviour, so the plain entry point stays honest for the single-tenant cases
// (`serve`, and every test that has one tenant by construction).
func buildStatusFor(ctx context.Context, lister InfraLister, artifacts []layout.Artifact, now time.Time, filter tenantFilter) InfraStatus {
	st := InfraStatus{GeneratedAt: wiretime.New(now), Artifacts: make([]ArtifactStatus, 0, len(artifacts))}
	// A prefix can be needed twice — once as its own row, once as another
	// artifact's RecoveryInput — and listing it twice would double the S3 calls
	// for no new information.
	list := newPrefixCache(lister)

	for _, a := range artifacts {
		row := ArtifactStatus{
			Name:        a.Name,
			Prefix:      a.S3Prefix,
			Durable:     a.Durable,
			Producer:    a.Producer,
			NoBackfill:  a.NoBackfill,
			Partitioned: a.Partitioned,
		}
		if a.MaxAge > 0 {
			row.MaxAgeSeconds = a.MaxAge.Seconds()
		}

		l, err := list.get(ctx, a.S3Prefix)
		if err != nil {
			row.Health = HealthUnknown
			row.Error = err.Error()
			st.Artifacts = append(st.Artifacts, row)
			continue
		}

		row.Objects, row.Bytes = l.Objects, l.Bytes
		row.LastModified = wiretime.New(l.LastModified)
		row.Subkeys = l.Subkeys
		row.Truncated = l.Truncated
		if !l.LastModified.IsZero() {
			row.AgeSeconds = now.Sub(l.LastModified).Seconds()
		}
		row.Health = artifactHealth(a, l, now)

		// Orphans and dormant tenants are split off BEFORE any judgement so
		// both the health loop and the gap scan below see the same tenant set.
		// Filtering one and not the other would leave an exempt tenant able to
		// redden the row through whichever path was missed.
		tenants, orphans, dormant := filter.split(l.Tenants)
		row.OrphanTenants, row.DormantTenants = orphans, dormant

		// For a per-tenant artifact the aggregate above answers the wrong
		// question. Re-judge each tenant on its own slice and let the worst one
		// set the row, naming it — otherwise the busiest tenant's freshness
		// speaks for everybody.
		//
		// The verdict is rebuilt from the judged segments rather than only
		// worsened from the aggregate, because the aggregate mixes judged and
		// exempt objects and can speak for neither. It never loses a real
		// finding: LastModified is the max over every object, so it can only be
		// FRESHER than the best tenant, never staler — any staleness it would
		// have reported is reported by the loop below. What it does fix is a
		// prefix whose only writers have since been deleted or gone dormant,
		// where the aggregate is entirely exempt data and would otherwise pin
		// the row red forever.
		if a.PerTenant && len(l.Tenants) > 0 {
			row.Health = artifactHealth(a, PrefixListing{Objects: l.Objects}, now)
			row.Tenants = len(tenants)
			for _, uid := range sortedTenantIDs(tenants) {
				t := tenants[uid]
				th := artifactHealth(a, PrefixListing{
					Objects: t.Objects, LastModified: t.LastModified,
				}, now)
				if healthWorseThan(th, row.Health) {
					row.Health, row.WorstTenant = th, uid
				}
			}
		}

		// Every field this block derives — Partitions, LatestPartition, Gaps,
		// LostGaps — is a claim about days that exist or don't, and a truncated
		// walk cannot tell "missing" from "past the cut". Skip the whole
		// derivation rather than compute it from a partial read: LatestPartition
		// in particular reads keys lexicographically, so a walk that stops early
		// can report an OLD day as the newest, the opposite of a floor.
		if a.Partitioned && len(l.Partitions) > 0 && !l.Truncated {
			days := make([]string, len(l.Partitions))
			copy(days, l.Partitions)
			sort.Strings(days)
			row.Partitions = len(days)
			row.LatestPartition = days[len(days)-1]
			row.Gaps = findGaps(days, now)

			// A gap is only a health FAILURE where the day cannot be recovered.
			// Grades can be re-graded from archived snapshots; team values
			// cannot be reconstructed at all (docs/adr/0002), so that gap is
			// permanent data loss and outranks a fresh newest-partition.
			// Gaps, likewise, must be computed per tenant. Unioning the dt=
			// values across tenants hides exactly the case worth seeing: a day
			// present for one tenant and missing for another.
			rec := recoveryFor(ctx, list, a)

			row.Skipped = l.SkippedDays

			// Gated on l.Tenants, NOT on the judged `tenants`, and the
			// difference is the whole correctness of the all-exempt case. The
			// health block above uses the aggregate condition; using the
			// narrower one here meant that when EVERY segment was exempt this
			// fell through to the else, which reads row.Gaps as computed from
			// the unioned partitions at the top — i.e. the exempt tenants' own
			// missing days. Health rebuilt to ok and then a NoBackfill
			// artifact was escalated back to HealthGap off holes belonging to
			// tenants no job runs for: a permanently red row with a "gone for
			// good" banner and no action able to clear it, which is precisely
			// what this exemption exists to prevent.
			//
			// One broken connection now suffices to reach that state — the
			// operator's own, on a password rotation — where orphaning every
			// tenant would have required deleting the deployment, which is why
			// it survived the orphan work unnoticed.
			//
			// Entering with an empty judged set is correct and not a no-op:
			// the resets below clear the union, and the loop then contributes
			// nothing, so an all-exempt prefix reports no gaps rather than
			// somebody else's.
			if a.PerTenant && len(l.Tenants) > 0 {
				row.Gaps = nil
				row.Skipped = nil
				// Qualifying a gap with the tenant id is only worth its cost
				// where it separates one tenant's missing day from another's.
				// With a single tenant it separates nothing, and the id is an
				// 87-character opaque WebAuthn handle that buries the date.
				qualify := len(tenants) > 1
				for _, uid := range sortedTenantIDs(tenants) {
					t := tenants[uid]
					if len(t.Partitions) == 0 {
						continue
					}
					td := append([]string(nil), t.Partitions...)
					sort.Strings(td)
					for _, g := range findGaps(td, now) {
						label := g
						if qualify {
							label = uid + "/" + g
						}
						row.Gaps = append(row.Gaps, label)
						if !rec.holds(uid, g) {
							row.LostGaps = append(row.LostGaps, label)
						}
					}
					// Qualified on the same rule as gaps, for the same reason:
					// with one tenant the id separates nothing and buries the
					// date under an 87-character handle.
					for _, d := range t.SkippedDays {
						if qualify {
							d = uid + "/" + d
						}
						row.Skipped = append(row.Skipped, d)
					}
				}
			} else {
				for _, g := range row.Gaps {
					if !rec.holds("", g) {
						row.LostGaps = append(row.LostGaps, g)
					}
				}
			}

			// LostGaps deliberately does NOT move Health. A NoBackfill artifact
			// escalates because a gap there is exceptional; an unrecoverable day
			// in an otherwise-recoverable series is a scar, and it never heals —
			// so escalating would leave this row red for the rest of the season
			// with no action that could ever clear it. A permanent red is how a
			// status page teaches you to stop reading it, which is the same
			// reason findGaps refuses to scan past yesterday. The fact is
			// reported in the detail, where it can be acted on or accepted.
			if a.NoBackfill && len(row.Gaps) > 0 && row.Health == HealthOK {
				row.Health = HealthGap
			}
		}

		// A truncated listing overrides every verdict computed above it, for a
		// Durable artifact — matching how a failed listing is already treated
		// (HealthUnknown), because the reason is the same: we don't know enough
		// to say. LastModified is only the newest object SEEN, so the staleness
		// check above can read OK or Stale while the truth is the opposite, and
		// nothing downstream should be trusted more than the walk that fed it.
		// Ephemeral artifacts are exempt: their health never depends on
		// freshness (artifactHealth returns HealthOK for them unconditionally),
		// so a cache/ prefix hitting the object cap is not itself a problem.
		if l.Truncated && a.Durable {
			row.Health = HealthUnknown
		}

		st.Artifacts = append(st.Artifacts, row)
	}
	return st
}

func (cfg Config) handleInfra(w http.ResponseWriter, r *http.Request) {
	if cfg.Infra == nil {
		writeErr(w, http.StatusNotImplemented, "infra status not configured")
		return
	}
	st := buildStatusFor(r.Context(), cfg.Infra, layout.All(), time.Now().UTC(),
		cfg.liveTenants(r.Context()))
	writeJSON(w, http.StatusOK, st)
}

// FileInfraStore lists the local-filesystem equivalents of the state-bucket
// prefixes, so `serve` shows the same view against a dev machine's .cache/,
// .analysis/, .teamvalue/ and friends. Mirrors the FileXxxStore pattern the
// other optional stores use.
//
// It takes the local dir from layout.Artifact.LocalDir rather than the S3
// prefix, which is the whole reason that table carries both.
type FileInfraStore struct{ root string }

// NewFileInfraStore roots the lister at a directory (usually "." — the layout's
// LocalDir values are already relative to the repo root).
func NewFileInfraStore(root string) *FileInfraStore { return &FileInfraStore{root: root} }

// ListPrefix walks the local directory matching the given S3 prefix. An absent
// directory is not an error: it lists as zero objects, which the health rules
// then read as "missing" for a durable artifact and "fine" for an ephemeral
// one — exactly as an empty S3 prefix would.
func (f *FileInfraStore) ListPrefix(ctx context.Context, prefix string) (PrefixListing, error) {
	dir := localDirFor(prefix)
	if dir == "" {
		return PrefixListing{}, nil
	}
	full := filepath.Join(f.root, dir)

	var out PrefixListing
	parts := map[string]bool{}
	subs := map[string]bool{}
	// Two sets, differenced at the end: a day is "skipped" only if it holds a
	// marker AND nothing else. See PrefixListing.SkippedDays for why presence
	// of the marker alone is not enough.
	markerDays := map[string]bool{}
	dataDays := map[string]bool{}

	err := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip the entry; returning it aborts WalkDir and the check below
			// blanks the whole /v1/infra listing for one bad entry, which is
			// the opposite of what the Infra page is for. Open question, NOT
			// settled here: the skip silently makes Objects/Bytes floors
			// without setting PrefixListing.Truncated — see rosterbot-xi3p.
			return nil //nolint:nilerr // deliberate skip; the silent-floor question is rosterbot-xi3p
		}
		rel, relErr := filepath.Rel(full, path)
		if relErr != nil {
			// WalkDir only yields paths under full, so Rel should not fail;
			// this is the inert branch, not a swallowed diagnosis. If it ever
			// does fire it drops the entry silently — same open question as
			// above, rosterbot-xi3p.
			return nil //nolint:nilerr // deliberate skip; the silent-floor question is rosterbot-xi3p
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if m := dtDirRe.FindStringSubmatch(rel); m != nil {
				parts[m[1]] = true
			}
			if m := systemDirRe.FindStringSubmatch(rel); m != nil {
				subs[m[1]] = true
			}
			return nil
		}
		if m := dayFileRe.FindStringSubmatch(rel); m != nil {
			parts[m[1]] = true
		}
		if m := dtDirRe.FindStringSubmatch(rel); m != nil {
			if pathpkg.Base(rel) == analysis.SkipMarkerFilename {
				markerDays[m[1]] = true
			} else {
				dataDays[m[1]] = true
			}
		}
		info, statErr := d.Info()
		if statErr != nil {
			// The entry vanished between readdir and stat. Skipping costs this
			// object's contribution to Objects/Bytes below, which is exactly
			// the un-marked floor rosterbot-xi3p is open on.
			return nil //nolint:nilerr // deliberate skip; the silent-floor question is rosterbot-xi3p
		}
		out.Objects++
		out.Bytes += info.Size()
		if info.ModTime().After(out.LastModified) {
			out.LastModified = info.ModTime()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return PrefixListing{}, err
	}

	out.Partitions = sortedStrings(parts)
	out.Subkeys = sortedStrings(subs)
	out.SkippedDays = MarkerOnlyDays(markerDays, dataDays)
	return out, nil
}

// MarkerOnlyDays is the set difference marker \ data: the days a skip marker
// covers and no real record does. Shared by both listers so the deployed S3
// path and local `serve` cannot answer the question differently.
func MarkerOnlyDays(marker, data map[string]bool) []string {
	var out []string
	for d := range marker {
		if !data[d] {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

var (
	dtDirRe     = regexp.MustCompile(`(?:^|/)dt=(\d{4}-\d{2}-\d{2})(?:/|$)`)
	systemDirRe = regexp.MustCompile(`(?:^|/)system=([^/]+)(?:/|$)`)
	// dayFileRe reads the day out of a file NAMED for it, which is how the
	// projection snapshots under backtest/ record their date — no dt= segment
	// anywhere in the key. Anchored on the basename so a date appearing in a
	// directory name cannot be mistaken for one.
	dayFileRe = regexp.MustCompile(`(?:^|/)(\d{4}-\d{2}-\d{2})\.[A-Za-z0-9]+$`)
)

// localDirFor maps an S3 prefix back to its local directory via the layout
// table, so the mapping is never written down twice.
func localDirFor(prefix string) string {
	for _, a := range layout.All() {
		if a.S3Prefix == prefix {
			return a.LocalDir
		}
	}
	return ""
}

func sortedStrings(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// healthOrder ranks verdicts worst-last so a row can take the worst tenant's.
var healthOrder = map[Health]int{
	HealthOK: 0, HealthGap: 1, HealthStale: 2, HealthMissing: 3, HealthUnknown: 4,
}

func healthWorseThan(a, b Health) bool { return healthOrder[a] > healthOrder[b] }

func sortedTenantIDs(m map[string]TenantListing) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
