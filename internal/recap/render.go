package recap

import (
	_ "embed"
	"fmt"
	"hash/fnv"
	"html"
	"html/template"
	"io"
	"math"
	"strings"
	"time"
)

//go:embed template.html
var templateHTML string

var tmpl = template.Must(template.New("recap").Funcs(funcMap).Parse(templateHTML))

var funcMap = template.FuncMap{
	"pts":               fmtPts,
	"pct":               fmtPct,
	"fmtDate":           fmtDate,
	"add":               func(a, b int) int { return a + b },
	"sub":               func(a, b int) int { return a - b },
	"mul":               func(a, b int) int { return a * b },
	"barWidth":          barWidth,
	"matchupWinnerName": matchupWinnerName,
	"matchupLoserName":  matchupLoserName,
	"matchupWinnerPts":  matchupWinnerPts,
	"matchupLoserPts":   matchupLoserPts,
	"matchupSideClass":  matchupSideClass,
	"awardEmoji":        awardEmoji,
	"sparkPath":         sparkPath,
	"fullChartPath":     fullChartPath,
	"curveForMatchup":   curveForMatchup,
	"truncate":          truncateString,
	"teamLogo":          teamLogo,
	"teamInitial":       teamInitial,
	"teamColor":         teamColor,
	"rankRange":         rankRange,
	"bumpColor":         bumpColor,
	"teamShort":         teamShort,
	"standingsPoints":   standingsPoints,
	"woba":              fmtWOBA,
	"fip":               fmtFIP,
	"mlbLogo":           mlbTeamLogo,
	"teamBadge":         teamBadge,
	"mlbBadge":          mlbBadge,
}

// teamBadge renders the circular fantasy-team avatar (logo when available,
// initial-color chip otherwise) for use to the left of a team name. Returns
// trusted markup: the logo URL comes from our own data and the name is HTML-
// escaped, so it's safe to emit as template.HTML.
func teamBadge(logos map[string]string, id, name string) template.HTML {
	if url := teamLogo(logos, id); url != "" {
		return template.HTML(fmt.Sprintf(`<img class="team-avatar" src="%s" alt=""/>`, html.EscapeString(url)))
	}
	return template.HTML(fmt.Sprintf(`<span class="team-avatar fallback" style="background:%s">%s</span>`,
		html.EscapeString(teamColor(id)), html.EscapeString(teamInitial(name))))
}

// mlbBadge renders the circular MLB-club avatar for a club abbreviation (logo
// when the abbreviation is known, abbreviation text chip otherwise). Used to
// the left of a player name in the pitching-highlight cards.
func mlbBadge(abbrev string) template.HTML {
	if url := mlbTeamLogo(abbrev); url != "" {
		return template.HTML(fmt.Sprintf(`<img class="team-avatar" src="%s" alt="%s" title="%s"/>`,
			html.EscapeString(url), html.EscapeString(abbrev), html.EscapeString(abbrev)))
	}
	if abbrev == "" {
		return ""
	}
	return template.HTML(fmt.Sprintf(`<span class="team-avatar fallback" style="background:%s" title="%s">%s</span>`,
		html.EscapeString(teamColor(abbrev)), html.EscapeString(abbrev), html.EscapeString(abbrev)))
}

// mlbTeamAbbrevToID maps an MLB club abbreviation to its statsapi team ID.
// Both the codebase-canonical form (CHW, ATH, ARI, …) and common upstream
// variants (CWS, OAK, AZ, …) are included so a logo resolves regardless of
// which abbreviation Fantrax emits.
var mlbTeamAbbrevToID = map[string]int{
	"LAA": 108,
	"ARI": 109, "AZ": 109,
	"BAL": 110,
	"BOS": 111,
	"CHC": 112,
	"CIN": 113,
	"CLE": 114,
	"COL": 115,
	"DET": 116,
	"HOU": 117,
	"KC":  118, "KCR": 118,
	"LAD": 119,
	"WSH": 120, "WSN": 120, "WAS": 120,
	"NYM": 121,
	"ATH": 133, "OAK": 133,
	"PIT": 134,
	"SD":  135, "SDP": 135,
	"SEA": 136,
	"SF":  137, "SFG": 137,
	"STL": 138,
	"TB":  139, "TBR": 139,
	"TEX": 140,
	"TOR": 141,
	"MIN": 142,
	"PHI": 143,
	"ATL": 144,
	"CHW": 145, "CWS": 145,
	"MIA": 146,
	"NYY": 147,
	"MIL": 158,
}

