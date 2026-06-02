package waivers

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
)

// mockBatSrc returns canned hitter projections by normalized name.
type mockBatSrc map[string]*projections.Projection

func (m mockBatSrc) GetProjection(name, _ string) (*projections.Projection, bool) {
	p, ok := m[projections.NormalizeName(name)]
	return p, ok
}

// mockPitSrc returns canned pitcher projections by normalized name.
type mockPitSrc map[string]*projections.PitcherProjection

func (m mockPitSrc) GetPitcherProjection(name, _ string) (*projections.PitcherProjection, bool) {
	p, ok := m[projections.NormalizeName(name)]
	return p, ok
}

func TestFilterFreeAgents(t *testing.T) {
	pool := []models.PoolPlayer{
		{Name: "FA Hitter", FantasyStatus: "FA", Positions: []string{auth_client.PosOF}, MultiPositions: "OF"},
		{Name: "FA Pitcher", FantasyStatus: "FA", Positions: []string{auth_client.PosSP}, MultiPositions: "SP"},
		{Name: "Waiver Player", FantasyStatus: "W3", Positions: []string{auth_client.Pos1B}, MultiPositions: "1B"},
		{Name: "Owned", FantasyStatus: "TEAM1", Positions: []string{auth_client.PosOF}, MultiPositions: "OF"},
		{Name: "Minors FA", FantasyStatus: "FA", MinorsEligible: true, Positions: []string{auth_client.PosOF}, MultiPositions: "OF"},
		{Name: "Empty Status", FantasyStatus: "", Positions: []string{auth_client.PosOF}, MultiPositions: "OF"},
	}

	got := filterFreeAgents(pool, nil)
	wantNames := map[string]bool{
		"FA Hitter":     true,
		"FA Pitcher":    true,
		"Waiver Player": true,
		"Empty Status":  true,
	}
	if len(got) != len(wantNames) {
		t.Fatalf("filterFreeAgents: want %d, got %d (%v)", len(wantNames), len(got), got)
	}
	for _, p := range got {
		if !wantNames[p.Name] {
			t.Errorf("unexpected FA: %s", p.Name)
		}
	}
}

func TestFilterFreeAgents_PositionFilter(t *testing.T) {
	pool := []models.PoolPlayer{
		{Name: "OF Player", FantasyStatus: "FA", Positions: []string{auth_client.PosOF}, MultiPositions: "OF"},
		{Name: "1B Player", FantasyStatus: "FA", Positions: []string{auth_client.Pos1B}, MultiPositions: "1B"},
		{Name: "SP Player", FantasyStatus: "FA", Positions: []string{auth_client.PosSP}, MultiPositions: "SP"},
	}

	got := filterFreeAgents(pool, []string{"OF", "SP"})
	if len(got) != 2 {
		t.Fatalf("position filter: want 2, got %d", len(got))
	}
}

func TestIsSPEligible(t *testing.T) {
	if !isSPEligible([]string{auth_client.PosSP}) {
		t.Error("SP-only should be SP eligible")
	}
	if !isSPEligible([]string{auth_client.PosSP, auth_client.PosRP}) {
		t.Error("SP+RP should be SP eligible")
	}
	if isSPEligible([]string{auth_client.PosRP}) {
		t.Error("RP-only should not be SP eligible")
	}
	if isSPEligible([]string{auth_client.PosOF}) {
		t.Error("OF should not be SP eligible")
	}
}

func TestIsHitterEligible(t *testing.T) {
	if !isHitterEligible([]string{auth_client.PosOF}) {
		t.Error("OF should be hitter eligible")
	}
	if !isHitterEligible([]string{auth_client.Pos1B, auth_client.PosUtil}) {
		t.Error("1B+UT should be hitter eligible")
	}
	if isHitterEligible([]string{auth_client.PosSP}) {
		t.Error("SP-only should not be hitter eligible")
	}
	if isHitterEligible([]string{auth_client.PosSP, auth_client.PosRP}) {
		t.Error("SP+RP should not be hitter eligible")
	}
}

func TestStripHTMLTags(t *testing.T) {
	if got := stripHTMLTags("<b>OF</b>,1B"); got != "OF,1B" {
		t.Errorf("stripHTMLTags: got %q", got)
	}
}

