package claims

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/waivers"
)

const (
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
	colorDim   = "\033[2m"
	colorReset = "\033[0m"
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
		added := "—"
		if len(m.Added) > 0 {
			added = m.Added[0].Name
		}
		fmt.Fprintf(b, "%s%d. %s (%s) %s\n", nbsp, i+1, added, m.TeamName, formatSignedValue(m.NetValue(), color))
	}
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

// FormatPushover renders a compact one-line-per-move digest, appending whole
// lines until the next would exceed Pushover's 1024-char limit (byte-slicing
// would split multibyte UTF-8 names, so we break on whole lines instead).
func FormatPushover(moves []Move) string {
	var b strings.Builder
	for _, m := range moves {
		added := "—"
		if len(m.Added) > 0 {
			added = m.Added[0].Name
		}
		line := fmt.Sprintf("%s: +%s (%+d)\n", m.TeamName, added, m.NetValue())
		if b.Len()+len(line) > 1024 {
			break
		}
		b.WriteString(line)
	}
	return b.String()
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