// mlbTeamLogo returns a PNG logo URL for an MLB club abbreviation via the
// mlbstatic "spots" endpoint (96px), or "" when the abbreviation is unknown
// (template falls back to a text chip).
func mlbTeamLogo(abbrev string) string {
	id, ok := mlbTeamAbbrevToID[strings.ToUpper(strings.TrimSpace(abbrev))]
	if !ok {
		return ""
	}
	return fmt.Sprintf("https://midfield.mlbstatic.com/v1/team/%d/spots/96", id)
}

// fmtWOBA formats a wOBA value in baseball convention: three decimals with no
// leading zero (e.g. 0.382 → ".382").
func fmtWOBA(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	return strings.TrimPrefix(s, "0")
}

// fmtFIP formats a FIP value to two decimals (e.g. 3.207 → "3.21").
func fmtFIP(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

// teamLogo returns the avatar URL for one team from the Recap.LogoURLs map.
// Returns empty when the map is nil or the team has no logo set; the
// template should `{{if}}`-guard the resulting <img> tag so layout stays
// clean for older archived recaps without logo data.
func teamLogo(logos map[string]string, id string) string {
	if logos == nil {
		return ""
	}
	return logos[id]
}

// teamInitial returns a single uppercase letter for use in initial-avatar
// fallbacks when a team has no custom logo set. Empty input → "?".
func teamInitial(name string) string {
	for _, r := range name {
		if r == ' ' {
			continue
		}
		return strings.ToUpper(string(r))
	}
	return "?"
}

// teamColor returns a CSS HSL color derived deterministically from the
// team ID, used as the background color for initial-avatar fallbacks.
// Fixed saturation and lightness so every fallback reads as a consistent
// "team chip" while hue varies per team.
func teamColor(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	hue := int(h.Sum32() % 360)
	return fmt.Sprintf("hsl(%d, 55%%, 38%%)", hue)
}

// truncateString returns s if len(s) <= n, otherwise the first (n-1) runes
// followed by an ellipsis. Used for fitting team names into the WP chart's
// fixed-width axis labels.
func truncateString(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// awardEmoji returns the visual icon shown next to a weekly award category in
// both the per-week awards section and the season-to-date leaderboard. Empty
// string for unknown labels (template renders nothing).
func awardEmoji(name string) string {
	switch name {
	case AwardMostEfficient:
		return "⭐"
	case AwardLeastEfficient:
		return "❌"
	case AwardHighestScore:
		return "👑"
	case AwardLowestScore:
		return "💩"
	case AwardBiggestBlowout:
		return "⚡"
	case AwardNarrowVictory:
		return "🎯"
	case AwardHighestPtsLoss:
		return "😭"
	case AwardLowestPtsWin:
		return "🍀"
	case AwardBestStart:
		return "🔥"
	case AwardWorstStart:
		return "💣"
	case AwardComeback:
		return "↩️"
	}
	return ""
}

// Render writes the recap HTML to w. No cross-week navigation or season
// leaderboard is rendered — single-week, standalone output.
func Render(w io.Writer, r *Recap) error {
	return renderTo(w, r, nil, nil)
}

// RenderSite is Render plus a nav dropdown linking to other matchup-week pages
// in the same directory and an optional season-to-date awards leaderboard.
// Pass nav=nil and season=nil for a standalone page.
func RenderSite(w io.Writer, r *Recap, nav []WeekLink, season *SeasonAwards) error {
	return renderTo(w, r, nav, season)
}

// renderInput is the wrapper passed to the template — promotes Recap fields
// while exposing Nav and Season as separate fields.
type renderInput struct {
	*Recap
	Nav    []WeekLink
	Season *SeasonAwards
}

func renderTo(w io.Writer, r *Recap, nav []WeekLink, season *SeasonAwards) error {
	if r == nil {
		return fmt.Errorf("nil recap")
	}
	return tmpl.Execute(w, renderInput{Recap: r, Nav: nav, Season: season})
}

func fmtPts(f float64) string {
	return fmt.Sprintf("%.2f", f)
}

func fmtPct(f float64) string {
	return fmt.Sprintf("%.1f%%", f*100)
}

func fmtDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("Jan 2")
}

// barWidth caps the displayed bar at 100% and clamps tiny values so a near-zero
// efficiency still shows a visible sliver.
func barWidth(eff float64) int {
	if math.IsNaN(eff) || eff <= 0 {
		return 0
	}
	w := int(math.Round(eff * 100))
	if w > 100 {
		w = 100
	}
	return w
}

// sparkPath returns an SVG <path d="..."> string for an inline sparkline.
// Width/height match the .matchup .spark CSS rule (60×24). Maps WP in [0,1]
// linearly to vertical pixel position (HomeWP=1.0 → top, =0.0 → bottom).
func sparkPath(curve MatchupWPCurve) string {
	if len(curve.Points) < 2 {
		return ""
	}
	const w, h = 60.0, 24.0
	n := len(curve.Points)
	step := w / float64(n-1)
	var out strings.Builder
	for i, p := range curve.Points {
		x := float64(i) * step
		y := (1.0 - p.HomeWP) * h
		if i == 0 {
			fmt.Fprintf(&out, "M%.2f,%.2f", x, y)
		} else {
			fmt.Fprintf(&out, " L%.2f,%.2f", x, y)
		}
	}
	return out.String()
}

// fullChartPath returns an SVG <path> for the Game of the Week hero chart.
// Width/height match the .game-of-week .wp-chart CSS (320×120 viewBox).
func fullChartPath(curve MatchupWPCurve) string {
	if len(curve.Points) < 2 {
		return ""
	}
	const w, h = 320.0, 120.0
	n := len(curve.Points)
	step := w / float64(n-1)
	var out strings.Builder
	for i, p := range curve.Points {
		x := float64(i) * step
		y := (1.0 - p.HomeWP) * h
		if i == 0 {
			fmt.Fprintf(&out, "M%.2f,%.2f", x, y)
		} else {
			fmt.Fprintf(&out, " L%.2f,%.2f", x, y)
		}
	}
	return out.String()
}

// curveForMatchup looks up the WP curve matching the given matchup. Returns
// an empty zero-value curve when not found (template must guard with
// {{if .Points}} before rendering).
func curveForMatchup(curves []MatchupWPCurve, m MatchupResult) MatchupWPCurve {
	for _, c := range curves {
		if (c.HomeTeamID == m.HomeTeamID && c.AwayTeamID == m.AwayTeamID) ||
			(c.HomeTeamID == m.AwayTeamID && c.AwayTeamID == m.HomeTeamID) {
			return c
		}
	}
	return MatchupWPCurve{}
}

func matchupWinnerName(m *MatchupResult) string {
	if m == nil {
		return ""
	}
	if m.WinnerID == m.HomeTeamID {
		return m.HomeTeamName
	}
	return m.AwayTeamName
}

func matchupLoserName(m *MatchupResult) string {
	if m == nil {
		return ""
	}
	if m.LoserID == m.HomeTeamID {
		return m.HomeTeamName
	}
	return m.AwayTeamName
}

func matchupWinnerPts(m *MatchupResult) float64 {
	if m == nil {
		return 0
	}
	if m.WinnerID == m.HomeTeamID {
		return m.HomePts
	}
	return m.AwayPts
}

func matchupLoserPts(m *MatchupResult) float64 {
	if m == nil {
		return 0
	}
	if m.LoserID == m.HomeTeamID {
		return m.HomePts
	}
	return m.AwayPts
}

// matchupSideClass returns "win", "lose", or "" (tie) for the team's side of
// the matchup, used by the template to color the score.
func matchupSideClass(m MatchupResult, teamID string) string {
	if m.IsTie {
		return ""
	}
	if m.WinnerID == teamID {
		return "win"
	}
	return "lose"
}
