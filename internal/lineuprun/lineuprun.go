// Package lineuprun owns the lineup-optimization orchestration: fetching
// rosters/projections, running the hitter and pitcher optimizers per date,
// archiving projection snapshots, publishing the read-only lineup API JSON,
// and applying the resulting moves to Fantrax. It is the shared engine behind
// both the `optimize` command (a single live run) and the `shadow` command
// (one dry-run capture per projection system) — the two adapters are
// distinguished entirely by the explicit Options passed to Run, not by
// package-level mutable state.
package lineuprun

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/progress"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/roster"
	"golang.org/x/term"
)

// cacheDir is the on-disk file cache root. Matches cmd.cacheDir — both name
// the same physical directory used by *fantrax.Client's cache layer, set up
// by the caller before Run is invoked.
const cacheDir = ".cache"

// Options carries every per-run behavior toggle that used to live as
// package-level cobra-bound vars (and, for snapshot output, the
// captureSystemRoot global) directly consulted by the orchestration code.
// Both cmd/optimize.go (a single real/dry-run pass) and cmd/shadow.go (one
// dry-run capture per projection system, looped) build an explicit Options
// value per call — there is no shared mutable state between them.
type Options struct {
	// Today is the caller's notion of "today" (ET midnight, UTC-normalized).
	Today time.Time

	// NeedsSeasonLookup resolves Dates to the full remaining season (the
	// `--dates all` case); NeedsMatchupLookup resolves to the remaining days
	// in the current matchup week (`--matchup`). Both require ft, so this
	// resolution happens inside Run rather than before it. At most one may be
	// true; the caller enforces mutual exclusivity against explicit --dates.
	NeedsSeasonLookup  bool
	NeedsMatchupLookup bool

	// HitterSystem and PitcherSystem select the FanGraphs system per role
	// (steamer, depthcharts, thebatx, atc, and their -ros variants). They split
	// what used to be one ProjectionSystem field so a tenant can run each role
	// on a different model (rosterbot-5qvs); shadow and the single --projections
	// flag set both to the same value. The two loads run sequentially, which is
	// what keeps SetProjectionSystem's package-global URL mutation safe.
	HitterSystem  string
	PitcherSystem string

	// CheckRoster enables the IL/Minors slot-mismatch alert block.
	CheckRoster bool

	// ShowPipeline enables the full per-player projection pipeline detail
	// tables (base → blend → matchup/gate → final).
	ShowPipeline bool

	// WriteSnapshots is the single resolved decision on whether this run
	// archives projection snapshots. The caller owns the policy: cmd's
	// resolveWriteSnapshots folds --snapshot, the deprecated
	// --archive-projections / BACKTEST_ARCHIVE=1 aliases and the dry-run
	// default into this one bool, and shadow simply passes true (a capture run
	// exists only to produce snapshots). This replaced three Options fields
	// encoding one behaviour plus an inline os.Getenv in Run (rosterbot-6rv).
	WriteSnapshots bool

	// SnapshotStore is where projection snapshots are persisted. Supplied by
	// cmd (S3 when STATE_BUCKET is set, else local .backtest/) so this package
	// never reads the environment itself — the same arrangement as
	// Options.Publisher. Required whenever WriteSnapshots is true.
	SnapshotStore backtest.SnapshotStore

	// SnapshotRoot is the directory within SnapshotStore that snapshots are
	// written into, RELATIVE to the store root: "snapshots" for a normal
	// optimize run, or a per-system shadow partition
	// ("snapshots-systems/system=<sys>"). It stopped being a filesystem path in
	// rosterbot-iqso, when snapshots moved off the bulk directory sync onto a
	// typed store; the store owns the root, this names the partition.
	SnapshotRoot string

	// PublishLineupFlag force-publishes today's read-only API lineup JSON
	// even in dry-run (mirrors --publish-lineup).
	PublishLineupFlag bool

	// Publisher is the destination for the read-only API lineup JSON, selected
	// by the caller (cmd, via internal/statestore) so this package no longer
	// reads STATE_BUCKET. Nil means "do not publish" (the shadow command).
	Publisher lineupapi.Publisher

	// ILStartMarkers dedups the IL-start alert, one marker per (player, start
	// date), via the check->send->mark discipline in internal/alertmarker.
	// Selected by the caller like Publisher, so this package still reads no
	// environment; lineupapi.BlobStore satisfies alertmarker.Store
	// structurally. Nil disables dedup rather than the alert: with no record
	// of what was sent, repeating is the safe direction — a duplicate push is
	// recoverable, a silently dropped one is the failure this alert exists for.
	ILStartMarkers alertmarker.Store

	// GSFloorMarkers dedups the proactive GS-floor alert, one marker per
	// (season, weekly period), through the same internal/alertmarker seam.
	// Nil disables dedup rather than the alert, the same direction
	// ILStartMarkers chooses and for the same reason: with no record of what
	// was sent, repeating is recoverable and going quiet is not.
	GSFloorMarkers alertmarker.Store

	// ApplyFailMarkers dedups the lineup apply-failure alert, one marker per
	// apply date with the failure text as the episode token, through the same
	// internal/alertmarker seam. Nil disables dedup rather than the alert,
	// like its two siblings above. Without it the alert is level-triggered:
	// the 2026-08-29 Cavalli lock paged the operator from every hourly run
	// for the rest of the day.
	ApplyFailMarkers alertmarker.Store

	// The five dependency seams. Each nil field is resolved to its production
	// implementation by withDefaults, called once at the top of Run — the
	// nil-means-default idiom Out already uses, extended from "where output
	// goes" to "where input comes from". cmd never sets them; TestRun sets all
	// five. Setting SOME of them is legal but easy to get wrong: a fake stack
	// that forgets one field silently reaches the real network for that
	// dependency, so tests build the full set through one helper rather than
	// hand-assembling a partial one.

	// Schedule serves MLB schedule facts (games, probables, locks, venues,
	// benchings) to the roster-alert, GS-budget and per-date optimize phases.
	// Nil constructs the real schedule.Client with CacheDir set to the package
	// cacheDir — matching Run's previous inline construction exactly,
	// including that the cache dir is set regardless of NoCache (a
	// pre-existing quirk the seam preserves, not fixes).
	Schedule ScheduleClient

	// LoadBattingProjections and LoadPitcherProjections load the FanGraphs
	// projection sources the run blends and optimizes against. Nil defaults to
	// projections.LoadBattingProjections / LoadPitcherProjections. Overriding
	// them is how a test substitutes fixture-backed sources (see
	// projections.NewFanGraphsSourceFromEntries) without a live fetch. Note
	// Run still calls projections.SetProjectionSystem up front — a package
	// global — so tests that inject these must not run t.Parallel() against
	// each other; when internal/projections someday takes the system as a real
	// parameter, only these defaults change, not Run or any fake.
	LoadBattingProjections func(ctx context.Context, system, cacheDir string, ttl time.Duration) (*projections.FanGraphsSource, projections.LoadResult, error)
	LoadPitcherProjections func(ctx context.Context, system, cacheDir string, ttl time.Duration) (*projections.FanGraphsPitcherSource, projections.LoadResult, error)

	// FetchHandedness resolves MLBAM IDs to bats/throws for matchup
	// adjustments. Nil defaults to projections.FetchMLBHandednessCached — the
	// field names the operation, not the caching behind it.
	FetchHandedness func(mlbamIDs map[string]int, cacheDir string, ttl time.Duration) (bats, throws map[string]string, err error)

	// LoadHKBMeta loads the HKB dynasty age/value enrichment for the published
	// lineup JSON. Nil defaults to this package's LoadHKBMeta. Like the
	// enrichment itself it is only ever called when Publisher is non-nil, so a
	// test that leaves Publisher nil never needs to fake it.
	LoadHKBMeta func(ctx context.Context, cacheDir string) (map[string]lineupapi.Dynasty, error)

	// Out is where the run's human-readable output goes — the per-date board,
	// the planned-moves block, the warning lines and the apply log. The caller
	// owns stdout (rosterbot-rr1): cmd passes os.Stdout, tests pass a buffer.
	// A nil Out defaults to os.Stdout so a caller that forgets it degrades to
	// the old behaviour rather than panicking mid-run.
	Out io.Writer

	// NoCache bypasses the file cache (mirrors the persistent --no-cache flag).
	NoCache bool

	// Verbose selects detailed log output instead of the interactive progress
	// display (mirrors the persistent --verbose flag).
	Verbose bool
}

