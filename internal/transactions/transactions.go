package transactions

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/jobwire"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/pushover"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
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

	// Total is the plain sum of HKB values; Adjusted discounts every asset
	// after the best by tradevalue.PackageDiscount. Both are reported because
	// on live offers they disagree about who wins -- see Trade.Verdict.
	Total    int
	Adjusted float64
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
	// IsPick marks a draft pick. Fantrax sends these as a row with an empty
	// player name, which used to render as a nameless bullet with no value —
	// indistinguishable from an unranked player, though HKB prices picks as
	// high as 1419.
	IsPick bool
	// Estimated marks a pick priced by averaging HKB's Early/Mid/Late tiers
	// for its year+round rather than a direct name match (rosterbot-uc3).
	// Always false for a player.
	Estimated bool

	// Key stats — exactly one set is populated based on player type.
	IsPitcher bool
	HasStats  bool
	OPS       float64 // hitters only
	ERA       float64 // pitchers only
	WHIP      float64 // pitchers only
}

// Trade represents a grouped trade between two or more teams.
//
// Sides is a slice, not a [2]TradeSide. The array silently dropped every side
// past the second: both group builders filled it from a map under `if i < 2`,
// so a three-team trade lost a third of itself and the report still printed a
// confident total. None have occurred in this league yet, which is exactly why
// it would have gone unnoticed. Sides is ordered by team name so the same
// trade renders identically on every run -- the map iteration it replaced put
// the two teams in an arbitrary order each time.
type Trade struct {
	ProcessedDate time.Time
	Sides         []TradeSide

	// Verdict is tradevalue's judgement, shared with the dashboard's Trades
	// tab so the two never contradict each other. It is deliberately withheld
	// when the sides disagree under the two pricing methods, or when an asset
	// (a draft pick) could not be priced at all.
	Verdict tradevalue.Verdict
}

// CheckTrades fetches recent and pending trades, values them via HKB, and sends a notification.
func CheckTrades(ctx context.Context, ft TradeClient, cacheDir string, dryRun bool) error {
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
		executedGrouped = groupTrades(txs, lookup, players)
		fmt.Println(formatTrades("Recent Trades", executedGrouped, true))
	}

	allTrades := append(append([]Trade{}, pendingGrouped...), executedGrouped...)
	jobwire.RecordOutput("transactions", toWireResult(allTrades))

	if dryRun {
		return nil
	}

	if body := notifyBody(pendingGrouped, executedGrouped); body != "" {
		if err := notify.Send(ctx, notify.Event{Kind: "transactions", Title: "Trade Alert", Message: body}); err != nil {
			log.Printf("notification failed: %v", err)
		}
	}
	return nil
}

// buildHKBLookup creates a map from normalized player name to HKB player.
func buildHKBLookup(players []hkb.Player) hkb.Lookup {
	return hkb.BuildLookup(players)
}

// groupTrades groups transaction rows by TradeGroupID into Trade structs.
// players is the full HKB roster (not just the name lookup) because pick
// pricing needs to scan every PICK asset for a year+round match rather than
// join by name (rosterbot-uc3).
func groupTrades(txs []models.Transaction, lookup hkb.Lookup, players []hkb.Player) []Trade {
	groups := make(map[string][]models.Transaction)
	for _, tx := range txs {
		groups[tx.TradeGroupID] = append(groups[tx.TradeGroupID], tx)
	}

	var trades []Trade
	for _, group := range groups {
		t := buildTrade(group, lookup, players)
		trades = append(trades, t)
	}
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].ProcessedDate.Before(trades[j].ProcessedDate)
	})
	return trades
}

// buildTrade constructs a Trade from a group of transactions sharing the same TradeGroupID.
func buildTrade(group []models.Transaction, lookup hkb.Lookup, players []hkb.Player) Trade {
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

		var tp TradePlayer
		if tx.IsDraftPick {
			tp = newDraftPickTradePlayer(tx, players)
		} else {
			tp = newTradePlayer(tx.PlayerName, tx.PlayerPosition, lookup)
		}
		side.Players = append(side.Players, tp)
	}

	trade := Trade{ProcessedDate: processedDate}
	trade.Sides, trade.Verdict = assembleSides(sides)
	return trade
}

// assembleSides turns the by-receiving-team accumulator into a deterministic
// slice and prices it through tradevalue.
//
// The valuation lives in tradevalue rather than here so this report and the
// dashboard's Trades tab cannot drift apart. Before this, the two carried
// separate models: the Pushover message summed raw HKB values and declared a
// winner, while the tab withheld the call whenever the package-adjusted sum
// named the other side. On the live 2-for-1 that motivated the tab, those are
// opposite answers.
func assembleSides(sides map[string]*TradeSide) ([]TradeSide, tradevalue.Verdict) {
	names := make([]string, 0, len(sides))
	for name := range sides {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]TradeSide, 0, len(names))
	valued := make([]tradevalue.Side, 0, len(names))
	for _, name := range names {
		s := *sides[name]
		vs := tradevalue.Side{Team: name, Assets: assetsOf(s.Players)}
		s.Total = vs.Raw()
		s.Adjusted = vs.Adjusted()
		out = append(out, s)
		valued = append(valued, vs)
	}
	return out, tradevalue.Evaluate(valued)
}