func TestBuildCandidates_SortAndIdempotent(t *testing.T) {
	freeAgents := []models.PoolPlayer{
		{Name: "Soto Juan", FantasyStatus: "FA", Positions: []string{auth_client.PosOF}, MultiPositions: "OF", MLBTeamShortName: "NYY"},
		{Name: "Strider Spencer", FantasyStatus: "FA", Positions: []string{auth_client.PosSP}, MultiPositions: "SP", MLBTeamShortName: "ATL"},
		{Name: "Buylow Pitcher", FantasyStatus: "FA", Positions: []string{auth_client.PosSP}, MultiPositions: "SP", MLBTeamShortName: "TEX"},
	}
	mlbamByName := map[string]int{
		projections.NormalizeName("Soto Juan"):       665742,
		projections.NormalizeName("Strider Spencer"): 675911,
		projections.NormalizeName("Buylow Pitcher"):  888001,
	}
	savant := &SavantBundle{
		HitterExp: map[int]SavantHitterRow{
			665742: {MLBAMID: 665742, PA: 200, WOBA: 0.310, XwOBA: 0.360},
		},
		HitterSC: map[int]SavantHitterStatcastRow{
			665742: {MLBAMID: 665742, Barrel: 12.0, HardHit: 46.0},
		},
		HitterExp14d: map[int]SavantHitterRow{},
		PitcherExp: map[int]SavantPitcherRow{
			675911: {MLBAMID: 675911, PA: 150, ERA: 3.30, XERA: 3.40, XwOBA: 0.310},
			888001: {MLBAMID: 888001, PA: 150, ERA: 4.50, XERA: 3.20, XwOBA: 0.290},
		},
		PitcherExp30d: map[int]SavantPitcherRow{
			675911: {MLBAMID: 675911, PA: 60, ERA: 2.80, XERA: 3.10},
		},
	}
	bat := mockBatSrc{
		projections.NormalizeName("Soto Juan"): {
			G: 150, H: 160, Doubles: 30, Triples: 2, HR: 35,
			RBI: 100, R: 105, BB: 90, SB: 5, HBP: 5, SO: 130,
		},
	}
	pit := mockPitSrc{
		projections.NormalizeName("Strider Spencer"): {
			G: 32, GS: 32, IP: 200, K: 250, BBA: 50, ER: 70, W: 17, L: 8, QS: 22, HRA: 18,
		},
		projections.NormalizeName("Buylow Pitcher"): {
			G: 30, GS: 30, IP: 175, K: 180, BBA: 55, ER: 75, W: 12, L: 10, QS: 15, HRA: 22,
		},
	}
	hitterScoring := fantrax.ScoringWeights{
		"1B": 1, "2B": 2, "3B": 3, "HR": 4, "RBI": 1, "R": 1, "BB": 1, "SB": 2, "SO": -0.5, "HBP": 1,
	}
	pitcherScoring := fantrax.ScoringWeights{
		"K": 2, "W": 5, "QS": 4, "ER": -1, "IP": 1, "BB": -0.5, "H": -0.5, "HR": -2,
	}

	got := buildCandidates(freeAgents, mlbamByName, savant, bat, pit, hitterScoring, pitcherScoring, rosteredPlayer{}, rosteredPlayer{}, DefaultThresholds())
	if len(got) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(got))
	}

	// Run twice — output must be byte-identical except for floating-point
	// summation noise inside ExpectedPts*FromProj (which iterates a Go map).
	// What matters for idempotency is the same MLBAM IDs in the same order
	// with ProjectedFPG within a tiny epsilon.
	got2 := buildCandidates(freeAgents, mlbamByName, savant, bat, pit, hitterScoring, pitcherScoring, rosteredPlayer{}, rosteredPlayer{}, DefaultThresholds())
	if len(got) != len(got2) {
		t.Fatalf("idempotency: lengths differ %d vs %d", len(got), len(got2))
	}
	for i := range got {
		if got[i].MLBAMID != got2[i].MLBAMID {
			t.Errorf("idx %d: MLBAMID drift %d vs %d", i, got[i].MLBAMID, got2[i].MLBAMID)
		}
		if math.Abs(got[i].ProjectedFPG-got2[i].ProjectedFPG) > 1e-9 {
			t.Errorf("idx %d: ProjectedFPG drift %v vs %v", i, got[i].ProjectedFPG, got2[i].ProjectedFPG)
		}
	}
}

