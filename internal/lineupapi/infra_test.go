package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

type fakeInfraLister struct {
	byPrefix map[string]PrefixListing
	err      error
	calls    int
}

func (f *fakeInfraLister) ListPrefix(ctx context.Context, prefix string) (PrefixListing, error) {
	f.calls++
	if f.err != nil {
		return PrefixListing{}, f.err
	}
	return f.byPrefix[prefix], nil
}

func ymd(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// --- health verdicts ---

func TestArtifactHealth_FreshIsOK(t *testing.T) {
	now := ymd(2026, 7, 25)
	a := layout.Artifact{Name: "X", Durable: true, MaxAge: 2 * layout.Day}
	got := artifactHealth(a, PrefixListing{Objects: 3, LastModified: now.Add(-1 * time.Hour)}, now)
	if got != HealthOK {
		t.Errorf("got %q, want %q", got, HealthOK)
	}
}

func TestArtifactHealth_PastMaxAgeIsStale(t *testing.T) {
	now := ymd(2026, 7, 25)
	a := layout.Artifact{Name: "X", Durable: true, MaxAge: 2 * layout.Day}
	got := artifactHealth(a, PrefixListing{Objects: 3, LastModified: now.Add(-3 * layout.Day)}, now)
	if got != HealthStale {
		t.Errorf("got %q, want %q", got, HealthStale)
	}
}

// An empty durable prefix is worse than a stale one: nothing has EVER landed.
func TestArtifactHealth_EmptyDurablePrefixIsMissing(t *testing.T) {
	now := ymd(2026, 7, 25)
	a := layout.Artifact{Name: "X", Durable: true, MaxAge: 2 * layout.Day}
	got := artifactHealth(a, PrefixListing{Objects: 0}, now)
	if got != HealthMissing {
		t.Errorf("got %q, want %q", got, HealthMissing)
	}
}

// Ephemeral data is TTL-evicted by design — age is not a health signal, and an
// empty cache is a cold start, not a fault.
func TestArtifactHealth_EphemeralIsNeverStale(t *testing.T) {
	now := ymd(2026, 7, 25)
	a := layout.Artifact{Name: "TTL Cache", Durable: false}
	for _, l := range []PrefixListing{
		{Objects: 0},
		{Objects: 5000, LastModified: now.Add(-90 * layout.Day)},
	} {
		if got := artifactHealth(a, l, now); got != HealthOK {
			t.Errorf("ephemeral listing %+v: got %q, want %q", l, got, HealthOK)
		}
	}
}

// --- gap detection (the unbackfillable case) ---

func TestFindGaps_ReportsMissingDays(t *testing.T) {
	got := findGaps([]string{"2026-07-20", "2026-07-21", "2026-07-24"}, ymd(2026, 7, 25))
	want := []string{"2026-07-22", "2026-07-23"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Today may legitimately not be written yet (the producer runs at 14:30 UTC),
// so the scan must stop at yesterday rather than always reporting today missing.
func TestFindGaps_DoesNotCountTodayAsMissing(t *testing.T) {
	got := findGaps([]string{"2026-07-23", "2026-07-24"}, ymd(2026, 7, 25))
	if len(got) != 0 {
		t.Errorf("today is not yet due — got gaps %v, want none", got)
	}
}

func TestFindGaps_ContiguousSeriesHasNoGaps(t *testing.T) {
	got := findGaps([]string{"2026-07-22", "2026-07-23", "2026-07-24"}, ymd(2026, 7, 25))
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestFindGaps_UnsortedInputStillWorks(t *testing.T) {
	got := findGaps([]string{"2026-07-24", "2026-07-20"}, ymd(2026, 7, 25))
	if len(got) != 3 {
		t.Errorf("got %v, want 3 missing days (07-21..07-23)", got)
	}
}

func TestFindGaps_EmptyOrSingleIsNoGap(t *testing.T) {
	if got := findGaps(nil, ymd(2026, 7, 25)); len(got) != 0 {
		t.Errorf("empty: got %v", got)
	}
	if got := findGaps([]string{"2026-07-24"}, ymd(2026, 7, 25)); len(got) != 0 {
		t.Errorf("single: got %v", got)
	}
}

// A gap in an unbackfillable artifact is permanent data loss, so it must
// degrade the verdict even when the newest partition is perfectly fresh.
func TestBuildStatus_GapInUnbackfillableArtifactIsSurfaced(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/team-values/": {
			Objects:      3,
			LastModified: now.Add(-2 * time.Hour), // fresh
			Bytes:        900,
			Partitions:   []string{"2026-07-21", "2026-07-22", "2026-07-24"},
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.TeamValues}, now)

	if len(st.Artifacts) != 1 {
		t.Fatalf("got %d artifacts", len(st.Artifacts))
	}
	a := st.Artifacts[0]
	if a.Health != HealthGap {
		t.Errorf("health = %q, want %q — a gap must outrank freshness here", a.Health, HealthGap)
	}
	if len(a.Gaps) != 1 || a.Gaps[0] != "2026-07-23" {
		t.Errorf("gaps = %v, want [2026-07-23]", a.Gaps)
	}
	if !a.NoBackfill {
		t.Error("NoBackfill must reach the client so the UI can say the loss is permanent")
	}
}

// The Daily Archive joins the Team Value Store above: a missing day is not a
// job waiting to be re-run, it is a day of upstream state that no longer exists
// anywhere (rosterbot-0l31). Before the flag this row read as a plain gap and
// infra.js footed it "Re-runnable", which is the same lie the RecoveryInput work
// removed from the Analysis Store — worse here, because the suggested remedy
// (`archive --date <past>`) would have appeared to succeed while writing today's
// data under a historical partition.
//
// LostGaps stays empty on purpose and its absence is not a weaker signal: it
// splits a MIXED series into recoverable and lost days, whereas NoBackfill says
// every day in this artifact is lost, and infra.js branches on no_backfill
// first to render them all as "Gone for good".
func TestBuildStatus_GapInTheDailyArchiveIsPermanentNotRerunnable(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"archive/": {
			Objects:      45,
			LastModified: now.Add(-3 * time.Hour), // today's capture landed fine
			Bytes:        4096,
			Partitions:   []string{"2026-07-21", "2026-07-22", "2026-07-24"},
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.Archive}, now)

	a := st.Artifacts[0]
	if a.Health != HealthGap {
		t.Errorf("health = %q, want %q — the 07-23 capture is gone for good, which outranks a fresh newest partition",
			a.Health, HealthGap)
	}
	if len(a.Gaps) != 1 || a.Gaps[0] != "2026-07-23" {
		t.Errorf("gaps = %v, want [2026-07-23]", a.Gaps)
	}
	if !a.NoBackfill {
		t.Error("NoBackfill must reach the client, or infra.js foots the day \"Re-runnable\" and " +
			"sends the operator after a backfill that cannot exist")
	}
}

// Gaps are only computed for partitioned artifacts, and only flagged as a
// health problem where they cannot be re-run.
func TestBuildStatus_GapInBackfillableArtifactIsNotAHealthFailure(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/grades/": {
			Objects:      3,
			LastModified: now.Add(-2 * time.Hour),
			Partitions:   []string{"2026-07-21", "2026-07-24"},
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.Analysis}, now)

	a := st.Artifacts[0]
	if a.Health != HealthOK {
		t.Errorf("health = %q, want %q — grades can be re-graded, so a gap is not a fault", a.Health, HealthOK)
	}
	if len(a.Gaps) == 0 {
		t.Error("gaps should still be REPORTED for a partitioned artifact, just not fatal")
	}
}