// assetsOf maps the report's display records onto tradevalue's pricing view.
// Ranked is the pricing bit: an unranked player and an unidentified draft pick
// are both unpriced, and either one is enough to suppress the verdict.
func assetsOf(players []TradePlayer) []tradevalue.Asset {
	assets := make([]tradevalue.Asset, 0, len(players))
	for _, p := range players {
		assets = append(assets, tradevalue.Asset{
			Name:      p.Name,
			Position:  p.Position,
			Value:     p.Value,
			Priced:    p.Ranked,
			IsPick:    p.IsPick,
			Estimated: p.Estimated,
		})
	}
	return assets
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
//	→ verdict
func formatTrades(header string, trades []Trade, color bool) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, t := range trades {
		b.WriteString(formatTrade(t, color))
	}
	return b.String()
}

// formatTrade renders one trade as a self-contained block, leading blank
// line included: the teams line, each side's players and totals, and the
// verdict — or "" for a trade with no sides. Shared by formatTrades (stdout,
// color) and notifyBody (the budgeted dispatcher message), so the two
// renderings cannot fork and the budget drops whole trades, never lines.
func formatTrade(t Trade, color bool) string {
	if len(t.Sides) == 0 {
		return ""
	}
	var b strings.Builder
	indent := strings.Repeat(nbsp, 3)
	b.WriteString("\n")
	names := make([]string, 0, len(t.Sides))
	for _, s := range t.Sides {
		names = append(names, s.TeamName)
	}
	fmt.Fprintf(&b, "%s\n", strings.Join(names, " ⇄ "))

	for si, side := range t.Sides {
		// The pairwise "(±diff)" only means something with exactly two
		// sides. With three it would compare against an arbitrary one of
		// the other two, so the verdict line below carries the comparison
		// instead.
		diff := 0
		if len(t.Sides) == 2 {
			diff = side.Total - t.Sides[1-si].Total
		}
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
		reset := ""
		if sideClr != "" {
			reset = colorReset
		}
		// Adjusted is shown only when it differs from raw, i.e. when the
		// side is a multi-asset package — for a one-for-one they are equal
		// by construction and the second number is noise.
		adj := ""
		if math.Abs(float64(side.Total)-side.Adjusted) >= 1 {
			adj = fmt.Sprintf(" · adj %s", hkb.FormatValue(int(math.Round(side.Adjusted))))
		}
		if diff != 0 {
			diffSign := "+"
			absDiff := diff
			if diff < 0 {
				diffSign = "-"
				absDiff = -diff
			}
			fmt.Fprintf(&b, "Total: %s%s (%s%s)%s%s\n", sideClr, hkb.FormatValue(side.Total), diffSign, hkb.FormatValue(absDiff), reset, adj)
		} else {
			fmt.Fprintf(&b, "Total: %s%s\n", hkb.FormatValue(side.Total), adj)
		}
	}
	fmt.Fprintf(&b, "→ %s\n", formatVerdict(t.Verdict))
	return b.String()
}

