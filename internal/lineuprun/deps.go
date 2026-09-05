package lineuprun

import (
	"context"
	"time"

	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/schedule"
)

// ScheduleClient is the MLB-schedule surface Run threads through its three
// schedule-consuming phases — the union of ilStartSchedule (il_starts.go),
// gsScheduleClient (gsbudget.go) and dateScheduleClient (optimize_dates.go).
// Each phase keeps its own narrower interface for its own tests; this type
// exists only so one Options field can feed all three call sites, which is
// also what makes "hermetic-except-one" diagnosis possible: pin the schedule
// while every other dependency stays real, the Run-level generalization of
// the diag_gsforecast_test.go pattern.
//
// *schedule.Client satisfies it structurally, and so does a map-backed fake.
type ScheduleClient interface {
	TeamsPlayingOn(ctx context.Context, date time.Time) (map[string]bool, error)
	LockedTeams(ctx context.Context, date time.Time) (map[string]bool, error)
	ProbableStarters(ctx context.Context, date time.Time) (map[string]string, error)
	GameVenues(ctx context.Context, date time.Time) (map[string]string, error)
	BenchedPlayers(ctx context.Context, date time.Time, rosterNames map[string]string) (map[string]bool, error)
}

// withDefaults returns a copy of o with every nil dependency field replaced
// by its production implementation. Run calls it exactly once, before any of
// the five fields is read — that ordering is the whole invariant: nothing
// downstream needs its own nil check because nothing downstream can observe a
// nil field after this point.
//
// It is pure assignment — it chooses which value to use, it never calls one —
// so a zero-value Options resolves to exactly the dependencies Run used to
// construct inline, and TestOptionsWithDefaults can assert the fallback
// wiring without touching the network.
//
// Two behaviors are deliberate and pinned by that test:
//   - The default Schedule is a *schedule.Client with CacheDir set to the
//     package cacheDir, regardless of o.NoCache — the same pre-existing quirk
//     as the inline construction it replaces. "Fixing" it here would be a
//     silent behavior change disguised as a refactor.
//   - Each loader default is the package function itself, not a wrapper, so
//     the seam cannot drift from production behavior.
func (o Options) withDefaults() Options {
	if o.Schedule == nil {
		sched := schedule.NewClient()
		sched.CacheDir = cacheDir // unconditionally, NoCache notwithstanding — the pinned pre-existing quirk
		o.Schedule = sched
	}
	if o.LoadBattingProjections == nil {
		o.LoadBattingProjections = projections.LoadBattingProjections
	}
	if o.LoadPitcherProjections == nil {
		o.LoadPitcherProjections = projections.LoadPitcherProjections
	}
	if o.FetchHandedness == nil {
		o.FetchHandedness = projections.FetchMLBHandednessCached
	}
	if o.LoadHKBMeta == nil {
		o.LoadHKBMeta = LoadHKBMeta
	}
	return o
}
