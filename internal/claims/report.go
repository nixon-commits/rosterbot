package claims

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/waivers"
)

const (
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
	colorDim   = "\033[2m"
	colorReset = "\033[0m"
	// indentTeam/indentPlayer are the two nesting levels of the Pushover digest:
	// the team line sits one nbsp under its date header, each player two more.
	indentTeam   = nbsp
	indentPlayer = nbsp + nbsp + nbsp
	// nbsp is U+00A0 (non-breaking space). Used for indentation that survives
	// Pushover's whitespace collapsing — regular leading spaces get stripped in
	// push notifications, so visual structure flattens without it.
	nbsp = " "
)

// FormatReport renders the full stdout report: per-move blocks, the daily value
// leaderboard, and the notable-drops watch. color enables ANSI coloring.
func FormatReport(moves []Move, dropsMin int, color bool) string {
	var b strings.Builder
	b.WriteString("Waiver Claims Recap\n")
	for _, m := range moves {
		writeMove(&b, m, color)
	}
	writeLeaderboard(&b, moves, color)
	writeDropsWatch(&b, notableDrops(moves, dropsMin))
	return b.String()
}

func writeMove(b *strings.Builder, m Move, color bool) {
	b.WriteString("\n")
	claimLabel := "FA"
	if m.ClaimType == "WW" {
		claimLabel = "Waiver"
	}
	fmt.Fprintf(b, "%s — %s claim", m.TeamName, claimLabel)
	if m.BidAmount != "" {
		fmt.Fprintf(b, " ($%s)", m.BidAmount)
	} else if m.Priority != "" {
		fmt.Fprintf(b, " (priority %s)", m.Priority)
	}
	if !m.ProcessedDate.IsZero() {
		fmt.Fprintf(b, " · %s", m.ProcessedDate.UTC().Format("Mon Jan 2"))
	}
	b.WriteString("\n")
	for _, p := range m.Added {
		fmt.Fprintf(b, "%s+ %s\n", nbsp, formatSidePlayer(p, true))
	}
	for _, p := range m.Dropped {
		fmt.Fprintf(b, "%s- %s\n", nbsp, formatSidePlayer(p, false))
	}
	fmt.Fprintf(b, "%sNet: %s\n", nbsp, formatSignedValue(m.NetValue(), color))
}

func formatSidePlayer(p SidePlayer, added bool) string {
	if !p.Ranked {
		return fmt.Sprintf("%s (%s) — unranked", p.Name, p.Position)
	}
	s := fmt.Sprintf("%s (%s) · #%d · %s", p.Name, p.Position, p.Rank, formatValue(p.Value))
	// 30-day trend (both added and dropped).
	if p.Trend30D > 0 {
		s += fmt.Sprintf(" ▲+%s", formatValue(p.Trend30D))
	} else if p.Trend30D < 0 {
		s += fmt.Sprintf(" ▼-%s", formatValue(-p.Trend30D))
	}
	if added && p.Signal != waivers.SignalNone {
		s += " · " + p.Signal.String()
	}
	if added && p.ProjectedFPG > 0 {
		s += fmt.Sprintf(" · %.1f FPG", p.ProjectedFPG)
	}
	// Key stat (both added and dropped).
	if p.HasStats {
		if p.IsPitcher {
			s += fmt.Sprintf(" · %.2f ERA", p.ERA)
		} else {
			s += " · " + formatOPS(p.OPS) + " OPS"
		}
	}
	return s
}

// formatOPS formats an OPS value like ".812" (no leading zero), matching the
// convention used in internal/transactions.
func formatOPS(ops float64) string {
	str := fmt.Sprintf("%.3f", ops)
	if strings.HasPrefix(str, "0") {
		return str[1:] // ".812" instead of "0.812"
	}
	return str // "1.012" stays as-is
}

func writeLeaderboard(b *strings.Builder, moves []Move, color bool) {
	if len(moves) == 0 {
		return
	}
	// moves arrive sorted by net value desc (BuildMoves guarantees this).
	b.WriteString("\nValue Leaderboard\n")
	for i, m := range moves {
		sym, name := moveHeadline(m)
		fmt.Fprintf(b, "%s%d. %s%s (%s) %s\n", nbsp, i+1, sym, name, m.TeamName, formatSignedValue(m.NetValue(), color))
	}
}

// moveHeadline returns the symbol and player name that best represent a move:
// the added player when present, otherwise the dropped player (a bare drop), so
// the leaderboard never renders a meaningless "+—".
func moveHeadline(m Move) (sym, name string) {
	if len(m.Added) > 0 {
		return "+", m.Added[0].Name
	}
	if len(m.Dropped) > 0 {
		return "-", m.Dropped[0].Name
	}
	return "+", "—"
}

