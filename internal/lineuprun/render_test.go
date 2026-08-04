package lineuprun

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// The board is ~280 lines of positional box-drawing where a one-space column
// shift is invisible to every other test in this package and obvious in a diff.
// These goldens make that drift a CI failure instead of something caught by eye
// against a baseline binary (rosterbot-rr1 criterion 3). They assert the FULL
// byte string including ANSI escapes — asserting "contains the player's name"
// would pass through exactly the misalignment they exist to catch.
//
// Regenerate after an intentional layout change:
//
//	go test ./internal/lineuprun/ -run TestRenderDateResult -update
//
// and read the resulting testdata diff — that diff IS the review.
var updateGolden = flag.Bool("update", false, "rewrite testdata golden files from current render output")

func scoredHitter(name, team, slot string, pts float64, active, hasGame bool) optimizer.ScoredPlayer {
	status := "Reserve"
	if active {
		status = "Active"
	}
	return optimizer.ScoredPlayer{
		Player: fantrax.Player{
			ID: name, Name: name, MLBTeam: team, Status: status, RosterPosition: slot,
		},
		ExpectedPts: pts,
		HasGame:     hasGame,
	}
}

func scoredPitcher(name, team, slot, pos string, pts float64, active, hasGame, starter bool) optimizer.ScoredPitcher {
	status := "Reserve"
	if active {
		status = "Active"
	}
	return optimizer.ScoredPitcher{
		Player: fantrax.Player{
			ID: name, Name: name, MLBTeam: team, Status: status,
			RosterPosition: slot, PosShortNames: pos,
		},
		ExpectedPts: pts,
		HasGame:     hasGame,
		IsStarter:   starter,
	}
}

func goldenSlotNames() map[string]string {
	return map[string]string{"001": "C", "012": "OF", "020": "P"}
}

// goldenResult is one date's board carrying every visual state the renderer
// knows about: an active hitter with a game, a locked one (🔒), one confirmed
// out of the real-life lineup (❌), a minor leaguer (rendered green), a bench
// section, a starred confirmed starter, and a bench pitcher with no game.
func goldenResult() dateResult {
	minors := scoredHitter("Farm Hand", "SEA", "012", 3.10, true, true)
	minors.Player.InMinors = true
	locked := scoredHitter("Locked Guy", "LAD", "001", 7.25, true, true)
	locked.Player.Locked = true

	return dateResult{
		date:    day(2026, 7, 25),
		period:  113,
		isToday: true,
		hitterResult: optimizer.Result{Scored: []optimizer.ScoredPlayer{
			scoredHitter("Star Hitter", "NYY", "012", 12.40, true, true),
			locked,
			scoredHitter("Sat Today", "BOS", "012", 9.00, true, true),
			minors,
			scoredHitter("Bench Bat", "TEX", "", 4.50, false, true),
		}},
		pitcherResult: optimizer.PitcherResult{Scored: []optimizer.ScoredPitcher{
			scoredPitcher("Ace Arm", "LAD", "020", "SP", 22.75, true, true, true),
			scoredPitcher("Rel Iever", "NYM", "020", "RP", 5.20, true, true, false),
			scoredPitcher("Bench Arm", "CHC", "", "RP", 8.10, false, false, false),
		}},
		benchedToday: map[string]bool{projections.NormalizeName("Sat Today"): true},
	}
}

// The plain single-date board — what a bare `optimize` run prints: no date box,
// no GS line, no pipeline tables.
func TestRenderDateResult_GoldenPlain(t *testing.T) {
	var buf bytes.Buffer
	renderDateResult(&buf, goldenResult(), false, goldenSlotNames(), false, nil)
	assertGolden(t, "board_plain.golden", buf.String())
}

