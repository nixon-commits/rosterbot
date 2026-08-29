package lineuprun

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// TestRun_AnnouncesGSTrackingDisabled pins the notice the disabled path owes
// the reader, and it is deliberately driven through Run rather than through a
// helper: the whole defect is that a branch in Run says nothing, so a test that
// called anything smaller could not observe it.
//
// The inputs are the SAME short week as TestRun_RaisesTheGSFloorAlert — four
// starts banked against a ten-start minimum, two days on which nobody of ours
// plays — with one difference, GSTrackingEnabled false. That framing is what
// makes the assertion mean something: this is a run that WOULD have alerted,
// so a silent output here is not "nothing to report", it is a cap, a floor and
// an alert dropped without a word.
func TestRun_AnnouncesGSTrackingDisabled(t *testing.T) {
	today := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	weekEnd := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	gsMin, gsMax := 10, 12

	ft := &fakeLineupClient{
		hitters: []fantrax.Player{
			{ID: "h1", Name: "Only Bat", MLBTeam: "NYY", Positions: []string{"012"}, Status: "Active", RosterPosition: "014"},
		},
		pitchers: []fantrax.Player{
			{ID: "p1", Name: "Lone Starter", MLBTeam: "BOS", Positions: []string{"015"}, PosShortNames: "SP", Status: "Active", RosterPosition: "017"},
		},
		seasonStart: time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		seasonEnd:   time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		period:      155,

		weekStart: weekStart,
		weekEnd:   weekEnd,
		periods: []fantrax.ScoringPeriod{
			{Number: 21, StartDate: weekStart, EndDate: weekEnd},
		},
		gsMin:  &gsMin,
		gsMax:  &gsMax,
		usedGS: 4,
	}

	bat := projections.NewFanGraphsSourceFromEntries([]projections.SourceEntry{
		{Name: "Only Bat", Team: "NYY", Proj: projections.Projection{G: 100, HR: 20}},
	})
	pit := projections.NewFanGraphsPitcherSourceFromEntries([]projections.PitcherSourceEntry{
		{Name: "Lone Starter", Team: "BOS", Proj: projections.PitcherProjection{G: 30, IP: 180, K: 200}},
	})
	sched := &fakeDateSchedule{
		fakeSchedule: fakeSchedule{
			playing: map[string]map[string]bool{
				today.Format("2006-01-02"): {"NYY": true, "BOS": true},
				"2026-08-29":               {"BOS": true},
				"2026-08-30":               {"BOS": true},
			},
		},
	}

	markers := newFakeMarkers()
	cfg := &config.Config{
		LeagueID: "lg1", TeamID: "team1",
		DryRun: false, AutoApply: true,
		GSTrackingEnabled: false,
		Dates:             []time.Time{today},
	}
	var out bytes.Buffer
	opts := withFakeDeps(Options{
		Today:            today,
		ProjectionSystem: "depthcharts",
		GSFloorMarkers:   markers,
		Out:              &out,
	}, bat, pit, sched)

	if _, err := Run(context.Background(), ft, cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()

	// The gate must keep disabling the phase — this bead is about announcing,
	// not enabling. Asserted first so a fix that "fixes" the silence by turning
	// the phase on fails here rather than passing on the notice alone.
	if strings.Contains(got, "gs floor check:") || strings.Contains(got, "GS Floor Risk") {
		t.Fatalf("the disabled gate ran the GS phase anyway; the flag must keep defaulting off.\n%s", got)
	}
	if len(markers.getKeys) != 0 {
		t.Fatalf("floor dedup consulted with tracking disabled: %v", markers.getKeys)
	}

	// The notice itself. The "GS tracking disabled (GS_TRACKING_ENABLED" prefix
	// is shared with cmd/gs_check.go so the two consumers of this flag are
	// greppable together; the tail diverges deliberately, because envBool
	// swallows a ParseBool error and "not set" would be a false cause.
	if !strings.Contains(got, "GS tracking disabled (GS_TRACKING_ENABLED unset or not truthy)") {
		t.Fatalf("Run dropped the GS cap, floor and floor alert without a word — "+
			"indistinguishable from a healthy run, and from the fail-open cascade that "+
			"at least logs a WARNING.\n%s", got)
	}
}
