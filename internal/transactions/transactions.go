package transactions

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/pmurley/go-fantrax/models"
)

// TradeClient is the subset of fantrax.Client needed for trade fetching.
type TradeClient interface {
	GetRecentTrades(since time.Time) ([]models.Transaction, error)
	GetPendingTrades() ([]fantrax.PendingTrade, error)
}

// TradeSide represents one team's side of a trade.
type TradeSide struct {
	TeamName string
	Players  []TradePlayer
	Total    int
}

// TradePlayer is a player involved in a trade with their HKB value and metadata.
type TradePlayer struct {
	Name           string
	Position       string
	Value          int     // HKB value (0 if unranked)
	Ranked         bool    // true if found in HKB
	Age            float64 // decimal age (e.g. 25.8)
	Rank           int     // overall HKB dynasty rank
	ValueChange30D int     // value delta over last 30 days
	ValueChange7D  int     // value delta over last 7 days
	RankChange30D  int     // rank delta over last 30 days (negative = improved)
	Level          string  // MLB, AAA, AA, etc.
	Team           string  // MLB team abbreviation
	Prospect       bool
	FYPD           bool
	// Key stats — exactly one set is populated based on player type.
	IsPitcher bool
	HasStats  bool
	OPS       float64 // hitters only
	ERA       float64 // pitchers only
	WHIP      float64 // pitchers only
}

// Trade represents a grouped trade between two teams.
type Trade struct {
	ProcessedDate time.Time
	Sides         [2]TradeSide
}

// CheckTrades fetches recent and pending trades, values them via HKB, and sends a notification.
func CheckTrades(ft TradeClient, cacheDir string, pushoverUserKey, pushoverAPIToken string, dryRun bool) error {
	since := time.Now().Add(-48 * time.Hour) // TODO: revert to -24h after testing
	txs, err := ft.GetRecentTrades(since)
	if err != nil {
		return fmt.Errorf("get recent trades: %w", err)
	}

	pendingTrades, err := ft.GetPendingTrades()
	if err != nil {
		return fmt.Errorf("get pending trades: %w", err)
	}

	if len(txs) == 0 && len(pendingTrades) == 0 {
		log.Println("No trades in the last 24 hours and no pending trades.")
		return nil
	}

	players, err := hkb.GetPlayers(cacheDir)
	if err != nil {
		return fmt.Errorf("get HKB players: %w", err)
	}
	lookup := buildHKBLookup(players)

	// Pending trades section.
	if len(pendingTrades) > 0 {
		grouped := groupPendingTrades(pendingTrades, lookup)
		pendingReport := formatPendingReport(grouped, true)
		fmt.Println(pendingReport)
	}

	// Executed trades section.
	if len(txs) > 0 {
		trades := groupTrades(txs, lookup)
		report := formatReport(trades, true)
		fmt.Println(report)
	}

	if dryRun {
		return nil
	}

	if pushoverUserKey == "" || pushoverAPIToken == "" {
		log.Println("Pushover credentials not set, skipping notification.")
		return nil
	}

	// Notification includes both pending and executed.
	var notifyParts []string
	if len(pendingTrades) > 0 {
		grouped := groupPendingTrades(pendingTrades, lookup)
		notifyParts = append(notifyParts, formatPendingReport(grouped, false))
	}
	if len(txs) > 0 {
		trades := groupTrades(txs, lookup)
		notifyParts = append(notifyParts, formatReport(trades, false))
	}
	if len(notifyParts) > 0 {
		plain := strings.Join(notifyParts, "\n")
		if err := notify.SendPushover(pushoverUserKey, pushoverAPIToken, "Trade Alert", plain); err != nil {
			log.Printf("notification failed: %v", err)
		}
	}
	return nil
}

// buildHKBLookup creates a map from normalized player name to HKB player.
func buildHKBLookup(players []hkb.Player) map[string]hkb.Player {
	m := make(map[string]hkb.Player, len(players))
	for _, p := range players {
		m[normalizeName(p.Name)] = p
	}
	return m
}

