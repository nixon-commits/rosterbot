package recap

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"math"
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
	"barWidth":          barWidth,
	"matchupWinnerName": matchupWinnerName,
	"matchupLoserName":  matchupLoserName,
	"matchupWinnerPts":  matchupWinnerPts,
	"matchupLoserPts":   matchupLoserPts,
	"matchupSideClass":  matchupSideClass,
}

// Render writes the recap HTML to w.
func Render(w io.Writer, r *Recap) error {
	if r == nil {
		return fmt.Errorf("nil recap")
	}
	return tmpl.Execute(w, r)
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