// Result reports per-run facts the caller needs after Run returns. Today this
// is just projection-system data availability — the shadow command uses it to
// detect a provider outage transition. It replaces the old
// lastProjectionLoadResult package var, which existed only because
// runOptimize's signature was fixed by cobra; Run has no such constraint.
type Result struct {
	HittersNoData  bool
	PitchersNoData bool
}

// recentStatsClient is the fantrax subset windowedHitterRecent uses.
type recentStatsClient interface {
	GetSeasonDateRange() (time.Time, time.Time, error)
	DailyFantasyPoints(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.DayRoster, error)
	MLBDailyFPts(players []fantrax.MLBPlayerRef, start, end time.Time) ([]fantrax.DayRoster, error)
}

// LineupClient is the narrow subset of *fantrax.Client that Run needs. It
// embeds recentStatsClient so Run can hand its ft to windowedHitterRecent.
// *fantrax.Client satisfies it implicitly — internal/fantrax is not modified.
// Landing this seam is what lets rosterbot-6rv's phase-level tests inject a fake.
type LineupClient interface {
	recentStatsClient
	GetHitterRoster() ([]fantrax.Player, error)
	GetPitcherRoster() ([]fantrax.Player, error)
	GetFullRoster() ([]fantrax.Player, fantrax.SlotCounts, error)
	GetActiveSlots() ([]fantrax.Slot, error)
	GetPitcherSlots() ([]fantrax.Slot, error)
	GetScoringWeights() (fantrax.ScoringWeights, error)
	GetPitcherScoringWeights() (fantrax.ScoringWeights, error)
	GetCurrentPeriod() (fantrax.DailyPeriod, error)
	GetMatchupWeekBounds(date, seasonStart time.Time) (weekStart, weekEnd time.Time, err error)
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
	DailyPeriodFor(seasonStart, date time.Time) fantrax.DailyPeriod
	GetHitterRosterForPeriod(period fantrax.DailyPeriod) ([]fantrax.Player, error)
	GetPitcherRosterForPeriod(period fantrax.DailyPeriod) ([]fantrax.Player, error)
	GetGSLimits(teamID string, period fantrax.WeeklyPeriod) (min, max *int, err error)
	GetTeamGS(teamID, teamName string, sp fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []fantrax.PitcherStart, error)
	GetTeamPitcherDays(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.PitcherDay, error)
	GetRecentPitcherStats(currentPeriod fantrax.DailyPeriod) (map[string]fantrax.RecentStat, error)
	ApplyLineup(period fantrax.DailyPeriod, active []fantrax.PlayerSlot, reserve []string) error
	InvalidatePeriodRosterCache(period fantrax.DailyPeriod) error
}

