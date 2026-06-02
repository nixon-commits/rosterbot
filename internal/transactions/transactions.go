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
	"github.com/nixon-commits/rosterbot/internal/playername"
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
	Level          string  // MLB, AAA, AA, etc.
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
	since := time.Now().Add(-24 * time.Hour)
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

	var pendingGrouped []Trade
	if len(pendingTrades) > 0 {
		pendingGrouped = groupPendingTrades(pendingTrades, lookup)
		fmt.Println(formatTrades("Pending Trades", pendingGrouped, true))
	}

	var executedGrouped []Trade
	if len(txs) > 0 {
		executedGrouped = groupTrades(txs, lookup)
		fmt.Println(formatTrades("Recent Trades", executedGrouped, true))
	}

	if dryRun {
		return nil
	}

	if pushoverUserKey == "" || pushoverAPIToken == "" {
		log.Println("Pushover credentials not set, skipping notification.")
		return nil
	}

	var notifyParts []string
	if len(pendingGrouped) > 0 {
		notifyParts = append(notifyParts, formatTrades("Pending Trades", pendingGrouped, false))
	}
	if len(executedGrouped) > 0 {
		notifyParts = append(notifyParts, formatTrades("Recent Trades", executedGrouped, false))
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

// nbsp is U+00A0 (non-breaking space). Used for indentation that survives
// Pushover's whitespace collapsing — regular leading spaces get stripped
// in push notifications, so the visual structure of the message would
// flatten without it. Renders as a normal-looking space everywhere else.
const nbsp = " "

// formatTrades produces a human-readable trade report with the given header.
// Layout (Pushover-friendly):
//
//	Header
//
//	Team A ⇄ Team B
//
//	Team A receives:
//	• Player Name (Pos), age N · flags
//	   #Rank · Value ▲+30d
//	   X.XX ERA · X.XX WHIP
//	Total: T (±diff)
//
//	Team B receives:
//	...
func formatTrades(header string, trades []Trade, color bool) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	indent := strings.Repeat(nbsp, 3)
	for _, t := range trades {
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s ⇄ %s\n", t.Sides[0].TeamName, t.Sides[1].TeamName)
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

			b.WriteString("\n")
			fmt.Fprintf(&b, "%s receives:\n", side.TeamName)
			for _, p := range side.Players {
				formatPlayer(&b, p, indent, color)
			}
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
				fmt.Fprintf(&b, "Total: %s%s (%s%s)%s\n", sideClr, formatValue(side.Total), diffSign, formatValue(absDiff), reset)
			} else {
				fmt.Fprintf(&b, "Total: %s\n", formatValue(side.Total))
			}
		}
	}
	return b.String()
}

// formatPlayer writes a multi-line player block. The first line is flush
// left with a bullet; the parameter lines (rank/value, stats) are indented
// using the given prefix so they visually nest under the player. Layout:
//
//   - Name (Pos), age N · flag1, flag2
//     #Rank · Value ▲+30d
//     X.XX ERA · X.XX WHIP   (or .OPS, omitted when no stats)
//
// Unranked players collapse to a single line: `• Name (Pos) — unranked`.
// indent should be NBSP-based so Pushover preserves it.
func formatPlayer(b *strings.Builder, p TradePlayer, indent string, color bool) {
	if !p.Ranked {
		fmt.Fprintf(b, "• %s (%s) — unranked\n", p.Name, p.Position)
		return
	}

	// Line 1: bullet, name, position, age, optional flags
	fmt.Fprintf(b, "• %s (%s), age %d", p.Name, p.Position, int(math.Floor(p.Age)))
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
		fmt.Fprintf(b, " · %s", strings.Join(flags, ", "))
	}
	b.WriteString("\n")

	// Line 2: rank · value · 30d trend, indented under bullet
	fmt.Fprintf(b, "%s#%d · %s ", indent, p.Rank, formatValue(p.Value))
	formatTrend(b, p.ValueChange30D, color)
	b.WriteString("\n")

	// Line 3 (only if stats present): performance line, same indent
	if p.HasStats {
		if p.IsPitcher {
			fmt.Fprintf(b, "%s%.2f ERA · %.2f WHIP\n", indent, p.ERA, p.WHIP)
		} else {
			fmt.Fprintf(b, "%s%s OPS\n", indent, formatOPS(p.OPS))
		}
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
	tp.Level = p.Level
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

// normalizeName normalizes a player name for cross-source matching.
func normalizeName(name string) string {
	return playername.Normalize(name)
}