// notableDrops returns dropped players whose HKB value exceeds min, sorted desc.
func notableDrops(moves []Move, min int) []SidePlayer {
	var out []SidePlayer
	for _, m := range moves {
		for _, p := range m.Dropped {
			if p.Ranked && p.Value > min {
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

func writeDropsWatch(b *strings.Builder, drops []SidePlayer) {
	if len(drops) == 0 {
		return
	}
	b.WriteString("\nNotable Drops (now available)\n")
	for _, p := range drops {
		fmt.Fprintf(b, "%s%s (%s) · %s\n", nbsp, p.Name, p.Position, formatValue(p.Value))
	}
}

// FormatPushover renders a compact digest grouped by processed date: a date
// header, then one stacked block per move (team + net on a line, each added/
// dropped player indented beneath). Whole move blocks are appended until the
// next would exceed Pushover's 1024-char limit — byte-slicing would split
// multibyte UTF-8 names, so we break on whole blocks instead.
// dateDivider is a horizontal rule drawn above each date group (after the
// first) to visually separate days. Box-drawing chars survive Pushover's
// whitespace collapsing and read as a rule rather than a drop "-" line.
const dateDivider = "──────────"

func FormatPushover(moves []Move) string {
	groups, dates := groupByDate(moves)
	var b strings.Builder
	first := true
outer:
	for _, d := range dates {
		header := dateHeader(d) + "\n"
		if !first {
			header = dateDivider + "\n" + header
		}
		if b.Len()+len(header) > 1024 {
			break
		}
		b.WriteString(header)
		first = false
		for _, t := range aggregateByTeam(groups[d]) {
			line := teamBlock(t)
			if b.Len()+len(line) > 1024 {
				break outer
			}
			b.WriteString(line)
		}
	}
	return b.String()
}

// teamDay aggregates all of one team's moves on a single date.
type teamDay struct {
	team    string
	added   []SidePlayer
	dropped []SidePlayer
}

func (t teamDay) net() int {
	var n int
	for _, p := range t.added {
		n += p.Value
	}
	for _, p := range t.dropped {
		n -= p.Value
	}
	return n
}

// aggregateByTeam merges a date's moves so each team appears once, with all its
// adds and drops combined and its net summed. Teams are ordered by net value
// descending, ties broken by team name. Insertion order of players is preserved.
func aggregateByTeam(moves []Move) []teamDay {
	idx := map[string]int{}
	out := make([]teamDay, 0, len(moves))
	for _, m := range moves {
		key := m.TeamID + "|" + m.TeamName
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, teamDay{team: m.TeamName})
		}
		out[i].added = append(out[i].added, m.Added...)
		out[i].dropped = append(out[i].dropped, m.Dropped...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].net() != out[j].net() {
			return out[i].net() > out[j].net()
		}
		return out[i].team < out[j].team
	})
	return out
}

// teamBlock renders one team's aggregated day: team and net on a line, then each
// added (+) and dropped (-) player on its own indented line beneath.
func teamBlock(t teamDay) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s (%+d)\n", indentTeam, t.team, t.net())
	for _, p := range t.added {
		fmt.Fprintf(&b, "%s+%s\n", indentPlayer, p.Name)
	}
	for _, p := range t.dropped {
		fmt.Fprintf(&b, "%s-%s\n", indentPlayer, p.Name)
	}
	return b.String()
}

// groupByDate buckets moves by processed calendar date and returns the buckets
// plus the date keys in chronological order. Within each bucket moves are sorted
// by net value descending; moves with no processed date bucket under "Undated"
// (sorted last).
func groupByDate(moves []Move) (map[string][]Move, []string) {
	groups := map[string][]Move{}
	for _, m := range moves {
		k := dateKey(m.ProcessedDate)
		groups[k] = append(groups[k], m)
	}
	dates := make([]string, 0, len(groups))
	for k := range groups {
		dates = append(dates, k)
		sort.SliceStable(groups[k], func(i, j int) bool {
			return groups[k][i].NetValue() > groups[k][j].NetValue()
		})
	}
	sort.Strings(dates) // "2006-01-02" keys sort chronologically; the undated sentinel sorts last
	return groups, dates
}

// undatedKey sorts after any real "2006-01-02" date key.
const undatedKey = "zzzz-undated"

func dateKey(t time.Time) string {
	if t.IsZero() {
		return undatedKey
	}
	return t.UTC().Format("2006-01-02")
}

func dateHeader(key string) string {
	if key == undatedKey {
		return "Undated"
	}
	t, err := time.Parse("2006-01-02", key)
	if err != nil {
		return key
	}
	return t.Format("Mon Jan 2")
}

func formatSignedValue(v int, color bool) string {
	sign := "+"
	mag := v
	if v < 0 {
		sign, mag = "-", -v
	}
	s := sign + formatValue(mag)
	if !color {
		return s
	}
	switch {
	case v > 0:
		return colorGreen + s + colorReset
	case v < 0:
		return colorRed + s + colorReset
	default:
		return colorDim + s + colorReset
	}
}

// formatValue formats an integer with comma separators (e.g. 12345 -> "12,345").
func formatValue(v int) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
