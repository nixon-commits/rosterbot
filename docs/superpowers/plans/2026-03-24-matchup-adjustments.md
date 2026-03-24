# Matchup Adjustments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add platoon split and opposing pitcher quality adjustments to hitter projections, plus fix the XBH park factor weighting bug.

**Architecture:** A new `MatchupAdjustedSource` wrapper sits after `ParkAdjustedSource` in the projection chain, applying two independent multipliers (platoon + pitcher quality) to hitter pts/game. Handedness and FIP data are sourced from the existing FanGraphs API calls with no additional network requests.

**Tech Stack:** Go, FanGraphs JSON API (existing), MLB Stats API (existing `ProbableStarters`)

**Spec:** `docs/superpowers/specs/2026-03-24-matchup-adjustments-design.md`

---

### Task 1: Fix XBH Park Factor Weighting

**Files:**
- Modify: `internal/projections/park_adjusted.go:124`
- Modify: `internal/projections/park_adjusted_test.go:10-33`

- [ ] **Step 1: Update the XBH park factor test to assert a specific value**

In `internal/projections/park_adjusted_test.go`, update `TestParkAdjustedSource_CoorsBoost` to add XBH to the scoring weights and assert a specific adjusted value. The test player has Doubles=20, Triples=5, HR=15. With Coors factors (H2B=1.19, H3B=2.02, HR=1.06):

Player-weighted XBH factor = `(20*1.19 + 5*2.02 + 15*1.06) / (20+5+15)` = `(23.8 + 10.1 + 15.9) / 40` = `1.245`

The old simple average would be `(1.19 + 2.02 + 1.06) / 3` = `1.4233`

Add `"XBH": 1.0` and `"TB": 1.0` to the scoring weights. Compute the expected adjusted value and assert it precisely. The test player has Doubles=20, Triples=5, HR=15. With Coors factors (H2B=1.19, H3B=2.02, HR=1.06), the player-weighted XBH factor is `(20*1.19 + 5*2.02 + 15*1.06) / 40 = 1.245`.

```go
// In TestParkAdjustedSource_CoorsBoost, update scoring and add a specific numeric assertion:
scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "2B": 2.0, "3B": 3.0, "R": 1.0, "RBI": 1.0, "BB": 1.0, "SO": -1.0, "XBH": 1.0, "TB": 1.0}

// After the existing directional check (adjPts > basePts), add:
// Compute expected value to verify player-weighted XBH factor.
// Re-run with the old simple-average formula: (1.19 + 2.02 + 1.06)/3 = 1.4233
// vs new player-weighted: (20*1.19 + 5*2.02 + 15*1.06)/40 = 1.245
// If the old formula were used, adjPts would be higher. Assert that adjPts
// matches the player-weighted calculation by computing the expected value
// from the scoring weights and park factors manually, then comparing.
```

The key assertion: after the fix, run the test with a known `basePts` and verify `adjPts` matches `basePts * expectedAdjustment` within epsilon, where the adjustment uses the player-weighted XBH factor (1.245) not the simple average (1.4233).

- [ ] **Step 2: Run the test to verify it fails with the old code**

Run: `go test ./internal/projections/ -run TestParkAdjustedSource_CoorsBoost -v`
Expected: FAIL — the specific numeric assertion should fail with the old simple-average formula

- [ ] **Step 3: Fix the XBH park factor line**

In `internal/projections/park_adjusted.go`, replace line 124:

```go
// Before:
"XBH": (pf.H2B + pf.H3B + pf.HR) / 3.0,

// After — initialize with fallback, then override if we have projection data:
"XBH": 1.0,
```

Then after the TB block (after line 131), add:

```go
if xbh > 0 {
    statFactor["XBH"] = (proj.Doubles*pf.H2B + proj.Triples*pf.H3B + proj.HR*pf.HR) / xbh
}
```

- [ ] **Step 4: Run all park factor tests**

