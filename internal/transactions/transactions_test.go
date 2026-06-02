package transactions

import (
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/pmurley/go-fantrax/models"
)

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Bobby Witt Jr.", "bobby witt"},
		{"Vladimir Guerrero Jr.", "vladimir guerrero"},
		{"Ken Griffey Sr.", "ken griffey"},
		{"Ronald Acuña Jr.", "ronald acuna"},
		{"Mike Trout", "mike trout"},
		{"  Juan Soto  ", "juan soto"},
		{"Cal Ripken III", "cal ripken"},
		{"Ken Griffey II", "ken griffey"},
	}
	for _, tt := range tests {
		if got := normalizeName(tt.input); got != tt.want {
			t.Errorf("normalizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildHKBLookup(t *testing.T) {
	players := []hkb.Player{
		{Name: "Bobby Witt Jr.", Value: 10000},
		{Name: "Juan Soto", Value: 8782},
	}
	lookup := buildHKBLookup(players)

	if p, ok := lookup["bobby witt"]; !ok || p.Value != 10000 {
		t.Error("expected Bobby Witt Jr. lookup to work")
	}
	if p, ok := lookup["juan soto"]; !ok || p.Value != 8782 {
		t.Error("expected Juan Soto lookup to work")
	}
}

func TestNewTradePlayer(t *testing.T) {
	lookup := buildHKBLookup([]hkb.Player{
		{
			Name: "Bobby Witt Jr.", Value: 10000, Age: 25.8, Rank: 1,
			ValueChange30Days: 200, ValueChange7Days: 50, RankChange30Days: -1,
			Level: "MLB", Team: "KC", Prospect: false,
			HitterStats: &hkb.HitterStats{OPS: 0.912},
		},
		{
			Name: "Cade Cavalli", Value: 1200, Age: 26.1, Rank: 120,
			ValueChange30Days: -150, Level: "MLB", Team: "WSH",
			PitcherStats: &hkb.PitcherStats{ERA: 3.45, WHIP: 1.12},
		},
		{
			Name: "James Wood", Value: 3500, Age: 21.3, Rank: 30,
			ValueChange30Days: 400, Level: "AAA", Team: "WSH", Prospect: true, FYPD: true,
			HitterStats: &hkb.HitterStats{OPS: 0.845},
		},
	})

	t.Run("ranked hitter", func(t *testing.T) {
		tp := newTradePlayer("Bobby Witt Jr.", "SS", lookup)
		if !tp.Ranked {
			t.Fatal("expected ranked")
		}
		if tp.Value != 10000 {
			t.Errorf("Value = %d, want 10000", tp.Value)
		}
		if tp.Age != 25.8 {
			t.Errorf("Age = %f, want 25.8", tp.Age)
		}
		if tp.Rank != 1 {
			t.Errorf("Rank = %d, want 1", tp.Rank)
		}
		if tp.ValueChange30D != 200 {
			t.Errorf("ValueChange30D = %d, want 200", tp.ValueChange30D)
		}
		if tp.IsPitcher {
			t.Error("expected hitter, not pitcher")
		}
		if !tp.HasStats || tp.OPS != 0.912 {
			t.Errorf("OPS = %f, want 0.912", tp.OPS)
		}
	})

	t.Run("ranked pitcher", func(t *testing.T) {
		tp := newTradePlayer("Cade Cavalli", "SP", lookup)
		if !tp.IsPitcher || !tp.HasStats {
			t.Fatal("expected pitcher with stats")
		}
		if tp.ERA != 3.45 {
			t.Errorf("ERA = %f, want 3.45", tp.ERA)
		}
		if tp.WHIP != 1.12 {
			t.Errorf("WHIP = %f, want 1.12", tp.WHIP)
		}
		if tp.ValueChange30D != -150 {
			t.Errorf("ValueChange30D = %d, want -150", tp.ValueChange30D)
		}
	})

	t.Run("prospect flags", func(t *testing.T) {
		tp := newTradePlayer("James Wood", "OF", lookup)
		if !tp.Prospect {
			t.Error("expected Prospect flag")
		}
		if !tp.FYPD {
			t.Error("expected FYPD flag")
		}
		if tp.Level != "AAA" {
			t.Errorf("Level = %q, want AAA", tp.Level)
		}
	})

	t.Run("unranked player", func(t *testing.T) {
		tp := newTradePlayer("Nobody McFakename", "OF", lookup)
		if tp.Ranked {
			t.Error("expected unranked")
		}
		if tp.Value != 0 {
			t.Errorf("Value = %d, want 0", tp.Value)
		}
	})
}

func TestGroupTrades(t *testing.T) {
	now := time.Now()
	txs := []models.Transaction{
		{
			TradeGroupID:   "trade1",
			ToTeamName:     "Team Alpha",
			FromTeamName:   "Team Beta",
			PlayerName:     "Bobby Witt Jr.",
			PlayerPosition: "SS",
			ProcessedDate:  now,
		},
		{
			TradeGroupID:   "trade1",
			ToTeamName:     "Team Beta",
			FromTeamName:   "Team Alpha",
			PlayerName:     "Juan Soto",
			PlayerPosition: "OF",
			ProcessedDate:  now,
		},
		{
			TradeGroupID:   "trade1",
			ToTeamName:     "Team Beta",
			FromTeamName:   "Team Alpha",
			PlayerName:     "Mike Trout",
			PlayerPosition: "OF",
			ProcessedDate:  now,
		},
	}

	lookup := buildHKBLookup([]hkb.Player{
		{Name: "Bobby Witt Jr.", Value: 10000},
		{Name: "Juan Soto", Value: 8782},
	})

	trades := groupTrades(txs, lookup)
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}

	trade := trades[0]
	// Find which side is Team Alpha and which is Team Beta.
	var alpha, beta *TradeSide
	for i := range trade.Sides {
		switch trade.Sides[i].TeamName {
		case "Team Alpha":
			alpha = &trade.Sides[i]
		case "Team Beta":
			beta = &trade.Sides[i]
		}
	}
	if alpha == nil || beta == nil {
		t.Fatal("expected both Team Alpha and Team Beta sides")
	}

	if len(alpha.Players) != 1 {
		t.Errorf("Team Alpha should receive 1 player, got %d", len(alpha.Players))
	}
	if alpha.Total != 10000 {
		t.Errorf("Team Alpha total = %d, want 10000", alpha.Total)
	}

	if len(beta.Players) != 2 {
		t.Errorf("Team Beta should receive 2 players, got %d", len(beta.Players))
	}
	if beta.Total != 8782 {
		t.Errorf("Team Beta total = %d, want 8782 (Soto=8782, Trout=unranked=0)", beta.Total)
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{500, "500"},
		{1000, "1,000"},
		{10000, "10,000"},
		{1234567, "1,234,567"},
	}
	for _, tt := range tests {
		if got := formatValue(tt.input); got != tt.want {
			t.Errorf("formatValue(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatOPS(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0.812, ".812"},
		{0.900, ".900"},
		{1.012, "1.012"},
		{0.000, ".000"},
	}
	for _, tt := range tests {
		if got := formatOPS(tt.input); got != tt.want {
			t.Errorf("formatOPS(%f) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatTrend(t *testing.T) {
	tests := []struct {
		name  string
		delta int
		color bool
		want  string
	}{
		{"positive no color", 200, false, "▲+200"},
		{"negative no color", -150, false, "▼-150"},
		{"zero no color", 0, false, "─"},
		{"positive with color", 200, true, colorGreen + "▲+200" + colorReset},
		{"negative with color", -150, true, colorRed + "▼-150" + colorReset},
		{"large positive", 1500, false, "▲+1,500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			formatTrend(&b, tt.delta, tt.color)
			if got := b.String(); got != tt.want {
				t.Errorf("formatTrend(%d, %v) = %q, want %q", tt.delta, tt.color, got, tt.want)
			}
		})
	}
}

func TestFormatPlayer(t *testing.T) {
	t.Run("ranked hitter with stats", func(t *testing.T) {
		p := TradePlayer{
			Name: "Bobby Witt Jr.", Position: "SS", Value: 10000, Ranked: true,
			Age: 25.8, Rank: 1, ValueChange30D: 200, Level: "MLB",
			HasStats: true, OPS: 0.912,
		}
		var b strings.Builder
		formatPlayer(&b, p, "", false)
		got := b.String()
		assertContains(t, got, "• Bobby Witt Jr. (SS), age 25")
		assertContains(t, got, "#1 · 10,000 ▲+200")
		assertContains(t, got, ".912 OPS")
	})

	t.Run("ranked pitcher", func(t *testing.T) {
		p := TradePlayer{
			Name: "Cade Cavalli", Position: "SP", Value: 1200, Ranked: true,
			Age: 26.1, Rank: 120, ValueChange30D: -150, Level: "MLB",
			IsPitcher: true, HasStats: true, ERA: 3.45, WHIP: 1.12,
		}
		var b strings.Builder
		formatPlayer(&b, p, "", false)
		got := b.String()
		assertContains(t, got, "• Cade Cavalli (SP), age 26")
		assertContains(t, got, "#120 · 1,200 ▼-150")
		assertContains(t, got, "3.45 ERA · 1.12 WHIP")
	})

	t.Run("prospect with flags", func(t *testing.T) {
		p := TradePlayer{
			Name: "James Wood", Position: "OF", Value: 3500, Ranked: true,
			Age: 21.3, Rank: 30, ValueChange30D: 400, Level: "AAA",
			Prospect: true, FYPD: true, HasStats: true, OPS: 0.845,
		}
		var b strings.Builder
		formatPlayer(&b, p, "", false)
		got := b.String()
		assertContains(t, got, "Prospect")
		assertContains(t, got, "FYPD")
		assertContains(t, got, "AAA")
	})

	t.Run("unranked player", func(t *testing.T) {
		p := TradePlayer{Name: "Nobody", Position: "OF"}
		var b strings.Builder
		formatPlayer(&b, p, "", false)
		got := b.String()
		if got != "• Nobody (OF) — unranked\n" {
			t.Errorf("got %q, want %q", got, "• Nobody (OF) — unranked\n")
		}
	})
}

func TestFormatReport(t *testing.T) {
	trades := []Trade{
		{
			ProcessedDate: time.Now(),
			Sides: [2]TradeSide{
				{
					TeamName: "Team A",
					Players: []TradePlayer{
						{Name: "Player X", Position: "SS", Value: 5200, Ranked: true, Age: 24, Rank: 10, ValueChange30D: 100, Level: "MLB", HasStats: true, OPS: 0.812},
					},
					Total: 5200,
				},
				{
					TeamName: "Team B",
					Players: []TradePlayer{
						{Name: "Player Y", Position: "OF", Value: 0, Ranked: false},
					},
					Total: 0,
				},
			},
		},
	}

	report := formatTrades("Recent Trades", trades, true)
	if report == "" {
		t.Fatal("expected non-empty report")
	}
	assertContains(t, report, "Recent Trades")
	assertContains(t, report, "Team A ⇄ Team B")
	assertContains(t, report, "• Player X (SS), age 24")
	assertContains(t, report, "#10 ·")
	assertContains(t, report, ".812 OPS")
	assertContains(t, report, "• Player Y (OF) — unranked")
	assertContains(t, report, colorGreen)
	assertContains(t, report, colorRed)
	assertContains(t, report, "Total:")
}

func TestGroupPendingTrades(t *testing.T) {
	pts := []fantrax.PendingTrade{
		{PlayerName: "Cade Cavalli", Position: "SP", FromTeam: "Team A", ToTeam: "Team B", TradeID: "t1"},
		{PlayerName: "Noelvi Marte", Position: "3B,INF,OF", FromTeam: "Team B", ToTeam: "Team A", TradeID: "t1"},
	}
	lookup := buildHKBLookup([]hkb.Player{
		{Name: "Cade Cavalli", Value: 1200},
		{Name: "Noelvi Marte", Value: 4500},
	})

	trades := groupPendingTrades(pts, lookup)
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}

	trade := trades[0]
	var sideA, sideB *TradeSide
	for i := range trade.Sides {
		switch trade.Sides[i].TeamName {
		case "Team A":
			sideA = &trade.Sides[i]
		case "Team B":
			sideB = &trade.Sides[i]
		}
	}
	if sideA == nil || sideB == nil {
		t.Fatal("expected both sides")
	}
	if sideA.Total != 4500 {
		t.Errorf("Team A total = %d, want 4500", sideA.Total)
	}
	if sideB.Total != 1200 {
		t.Errorf("Team B total = %d, want 1200", sideB.Total)
	}
}

func TestFormatPendingReport(t *testing.T) {
	trades := []Trade{
		{
			Sides: [2]TradeSide{
				{TeamName: "Team A", Players: []TradePlayer{{Name: "P1", Position: "SP", Value: 1200, Ranked: true, Rank: 80, Age: 25, Level: "MLB"}}, Total: 1200},
				{TeamName: "Team B", Players: []TradePlayer{{Name: "P2", Position: "3B", Value: 4500, Ranked: true, Rank: 40, Age: 23, Level: "MLB"}}, Total: 4500},
			},
		},
	}
	report := formatTrades("Pending Trades", trades, false)
	assertContains(t, report, "Pending Trades")
	assertContains(t, report, "Team A ⇄ Team B")
	assertContains(t, report, "P1 (SP), age 25")
	assertContains(t, report, "#80 ·")
	assertContains(t, report, "P2 (3B), age 23")
	assertContains(t, report, "#40 ·")
	assertContains(t, report, "Total:")
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q, got:\n%s", substr, s)
	}
}