func TestBuildCandidates_SkipsWithoutMLBAMID(t *testing.T) {
	freeAgents := []models.PoolPlayer{
		{Name: "Unknown Player", FantasyStatus: "FA", Positions: []string{auth_client.PosOF}, MultiPositions: "OF"},
	}
	got := buildCandidates(freeAgents, map[string]int{}, &SavantBundle{}, mockBatSrc{}, mockPitSrc{},
		fantrax.ScoringWeights{}, fantrax.ScoringWeights{}, rosteredPlayer{}, rosteredPlayer{}, DefaultThresholds())
	if len(got) != 0 {
		t.Fatalf("want 0 candidates without MLBAM ID, got %d", len(got))
	}
}

func TestBuildCandidates_SkipsWithoutProjection(t *testing.T) {
	freeAgents := []models.PoolPlayer{
		{Name: "Has Signal", FantasyStatus: "FA", Positions: []string{auth_client.PosOF}, MultiPositions: "OF"},
	}
	mlbamByName := map[string]int{projections.NormalizeName("Has Signal"): 1}
	savant := &SavantBundle{
		HitterExp: map[int]SavantHitterRow{
			1: {MLBAMID: 1, PA: 200, WOBA: 0.310, XwOBA: 0.360},
		},
		HitterSC: map[int]SavantHitterStatcastRow{
			1: {MLBAMID: 1, Barrel: 12.0, HardHit: 46.0},
		},
	}
	// No projection in mockBatSrc → candidate dropped.
	got := buildCandidates(freeAgents, mlbamByName, savant, mockBatSrc{}, mockPitSrc{},
		fantrax.ScoringWeights{}, fantrax.ScoringWeights{}, rosteredPlayer{}, rosteredPlayer{}, DefaultThresholds())
	if len(got) != 0 {
		t.Fatalf("want 0 candidates without projection, got %d", len(got))
	}
}

func TestPushoverFitsLimit(t *testing.T) {
	// Force 15 candidates with long-ish names; ensure formatPushover stays under 1024.
	r := Report{Total: 15}
	for i := 0; i < 15; i++ {
		r.Top = append(r.Top, Candidate{
			Name:         "Reasonable LongName",
			MLBTeam:      "TEAM",
			Position:     "OF",
			Signal:       SignalBuyLow,
			BuyLowDelta:  0.045,
			Barrel:       12.5,
			HardHit:      45,
			ProjectedFPG: 6.5,
		})
	}
	msg := formatPushover(r)
	if len(msg) > 1024 {
		t.Fatalf("formatPushover exceeded 1024 chars: %d", len(msg))
	}
	if msg == "" {
		t.Fatal("formatPushover returned empty")
	}
}

