package claims

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func sampleMoves() []Move {
	return []Move{
		{TeamName: "Aces", ClaimType: "WW", BidAmount: "12",
			Added:   []SidePlayer{{Name: "Added Guy", Position: "OF", Ranked: true, Value: 3000, Rank: 120}},
			Dropped: []SidePlayer{{Name: "Dropped Guy", Position: "SP", Ranked: true, Value: 1000}}},
		{TeamName: "Bandits", ClaimType: "FA",
			Added:   []SidePlayer{{Name: "Reach", Position: "1B", Ranked: true, Value: 200}},
			Dropped: []SidePlayer{{Name: "Good Drop", Position: "OF", Ranked: true, Value: 2500}}},
	}
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