// A listing failure for one artifact must not blank the whole page.
func TestBuildStatus_PerArtifactErrorIsIsolated(t *testing.T) {
	now := ymd(2026, 7, 25)
	lister := &fakeInfraLister{err: errors.New("s3 down")}

	st := buildStatus(context.Background(), lister, []layout.Artifact{layout.TeamValues, layout.Analysis}, now)

	if len(st.Artifacts) != 2 {
		t.Fatalf("got %d artifacts, want both present even on error", len(st.Artifacts))
	}
	for _, a := range st.Artifacts {
		if a.Health != HealthUnknown {
			t.Errorf("%s: health = %q, want %q", a.Name, a.Health, HealthUnknown)
		}
		if a.Error == "" {
			t.Errorf("%s: the error must be reported, not swallowed into a healthy-looking row", a.Name)
		}
	}
}

// The page must never present its own staleness as health: it reports when it
// was generated, and that is always "now" because it reads live.
func TestBuildStatus_StampsGeneratedAt(t *testing.T) {
	now := ymd(2026, 7, 25)
	st := buildStatus(context.Background(), &fakeInfraLister{}, nil, now)
	if !st.GeneratedAt.Time().Equal(now) {
		t.Errorf("GeneratedAt = %v, want %v", st.GeneratedAt, now)
	}
}

// --- handler ---

func TestHandleInfra_NotConfiguredReturns501(t *testing.T) {
	h := Handler(Config{Token: "t"})
	rec := do(t, h, http.MethodGet, "/v1/infra")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

func TestHandleInfra_ServesJSON(t *testing.T) {
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"lineup/": {Objects: 2, LastModified: time.Now(), Bytes: 400},
	}}
	h := Handler(Config{Token: "t", Infra: lister})

	rec := do(t, h, http.MethodGet, "/v1/infra")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var st InfraStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(st.Artifacts) != len(layout.All()) {
		t.Errorf("got %d artifacts, want %d (the whole layout table)", len(st.Artifacts), len(layout.All()))
	}
	if st.GeneratedAt.IsZero() {
		t.Error("GeneratedAt must be set so the client can prove the reading is live")
	}
}