func TestPushoverFormat_TwoLineStructure(t *testing.T) {
	r := Report{
		Date:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Total: 2,
		Top: []Candidate{
			{Signal: SignalHot, Name: "Nathaniel Lowe", MLBTeam: "CIN", DropName: "Colt Emerson",
				Gap: 0.77, HotHitter: HotHitterMetrics{Window14dWOBA: 0.387}, Barrel: 14.1, HardHit: 46},
			{Signal: SignalBuyLow, Name: "Tyler Stephenson", MLBTeam: "CIN", DropName: "Colt Emerson",
				Gap: 0.09, BuyLowDelta: 0.046, Barrel: 12.0, HardHit: 44},
		},
	}
	msg := formatPushover(r)
	if len(msg) > 1024 {
		t.Fatalf("exceeded 1024 chars: %d", len(msg))
	}
	for _, want := range []string{"🔥", "📉", "<b>N. Lowe</b>", "<b>T. Stephenson</b>", "+0.77 FPG", "xwOBA"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	// Action line and stat line must be separated by a newline for each candidate.
	if strings.Count(msg, "\n") < 4 {
		t.Errorf("expected at least 4 newlines for 2-line-per-candidate format, got:\n%s", msg)
	}
}

func TestBuildCandidates_GapFilter(t *testing.T) {
	// Two FAs both pass the BUY-LOW signal. One projects above the drop, one below.
	// The one below the drop must be filtered out.
	freeAgents := []models.PoolPlayer{
		{Name: "Better FA", FantasyStatus: "FA", Positions: []string{auth_client.PosOF}, MultiPositions: "OF", MLBTeamShortName: "BOS"},
		{Name: "Worse FA", FantasyStatus: "FA", Positions: []string{auth_client.PosOF}, MultiPositions: "OF", MLBTeamShortName: "PIT"},
	}
	mlbamByName := map[string]int{
		projections.NormalizeName("Better FA"): 100,
		projections.NormalizeName("Worse FA"):  101,
	}
	savant := &SavantBundle{
		HitterExp: map[int]SavantHitterRow{
			100: {MLBAMID: 100, PA: 200, WOBA: 0.310, XwOBA: 0.360},
			101: {MLBAMID: 101, PA: 200, WOBA: 0.310, XwOBA: 0.360},
		},
		HitterSC: map[int]SavantHitterStatcastRow{
			100: {MLBAMID: 100, Barrel: 12.0, HardHit: 46.0},
			101: {MLBAMID: 101, Barrel: 12.0, HardHit: 46.0},
		},
	}
	bat := mockBatSrc{
		// Better FA projects ~5 FPG, Worse FA projects ~2 FPG.
		projections.NormalizeName("Better FA"): {G: 100, H: 100, HR: 25, RBI: 80, R: 80, BB: 60, SO: 100},
		projections.NormalizeName("Worse FA"):  {G: 100, H: 50, HR: 5, RBI: 30, R: 30, BB: 25, SO: 80},
	}
	hitterScoring := fantrax.ScoringWeights{"1B": 1, "2B": 2, "HR": 4, "RBI": 1, "R": 1, "BB": 1, "SO": -0.5}

	// Drop FPG = 3.0 — Better FA beats it, Worse FA doesn't.
	drop := rosteredPlayer{Name: "Bench Player", FPG: 3.0}
	got := buildCandidates(freeAgents, mlbamByName, savant, bat, mockPitSrc{},
		hitterScoring, fantrax.ScoringWeights{}, drop, rosteredPlayer{}, DefaultThresholds())
	if len(got) != 1 {
		t.Fatalf("want 1 candidate (Better FA only), got %d", len(got))
	}
	if got[0].Name != "Better FA" {
		t.Errorf("want Better FA, got %q", got[0].Name)
	}
	if got[0].DropName != "Bench Player" {
		t.Errorf("DropName not set: %q", got[0].DropName)
	}
	if got[0].Gap <= 0 {
		t.Errorf("Gap should be positive, got %v", got[0].Gap)
	}
}

func TestWorstRosteredHitter(t *testing.T) {
	roster := []fantrax.Player{
		{Name: "Big Star", MLBTeam: "NYY"},
		{Name: "Bench Guy", MLBTeam: "OAK"},
		{Name: "Injured Guy", MLBTeam: "NYM", IsInjured: true},
		{Name: "Minors Guy", MLBTeam: "PHI", InMinors: true},
	}
	bat := mockBatSrc{
		projections.NormalizeName("Big Star"):    {G: 100, H: 150, HR: 30, RBI: 90, R: 90, BB: 60, SO: 80},
		projections.NormalizeName("Bench Guy"):   {G: 100, H: 50, HR: 3, RBI: 25, R: 25, BB: 20, SO: 100},
		projections.NormalizeName("Injured Guy"): {G: 100, H: 30, HR: 1, RBI: 10, R: 10, BB: 10, SO: 50},
	}
	scoring := fantrax.ScoringWeights{"1B": 1, "HR": 4, "RBI": 1, "R": 1, "BB": 1, "SO": -0.5}

	got := worstRosteredHitter(roster, bat, scoring)
	if got.Name != "Bench Guy" {
		t.Errorf("want Bench Guy as worst, got %q (FPG %.2f)", got.Name, got.FPG)
	}
}

func TestShortName(t *testing.T) {
	if got := shortName("Juan Soto"); got != "J. Soto" {
		t.Errorf("shortName: got %q", got)
	}
	if got := shortName("Madonna"); got != "Madonna" {
		t.Errorf("single-name shortName: got %q", got)
	}
	if got := shortName("Vladimir Guerrero Jr"); got != "V. Guerrero Jr" {
		t.Errorf("multi-word: got %q", got)
	}
}