Run: `go test ./internal/projections/ -run TestParkAdjusted -v`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add internal/projections/park_adjusted.go internal/projections/park_adjusted_test.go
git commit -m "fix: use player-weighted XBH park factor instead of simple average"
```

---

### Task 2: Add Bats Field to Hitter Projections

**Files:**
- Modify: `internal/projections/fangraphs.go:18-34,41-59,91-107`
- Modify: `internal/projections/fangraphs_test.go` (if fixture needs updating)

- [ ] **Step 1: Write a test for HitterBats accessor**

Add to `internal/projections/fangraphs_test.go`:

```go
func TestFanGraphsSource_HitterBats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"PlayerName": "Aaron Judge", "Team": "NYY", "G": 150.0, "PA": 600.0, "H": 160.0,
				"1B": 80.0, "2B": 30.0, "3B": 2.0, "HR": 48.0, "RBI": 120.0, "R": 100.0,
				"BB": 90.0, "SB": 5.0, "CS": 2.0, "HBP": 10.0, "SO": 170.0, "GDP": nil,
				"Bats": "R"},
			{"PlayerName": "Juan Soto", "Team": "NYM", "G": 155.0, "PA": 650.0, "H": 165.0,
				"1B": 90.0, "2B": 32.0, "3B": 1.0, "HR": 35.0, "RBI": 100.0, "R": 110.0,
				"BB": 120.0, "SB": 3.0, "CS": 1.0, "HBP": 8.0, "SO": 130.0, "GDP": 10.0,
				"Bats": "L"},
			{"PlayerName": "Ozzie Albies", "Team": "ATL", "G": 140.0, "PA": 580.0, "H": 150.0,
				"1B": 85.0, "2B": 30.0, "3B": 5.0, "HR": 25.0, "RBI": 80.0, "R": 85.0,
				"BB": 35.0, "SB": 15.0, "CS": 5.0, "HBP": 3.0, "SO": 100.0, "GDP": 12.0,
				"Bats": "B"},
		}))
	}))
	defer srv.Close()

	old := fangraphsBattingURL
	fangraphsBattingURL = srv.URL
	defer func() { fangraphsBattingURL = old }()

	src, err := NewFanGraphsSource()
	if err != nil {
		t.Fatal(err)
	}

	bats := src.HitterBats()
	if bats["aaron judge"] != "R" {
		t.Errorf("Judge: got %q, want R", bats["aaron judge"])
	}
	if bats["juan soto"] != "L" {
		t.Errorf("Soto: got %q, want L", bats["juan soto"])
	}
	if bats["ozzie albies"] != "S" {
		t.Errorf("Albies: got %q, want S (normalized from B)", bats["ozzie albies"])
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/projections/ -run TestFanGraphsSource_HitterBats -v`
Expected: FAIL — `HitterBats` method does not exist

- [ ] **Step 3: Add Bats field to structs and HitterBats accessor**

In `internal/projections/fangraphs.go`:

Add `Bats string` field to the `Projection` struct (after `GIDP`):
```go
type Projection struct {
	// ... existing fields ...
	GIDP    float64
	Bats    string // "R", "L", or "S" (switch)
}
```

Add `Bats string \`json:"Bats"\`` to `fgRow` (after `GIDP`):
```go
type fgRow struct {
	// ... existing fields ...
	GIDP       float64 `json:"GDP"`
	Bats       string  `json:"Bats"`
}
```

Populate in `NewFanGraphsSource` parsing loop (after line 106, `GIDP: row.GIDP`):
```go
Bats: row.Bats,
```

Add the `HitterBats` accessor:
```go
// HitterBats returns a map of NormalizeName(name) → bat side ("R", "L", "S").
// Normalizes "B" (both) to "S" (switch).
func (s *FanGraphsSource) HitterBats() map[string]string {
	bats := make(map[string]string, len(s.projections))
	for key, proj := range s.projections {
		name := strings.SplitN(key, "|", 2)[0] // extract name from "name|team" key
		b := strings.ToUpper(proj.Bats)
		if b == "B" {
			b = "S"
		}
		if b == "R" || b == "L" || b == "S" {
			bats[name] = b
		}
	}
	return bats
}
```

Note: The map is keyed by `NormalizeName(name)` since `projKey` already normalizes the name portion.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/projections/ -run TestFanGraphsSource_HitterBats -v`
Expected: PASS

- [ ] **Step 5: Run all existing fangraphs tests to ensure no regressions**

Run: `go test ./internal/projections/ -run TestFanGraphs -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/projections/fangraphs.go internal/projections/fangraphs_test.go
git commit -m "feat: add Bats field to hitter projections from FanGraphs"
```

---

### Task 3: Add Throws and FIP Fields to Pitcher Projections

**Files:**
- Modify: `internal/projections/pitcher_fangraphs.go:15-36,43-66,91-108`
- Create/modify: pitcher fangraphs test file

- [ ] **Step 1: Write a test for PitcherInfo accessor**

Add to pitcher test file (create `internal/projections/pitcher_fangraphs_test.go` if it doesn't exist):

```go
package projections

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFanGraphsPitcherSource_PitcherInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"PlayerName": "Gerrit Cole", "Team": "NYY", "G": 30.0, "GS": 30.0, "IP": 190.0,
				"SO": 220.0, "BB": 45.0, "H": 150.0, "ER": 60.0, "HR": 20.0,
				"W": 14.0, "L": 7.0, "QS": 18.0, "SV": 0.0, "HLD": 0.0, "BS": 0.0,
				"HBP": 5.0, "WP": 3.0, "BK": 0.0, "CG": 1.0, "SHO": 0.0, "PKO": 1.0,
				"Throws": "R", "FIP": 2.80},
			{"PlayerName": "Yusei Kikuchi", "Team": "LAA", "G": 28.0, "GS": 28.0, "IP": 160.0,
				"SO": 180.0, "BB": 55.0, "H": 140.0, "ER": 70.0, "HR": 22.0,
				"W": 10.0, "L": 9.0, "QS": 14.0, "SV": 0.0, "HLD": 0.0, "BS": 0.0,
				"HBP": 4.0, "WP": 5.0, "BK": 1.0, "CG": 0.0, "SHO": 0.0, "PKO": 0.0,
				"Throws": "L", "FIP": 4.20},
		}))
	}))
	defer srv.Close()

	old := fangraphsPitchingURL
	fangraphsPitchingURL = srv.URL
	defer func() { fangraphsPitchingURL = old }()

	src, err := NewFanGraphsPitcherSource()
	if err != nil {
		t.Fatal(err)
	}

	handedness, fip, avgFIP := src.PitcherInfo()

	if handedness["gerrit cole"] != "R" {
		t.Errorf("Cole handedness: got %q, want R", handedness["gerrit cole"])
	}
	if handedness["yusei kikuchi"] != "L" {
		t.Errorf("Kikuchi handedness: got %q, want L", handedness["yusei kikuchi"])
	}
	if math.Abs(fip["gerrit cole"]-2.80) > 0.01 {
		t.Errorf("Cole FIP: got %.2f, want 2.80", fip["gerrit cole"])
	}
	// IP-weighted avg: (190*2.80 + 160*4.20) / (190+160) = (532 + 672) / 350 = 3.44
	wantAvg := (190.0*2.80 + 160.0*4.20) / (190.0 + 160.0)
	if math.Abs(avgFIP-wantAvg) > 0.01 {
		t.Errorf("avgFIP: got %.2f, want %.2f", avgFIP, wantAvg)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/projections/ -run TestFanGraphsPitcherSource_PitcherInfo -v`
Expected: FAIL — `PitcherInfo` method does not exist

- [ ] **Step 3: Add Throws and FIP fields to structs and PitcherInfo accessor**

In `internal/projections/pitcher_fangraphs.go`:

Add to `PitcherProjection` struct (after `PKO`):
```go
Throws string  // "R" or "L"
FIP    float64 // Fielding Independent Pitching
```

Add to `fgPitchRow` struct (after `PKO`):
```go
Throws string  `json:"Throws"`
FIP    float64 `json:"FIP"`
```

Populate in `NewFanGraphsPitcherSource` parsing (after `PKO: row.PKO`):
```go
Throws: row.Throws,
FIP:    row.FIP,
```

Add the `PitcherInfo` accessor:
```go
// PitcherInfo returns pitcher handedness, FIP, and IP-weighted league average FIP.
func (s *FanGraphsPitcherSource) PitcherInfo() (handedness map[string]string, fip map[string]float64, leagueAvgFIP float64) {
	handedness = make(map[string]string, len(s.projections))
	fip = make(map[string]float64, len(s.projections))
	var totalFIPxIP, totalIP float64
	for key, proj := range s.projections {
		name := strings.SplitN(key, "|", 2)[0]
		if t := strings.ToUpper(proj.Throws); t == "R" || t == "L" {
			handedness[name] = t
		}
		if proj.FIP > 0 {
			fip[name] = proj.FIP
		}
		if proj.IP > 0 && proj.FIP > 0 {
			totalFIPxIP += proj.FIP * proj.IP
			totalIP += proj.IP
		}
	}
	if totalIP > 0 {
		leagueAvgFIP = totalFIPxIP / totalIP
	}
	return
}
```

Note: Need to add `"strings"` to imports if not already present.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/projections/ -run TestFanGraphsPitcherSource_PitcherInfo -v`
Expected: PASS

- [ ] **Step 5: Run all projections tests**

Run: `go test ./internal/projections/ -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/projections/pitcher_fangraphs.go internal/projections/pitcher_fangraphs_test.go
git commit -m "feat: add Throws and FIP fields to pitcher projections from FanGraphs"
```

---

### Task 4: Implement MatchupAdjustedSource

**Files:**
- Create: `internal/projections/matchup_adjusted.go`
- Create: `internal/projections/matchup_adjusted_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/projections/matchup_adjusted_test.go`:

```go
package projections

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// stubPPSSource implements both Source and PtsPerGameSource for testing.
type stubPPSSource struct {
	proj map[string]*Projection
	pts  map[string]float64 // NormalizeName → pts/game
}

func (s *stubPPSSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	p, ok := s.proj[NormalizeName(name)]
	return p, ok
}

func (s *stubPPSSource) GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	pts, ok := s.pts[NormalizeName(name)]
	return pts, ok
}

func TestMatchupAdjusted_FavorablePlatoon(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	opp := map[string]OpposingPitcher{"NYY": {Name: "some pitcher", Throws: "L", FIP: 4.00}}
	bats := map[string]string{"test player": "R"} // RHH vs LHP = favorable
	src := NewMatchupAdjustedSource(inner, opp, bats, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	// Favorable platoon (1.00) * neutral quality (4.00/4.00=1.00) = 1.00
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("favorable platoon: got %.4f, want 5.00", pts)
	}
}

func TestMatchupAdjusted_UnfavorablePlatoon(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	opp := map[string]OpposingPitcher{"NYY": {Name: "some pitcher", Throws: "L", FIP: 4.00}}
	bats := map[string]string{"test player": "L"} // LHH vs LHP = unfavorable
	src := NewMatchupAdjustedSource(inner, opp, bats, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	// Unfavorable platoon (0.93) * neutral quality (1.00) = 0.93
	want := 5.00 * 0.93
	if math.Abs(pts-want) > 0.001 {
		t.Errorf("unfavorable platoon: got %.4f, want %.4f", pts, want)
	}
}

func TestMatchupAdjusted_SwitchHitter(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	opp := map[string]OpposingPitcher{"NYY": {Name: "some pitcher", Throws: "L", FIP: 4.00}}
	bats := map[string]string{"test player": "S"}
	src := NewMatchupAdjustedSource(inner, opp, bats, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("switch hitter: got %.4f, want 5.00", pts)
	}
}

func TestMatchupAdjusted_AceSuppression(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	// Ace with FIP 2.80 vs league avg 4.00
	opp := map[string]OpposingPitcher{"NYY": {Name: "ace", Throws: "L", FIP: 2.80}}
	bats := map[string]string{"test player": "R"} // favorable platoon
	src := NewMatchupAdjustedSource(inner, opp, bats, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	// Platoon 1.00 * quality clamp(2.80/4.00=0.70, 0.85, 1.15) = 0.85
	want := 5.00 * 0.85
	if math.Abs(pts-want) > 0.001 {
		t.Errorf("ace suppression: got %.4f, want %.4f", pts, want)
	}
}

func TestMatchupAdjusted_BadPitcherBoost(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	opp := map[string]OpposingPitcher{"NYY": {Name: "bad", Throws: "R", FIP: 5.50}}
	bats := map[string]string{"test player": "L"} // favorable platoon
	src := NewMatchupAdjustedSource(inner, opp, bats, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	// Platoon 1.00 * quality clamp(5.50/4.00=1.375, 0.85, 1.15) = 1.15
	want := 5.00 * 1.15
	if math.Abs(pts-want) > 0.001 {
		t.Errorf("bad pitcher boost: got %.4f, want %.4f", pts, want)
	}
}

func TestMatchupAdjusted_CombinedCap(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	// Unfavorable platoon + ace = 0.93 * 0.85 = 0.7905 → capped at 0.80
	opp := map[string]OpposingPitcher{"NYY": {Name: "ace", Throws: "L", FIP: 2.80}}
	bats := map[string]string{"test player": "L"} // LHH vs LHP = unfavorable
	src := NewMatchupAdjustedSource(inner, opp, bats, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	want := 5.00 * 0.80 // combined floor
	if math.Abs(pts-want) > 0.001 {
		t.Errorf("combined cap: got %.4f, want %.4f", pts, want)
	}
}

func TestMatchupAdjusted_NoOpposingPitcher(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	// No entry for NYY in opposingPitchers
	src := NewMatchupAdjustedSource(inner, map[string]OpposingPitcher{}, map[string]string{"test player": "R"}, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("no opposing pitcher: got %.4f, want 5.00", pts)
	}
}

func TestMatchupAdjusted_UnknownHitterHandedness(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	opp := map[string]OpposingPitcher{"NYY": {Name: "some pitcher", Throws: "L", FIP: 4.00}}
	// No entry for test player in hitterBats
	src := NewMatchupAdjustedSource(inner, opp, map[string]string{}, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("unknown handedness: got %.4f, want 5.00", pts)
	}
}

func TestMatchupAdjusted_TeamNotPlaying(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	// Opposing pitchers exist but not for BOS
	opp := map[string]OpposingPitcher{"NYY": {Name: "some pitcher", Throws: "L", FIP: 3.00}}
	bats := map[string]string{"test player": "R"}
	src := NewMatchupAdjustedSource(inner, opp, bats, 4.00)

	pts, ok := src.GetPtsPerGame("Test Player", "BOS", nil)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("team not playing: got %.4f, want 5.00", pts)
	}
}

func TestMatchupAdjusted_ZeroLeagueAvgFIP(t *testing.T) {
	inner := &stubPPSSource{
		proj: map[string]*Projection{"test player": {G: 100}},
		pts:  map[string]float64{"test player": 5.00},
	}
	opp := map[string]OpposingPitcher{"NYY": {Name: "some pitcher", Throws: "L", FIP: 3.00}}
	bats := map[string]string{"test player": "R"}
	src := NewMatchupAdjustedSource(inner, opp, bats, 0) // zero avg FIP

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", nil)
	if !ok {
		t.Fatal("expected true")
	}
	// With zero leagueAvgFIP, quality multiplier should be 1.0 (guard), platoon is favorable (1.0)
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("zero avg FIP: got %.4f, want 5.00", pts)
	}
}

func TestMatchupAdjusted_FullChainComposability(t *testing.T) {
	innerProj := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, Singles: 60, Doubles: 20, Triples: 5, HR: 15, RBI: 50, R: 40, BB: 30},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "R": 1.0, "RBI": 1.0}

	// Layer 1: BlendedSource
	blended := NewBlendedSource(innerProj, map[string]fantrax.RecentStat{
		"player1": {TotalFP: 15.0, GamesPlayed: 5},
	}, scoring, map[string]string{"test player": "player1"})

	// Layer 2: ParkAdjustedSource (Coors)
	parkFactors := map[string]ParkFactors{
		"COL": {Team: "COL", HR: 1.06, H: 1.17, R: 1.28, BB: 1.01, SO: 0.90, H1B: 1.16, H2B: 1.19, H3B: 2.02},
	}
	venues := map[string]string{"NYY": "COL"}
	parkAdj := NewParkAdjustedSource(blended, parkFactors, venues)

	// Layer 3: MatchupAdjustedSource (unfavorable platoon)
	opp := map[string]OpposingPitcher{"NYY": {Name: "lefty", Throws: "L", FIP: 4.00}}
	bats := map[string]string{"test player": "L"} // LHH vs LHP = unfavorable
	matchupAdj := NewMatchupAdjustedSource(parkAdj, opp, bats, 4.00)

	// Get values at each layer
	blendedPts, _ := blended.GetPtsPerGame("Test Player", "NYY", scoring)
	parkPts, _ := parkAdj.GetPtsPerGame("Test Player", "NYY", scoring)
	finalPts, ok := matchupAdj.GetPtsPerGame("Test Player", "NYY", scoring)

	if !ok {
		t.Fatal("expected true")
	}
	// Park should boost blended (Coors)
	if parkPts <= blendedPts {
		t.Errorf("park should boost: park=%.4f, blended=%.4f", parkPts, blendedPts)
	}
	// Matchup should reduce park-adjusted (unfavorable platoon, 0.93)
	if finalPts >= parkPts {
		t.Errorf("matchup should reduce: final=%.4f, park=%.4f", finalPts, parkPts)
	}
	// Final should be park * 0.93 (neutral quality)
	want := parkPts * 0.93
	if math.Abs(finalPts-want) > 0.001 {
		t.Errorf("full chain: got %.4f, want %.4f", finalPts, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/projections/ -run TestMatchupAdjusted -v`
Expected: FAIL — `MatchupAdjustedSource` not defined

- [ ] **Step 3: Implement MatchupAdjustedSource**

Create `internal/projections/matchup_adjusted.go`:

```go
package projections

import (
	"math"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

const (
	unfavorablePlatoonMult = 0.93
	qualityMultMin         = 0.85
	qualityMultMax         = 1.15
	combinedMultMin        = 0.80
	combinedMultMax        = 1.15
)

// OpposingPitcher holds information about the starting pitcher a team faces today.
type OpposingPitcher struct {
	Name   string
	Team   string
	Throws string  // "R" or "L"
	FIP    float64 // from Steamer projection
}

// MatchupAdjustedSource wraps a projection source and applies platoon split
// and opposing pitcher quality adjustments to hitter pts/game.
type MatchupAdjustedSource struct {
	inner            Source
	innerPPS         PtsPerGameSource
	opposingPitchers map[string]OpposingPitcher // batting team abbr → opposing SP
	hitterBats       map[string]string          // NormalizeName(name) → "R"/"L"/"S"
	leagueAvgFIP     float64
}

// NewMatchupAdjustedSource creates a matchup-adjusted wrapper.
func NewMatchupAdjustedSource(
	inner Source,
	opposingPitchers map[string]OpposingPitcher,
	hitterBats map[string]string,
	leagueAvgFIP float64,
) *MatchupAdjustedSource {
	pps, _ := inner.(PtsPerGameSource)
	return &MatchupAdjustedSource{
		inner:            inner,
		innerPPS:         pps,
		opposingPitchers: opposingPitchers,
		hitterBats:       hitterBats,
		leagueAvgFIP:     leagueAvgFIP,
	}
}

// GetProjection delegates to the inner source (unadjusted).
func (s *MatchupAdjustedSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	return s.inner.GetProjection(name, mlbTeam)
}

// GetPtsPerGame returns matchup-adjusted points per game.
func (s *MatchupAdjustedSource) GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	var basePts float64
	var ok bool
	if s.innerPPS != nil {
		basePts, ok = s.innerPPS.GetPtsPerGame(name, mlbTeam, scoring)
	}
	if !ok {
		proj, projOK := s.inner.GetProjection(name, mlbTeam)
		if !projOK || proj.G <= 0 {
			return 0, false
		}
		basePts = ExpectedPtsFromProj(proj, scoring)
	}

	// Look up opposing pitcher for this hitter's team.
	opp, oppOK := s.opposingPitchers[mlbTeam]
	if !oppOK {
		return basePts, true
	}

	// Platoon multiplier.
	platoonMult := 1.0
	if bats, batsOK := s.hitterBats[NormalizeName(name)]; batsOK && opp.Throws != "" {
		if bats != "S" && bats == opp.Throws {
			// Same-handed: unfavorable matchup.
			platoonMult = unfavorablePlatoonMult
		}
	}

	// Pitcher quality multiplier.
	qualityMult := 1.0
	if s.leagueAvgFIP > 0 && opp.FIP > 0 {
		qualityMult = math.Max(qualityMultMin, math.Min(qualityMultMax, opp.FIP/s.leagueAvgFIP))
	}

	// Combined cap.
	combined := math.Max(combinedMultMin, math.Min(combinedMultMax, platoonMult*qualityMult))
	return basePts * combined, true
}
```

- [ ] **Step 4: Run all matchup tests**

Run: `go test ./internal/projections/ -run TestMatchupAdjusted -v`
Expected: All PASS

- [ ] **Step 5: Run full projections test suite**

Run: `go test ./internal/projections/ -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/projections/matchup_adjusted.go internal/projections/matchup_adjusted_test.go
git commit -m "feat: add MatchupAdjustedSource for platoon and pitcher quality adjustments"
```

---

### Task 5: Wire MatchupAdjustedSource into optimize command

**Files:**
- Modify: `cmd/optimize.go:182-212,424-428`

- [ ] **Step 1: Add hitter Bats extraction after FanGraphs source creation**

In `cmd/optimize.go`, after the hitter projection setup block (after `hitterProjSrc` is assigned, around line 212), add:

```go
// Extract hitter handedness for matchup adjustments.
var hitterBats map[string]string
if fgSrc != nil {
    hitterBats = fgSrc.HitterBats()
    log.Printf("hitter handedness loaded: %d players", len(hitterBats))
}
```

Note: `fgSrc` is only non-nil when `NewFanGraphsSource()` succeeds (line 183). The `var` declaration ensures `hitterBats` is nil when FanGraphs is unavailable.

- [ ] **Step 2: Add pitcher info extraction after pitcher FanGraphs source creation**

After the pitcher projection setup block (after `pitcherProjSrc` is assigned, around line 243), add:

```go
// Extract pitcher handedness and FIP for matchup adjustments.
var pitcherHandedness map[string]string
var pitcherFIP map[string]float64
var leagueAvgFIP float64
if fgPitSrc != nil {
    pitcherHandedness, pitcherFIP, leagueAvgFIP = fgPitSrc.PitcherInfo()
    log.Printf("pitcher info loaded: %d handedness, %d FIP, league avg FIP=%.2f", len(pitcherHandedness), len(pitcherFIP), leagueAvgFIP)
}
```

- [ ] **Step 3: Decouple venues fetch from park factors and build opposingPitchers**

In the per-date `g.Go` closure, the existing code only fetches `venues` when `parkFactors != nil` (line 414-422). Matchup adjustments need `venues` independently to map batting teams to opposing pitchers. Move the `GameVenues` call to run unconditionally (it's needed by both park factors and matchup adjustments):

```go
// Fetch game venues (needed for both park factors and matchup adjustments).
var venues map[string]string
v, err := schedClient.GameVenues(date)
if err != nil {
    warnings = append(warnings, fmt.Sprintf("game venues unavailable for %s (%v)", date.Format("2006-01-02"), err))
} else {
    venues = v
}
```

Then keep the existing park factor wrapping guarded by `parkFactors != nil`:
```go
if venues != nil && parkFactors != nil {
    dateHitterSrc = projections.NewParkAdjustedSource(hitterProjSrc, parkFactors, venues)
}
```

After the park factor wrapping, add the matchup adjustment wrapping:

```go
// Build opposing pitcher map for matchup adjustments.
if len(probableStarters) > 0 && leagueAvgFIP > 0 && venues != nil {
    opposingPitchers := make(map[string]projections.OpposingPitcher)
    for pitcherName, pitcherTeam := range probableStarters {
        pitcherHome := venues[pitcherTeam]
        for team, homeTeam := range venues {
            if team == pitcherTeam {
                continue
            }
            if homeTeam == pitcherHome {
                opp := projections.OpposingPitcher{
                    Name: pitcherName,
                    Team: pitcherTeam,
                }
                if h, ok := pitcherHandedness[pitcherName]; ok {
                    opp.Throws = h
                }
                if f, ok := pitcherFIP[pitcherName]; ok {
                    opp.FIP = f
                }
                opposingPitchers[team] = opp
                break
            }
        }
    }
    if len(opposingPitchers) > 0 {
        dateHitterSrc = projections.NewMatchupAdjustedSource(dateHitterSrc, opposingPitchers, hitterBats, leagueAvgFIP)
    }
}
```

- [ ] **Step 4: Build and verify compilation**

Run: `go build -o /dev/null .`
Expected: No errors

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/optimize.go
git commit -m "feat: wire matchup adjustments into optimize command"
```

---

### Task 6: Integration Dry-Run Verification

- [ ] **Step 1: Run a dry-run to verify the full pipeline works**

Run: `go run . optimize --dry-run`

Expected: The output should show the normal hitter/pitcher lineup table. Look for the new log lines:
- `hitter handedness loaded: N players`
- `pitcher info loaded: N handedness, N FIP, league avg FIP=X.XX`

The Pts/G values should now reflect matchup adjustments when probable starters are available.

- [ ] **Step 2: Verify idempotency**

Run the same command twice and confirm the second run shows "No changes needed":

Run: `go run . optimize --dry-run`
Expected: Same output as step 1 — idempotent

- [ ] **Step 3: Final commit if any adjustments were needed**

Only if code changes were made during verification:
```bash
git add -A
git commit -m "fix: address issues found during integration testing"
```
