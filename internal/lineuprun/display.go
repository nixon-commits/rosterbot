package lineuprun

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/roster"
)

// zeroGainEps matches the optimizer's float-comparison epsilon. Combined
// hitter+pitcher move sets whose net pts gain is within this tolerance are
// dropped before staging — the optimizer can construct cosmetic swaps among
// equally-valued bench players (e.g. two zero-projection players trading UT
// slots), and Fantrax atomically rejects the whole payload if any one of
// those players is per-player-locked, dropping any other valid moves with it.
const zeroGainEps = 1e-9

// combinedMovesDelta returns the net pts gain from a combined hitter+pitcher
// move set. ptsMap maps player ID to effective pts (already discounted for
// non-starting SPs by the caller).
func combinedMovesDelta(activate []fantrax.PlayerSlot, bench []string, ptsMap map[string]float64) float64 {
	var delta float64
	for _, ps := range activate {
		delta += ptsMap[ps.PlayerID]
	}
	for _, id := range bench {
		delta -= ptsMap[id]
	}
	return delta
}

// isZeroGainDelta reports whether a combined-move delta is within zeroGainEps of zero.
func isZeroGainDelta(delta float64) bool {
	return math.Abs(delta) < zeroGainEps
}

// pitcherProjectedPts returns a pitcher's projected fantasy pts per game using
// the blended source (if available) or the raw season projection. Returns 0
// when no projection exists. Used by the GS budget forecast to rank starters
// across the week by value.
func pitcherProjectedPts(p fantrax.Player, src projections.PitcherSource, scoring fantrax.ScoringWeights) float64 {
	if pps, ok := src.(projections.PitcherPtsPerGameSource); ok {
		if v, ok := pps.GetPitcherPtsPerGame(p.Name, p.MLBTeam, scoring); ok {
			return v
		}
	}
	proj, ok := src.GetPitcherProjection(p.Name, p.MLBTeam)
	if !ok || proj.G <= 0 {
		return 0
	}
	return projections.PitcherExpectedPtsFromProj(proj, scoring)
}

