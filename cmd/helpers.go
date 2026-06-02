package cmd

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
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

// parseDates parses the --dates flag value into a slice of dates.
func parseDates(s string, today time.Time) ([]time.Time, error) {
	if s == "" {
		return []time.Time{today}, nil
	}
	if parts := strings.SplitN(s, ":", 2); len(parts) == 2 {
		start, err := time.Parse("2006-01-02", parts[0])
		if err != nil {
			return nil, fmt.Errorf("start date: %w", err)
		}
		end, err := time.Parse("2006-01-02", parts[1])
		if err != nil {
			return nil, fmt.Errorf("end date: %w", err)
		}
		if end.Before(start) {
			return nil, fmt.Errorf("end date %s is before start date %s", parts[1], parts[0])
		}
		var dates []time.Time
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			dates = append(dates, d)
		}
		return dates, nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, err
	}
	return []time.Time{d}, nil
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
