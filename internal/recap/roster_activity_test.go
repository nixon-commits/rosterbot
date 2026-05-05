package recap

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"
)

func TestBuildRosterActivity_ClaimDrop(t *testing.T) {
	d := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "CLAIM", TeamID: "1", PlayerName: "Hayes", ProcessedDate: d, ClaimType: "FA"},
		{Type: "DROP", TeamID: "2", PlayerName: "Carroll", ProcessedDate: d},
	}
	teamNames := map[string]string{"1": "Wahoos", "2": "Sliders"}

	got := BuildRosterActivity(txs, teamNames)
	if got == nil || len(got.Teams) != 2 {
		t.Fatalf("want 2 teams, got %+v", got)
	}
	// Sorted by team name asc → Sliders, Wahoos
	if got.Teams[0].TeamName != "Sliders" || got.Teams[1].TeamName != "Wahoos" {
		t.Errorf("teams not sorted: %v", got.Teams)
	}
	w := got.Teams[1]
	if len(w.Entries) != 1 || w.Entries[0].Kind != "claim" || w.Entries[0].Player != "Hayes" {
		t.Errorf("Wahoos entry wrong: %+v", w.Entries)
	}
	if w.Entries[0].ClaimType != "FA" {
		t.Errorf("ClaimType: want FA, got %q", w.Entries[0].ClaimType)
	}
}

func TestBuildRosterActivity_Swap(t *testing.T) {
	d := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "CLAIM", TeamID: "1", PlayerName: "Hayes", ProcessedDate: d, ClaimType: "FA"},
		{Type: "DROP", TeamID: "1", PlayerName: "Carroll", ProcessedDate: d},
	}
	teamNames := map[string]string{"1": "Wahoos"}

	got := BuildRosterActivity(txs, teamNames)
	if got == nil || len(got.Teams) != 1 || len(got.Teams[0].Entries) != 1 {
		t.Fatalf("want 1 team, 1 entry, got %+v", got)
	}
	e := got.Teams[0].Entries[0]
	if e.Kind != "swap" || e.SwapIn != "Hayes" || e.SwapOut != "Carroll" {
		t.Errorf("swap entry wrong: %+v", e)
	}
}

func TestBuildRosterActivity_NoSwapWhenMultiple(t *testing.T) {
	d := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "CLAIM", TeamID: "1", PlayerName: "Hayes", ProcessedDate: d, ClaimType: "FA"},
		{Type: "CLAIM", TeamID: "1", PlayerName: "Lee", ProcessedDate: d, ClaimType: "FA"},
		{Type: "DROP", TeamID: "1", PlayerName: "Carroll", ProcessedDate: d},
	}
	teamNames := map[string]string{"1": "Wahoos"}

	got := BuildRosterActivity(txs, teamNames)
	// 2 CLAIMs + 1 DROP same day → don't merge any; render all 3.
	if got == nil || len(got.Teams) != 1 || len(got.Teams[0].Entries) != 3 {
		t.Fatalf("want 3 entries, got %+v", got)
	}
	for _, e := range got.Teams[0].Entries {
		if e.Kind == "swap" {
			t.Errorf("did not expect swap merge: %+v", e)
		}
	}
}

func TestBuildRosterActivity_Trade(t *testing.T) {
	d := time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "TRADE", FromTeamID: "1", ToTeamID: "2", PlayerName: "Hayes", ProcessedDate: d, TradeGroupID: "tg1"},
		{Type: "TRADE", FromTeamID: "2", ToTeamID: "1", PlayerName: "Carroll", ProcessedDate: d, TradeGroupID: "tg1"},
	}
	teamNames := map[string]string{"1": "Wahoos", "2": "Sliders"}

	got := BuildRosterActivity(txs, teamNames)
	if got == nil || len(got.Teams) != 2 {
		t.Fatalf("want 2 teams, got %+v", got)
	}
	// Sliders (sorted first): received Hayes, sent Carroll
	s := got.Teams[0]
	if s.TeamName != "Sliders" {
		t.Fatalf("teams[0]: want Sliders, got %q", s.TeamName)
	}
	if len(s.Entries) != 1 || s.Entries[0].Kind != "trade" {
		t.Fatalf("Sliders entries: %+v", s.Entries)
	}
	tr := s.Entries[0]
	if tr.OtherTeam != "Wahoos" || len(tr.Received) != 1 || tr.Received[0] != "Hayes" || len(tr.Sent) != 1 || tr.Sent[0] != "Carroll" {
		t.Errorf("Sliders trade entry wrong: %+v", tr)
	}
	// Wahoos: opposite
	w := got.Teams[1]
	if w.Entries[0].OtherTeam != "Sliders" || w.Entries[0].Received[0] != "Carroll" || w.Entries[0].Sent[0] != "Hayes" {
		t.Errorf("Wahoos trade entry wrong: %+v", w.Entries[0])
	}
}

func TestBuildRosterActivity_Empty(t *testing.T) {
	if got := BuildRosterActivity(nil, nil); got != nil {
		t.Errorf("nil input → want nil RosterActivity, got %+v", got)
	}
}
