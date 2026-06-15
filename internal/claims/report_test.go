package claims

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func sampleMoves() []Move {
	jun9 := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	return []Move{
		{TeamName: "Aces", ClaimType: "WW", BidAmount: "12", ProcessedDate: jun9,
			Added:   []SidePlayer{{Name: "Added Guy", Position: "OF", Ranked: true, Value: 3000, Rank: 120}},
			Dropped: []SidePlayer{{Name: "Dropped Guy", Position: "SP", Ranked: true, Value: 1000}}},
		{TeamName: "Bandits", ClaimType: "FA", ProcessedDate: jun9,
			Added:   []SidePlayer{{Name: "Reach", Position: "1B", Ranked: true, Value: 200}},
			Dropped: []SidePlayer{{Name: "Good Drop", Position: "OF", Ranked: true, Value: 2500}}},
	}
}

func TestFormatSidePlayer_TrendAndStat(t *testing.T) {
	t.Run("hitter with positive trend and OPS", func(t *testing.T) {
		p := SidePlayer{
			Name: "Joe Hitter", Position: "OF", Ranked: true, Value: 3500, Rank: 45,
			Trend30D: 250,
			HasStats: true, IsPitcher: false, OPS: 0.812,
		}
		got := formatSidePlayer(p, false)
		if !strings.Contains(got, "▲+250") {
			t.Errorf("want ▲+250 in %q", got)
		}
		if !strings.Contains(got, ".812 OPS") {
			t.Errorf("want .812 OPS in %q", got)
		}
	})

	t.Run("pitcher with negative trend and ERA", func(t *testing.T) {
		p := SidePlayer{
			Name: "Ace Pitcher", Position: "SP", Ranked: true, Value: 4000, Rank: 30,
			Trend30D: -100,
			HasStats: true, IsPitcher: true, ERA: 3.14,
		}
		got := formatSidePlayer(p, true)
		if !strings.Contains(got, "▼-100") {
			t.Errorf("want ▼-100 in %q", got)
		}
		if !strings.Contains(got, "3.14 ERA") {
			t.Errorf("want 3.14 ERA in %q", got)
		}
	})

	t.Run("no trend or stats emits clean line", func(t *testing.T) {
		p := SidePlayer{Name: "Plain Player", Position: "RP", Ranked: true, Value: 1000, Rank: 200}
		got := formatSidePlayer(p, false)
		if strings.Contains(got, "▲") || strings.Contains(got, "▼") {
			t.Errorf("unexpected trend symbol in %q", got)
		}
		if strings.Contains(got, "ERA") || strings.Contains(got, "OPS") {
			t.Errorf("unexpected stat in %q", got)
		}
	})
}

func TestNotableDrops_FiltersByThreshold(t *testing.T) {
	drops := notableDrops(sampleMoves(), 2000)
	if len(drops) != 1 || drops[0].Name != "Good Drop" {
		t.Fatalf("want only Good Drop above 2000, got %+v", drops)
	}
}

func TestFormatReport_IncludesMovesAndLeaderboard(t *testing.T) {
	out := FormatReport(sampleMoves(), 2000, false)
	for _, want := range []string{
		"Aces", "Bandits", "Added Guy", "Good Drop", "+2,000",
		"Value Leaderboard", "Notable Drops (now available)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}

func TestFormatPushover_Truncates(t *testing.T) {
	msg := FormatPushover(sampleMoves())
	if len(msg) > 1024 {
		t.Errorf("pushover message exceeds 1024 chars: %d", len(msg))
	}
}

func TestFormatPushover_DateHeaderAndBareDrop(t *testing.T) {
	jun9 := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	jun10 := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	moves := []Move{
		// Claim with a paired drop — both must appear on one line, same date.
		{TeamName: "Aces", ProcessedDate: jun10,
			Added:   []SidePlayer{{Name: "New Guy", Value: 500}},
			Dropped: []SidePlayer{{Name: "Old Guy", Value: 100}}},
		// Bare drop (no add) — must render the dropped name, not "+—".
		{TeamName: "Bandits", ProcessedDate: jun9,
			Dropped: []SidePlayer{{Name: "Cut Vet", Value: 900}}},
	}
	msg := FormatPushover(moves)

	// Date headers present, chronological (Jun 9 group before Jun 10).
	if !strings.Contains(msg, "Jun 9") || !strings.Contains(msg, "Jun 10") {
		t.Errorf("missing date headers in:\n%s", msg)
	}
	if strings.Index(msg, "Jun 9") > strings.Index(msg, "Jun 10") {
		t.Errorf("date groups out of chronological order:\n%s", msg)
	}
	// A claim is stacked: team + net on one line, add and drop on their own lines.
	if !strings.Contains(msg, "Aces (+400)") {
		t.Errorf("want team+net line 'Aces (+400)', got:\n%s", msg)
	}
	for _, want := range []string{"+New Guy", "-Old Guy"} {
		if !strings.Contains(msg, want+"\n") {
			t.Errorf("claim should list %q on its own line, got:\n%s", want, msg)
		}
	}
	// Add and drop are NOT on one combined line anymore.
	if strings.Contains(msg, "+New Guy -Old Guy") {
		t.Errorf("add and drop should be on separate lines, got:\n%s", msg)
	}
	// Bare drop renders the dropped player, never "+—".
	if !strings.Contains(msg, "-Cut Vet") {
		t.Errorf("bare drop should render dropped name with '-', got:\n%s", msg)
	}
	if strings.Contains(msg, "+—") {
		t.Errorf("digest should not contain '+—':\n%s", msg)
	}
}

func TestFormatReport_IncludesDate(t *testing.T) {
	out := FormatReport(sampleMoves(), 2000, false)
	if !strings.Contains(out, "Jun 9") {
		t.Errorf("per-move header should include the processed date, got:\n%s", out)
	}
}

func TestFormatPushover_TruncatesLongInput(t *testing.T) {
	// Build ~60 moves with a multibyte UTF-8 name to verify we never byte-slice
	// mid-character and that the result stays within Pushover's limit.
	moves := make([]Move, 60)
	for i := range moves {
		moves[i] = Move{
			TeamName: "Team Alpha",
			Added: []SidePlayer{
				{Name: "Luis García", Position: "OF", Ranked: true, Value: 1000 + i},
			},
			Dropped: []SidePlayer{
				{Name: "Bench Warmer", Position: "SP", Ranked: true, Value: 500},
			},
		}
	}
	msg := FormatPushover(moves)
	if len(msg) > 1024 {
		t.Errorf("pushover message exceeds 1024 bytes: %d", len(msg))
	}
	if !utf8.ValidString(msg) {
		t.Errorf("pushover message is not valid UTF-8 — multibyte name was split")
	}
}