// padRight pads s with spaces to the given display width.
// Accounts for double-width characters (emoji, CJK) that occupy 2 terminal columns.
func padRight(s string, width int) string {
	w := displayWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// displayWidth returns the number of terminal columns a string occupies.
// Characters in the Supplementary Multilingual Plane (U+10000+) like emoji
// are double-width; BMP characters (★, ✓, ▸) are single-width.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x10000 {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// colorDelta formats a pipeline delta with ANSI green (positive) or red (negative).
// All branches use the same ANSI prefix/suffix lengths (\033[XXm … \033[0m) so
// the total byte length is consistent and fmt.Printf %s columns stay aligned.
func colorDelta(delta float64) string {
	if delta > 0.005 {
		return fmt.Sprintf("\033[32m%+7.2f\033[0m", delta)
	}
	if delta < -0.005 {
		return fmt.Sprintf("\033[31m%7.2f\033[0m", delta)
	}
	return "\033[90m      -\033[0m"
}

// formatBlendMix renders the base-projection weight as a fixed-width 4-char
// percentage cell (e.g. " 60%", "100%"). When no recent stats exist the cell
// is rendered in dim grey to flag that no blending was actually applied.
func formatBlendMix(baseWt float64, hasRecent bool) string {
	if !hasRecent {
		return "\033[90m100%\033[0m"
	}
	return fmt.Sprintf("%3.0f%%", baseWt*100)
}

// truncName truncates a name to maxLen runes.
func truncName(name string, maxLen int) string {
	runes := []rune(name)
	if len(runes) <= maxLen {
		return name
	}
	return string(runes[:maxLen])
}

func formatDates(dates []time.Time) string {
	if len(dates) == 1 {
		return dates[0].Format("2006-01-02")
	}
	return fmt.Sprintf("%s..%s (%d days)",
		dates[0].Format("2006-01-02"),
		dates[len(dates)-1].Format("2006-01-02"),
		len(dates))
}

func alertLabel(t roster.AlertType) string {
	switch t {
	case roster.HealthyInIL:
		return "Healthy but in IL slot"
	case roster.CalledUpInMinors:
		return "Called up but in Minors slot"
	case roster.InjuredInActive:
		return "Injured but in Active/Reserve slot"
	case roster.MinorInActive:
		return "Minor leaguer but in Active/Reserve slot"
	default:
		return string(t)
	}
}

func countActive(players []fantrax.Player) int {
	n := 0
	for _, p := range players {
		if p.Status == "Active" {
			n++
		}
	}
	return n
}

// allTeamsPlaying returns a map treating all roster players as having games —
// used as a safe fallback when the MLB schedule API is unavailable.
func allTeamsPlaying(players []fantrax.Player) map[string]bool {
	m := make(map[string]bool)
	for _, p := range players {
		m[p.MLBTeam] = true
	}
	return m
}

// rosterSPNames returns a map of normalized pitcher name → Player for all
// SP-eligible, non-injured, non-minors pitchers on the roster.
func rosterSPNames(roster []fantrax.Player) map[string]fantrax.Player {
	m := make(map[string]fantrax.Player)
	for _, p := range roster {
		if p.InMinors || p.IsInjured {
			continue
		}
		if strings.Contains(p.PosShortNames, "SP") {
			m[projections.NormalizeName(p.Name)] = p
		}
	}
	return m
}

// renderDateResult prints one date's side-by-side hitter/pitcher board and,
// when requested, the two projection-pipeline detail tables.
//
// Extracted verbatim from Run's print loop (rosterbot-6rv criterion 4): ~280
// lines of box-drawing and ANSI colour had no business sitting inside the
// orchestration function. Nothing in the block escaped it — every local it
// builds (the column line buffers, the active/bench partitions, the running
// point totals) dies at the end — which is what made a mechanical extraction
// safe. Output is byte-for-byte what Run emitted before.
//
// w is injected rather than assumed to be os.Stdout (rosterbot-rr1, the other
// half of criterion 4). The tidiness is secondary; the point is that ~280 lines
// of positional box-drawing become golden-testable. A one-space column shift
// here is invisible to every other test in this package and obvious in a diff,
// so TestRenderDateResult_Golden is the only thing standing between a
// misaligned board and production.
func renderDateResult(w io.Writer, dr dateResult, multiDate bool, slotName map[string]string, showPipeline bool, gsBudget *optimizer.GSBudget) {
	// --- Build side-by-side hitter/pitcher display ---
	const (
		colL = 43 // hitter column width (runes)
		colR = 48 // pitcher column width (runes)
	)

	// Date header
	dateLabel := dr.date.Format("Mon Jan 2")
	if dr.isToday {
		dateLabel += " (today)"
	}
	if multiDate {
		boxW := colL + 3 + colR
		fmt.Fprintf(w, "\n  ╔%s╗\n", strings.Repeat("═", boxW))
		fmt.Fprintf(w, "  ║  %-*s║\n", boxW-2, dateLabel)
		fmt.Fprintf(w, "  ╚%s╝\n", strings.Repeat("═", boxW))
	}

	// --- Hitter lines ---
	var hLines []string
	var hGreen []bool // parallel: true = render line in green (minor leaguer)
	hLines = append(hLines, "Hitters "+strings.Repeat("─", colL-8))
	hGreen = append(hGreen, false)
	hLines = append(hLines, "  "+padRight("Player", 19)+" "+padRight("Team", 4)+" "+fmt.Sprintf("%6s", "Pts/G")+" "+padRight("Slot", 4)+" Game")
	hGreen = append(hGreen, false)
	hLines = append(hLines, strings.Repeat("─", colL))
	hGreen = append(hGreen, false)

	var hitterStartingPts float64
	var hActive, hBench []optimizer.ScoredPlayer
	for _, sp := range dr.hitterResult.Scored {
		if sp.Player.Status == "Active" {
			hActive = append(hActive, sp)
			if sp.HasGame {
				hitterStartingPts += sp.ExpectedPts
			}
		} else {
			hBench = append(hBench, sp)
		}
	}

	for _, sp := range hActive {
		slot := ""
		if name, ok := slotName[sp.Player.RosterPosition]; ok {
			slot = name
		}
		game := " "
		if sp.Player.Locked {
			game = "🔒"
		} else if dr.benchedToday[projections.NormalizeName(sp.Player.Name)] {
			game = "❌"
		} else if sp.HasGame {
			game = "✓"
		}
		line := padRight("▸", 1) + " " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
			padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) +
			" " + padRight(slot, 4) + " " + game
		hLines = append(hLines, line)
		hGreen = append(hGreen, sp.Player.InMinors)
	}
	if len(hBench) > 0 {
		hLines = append(hLines, strings.Repeat("·", colL))
		hGreen = append(hGreen, false)
		for _, sp := range hBench {
			game := " "
			if sp.Player.Locked {
				game = "🔒"
			} else if dr.benchedToday[projections.NormalizeName(sp.Player.Name)] {
				game = "❌"
			} else if sp.HasGame {
				game = "✓"
			}
			line := "  " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
				padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) +
				" " + padRight("", 4) + " " + game
			hLines = append(hLines, line)
			hGreen = append(hGreen, sp.Player.InMinors)
		}
	}

	// --- Pitcher lines ---
	var pLines []string
	var pGreen []bool // parallel: true = render line in green (minor leaguer)
	pLines = append(pLines, "Pitchers "+strings.Repeat("─", colR-9))
	pGreen = append(pGreen, false)
	pLines = append(pLines, "  "+padRight("Player", 19)+" "+padRight("Team", 4)+" "+fmt.Sprintf("%6s", "Pts/G")+" "+padRight("Slot", 4)+" "+padRight("Pos", 4)+" Game")
	pGreen = append(pGreen, false)
	pLines = append(pLines, strings.Repeat("─", colR))
	pGreen = append(pGreen, false)

	var pitcherStartingPts float64
	var pActive, pBench []optimizer.ScoredPitcher
	for _, sp := range dr.pitcherResult.Scored {
		if sp.Player.Status == "Active" {
			pActive = append(pActive, sp)
			isRP := !strings.Contains(sp.Player.PosShortNames, "SP")
			if sp.HasGame && (sp.IsStarter || isRP) {
				pitcherStartingPts += sp.ExpectedPts
			}
		} else {
			pBench = append(pBench, sp)
		}
	}
	for _, sp := range pActive {
		slot := ""
		if name, ok := slotName[sp.Player.RosterPosition]; ok {
			slot = name
		}
		role := sp.Player.PosShortNames
		if role == "" {
			role = "P"
		}
		if sp.IsStarter {
			role += "★"
		}
		game := " "
		if sp.Player.Locked {
			game = "🔒"
		} else if sp.HasGame {
			game = "✓"
		}
		line := padRight("▸", 1) + " " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
			padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) + " " +
			padRight(slot, 4) + " " + padRight(role, 4) + " " + game
		pLines = append(pLines, line)
		pGreen = append(pGreen, sp.Player.InMinors)
	}
	if len(pBench) > 0 {
		pLines = append(pLines, strings.Repeat("·", colR))
		pGreen = append(pGreen, false)
		for _, sp := range pBench {
			role := sp.Player.PosShortNames
			if role == "" {
				role = "P"
			}
			if sp.IsStarter {
				role += "★"
			}
			game := " "
			if sp.Player.Locked {
				game = "🔒"
			} else if sp.HasGame {
				game = "✓"
			}
			line := "  " + padRight(truncName(sp.Player.Name, 19), 19) + " " +
				padRight(sp.Player.MLBTeam, 4) + " " + fmt.Sprintf("%6.2f", sp.ExpectedPts) + " " +
				padRight("", 4) + " " + padRight(role, 4) + " " + game
			pLines = append(pLines, line)
			pGreen = append(pGreen, sp.Player.InMinors)
		}
	}
	// Pad data sections to same height so totals align.
	for len(hLines) < len(pLines) {
		hLines = append(hLines, "")
		hGreen = append(hGreen, false)
	}
	for len(pLines) < len(hLines) {
		pLines = append(pLines, "")
		pGreen = append(pGreen, false)
	}

	// Append footer lines (separator + total) — now on the same row.
	hLines = append(hLines, strings.Repeat("─", colL))
	hGreen = append(hGreen, false)
	hLines = append(hLines, "  "+padRight("Total", 19)+" "+padRight("", 4)+" "+fmt.Sprintf("%6.2f", hitterStartingPts))
	hGreen = append(hGreen, false)

	pLines = append(pLines, strings.Repeat("─", colR))
	pGreen = append(pGreen, false)
	pLines = append(pLines, "  "+padRight("Total", 19)+" "+padRight("", 4)+" "+fmt.Sprintf("%6.2f", pitcherStartingPts))
	pGreen = append(pGreen, false)
	if gsBudget != nil {
		remaining := gsBudget.Remaining()
		hLines = append(hLines, "")
		hGreen = append(hGreen, false)
		pLines = append(pLines, fmt.Sprintf("GS: %d/%d used (%d rem, %.1f future)",
			gsBudget.Used, gsBudget.Limit, remaining, gsBudget.FutureDemand()))
		pGreen = append(pGreen, false)
	}

	// Print side by side.
	fmt.Fprintln(w)
	for i := range hLines {
		left := padRight(hLines[i], colL)
		right := padRight(pLines[i], colR)
		if hGreen[i] {
			left = "\033[32m" + left + "\033[0m"
		}
		if pGreen[i] {
			right = "\033[32m" + right + "\033[0m"
		}
		fmt.Fprintf(w, "  %s │ %s\n", left, right)
	}

	// Combined total.
	fmt.Fprintf(w, "\n  %-26s %6.2f\n", "Combined Expected", hitterStartingPts+pitcherStartingPts)

	// --- Hitter pipeline detail ---
	// Both pipeline tables share the same column geometry so they line up:
	//   indent(2) Player(24) Base(7) Mix(4) Blend(7) Mid1(7) Mid2(7) Final(7)
	// with 2-space gaps. Total visible width = 2 + 24 + 2 + 7 + 2 + 4 + 2 + 7
	// + 2 + 7 + 2 + 7 + 2 + 7 + 1(│) = 78.
	// Hitters fill Mid1 with Platoon and Mid2 with Opp SP. Pitchers leave
	// Mid1 blank and put Gate in Mid2 so the rightmost adjustment column
	// aligns between tables.
	const pipelineWidth = 78

	if showPipeline && len(dr.hitterPipelines) > 0 {
		fmt.Fprintln(w)

		pipelineSorted := make([]optimizer.ScoredPlayer, 0, len(dr.hitterPipelines))
		for _, sp := range dr.hitterResult.Scored {
			if _, ok := dr.hitterPipelines[sp.Player.ID]; ok {
				pipelineSorted = append(pipelineSorted, sp)
			}
		}
		sort.Slice(pipelineSorted, func(i, j int) bool {
			pi := dr.hitterPipelines[pipelineSorted[i].Player.ID]
			pj := dr.hitterPipelines[pipelineSorted[j].Player.ID]
			return pi.FinalPtsPerGame > pj.FinalPtsPerGame
		})

		titlePrefix := "  Hitter Pipeline "
		fmt.Fprintf(w, "%s%s╮\n", titlePrefix, strings.Repeat("─", pipelineWidth-len(titlePrefix)-1))
		fmt.Fprintf(w, "  %-24s  %7s  %4s  %7s  %7s  %7s  %7s│\n",
			"Player", "Base", "Mix", "Blend", "Platoon", "Opp SP", "Final")
		fmt.Fprintf(w, "  %s╯\n", strings.Repeat("─", pipelineWidth-2-1))

		for _, sp := range pipelineSorted {
			pd := dr.hitterPipelines[sp.Player.ID]
			fmt.Fprintf(w, "  %-24s  %7.2f  %s  %s  %s  %s  %7.2f\n",
				truncName(sp.Player.Name, 24),
				pd.BasePtsPerGame,
				formatBlendMix(pd.BaseWt, pd.HasRecent),
				colorDelta(pd.BlendDelta),
				colorDelta(pd.PlatoonDelta),
				colorDelta(pd.QualityDelta),
				pd.FinalPtsPerGame,
			)
		}
	}

	// --- Pitcher pipeline detail ---
	if showPipeline && len(dr.pitcherPipelines) > 0 {
		fmt.Fprintln(w)

		pitPipelineSorted := make([]optimizer.ScoredPitcher, 0, len(dr.pitcherPipelines))
		for _, sp := range dr.pitcherResult.Scored {
			if _, ok := dr.pitcherPipelines[sp.Player.ID]; ok {
				pitPipelineSorted = append(pitPipelineSorted, sp)
			}
		}
		sort.Slice(pitPipelineSorted, func(i, j int) bool {
			pi := dr.pitcherPipelines[pitPipelineSorted[i].Player.ID]
			pj := dr.pitcherPipelines[pitPipelineSorted[j].Player.ID]
			return pi.FinalPtsPerGame > pj.FinalPtsPerGame
		})

		titlePrefix := "  Pitcher Pipeline "
		fmt.Fprintf(w, "%s%s╮\n", titlePrefix, strings.Repeat("─", pipelineWidth-len(titlePrefix)-1))
		fmt.Fprintf(w, "  %-24s  %7s  %4s  %7s  %7s  %7s  %7s│\n",
			"Player", "Base", "Mix", "Blend", "", "Gate", "Final")
		fmt.Fprintf(w, "  %s╯\n", strings.Repeat("─", pipelineWidth-2-1))

		for _, sp := range pitPipelineSorted {
			pd := dr.pitcherPipelines[sp.Player.ID]
			fmt.Fprintf(w, "  %-24s  %7.2f  %s  %s  %7s  %s  %7.2f\n",
				truncName(sp.Player.Name, 24),
				pd.BasePtsPerGame,
				formatBlendMix(pd.BaseWt, pd.HasRecent),
				colorDelta(pd.BlendDelta),
				"",
				colorDelta(pd.GateDelta),
				pd.FinalPtsPerGame,
			)
		}
	}
}
