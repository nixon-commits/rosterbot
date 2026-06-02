package recap

import (
	"fmt"
	"strings"
)

// bumpColors is a fixed palette of 12 distinct colors for the standings bump
// chart. Chosen to be legible on the dark background and distinguishable from
// each other at small sizes.
var bumpColors = []string{
	"#f97316", // orange (accent)
	"#60a5fa", // blue
	"#4ade80", // green
	"#f472b6", // pink
	"#facc15", // yellow
	"#a78bfa", // purple
	"#34d399", // teal
	"#fb923c", // light orange
	"#38bdf8", // sky
	"#e879f9", // fuchsia
	"#a3e635", // lime
	"#f87171", // red
}

// bumpColor returns a distinct chart color for a team at the given index.
func bumpColor(i int) string {
	return bumpColors[i%len(bumpColors)]
}

// rankRange returns a slice [0, 1, ..., n-1] for SVG grid line iteration.
func rankRange(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// standingsPoints builds the SVG polyline `points` attribute value for one
// team across all historical weeks. Each point is "cx,cy" where cx is the
// X coordinate for that week and cy is the Y coordinate for that team's rank.
// stepX and padT must match the template's layout constants.
func standingsPoints(hist []WeekStandings, teamID string, padL, padT int) string {
	const stepX = 56
	const stepY = 24
	pts := make([]string, 0, len(hist))
	for wi, w := range hist {
		cx := padL + wi*stepX
		for _, s := range w.Standings {
			if s.TeamID == teamID {
				cy := padT + (s.Rank-1)*stepY
				pts = append(pts, fmt.Sprintf("%d,%d", cx, cy))
				break
			}
		}
	}
	return strings.Join(pts, " ")
}

// teamShort truncates a team name to fit the label column next to the chart.
// Keeps up to 12 runes; if longer, returns first 11 runes + "…".
func teamShort(name string) string {
	runes := []rune(name)
	if len(runes) <= 12 {
		return name
	}
	return string(runes[:11]) + "…"
}