// displayNameFor labels a (possibly split) pair of projection systems: the
// one name when both roles use the same system — every pre-split output is
// byte-identical through this path — and both, hitters first, when they
// differ, so a run's output always says which model produced which half.
func displayNameFor(hitSys, pitSys string) string {
	if hitSys == pitSys {
		return projDisplayName[hitSys]
	}
	return projDisplayName[hitSys] + " / " + projDisplayName[pitSys]
}

// projDisplayName maps projection system flag values to display-friendly names.
var projDisplayName = map[string]string{
	"steamer":         "Steamer",
	"depthcharts":     "DepthCharts",
	"thebatx":         "TheBatX",
	"atc":             "ATC",
	"steamer-ros":     "Steamer RoS",
	"depthcharts-ros": "DepthCharts RoS",
	"thebatx-ros":     "TheBatX RoS",
	"atc-ros":         "ATC RoS",
}

func cacheTTL(noCache bool, d time.Duration) time.Duration {
	if noCache {
		return 0
	}
	return d
}

// Run executes one full lineup-optimization pass: fetch rosters/projections,
// optimize hitters and pitchers per date, archive projection snapshots,
// publish today's lineup for the read-only API, print the plan, and apply it
// (unless cfg.DryRun). ft and cfg are wired up by the caller (cmd.initApp);
// Run owns everything downstream of that.
//
// cfg is read-only. Run used to append the resolved `--dates all` / `--matchup`
// expansion straight back into cfg.Dates — an undocumented output parameter;
// that now happens in the ResolveDates phase, which returns a value
// (rosterbot-6rv).
func Run(ctx context.Context, ft LineupClient, cfg *config.Config, opts Options) (Result, error) {
	// Fail-fast validation of both systems; the loaders re-set the package
	// globals per call, so this is the last time order matters here.
	for _, sys := range []string{opts.HitterSystem, opts.PitcherSystem} {
		if err := projections.SetProjectionSystem(sys); err != nil {
			return Result{}, err
		}
	}
	// Resolve the five dependency seams before anything reads them; every nil
	// field becomes its production implementation and nothing downstream needs
	// its own nil check. Keep this ahead of any dependency use.
	opts = opts.withDefaults()
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	// Set up progress display. The interactive check stays on the real stdout
	// file descriptor — that is what decides whether a redraw-in-place bar is
	// legible — while the writing itself goes to out like every other line.
	var prog *progress.Progress
	if opts.Verbose {
		prog = progress.NewVerbose()
	} else {
		interactive := term.IsTerminal(int(os.Stdout.Fd()))
		prog = progress.New(interactive, out)
	}

	// Cache TTLs (0 when --no-cache is set).
	projTTL := cacheTTL(opts.NoCache, projections.ProjectionCacheTTL)
	staticTTL := cacheTTL(opts.NoCache, 7*24*time.Hour)

	today := opts.Today

	// Resolve "all" / "--matchup" now that the client is available. dates is a
	// local value — Run does not write back into cfg (rosterbot-6rv).
	dates, seasonStart, err := ResolveDates(ft, cfg.Dates, opts, prog.Logf)
	if err != nil {
		return Result{}, err
	}

	// --- Load projections early to determine system for header ---
	fgSrc, batLoadResult, err := opts.LoadBattingProjections(ctx, opts.HitterSystem, cacheDir, projTTL)
	if err != nil {
		msg := fmt.Sprintf("batting projections unavailable: %v", err)
		sendOptimizeNotify(ctx, msg)
		return Result{}, fmt.Errorf("batting projections unavailable: %w", err)
	}
	if batLoadResult.NoData {
		prog.Logf("WARNING: batting projections unavailable — running on schedule + recent stats only")
	}
	fgPitSrc, pitLoadResult, err := opts.LoadPitcherProjections(ctx, opts.PitcherSystem, cacheDir, projTTL)
	if err != nil {
		msg := fmt.Sprintf("pitching projections unavailable: %v", err)
		sendOptimizeNotify(ctx, msg)
		return Result{}, fmt.Errorf("pitching projections unavailable: %w", err)
	}
	if pitLoadResult.NoData {
		prog.Logf("WARNING: pitching projections unavailable — running on schedule + recent stats only")
	}
	result := Result{
		HittersNoData:  batLoadResult.NoData,
		PitchersNoData: pitLoadResult.NoData,
	}

	prog.Header(displayNameFor(batLoadResult.System, pitLoadResult.System), formatDates(dates), cfg.DryRun)

	// Hoisted above the roster-alert block: CheckILStarters needs probables,
	// and the alert has to run before the per-date optimize pass so an
	// operator hears about a lost start while it is still recoverable. One
	// value feeds all three schedule-consuming phases, so a shared underlying
	// fetch (the sticky probables cache) is reused rather than re-fetched.
	schedClient := opts.Schedule

	// --- Roster alerts (if requested) ---
	if opts.CheckRoster {
		fullRoster, counts, err := ft.GetFullRoster()
		if err != nil {
			return result, fmt.Errorf("get full roster: %w", err)
		}
		counts.ILCapacity = cfg.ILSlots
		counts.MinorsCapacity = cfg.MinorsSlots
		alerts := roster.CheckRoster(fullRoster, counts)
		if len(alerts) > 0 {
			fmt.Fprintln(out, "\n=== Roster Alerts ===")
			for _, a := range alerts {
				label := alertLabel(a.Type)
				fmt.Fprintf(out, "  ⚠ %-25s (%s)  %s → %s\n", a.Player.Name, a.Player.MLBTeam, label, a.Suggestion)
			}
			fmt.Fprintln(out)
		}

		reportILStarts(ctx, ilStartInputs{
			Roster:  fullRoster,
			Sched:   schedClient,
			Today:   today,
			Markers: opts.ILStartMarkers,
			// notify.Send returns an error only on a failed FEED write, which
			// is the durable half of check→send→mark: a recorded alert may
			// safely be marked sent even if its push half misfires, because
			// the feed record is what the marker's dedup protects. The
			// Configured guard keeps the third state honest — an unconfigured
			// dispatcher no-ops Send with a nil error, and marking on that
			// would mute the alert forever without anything having been
			// recorded anywhere (the old creds guard's job, one layer up).
			Notify: func(message string) error {
				if !notify.Configured() {
					return fmt.Errorf("notify dispatcher not configured")
				}
				return notify.Send(ctx, notify.Event{Kind: "lineup", Title: "IL Start Alert", Message: message})
			},
			DryRun: cfg.DryRun,
			Out:    out,
		})
	}

	// --- Load the date-invariant Fantrax inputs (six fetches + two period
	// lookups, concurrent; see LoadInputs for the failure policy) ---
	inputs, err := LoadInputs(ft, prog, displayNameFor(opts.HitterSystem, opts.PitcherSystem))
	if err != nil {
		return result, err
	}
	hitterRoster, hitterSlots, hitterScoring := inputs.HitterRoster, inputs.HitterSlots, inputs.HitterScoring
	pitcherRoster, pitcherSlots, pitcherScoring := inputs.PitcherRoster, inputs.PitcherSlots, inputs.PitcherScoring
	currentPeriod, periodErr := inputs.CurrentPeriod, inputs.PeriodErr
	periods, periodsErr := inputs.Periods, inputs.PeriodsErr

	// --- Hitter projections (shared across dates) ---
	prog.Start("Projections")
	if batLoadResult.FellBack {
		prog.Logf("WARNING: %s RoS projections unavailable — using %s preseason", projDisplayName[opts.HitterSystem], projDisplayName[opts.HitterSystem])
	}
	if batLoadResult.FromCSV {
		prog.Logf("WARNING: API projections unavailable — using CSV file")
	}
	prog.Logf("fangraphs batting projections loaded (%s, %d players)", projDisplayName[batLoadResult.System], fgSrc.Len())
	if pitLoadResult.FellBack {
		prog.Logf("WARNING: %s RoS pitching projections unavailable — using %s preseason", projDisplayName[opts.PitcherSystem], projDisplayName[opts.PitcherSystem])
	}
	if pitLoadResult.FromCSV {
		prog.Logf("WARNING: API pitching projections unavailable — using CSV file")
	}
	prog.Logf("fangraphs pitching projections loaded (%s, %d players)", projDisplayName[pitLoadResult.System], fgPitSrc.Len())
	// --- Blend the base sources with recent Fantrax production ---
	// Hitters use a trailing 30-day window, pitchers season-to-date YTD; that
	// asymmetry is backtest-justified and documented on BlendSources.
	blend := BlendSources(ft, BlendInputs{
		HitterBase:     projections.NewChainedSource(fgSrc, projections.NewRollingSource()),
		PitcherBase:    projections.NewPitcherChainedSource(fgPitSrc, projections.NewPitcherRollingSource()),
		HitterRoster:   hitterRoster,
		PitcherRoster:  pitcherRoster,
		HitterScoring:  hitterScoring,
		PitcherScoring: pitcherScoring,
		HitterAvgFPG:   fgSrc.AverageFPG(hitterScoring),
		PitcherAvgFPG:  fgPitSrc.AverageFPG(pitcherScoring),
		TeamID:         cfg.TeamID,
		Today:          today,
		SeasonStart:    seasonStart,
		CurrentPeriod:  currentPeriod,
		PeriodErr:      periodErr,
		BlendMinGP:     cfg.BlendMinGP,
		NoCache:        opts.NoCache,
		SystemName:     displayNameFor(opts.HitterSystem, opts.PitcherSystem),
	})
	for _, line := range blend.Logs {
		prog.Logf("%s", line)
	}
	hitterProjSrc, pitcherProjSrc := blend.Hitters, blend.Pitchers

	// Collect MLBAM IDs for handedness lookup.
	hitterMLBAMIDs := fgSrc.MLBAMIDs()

	prog.Done("Projections", "batting + pitching loaded")

	prog.Start("Recent stats")
	prog.Done("Recent stats", fmt.Sprintf("%d hitters · %d pitchers", blend.RecentHitterCount, blend.RecentPitcherCount))

	// Extract pitcher FIP for matchup adjustments.
	prog.Start("Pitcher info")
	var pitcherFIP map[string]float64
	var leagueAvgFIP float64
	var pitcherMLBAMIDs map[string]int
	pitcherFIP, leagueAvgFIP = fgPitSrc.PitcherInfo()
	pitcherMLBAMIDs = fgPitSrc.MLBAMIDs()
	prog.Logf("pitcher info loaded: %d FIP, league avg FIP=%.2f", len(pitcherFIP), leagueAvgFIP)
	prog.Done("Pitcher info", fmt.Sprintf("%d FIP entries · league avg %.2f", len(pitcherFIP), leagueAvgFIP))

	// Fetch handedness from MLB Stats API using MLBAM IDs from FanGraphs.
	var hitterBats map[string]string
	var pitcherHandedness map[string]string
	allMLBAMIDs := make(map[string]int)
	for k, v := range hitterMLBAMIDs {
		allMLBAMIDs[k] = v
	}
	for k, v := range pitcherMLBAMIDs {
		allMLBAMIDs[k] = v
	}
	prog.Start("Handedness")
	if len(allMLBAMIDs) > 0 {
		bats, throws, err := opts.FetchHandedness(allMLBAMIDs, cacheDir, staticTTL)
		if err != nil {
			prog.Logf("WARNING: MLB handedness unavailable (%v) — matchup adjustments disabled", err)
			prog.Warn("Handedness", "unavailable — matchup adjustments disabled")
		} else {
			hitterBats = bats
			pitcherHandedness = throws
			prog.Logf("handedness loaded: %d hitter bats, %d pitcher throws", len(hitterBats), len(pitcherHandedness))
			prog.Done("Handedness", fmt.Sprintf("%d bats · %d throws", len(hitterBats), len(pitcherHandedness)))
		}
	} else {
		prog.Done("Handedness", "skipped — no MLBAM IDs")
	}

	multiDate := len(dates) > 1

	// Get season start date for period calculation.
	// If ResolveDates already fetched the season range (--dates all or --matchup),
	// reuse seasonStart from above instead of refetching it.
	if seasonStart.IsZero() {
		s, _, err := ft.GetSeasonDateRange()
		if err != nil {
			prog.Logf("WARNING: could not get season start (%v) — only today's lineup can be set", err)
		} else {
			seasonStart = s
		}
	}

	// Skip optimization if today is before the season start.
	if !seasonStart.IsZero() && today.Before(seasonStart) && !multiDate {
		prog.Logf("season starts %s — nothing to optimize yet", seasonStart.Format("2006-01-02"))
		fmt.Fprintf(out, "\nSeason starts %s. No games to optimize for today.\n", seasonStart.Format("2006-01-02"))
		return result, nil
	}

	// --- GS Budget (weekly game-start limit awareness) ---
	// The cascade itself lives in ComputeGSBudget; Run keeps only the two
	// side effects the phase deliberately does not perform — writing to the
	// progress display and actually sending the Pushover it asked for.
	var gsBudget *optimizer.GSBudget
	if cfg.GSTrackingEnabled {
		prog.Start("GS budget")

		dec := ComputeGSBudget(ctx, ft, schedClient, GSInputs{
			TeamID:          cfg.TeamID,
			Today:           today,
			SeasonStart:     seasonStart,
			Periods:         periods,
			PeriodsErr:      periodsErr,
			PitcherRoster:   pitcherRoster,
			NumPitcherSlots: len(pitcherSlots),
			NoCache:         opts.NoCache,
			ProjPts: func(p fantrax.Player) float64 {
				return pitcherProjectedPts(p, pitcherProjSrc, pitcherScoring)
			},
		})
		for _, line := range dec.Logs {
			prog.Logf("%s", line)
		}
		if dec.Alert != nil {
			if perr := notify.Send(ctx, notify.Event{Kind: "alert", Title: dec.Alert.Title, Message: dec.Alert.Message}); perr != nil {
				prog.Logf("WARNING: failed to record GS alert: %v", perr)
			}
		}
		gsBudget = dec.Budget

		if gsBudget != nil {
			prog.Done("GS budget", fmt.Sprintf("%d/%d used · %.1f projected", gsBudget.Used, gsBudget.Limit, gsBudget.FutureDemand()))
		} else {
			prog.Warn("GS budget", "unavailable — limit disabled")
		}

		// The floor alert reads the budget this phase just built and fetches
		// nothing of its own. It sits here, beside the ceiling-side gate rather
		// than in Emit, because it is a statement about the WEEK and not about
		// any one date's lineup — a --matchup run optimizing five dates still
		// has exactly one floor to be short of.
		reportGSFloor(gsFloorInputs{
			Budget:  gsBudget,
			Period:  dec.Period,
			Season:  dec.Season,
			Markers: opts.GSFloorMarkers,
			// Same contract as the IL-start alert: notify.Send errors only on a
			// failed FEED write, which is the durable half of check→send→mark,
			// and an unconfigured dispatcher no-ops with a nil error — so the
			// Configured guard is what stops a marker being written for an
			// alert that was never recorded anywhere.
			Notify: func(message string) error {
				if !notify.Configured() {
					return fmt.Errorf("notify dispatcher not configured")
				}
				return notify.Send(ctx, notify.Event{Kind: "lineup", Title: "GS Floor Risk", Message: message})
			},
			DryRun: cfg.DryRun,
			Out:    out,
		})
	} else {
		// The disabled path owes the reader a line, on the same reasoning as
		// the "gs floor check:" and "il-start check:" coverage lines above it:
		// three things are dropped here — the weekly CAP (applyGSGate no-ops on
		// a nil budget), the rosterbot-dpm FLOOR, and the rosterbot-cs9p floor
		// ALERT — and without a notice the result is byte-indistinguishable
		// from a healthy run. Worse, it is indistinguishable from the fail-open
		// cascade, which drops the same three for a genuinely broken reason and
		// at least says "GS budget: unavailable — limit disabled": the env gate
		// used to log LESS than the failure mode it mimics, and the 2026-08-29
		// audit read it as a live cascade regression.
		//
		// It goes to out rather than prog.Logf because Logf is verbose-only —
		// a notice invisible on the plain `optimize --dry-run` that produced
		// the misreading would be the same silence under another name.
		//
		// "unset or not truthy" rather than the "not set" this wording is
		// otherwise byte-shared with cmd/gs_check.go for: config.envBool
		// returns its fallback on an UNPARSEABLE value as well as an empty one,
		// discarding the ParseBool error, so `GS_TRACKING_ENABLED=yes` lands
		// here too. Naming the wrong cause would send an operator to echo a var
		// that is already set and back to guessing — a smaller copy of the
		// misdiagnosis this line exists to end.
		fmt.Fprintln(out, "  gs budget check: GS tracking disabled (GS_TRACKING_ENABLED unset or not truthy) — no cap, no floor, no floor alert this run")
	}

	// Build name/slot lookup maps for display.
	playerName := make(map[string]string)
	for _, p := range hitterRoster {
		playerName[p.ID] = p.Name
	}
	for _, p := range pitcherRoster {
		playerName[p.ID] = p.Name
	}
	slotName := make(map[string]string)
	for _, s := range hitterSlots {
		slotName[s.PosID] = s.PosName
	}
	for _, s := range pitcherSlots {
		slotName[s.PosID] = s.PosName
	}

	// --- Optimize every date in parallel ---
	prog.Start("Optimize")
	results := OptimizeDates(ctx, ft, schedClient, OptimizeInputs{
		Dates:             dates,
		Today:             today,
		SeasonStart:       seasonStart,
		HitterRoster:      hitterRoster,
		PitcherRoster:     pitcherRoster,
		HitterSlots:       hitterSlots,
		PitcherSlots:      pitcherSlots,
		HitterScoring:     hitterScoring,
		PitcherScoring:    pitcherScoring,
		HitterSrc:         hitterProjSrc,
		PitcherSrc:        pitcherProjSrc,
		HitterBats:        hitterBats,
		PitcherHandedness: pitcherHandedness,
		PitcherFIP:        pitcherFIP,
		LeagueAvgFIP:      leagueAvgFIP,
		GSBudget:          gsBudget,
		ShowPipeline:      opts.ShowPipeline,
	})
	prog.Done("Optimize", "done")

	// --- Dynasty enrichment for the published lineup JSON ---
	// Gated on the same condition publishToday applies: a Publisher at all.
	// Dry runs used to publish nothing and so were excluded here; they now
	// publish a preview, and the preview has to carry the same age/value
	// columns as the applied lineup or the app would render two visibly
	// different lineups for the same roster. The added cost is a cached
	// hkb.GetPlayers read, not a fresh scrape per run.
	//
	// Soft-fail: this is display enrichment on a lineup the optimizer has
	// already decided, so an HKB outage costs the age/value columns and
	// nothing else.
	var hkbMeta map[string]lineupapi.Dynasty
	if opts.Publisher != nil {
		var err error
		if hkbMeta, err = opts.LoadHKBMeta(ctx, cacheDir); err != nil {
			prog.Logf("WARNING: HKB values unavailable — publishing lineup without age/value: %v", err)
		}
	}

	prog.Finish()

	// --- Emit: snapshot, publish, print, apply, notify ---
	Emit(ft, EmitInputs{
		Results:        results,
		MultiDate:      multiDate,
		SlotName:       slotName,
		PlayerName:     playerName,
		HitterSlots:    hitterSlots,
		PitcherSlots:   pitcherSlots,
		GSBudget:       gsBudget,
		ShowPipeline:   opts.ShowPipeline,
		WriteSnapshots: opts.WriteSnapshots,
		SnapshotStore:  opts.SnapshotStore,
		SnapshotRoot:   opts.SnapshotRoot,
		Projections: projInputs{
			HitterSystem:   batLoadResult.System,
			PitcherSystem:  pitLoadResult.System,
			HittersNoData:  batLoadResult.NoData,
			PitchersNoData: pitLoadResult.NoData,
			// The one fact the snapshot could not previously record: how old
			// these projections already were when the run scored with them.
			// A capture built on the cache's stale fallback is otherwise
			// indistinguishable from a fresh one at grade time
			// (rosterbot-c61b).
			HitterFetchedAt:  batLoadResult.FetchedAt,
			PitcherFetchedAt: pitLoadResult.FetchedAt,
		},
		PublishLineup: opts.PublishLineupFlag,
		Publisher:     opts.Publisher,
		HKB:           hkbMeta,
		Cfg:           cfg,
		Out:           out,
		Notify: func(message string) {
			sendOptimizeNotify(ctx, message)
		},
		// The same Configured guard as the IL-start and GS-floor notifiers: an
		// unconfigured dispatcher no-ops Send with a nil error, and marking on
		// that would mute the apply-failure alert with nothing recorded.
		NotifyAlert: func(message string) error {
			if !notify.Configured() {
				return fmt.Errorf("notify dispatcher not configured")
			}
			return notify.Send(ctx, notify.Event{Kind: "lineup", Title: "Fantrax Lineup", Message: message})
		},
		ApplyFailMarkers: opts.ApplyFailMarkers,
	})

	return result, nil
}

// sendOptimizeNotify emits the lineup event through the dispatcher (feed
// record first, then APNs — and Pushover during the cutover window). An
// unconfigured dispatcher is a silent no-op, so no creds guard remains.
func sendOptimizeNotify(ctx context.Context, message string) {
	if err := notify.Send(ctx, notify.Event{Kind: "lineup", Title: "Fantrax Lineup", Message: message}); err != nil {
		log.Printf("WARNING: lineup notification failed: %v", err)
	}
}