// groupTrades groups transaction rows by TradeGroupID into Trade structs.
func groupTrades(txs []models.Transaction, lookup map[string]hkb.Player) []Trade {
	groups := make(map[string][]models.Transaction)
	for _, tx := range txs {
		groups[tx.TradeGroupID] = append(groups[tx.TradeGroupID], tx)
	}

	var trades []Trade
	for _, group := range groups {
		t := buildTrade(group, lookup)
		trades = append(trades, t)
	}
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].ProcessedDate.Before(trades[j].ProcessedDate)
	})
	return trades
}

// buildTrade constructs a Trade from a group of transactions sharing the same TradeGroupID.
func buildTrade(group []models.Transaction, lookup map[string]hkb.Player) Trade {
	// Partition by direction: players moving to each team.
	sides := make(map[string]*TradeSide)
	var processedDate time.Time

	for _, tx := range group {
		if tx.ProcessedDate.After(processedDate) {
			processedDate = tx.ProcessedDate
		}

		// ToTeamName receives the player.
		key := tx.ToTeamName
		side, ok := sides[key]
		if !ok {
			side = &TradeSide{TeamName: tx.ToTeamName}
			sides[key] = side
		}

		tp := newTradePlayer(tx.PlayerName, tx.PlayerPosition, lookup)
		side.Players = append(side.Players, tp)
		side.Total += tp.Value
	}

	trade := Trade{ProcessedDate: processedDate}
	i := 0
	for _, side := range sides {
		if i < 2 {
			trade.Sides[i] = *side
			i++
		}
	}
	return trade
}

const (
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
	colorDim   = "\033[2m"
	colorReset = "\033[0m"
)

// formatReport produces a human-readable trade report. When color is true,
// ANSI escape codes highlight the winning (green) and losing (red) side.
func formatReport(trades []Trade, color bool) string {
	return formatTrades("Recent Trades", trades, color)
}

// formatTrades is the shared formatter for both pending and executed trades.
func formatTrades(header string, trades []Trade, color bool) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	b.WriteString(strings.Repeat("─", 50) + "\n")
	for i, t := range trades {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Trade: %s <-> %s\n", t.Sides[0].TeamName, t.Sides[1].TeamName)
		for si, side := range t.Sides {
			other := t.Sides[1-si]
			diff := side.Total - other.Total
			var sideClr string
			if color {
				switch {
				case diff > 0:
					sideClr = colorGreen
				case diff < 0:
					sideClr = colorRed
				}
			}

			fmt.Fprintf(&b, "  %s receives:\n", side.TeamName)
			for _, p := range side.Players {
				b.WriteString("    ")
				formatPlayer(&b, p, color)
				b.WriteString("\n")
			}
			// Total line with diff.
			diffSign := "+"
			absDiff := diff
			if diff < 0 {
				diffSign = "-"
				absDiff = -diff
			}
			reset := ""
			if sideClr != "" {
				reset = colorReset
			}
			if diff != 0 {
				fmt.Fprintf(&b, "    Total: %s%s (%s%s)%s\n", sideClr, formatValue(side.Total), diffSign, formatValue(absDiff), reset)
			} else {
				fmt.Fprintf(&b, "    Total: %s\n", formatValue(side.Total))
			}
		}
	}
	return b.String()
}

// formatPlayer writes one inline player detail line.
// Format: Name (Pos) #Rank | Value ▲+30d | Age N | .OPS or ERA/WHIP | [flags]
func formatPlayer(b *strings.Builder, p TradePlayer, color bool) {
	if !p.Ranked {
		fmt.Fprintf(b, "%s (%s) unranked", p.Name, p.Position)
		return
	}

	// Name (Pos) #Rank
	fmt.Fprintf(b, "%s (%s) #%d", p.Name, p.Position, p.Rank)

	// | Value with 30d trend arrow
	b.WriteString(" | ")
	b.WriteString(formatValue(p.Value))
	b.WriteString(" ")
	formatTrend(b, p.ValueChange30D, color)

	// | Age
	fmt.Fprintf(b, " | Age %d", int(math.Floor(p.Age)))

	// | Key stat
	if p.HasStats {
		if p.IsPitcher {
			fmt.Fprintf(b, " | %.2f ERA / %.2f WHIP", p.ERA, p.WHIP)
		} else {
			fmt.Fprintf(b, " | %s OPS", formatOPS(p.OPS))
		}
	}

	// | Flags
	var flags []string
	if p.Prospect {
		flags = append(flags, "Prospect")
	}
	if p.FYPD {
		flags = append(flags, "FYPD")
	}
	if p.Level != "" && p.Level != "MLB" {
		flags = append(flags, p.Level)
	}
	if len(flags) > 0 {
		fmt.Fprintf(b, " | %s", strings.Join(flags, ", "))
	}
}

