package lineupapi

import (
	"context"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// A truncated listing forces HealthUnknown for a Durable artifact — the same
// treatment a failed listing already gets, and for the same reason: the walk
// can no longer tell "missing" from "past the cut", so nothing it derived is
// trustworthy, including a LastModified that reads fresh only because it never
// reached whatever object would have said otherwise.
func TestBuildStatus_TruncatedDurableListingIsHealthUnknown(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/team-values/": {
			Objects:      20000,
			LastModified: now.Add(-2 * time.Hour), // would read fresh if trusted
			Partitions:   []string{"2026-07-21", "2026-07-22", "2026-07-24"},
			Truncated:    true,
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.TeamValues}, now)

	a := st.Artifacts[0]
	if !a.Truncated {
		t.Error("Truncated must reach ArtifactStatus")
	}
	if a.Health != HealthUnknown {
		t.Errorf("health = %q, want %q — a truncated walk cannot support a freshness verdict", a.Health, HealthUnknown)
	}
}

// The whole point of withholding rather than guessing: a partial listing that
// happens to have seen every day so far must not manufacture a gap for a day
// that in fact sits past the cut, unread. Before this fix findGaps ran
// regardless of truncation and would have reported 2026-07-23 missing here
// even though the listing never got far enough to know.
func TestBuildStatus_TruncatedListingWithholdsGapsAndLatestPartition(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/team-values/": {
			Objects:      20000,
			LastModified: now.Add(-2 * time.Hour),
			Partitions:   []string{"2026-07-21", "2026-07-22", "2026-07-24"},
			Truncated:    true,
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.TeamValues}, now)

	a := st.Artifacts[0]
	if len(a.Gaps) != 0 {
		t.Errorf("gaps = %v, want none — a truncated listing cannot assert a day is missing", a.Gaps)
	}
	if len(a.LostGaps) != 0 {
		t.Errorf("lost_gaps = %v, want none", a.LostGaps)
	}
	if a.LatestPartition != "" {
		t.Errorf("latest_partition = %q, want empty — the true newest day may sit past the cut, lexicographically after what was seen", a.LatestPartition)
	}
	if a.Partitions != 0 {
		t.Errorf("partitions = %d, want 0 — the count itself is a floor a truncated walk cannot vouch for", a.Partitions)
	}
	// NoBackfill artifacts otherwise escalate on any gap; a withheld listing
	// must not manufacture that escalation via HealthGap either.
	if a.Health == HealthGap {
		t.Error("health must not be HealthGap from a withheld gap scan")
	}
}

// Ephemeral artifacts never depend on freshness — cache/ hitting the object
// cap is not itself a fault — so the truncated-forces-Unknown override must
// not fire for them the way it does for a Durable row.
func TestBuildStatus_TruncatedEphemeralListingStaysOK(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"cache/": {
			Objects:   20000,
			Truncated: true,
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.Cache}, now)

	a := st.Artifacts[0]
	if !a.Truncated {
		t.Error("Truncated must still be reported even though it doesn't move health")
	}
	if a.Health != HealthOK {
		t.Errorf("health = %q, want %q — an ephemeral prefix's health never depends on freshness", a.Health, HealthOK)
	}
}

// An untruncated listing must not carry the flag or lose any of its derived
// fields — this whole feature must be a no-op on the common case.
func TestBuildStatus_UntruncatedListingIsUnaffected(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/team-values/": {
			Objects:      3,
			LastModified: now.Add(-2 * time.Hour),
			Partitions:   []string{"2026-07-21", "2026-07-22", "2026-07-24"},
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.TeamValues}, now)

	a := st.Artifacts[0]
	if a.Truncated {
		t.Error("Truncated must be false when the listing never hit its cap")
	}
	if len(a.Gaps) != 1 || a.Gaps[0] != "2026-07-23" {
		t.Errorf("gaps = %v, want [2026-07-23]", a.Gaps)
	}
	if a.LatestPartition != "2026-07-24" {
		t.Errorf("latest_partition = %q, want 2026-07-24", a.LatestPartition)
	}
}