// Everything switched on at once: the multi-date header box, the GS-budget
// footer line (which is appended to the pitcher column only, so it also pins
// the left-column padding that keeps the two halves aligned), and both
// pipeline detail tables with their shared column geometry.
func TestRenderDateResult_GoldenFull(t *testing.T) {
	dr := goldenResult()
	dr.hitterPipelines = map[string]*projections.HitterPipelineDetail{
		"Star Hitter": {
			PlayerName: "Star Hitter", PlayerID: "Star Hitter", MLBTeam: "NYY",
			BasePtsPerGame: 10.00, BlendedPtsPerGame: 11.00, BlendDelta: 1.00,
			BaseWt: 0.72, HasRecent: true,
			PlatoonDelta: 0.80, QualityDelta: -0.40, FinalPtsPerGame: 12.40,
		},
		// No recent stats: exercises the dim-grey "100%" mix cell and the
		// zero-delta dash, both of which carry ANSI runs of a different length
		// than the coloured branches.
		"Farm Hand": {
			PlayerName: "Farm Hand", PlayerID: "Farm Hand", MLBTeam: "SEA",
			BasePtsPerGame: 3.10, BlendedPtsPerGame: 3.10, FinalPtsPerGame: 3.10,
		},
	}
	dr.pitcherPipelines = map[string]*projections.PitcherPipelineDetail{
		"Ace Arm": {
			PlayerName: "Ace Arm", PlayerID: "Ace Arm", MLBTeam: "LAD", Role: "SP",
			BasePtsPerGame: 20.00, BlendedPtsPerGame: 22.75, BlendDelta: 2.75,
			BaseWt: 0.55, HasRecent: true, FinalPtsPerGame: 22.75,
		},
	}
	gs := &optimizer.GSBudget{Limit: 12, Used: 5, Today: day(2026, 7, 25), WeekEnd: day(2026, 7, 26)}

	var buf bytes.Buffer
	renderDateResult(&buf, dr, true, goldenSlotNames(), true, gs)
	assertGolden(t, "board_full.golden", buf.String())
}

// The GS-budget footer with the gate having actually declined starts: the
// suppressed-starts line only fires when GateReport.Suppressed is non-empty
// (board_full.golden's budget fixture has none, so it never exercises this
// branch). Also pins the pluralization and the gross-forgone-points total.
func TestRenderDateResult_GoldenGSSuppressed(t *testing.T) {
	dr := goldenResult()
	dr.pitcherResult.GateReport = optimizer.GSGateReport{
		Suppressed: []optimizer.SuppressedStart{
			{PlayerID: "p1", Name: "Some SP", ProjectedPts: 8.50},
			{PlayerID: "p2", Name: "Another SP", ProjectedPts: 4.75},
		},
		Limit: 12, Used: 5, Remaining: 7,
	}
	gs := &optimizer.GSBudget{Limit: 12, Used: 5, Today: day(2026, 7, 25), WeekEnd: day(2026, 7, 26)}

	var buf bytes.Buffer
	renderDateResult(&buf, dr, false, goldenSlotNames(), false, gs)
	assertGolden(t, "board_gs_suppressed.golden", buf.String())
}

// renderDateResult must not reach for os.Stdout — the whole point of the
// injected writer (rosterbot-rr1 criterion 2). If it ever did, this would
// produce an empty buffer while the board leaked to the test's own output.
func TestRenderDateResult_WritesOnlyToTheInjectedWriter(t *testing.T) {
	var buf bytes.Buffer
	renderDateResult(&buf, goldenResult(), false, goldenSlotNames(), false, nil)
	if buf.Len() == 0 {
		t.Fatal("nothing written to the injected writer")
	}
	if !strings.Contains(buf.String(), "Star Hitter") {
		t.Error("board content did not reach the injected writer")
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update): %v", err)
	}
	if got == string(want) {
		return
	}
	t.Errorf("board drift in %s — regenerate with -update and review the diff.\n"+
		"--- got ---\n%s\n--- want ---\n%s", name, visible(got), visible(string(want)))
}

// visible makes an ANSI-coloured board legible in a failure diff: without it
// the escape sequences colour the test output itself, hiding exactly the
// byte-level differences that caused the failure. Spaces become · so trailing
// padding — the most common drift — is countable.
func visible(s string) string {
	r := strings.NewReplacer("\x1b", "\\e", " ", "·")
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(r.Replace(line))
	}
	return b.String()
}
