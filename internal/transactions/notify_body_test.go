package transactions

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/pushover"
)

// bulkyTrade builds a trade whose rendered block is large and carries a
// unique begin/end sentinel pair (the two team names), so a test can assert
// block atomicity: for any trade, either the whole block made it into the
// body or none of it did.
func bulkyTrade(i int) Trade {
	side := func(team string) TradeSide {
		players := make([]TradePlayer, 6)
		for p := range players {
			players[p] = TradePlayer{
				Name:     fmt.Sprintf("Journeyman Utilityman %d-%d", i, p),
				Position: "OF",
				Value:    500 + p,
				Ranked:   true,
			}
		}
		return TradeSide{TeamName: team, Players: players, Total: 3000, Adjusted: 3000}
	}
	return Trade{Sides: []TradeSide{
		side(fmt.Sprintf("TRADE%dBEGIN", i)),
		side(fmt.Sprintf("TRADE%dEND", i)),
	}}
}

// TestNotifyBody_FitsTheCapOnWholeTradeBlocks pins the fix for the one
// formatter that used to send with NO length budgeting at all: an oversized
// trade digest reached pushover.Send raw and was byte-sliced mid-rune. The
// notify body must now fit MaxMessageLen by dropping whole trades, never by
// splitting one, and must say how many were dropped.
func TestNotifyBody_FitsTheCapOnWholeTradeBlocks(t *testing.T) {
	var executed []Trade
	for i := 0; i < 8; i++ {
		executed = append(executed, bulkyTrade(i))
	}

	body := notifyBody(nil, executed)

	if len(body) > pushover.MaxMessageLen {
		t.Fatalf("notify body = %d bytes, want <= %d", len(body), pushover.MaxMessageLen)
	}
	if !strings.Contains(body, "Recent Trades") {
		t.Fatal("section header missing from notify body")
	}
	included := 0
	for i := 0; i < 8; i++ {
		hasBegin := strings.Contains(body, fmt.Sprintf("TRADE%dBEGIN", i))
		hasEnd := strings.Contains(body, fmt.Sprintf("TRADE%dEND", i))
		if hasBegin != hasEnd {
			t.Fatalf("trade %d was split mid-block (begin=%v end=%v)", i, hasBegin, hasEnd)
		}
		if hasBegin {
			included++
		}
	}
	if included == 0 {
		t.Fatal("no trade block fit at all — the budget is being spent on something else")
	}
	if included == 8 {
		t.Fatal("fixture too small to force truncation — the test proves nothing")
	}
	if !strings.Contains(body, fmt.Sprintf("+%d more", 8-included)) {
		t.Fatalf("dropped trades are not counted; body:\n%s", body)
	}
}

// slimTrade is bulkyTrade's small sibling: one short-named player per side,
// so a couple of them comfortably fit one message.
func slimTrade(i int) Trade {
	side := func(team string) TradeSide {
		return TradeSide{
			TeamName: team,
			Players:  []TradePlayer{{Name: "A. Util", Position: "OF", Value: 500, Ranked: true}},
			Total:    500, Adjusted: 500,
		}
	}
	return Trade{Sides: []TradeSide{
		side(fmt.Sprintf("TRADE%dBEGIN", i)),
		side(fmt.Sprintf("TRADE%dEND", i)),
	}}
}

// TestNotifyBody_SmallDigestIsCompleteAndUnannotated: when everything fits,
// the body carries every trade and no "+N more" line.
func TestNotifyBody_SmallDigestIsCompleteAndUnannotated(t *testing.T) {
	pending := []Trade{slimTrade(0)}
	executed := []Trade{slimTrade(1)}

	body := notifyBody(pending, executed)

	for _, want := range []string{"Pending Trades", "Recent Trades", "TRADE0BEGIN", "TRADE0END", "TRADE1BEGIN", "TRADE1END"} {
		if !strings.Contains(body, want) {
			t.Fatalf("notify body missing %q", want)
		}
	}
	if strings.Contains(body, "more") {
		t.Fatalf("nothing was dropped, so no '+N more' line belongs; body:\n%s", body)
	}
}

// TestNotifyBody_MatchesFormatTradesRendering pins that the per-trade block
// the budgeter assembles is the SAME rendering formatTrades produces for
// stdout — the extraction must not fork the format.
func TestNotifyBody_MatchesFormatTradesRendering(t *testing.T) {
	trades := []Trade{bulkyTrade(0)}
	want := formatTrades("Recent Trades", trades, false)
	got := notifyBody(nil, trades)
	if got != want {
		t.Fatalf("notifyBody diverges from formatTrades for a fitting digest:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