// formatTrend writes a value trend indicator: ▲+N, ▼-N, or ─ for no change.
func formatTrend(b *strings.Builder, delta int, color bool) {
	switch {
	case delta > 0:
		if color {
			b.WriteString(colorGreen)
		}
		fmt.Fprintf(b, "▲+%s", formatValue(delta))
		if color {
			b.WriteString(colorReset)
		}
	case delta < 0:
		if color {
			b.WriteString(colorRed)
		}
		fmt.Fprintf(b, "▼-%s", formatValue(-delta))
		if color {
			b.WriteString(colorReset)
		}
	default:
		if color {
			b.WriteString(colorDim)
		}
		b.WriteString("─")
		if color {
			b.WriteString(colorReset)
		}
	}
}

// formatOPS formats an OPS value like ".812" (no leading zero).
func formatOPS(ops float64) string {
	s := fmt.Sprintf("%.3f", ops)
	if strings.HasPrefix(s, "0") {
		return s[1:] // ".812" instead of "0.812"
	}
	return s // "1.012" stays as-is
}

// formatValue formats an HKB value integer with comma separators.
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

// groupPendingTrades groups pending trade moves by TradeID into Trade structs.
func groupPendingTrades(pts []fantrax.PendingTrade, lookup map[string]hkb.Player) []Trade {
	groups := make(map[string][]fantrax.PendingTrade)
	for _, pt := range pts {
		groups[pt.TradeID] = append(groups[pt.TradeID], pt)
	}

	var trades []Trade
	for _, group := range groups {
		sides := make(map[string]*TradeSide)
		for _, pt := range group {
			key := pt.ToTeam
			side, ok := sides[key]
			if !ok {
				side = &TradeSide{TeamName: pt.ToTeam}
				sides[key] = side
			}
			tp := newTradePlayer(pt.PlayerName, pt.Position, lookup)
			side.Players = append(side.Players, tp)
			side.Total += tp.Value
		}
		t := Trade{}
		i := 0
		for _, side := range sides {
			if i < 2 {
				t.Sides[i] = *side
				i++
			}
		}
		trades = append(trades, t)
	}
	return trades
}

// formatPendingReport produces a human-readable pending trade report.
func formatPendingReport(trades []Trade, color bool) string {
	return formatTrades("Pending Trades", trades, color)
}

// newTradePlayer creates a TradePlayer populated with HKB metadata if found.
func newTradePlayer(name, position string, lookup map[string]hkb.Player) TradePlayer {
	tp := TradePlayer{
		Name:     name,
		Position: position,
	}
	p, found := lookup[normalizeName(name)]
	if !found {
		return tp
	}
	tp.Ranked = true
	tp.Value = p.Value
	tp.Age = p.Age
	tp.Rank = p.Rank
	tp.ValueChange30D = p.ValueChange30Days
	tp.ValueChange7D = p.ValueChange7Days
	tp.RankChange30D = p.RankChange30Days
	tp.Level = p.Level
	tp.Team = p.Team
	tp.Prospect = p.Prospect
	tp.FYPD = p.FYPD
	if p.PitcherStats != nil {
		tp.IsPitcher = true
		tp.HasStats = true
		tp.ERA = p.PitcherStats.ERA
		tp.WHIP = p.PitcherStats.WHIP
	} else if p.HitterStats != nil {
		tp.HasStats = true
		tp.OPS = p.HitterStats.OPS
	}
	return tp
}

// normalizeName lowercases and strips common suffixes for name matching.
func normalizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{" jr.", " sr.", " iv", " iii", " ii"} {
		n = strings.TrimSuffix(n, suffix)
	}
	return strings.TrimSpace(n)
}
