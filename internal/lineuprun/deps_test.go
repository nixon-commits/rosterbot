package lineuprun

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/schedule"
)

// samefunc reports whether two function values are the same function. Go only
// permits comparing funcs to nil, so the identity check goes through reflect;
// it holds for direct assignments (o.X = pkg.F), which is exactly the claim
// under test — the default IS the package function, not a wrapper that could
// drift from it.
func samefunc(a, b any) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// TestOptionsWithDefaults is the contract for Options.withDefaults: every nil
// dependency field resolves to its production implementation, every non-nil
// field passes through untouched, and the resolution is pure assignment (no
// network, no I/O — this test would hang or fail on a sandboxed runner
// otherwise).
func TestOptionsWithDefaults(t *testing.T) {
	t.Run("zero value resolves every dependency to production", func(t *testing.T) {
		d := Options{}.withDefaults()

		sched, ok := d.Schedule.(*schedule.Client)
		if !ok {
			t.Fatalf("Schedule = %T, want *schedule.Client", d.Schedule)
		}
		// The pre-existing quirk, preserved: CacheDir is set unconditionally,
		// NoCache notwithstanding — byte-for-byte the inline construction this
		// replaced.
		if sched.CacheDir != cacheDir {
			t.Fatalf("Schedule.CacheDir = %q, want %q", sched.CacheDir, cacheDir)
		}

		if d.LoadBattingProjections == nil || !samefunc(d.LoadBattingProjections, projections.LoadBattingProjections) {
			t.Fatal("LoadBattingProjections default is not projections.LoadBattingProjections")
		}
		if d.LoadPitcherProjections == nil || !samefunc(d.LoadPitcherProjections, projections.LoadPitcherProjections) {
			t.Fatal("LoadPitcherProjections default is not projections.LoadPitcherProjections")
		}
		if d.FetchHandedness == nil || !samefunc(d.FetchHandedness, projections.FetchMLBHandednessCached) {
			t.Fatal("FetchHandedness default is not projections.FetchMLBHandednessCached")
		}
		if d.LoadHKBMeta == nil || !samefunc(d.LoadHKBMeta, LoadHKBMeta) {
			t.Fatal("LoadHKBMeta default is not the package LoadHKBMeta")
		}
	})

	t.Run("populated fields pass through unchanged", func(t *testing.T) {
		fakeSched := &fakeDateSchedule{}
		bat := func(context.Context, string, string, time.Duration) (*projections.FanGraphsSource, projections.LoadResult, error) {
			return nil, projections.LoadResult{}, nil
		}
		pit := func(context.Context, string, string, time.Duration) (*projections.FanGraphsPitcherSource, projections.LoadResult, error) {
			return nil, projections.LoadResult{}, nil
		}
		hand := func(map[string]int, string, time.Duration) (map[string]string, map[string]string, error) {
			return nil, nil, nil
		}
		hkb := func(context.Context, string) (map[string]lineupapi.Dynasty, error) { return nil, nil }

		o := Options{
			Schedule:               fakeSched,
			LoadBattingProjections: bat,
			LoadPitcherProjections: pit,
			FetchHandedness:        hand,
			LoadHKBMeta:            hkb,
		}
		d := o.withDefaults()

		if d.Schedule != ScheduleClient(fakeSched) {
			t.Fatal("withDefaults replaced a non-nil Schedule")
		}
		if !samefunc(d.LoadBattingProjections, bat) {
			t.Fatal("withDefaults replaced a non-nil LoadBattingProjections")
		}
		if !samefunc(d.LoadPitcherProjections, pit) {
			t.Fatal("withDefaults replaced a non-nil LoadPitcherProjections")
		}
		if !samefunc(d.FetchHandedness, hand) {
			t.Fatal("withDefaults replaced a non-nil FetchHandedness")
		}
		if !samefunc(d.LoadHKBMeta, hkb) {
			t.Fatal("withDefaults replaced a non-nil LoadHKBMeta")
		}
	})
}
