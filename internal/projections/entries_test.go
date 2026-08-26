package projections

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// The in-memory constructors exist so TestRun-style tests can substitute the
// FanGraphs load with fixture data while keeping the concrete
// *FanGraphsSource / *FanGraphsPitcherSource the pipeline consumes. The
// assertions below pin keying parity with the API/CSV builders: a lookup that
// works against a built source must work identically against an entries one.

func TestNewFanGraphsSourceFromEntries(t *testing.T) {
	src := NewFanGraphsSourceFromEntries([]SourceEntry{
		// Team deliberately lower-case: the API and CSV builders upper-case
		// before keying, and the entries constructor must match or a fixture
		// would silently miss on every lookup.
		{Name: "Aaron Judge", Team: "nyy", MLBAMID: 592450, Proj: Projection{G: 100, HR: 30}},
		{Name: "Cold Bat", Team: "BOS", Proj: Projection{G: 100, HR: 5}},
		{Name: "", Team: "TOR", Proj: Projection{G: 1}}, // empty name: skipped, like the builders
	})

	if got := src.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2 (empty-name entry must be skipped)", got)
	}

	proj, ok := src.GetProjection("Aaron Judge", "NYY")
	if !ok {
		t.Fatal("GetProjection(Aaron Judge, NYY) missed — entries keying diverges from the API builder")
	}
	if proj.HR != 30 {
		t.Fatalf("HR = %v, want 30", proj.HR)
	}

	ids := src.MLBAMIDs()
	if got := ids[NormalizeName("Aaron Judge")]; got != 592450 {
		t.Fatalf("MLBAMIDs[aaron judge] = %d, want 592450", got)
	}
	if _, recorded := ids[NormalizeName("Cold Bat")]; recorded {
		t.Fatal("a zero MLBAMID must not be recorded, matching the API builder")
	}

	if avg := src.AverageFPG(fantrax.ScoringWeights{"HR": 4}); avg <= 0 {
		t.Fatalf("AverageFPG = %v, want > 0", avg)
	}
}

func TestNewFanGraphsSourceFromEntries_DoesNotAliasCallerProjection(t *testing.T) {
	entry := SourceEntry{Name: "Aaron Judge", Team: "NYY", Proj: Projection{G: 100, HR: 30}}
	src := NewFanGraphsSourceFromEntries([]SourceEntry{entry})

	entry.Proj.HR = 99 // mutating the caller's copy must not reach the source

	proj, ok := src.GetProjection("Aaron Judge", "NYY")
	if !ok {
		t.Fatal("GetProjection missed")
	}
	if proj.HR != 30 {
		t.Fatalf("HR = %v, want 30 — source aliases the caller's Projection", proj.HR)
	}
}

func TestNewFanGraphsPitcherSourceFromEntries(t *testing.T) {
	src := NewFanGraphsPitcherSourceFromEntries([]PitcherSourceEntry{
		{Name: "Ace Starter", Team: "nyy", MLBAMID: 543037, Proj: PitcherProjection{G: 30, GS: 30, IP: 180, K: 200, FIP: 3.10}},
		{Name: "Steady Reliever", Team: "BOS", Proj: PitcherProjection{G: 60, IP: 65, K: 70}},
	})

	if got := src.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}

	proj, ok := src.GetPitcherProjection("Ace Starter", "NYY")
	if !ok {
		t.Fatal("GetPitcherProjection(Ace Starter, NYY) missed — entries keying diverges from the API builder")
	}
	if proj.K != 200 {
		t.Fatalf("K = %v, want 200", proj.K)
	}

	fip, league := src.PitcherInfo()
	if got := fip[NormalizeName("Ace Starter")]; got != 3.10 {
		t.Fatalf("PitcherInfo fip[ace starter] = %v, want 3.10", got)
	}
	if league != 3.10 {
		t.Fatalf("league avg FIP = %v, want 3.10 (only one FIP carrier)", league)
	}

	if got := src.MLBAMIDs()[NormalizeName("Ace Starter")]; got != 543037 {
		t.Fatalf("MLBAMIDs[ace starter] = %d, want 543037", got)
	}
}