// formatVerdict renders tradevalue's judgement as one line. It says "too close
// to call" rather than naming a winner whenever the raw and package-adjusted
// sums disagree — the case the raw-only report used to call confidently and
// wrongly.
func formatVerdict(v tradevalue.Verdict) string {
	switch v.Status {
	case tradevalue.StatusFavors:
		return fmt.Sprintf("favors %s (raw %.1f%%, adj %.1f%%)", v.FavoredTeam, v.RawPct, v.AdjPct)
	case tradevalue.StatusDeadEven:
		// Everything priced and both methods exactly level. Naming the sides
		// would be the rosterbot-h688 bug restated in prose.
		return "no verdict — the leading sides price exactly level"
	case tradevalue.StatusTooClose:
		return fmt.Sprintf("too close to call — raw %s, adjusted %s",
			tradevalue.MethodReading(v.RawLeader, v.RawPct), tradevalue.MethodReading(v.AdjLeader, v.AdjPct))
	default:
		return fmt.Sprintf("no verdict — %d asset(s) could not be priced", v.UnpricedAssets)
	}
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
	// A pick with a recovered identity (rosterbot-uc3) prices at the average
	// of HKB's Early/Mid/Late tiers for its year+round -- marked "~" and
	// "avg" since it is a coarser estimate than a direct name match, not the
	// player-line format below. An unidentified or already-drafted pick
	// falls back to a bare name: HKB does price those picks, we just cannot
	// tell which one this is or it no longer lists one to average.
	if p.IsPick {
		if p.Ranked {
			fmt.Fprintf(b, "• %s\n%s~%s (avg of Early/Mid/Late)\n", p.Name, indent, hkb.FormatValue(p.Value))
		} else {
			fmt.Fprintf(b, "• %s\n", p.Name)
		}
		return
	}
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
	fmt.Fprintf(b, "%s#%d · %s ", indent, p.Rank, hkb.FormatValue(p.Value))
	formatTrend(b, p.ValueChange30D, color)
	b.WriteString("\n")

	// Line 3 (only if stats present): performance line, same indent
	if p.HasStats {
		if p.IsPitcher {
			fmt.Fprintf(b, "%s%.2f ERA · %.2f WHIP\n", indent, p.ERA, p.WHIP)
		} else {
			fmt.Fprintf(b, "%s%s OPS\n", indent, hkb.FormatOPS(p.OPS))
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
		fmt.Fprintf(b, "▲+%s", hkb.FormatValue(delta))
		if color {
			b.WriteString(colorReset)
		}
	case delta < 0:
		if color {
			b.WriteString(colorRed)
		}
		fmt.Fprintf(b, "▼-%s", hkb.FormatValue(-delta))
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

// groupPendingTrades groups pending trade moves by TradeID into Trade structs.
func groupPendingTrades(pts []fantrax.PendingTrade, lookup hkb.Lookup) []Trade {
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
		}
		t := Trade{}
		t.Sides, t.Verdict = assembleSides(sides)
		trades = append(trades, t)
	}
	// Pending trades carry no processed date to sort by, so order on TradeID:
	// ranging the groups map put them in a different order every run.
	sort.Slice(trades, func(i, j int) bool { return tradeKey(trades[i]) < tradeKey(trades[j]) })
	return trades
}

// tradeKey is a stable ordering key for a trade with no timestamp: the
// concatenated side names, which are themselves already sorted.
func tradeKey(t Trade) string {
	names := make([]string, 0, len(t.Sides))
	for _, s := range t.Sides {
		names = append(names, s.TeamName)
	}
	return strings.Join(names, "\x00")
}

// newTradePlayer creates a TradePlayer populated with HKB metadata if found.
//
// An empty name is Fantrax's representation of a draft pick, labelled here the
// same way tradevalue.NewAsset labels it so the report and the tab call the
// same thing by the same name.
func newTradePlayer(name, position string, lookup hkb.Lookup) TradePlayer {
	if name == "" {
		return TradePlayer{Name: "Draft pick (unidentified)", IsPick: true}
	}
	e := hkb.Enrich(name, position, lookup, false)
	return TradePlayer{
		Name:           e.Name,
		Position:       e.Position,
		Value:          e.Value,
		Ranked:         e.Ranked,
		Age:            e.Age,
		Rank:           e.Rank,
		ValueChange30D: e.ValueChange30Days,
		Level:          e.Level,
		Prospect:       e.Prospect,
		FYPD:           e.FYPD,
		IsPitcher:      e.IsPitcher,
		HasStats:       e.HasStats,
		OPS:            e.OPS,
		ERA:            e.ERA,
		WHIP:           e.WHIP,
	}
}

// newDraftPickTradePlayer builds a TradePlayer for a row identified as a
// draft pick (tx.IsDraftPick), using the identity go-fantrax recovered from
// the row's draftPickDisplayParts (rosterbot-uc3) rather than the
// "Draft pick (unidentified)" fallback newTradePlayer uses for a bare empty
// name.
func newDraftPickTradePlayer(tx models.Transaction, players []hkb.Player) TradePlayer {
	a := tradevalue.NewDraftPickAsset(tx.DraftPickYear, tx.DraftPickRound, tx.DraftPickNumber, tx.DraftPickOriginalTeam, players)
	return TradePlayer{
		Name:      a.Name,
		Value:     a.Value,
		Ranked:    a.Priced,
		IsPick:    true,
		Estimated: a.Estimated,
	}
}

// notifyBody assembles the dispatcher message from whole per-trade blocks
// under pushover.MaxMessageLen. This formatter used to reach pushover.Send
// with no budget at all — the only protection was Send's raw byte-slice
// backstop, i.e. a mid-rune cut on exactly the multibyte names the other
// formatters went out of their way to keep whole. Sections mirror
// formatTrades' rendering (formatTrade is shared); a section whose trades do
// not all fit gains a "+N more" line naming the dropped count.
func notifyBody(pending, executed []Trade) string {
	var b pushover.Builder
	first := true
	section := func(header string, trades []Trade) {
		if len(trades) == 0 {
			return
		}
		h := header + "\n"
		if !first {
			h = "\n" + h
		}
		if !b.Add(h) {
			return
		}
		first = false
		dropped := 0
		for _, t := range trades {
			if !b.Add(formatTrade(t, false)) {
				dropped++
			}
		}
		if dropped > 0 {
			b.Add(fmt.Sprintf("\n+%d more\n", dropped))
		}
	}
	section("Pending Trades", pending)
	section("Recent Trades", executed)
	return b.String()
}
