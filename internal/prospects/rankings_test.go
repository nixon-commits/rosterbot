package prospects

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- FanGraphsRankingSource ---

func TestFanGraphsRankingSource_ParsesResponse(t *testing.T) {
	fixture := []map[string]interface{}{
		{
			"playerName":  "Konnor Griffin",
			"Team":        "PIT",
			"Position":    "SS",
			"Ovr_Rank":    1,
			"FV_Current":  70,
			"ETA_Current": 2026,
			"mlevel":      "AA",
		},
		{
			"playerName":  "Nolan McLean",
			"Team":        "NYM",
			"Position":    "SP",
			"Ovr_Rank":    3,
			"FV_Current":  65,
			"ETA_Current": 2026,
			"mlevel":      "AAA",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := fgProspectURL
	fgProspectURL = srv.URL + "?draft=%dprospect&season=%d"
	defer func() { fgProspectURL = origURL }()

	src := &FanGraphsRankingSource{}
	prospects, err := src.GetTopProspects(2026)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prospects) != 2 {
		t.Fatalf("expected 2 prospects, got %d", len(prospects))
	}

	p := prospects[0]
	if p.Name != "Konnor Griffin" {
		t.Errorf("expected name 'Konnor Griffin', got %q", p.Name)
	}
	if p.MLBTeam != "PIT" {
		t.Errorf("expected team PIT, got %q", p.MLBTeam)
	}
	if p.Rank != 1 {
		t.Errorf("expected rank 1, got %d", p.Rank)
	}
	if p.FV != 70 {
		t.Errorf("expected FV 70, got %d", p.FV)
	}
	if p.ETA != "2026" {
		t.Errorf("expected ETA 2026, got %q", p.ETA)
	}
	if p.IsPitcher {
		t.Error("expected SS to not be marked as pitcher")
	}

	p2 := prospects[1]
	if !p2.IsPitcher {
		t.Error("expected SP to be marked as pitcher")
	}
}

func TestFanGraphsRankingSource_Returns403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	origURL := fgProspectURL
	fgProspectURL = srv.URL + "?draft=%dprospect&season=%d"
	defer func() { fgProspectURL = origURL }()

	src := &FanGraphsRankingSource{}
	_, err := src.GetTopProspects(2026)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Errorf("expected ErrSourceUnavailable, got %v", err)
	}
}

// --- ChainedRankingSource ---

type failingSource struct{}

func (f *failingSource) GetTopProspects(season int) ([]RankedProspect, error) {
	return nil, ErrSourceUnavailable
}

type succeedingSource struct {
	prospects []RankedProspect
}

func (s *succeedingSource) GetTopProspects(season int) ([]RankedProspect, error) {
	return s.prospects, nil
}

func TestChainedRankingSource_FallsThrough(t *testing.T) {
	expected := []RankedProspect{{Name: "test player", Rank: 1}}
	chain := NewChainedRankingSource(&failingSource{}, &succeedingSource{prospects: expected})

	result, err := chain.GetTopProspects(2026)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].Name != "test player" {
		t.Errorf("unexpected result: %v", result)
	}
}

// --- LoadRankings cache tests ---

type panicSource struct{}

func (p *panicSource) GetTopProspects(season int) ([]RankedProspect, error) {
	panic("should not be called")
}

func TestLoadRankings_UsesCacheWhenFresh(t *testing.T) {
	tmpDir := t.TempDir()
	origFile := rankingsCacheFile
	rankingsCacheFile = filepath.Join(tmpDir, "rankings.json")
	defer func() { rankingsCacheFile = origFile }()

	cached := rankingsCache{
		FetchedAt: time.Now(),
		Prospects: []RankedProspect{{Name: "cached player", Rank: 5}},
	}
	data, _ := json.Marshal(cached)
	os.MkdirAll(filepath.Dir(rankingsCacheFile), 0o755)
	os.WriteFile(rankingsCacheFile, data, 0o644)

	result, err := LoadRankings(&panicSource{}, 2026, 24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].Name != "cached player" {
		t.Errorf("expected cached player, got %v", result)
	}
}

func TestLoadRankings_FetchesWhenStale(t *testing.T) {
	tmpDir := t.TempDir()
	origFile := rankingsCacheFile
	rankingsCacheFile = filepath.Join(tmpDir, "rankings.json")
	defer func() { rankingsCacheFile = origFile }()

	cached := rankingsCache{
		FetchedAt: time.Now().Add(-48 * time.Hour),
		Prospects: []RankedProspect{{Name: "old player", Rank: 99}},
	}
	data, _ := json.Marshal(cached)
	os.MkdirAll(filepath.Dir(rankingsCacheFile), 0o755)
	os.WriteFile(rankingsCacheFile, data, 0o644)

	fresh := []RankedProspect{{Name: "fresh player", Rank: 1}}
	src := &succeedingSource{prospects: fresh}

	result, err := LoadRankings(src, 2026, 24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].Name != "fresh player" {
		t.Errorf("expected fresh player, got %v", result)
	}
}

// --- FindUpgrades ---

func TestFindUpgrades_TieredThreshold(t *testing.T) {
	rostered := []RankedProspect{{Name: "a", Rank: 40}}      // tier 11-50, threshold 15
	available := []RankedProspect{{Name: "b", Rank: 24}}      // gap = 16, meets threshold
	upgrades := FindUpgrades(rostered, available, "2026")
	if len(upgrades) != 1 {
		t.Fatalf("expected 1 upgrade, got %d", len(upgrades))
	}
	if upgrades[0].RankGap != 16 {
		t.Errorf("expected gap 16, got %d", upgrades[0].RankGap)
	}

	// gap of 14 should NOT meet threshold for rank 40
	available2 := []RankedProspect{{Name: "c", Rank: 26}}     // gap = 14
	upgrades2 := FindUpgrades(rostered, available2, "2026")
	if len(upgrades2) != 0 {
		t.Errorf("expected 0 upgrades for gap 14 (threshold 15), got %d", len(upgrades2))
	}
}

func TestFindUpgrades_UnrankedAlwaysReplaceable(t *testing.T) {
	rostered := []RankedProspect{{Name: "unranked guy", Rank: 0}}
	available := []RankedProspect{{Name: "ranked fa", Rank: 99}}
	upgrades := FindUpgrades(rostered, available, "2026")
	if len(upgrades) != 1 {
		t.Fatalf("expected 1 upgrade for unranked, got %d", len(upgrades))
	}
	if upgrades[0].Drop.Name != "unranked guy" {
		t.Errorf("expected to drop unranked guy")
	}
}

func TestFindUpgrades_NearTermETA(t *testing.T) {
	rostered := []RankedProspect{{Name: "drop", Rank: 0}}
	available := []RankedProspect{
		{Name: "near", Rank: 10, ETA: "2026"},
		{Name: "far", Rank: 5, ETA: "2029"},
	}
	// best FA by rank is "far" at rank 5 — but we want to check NearTerm tagging
	upgrades := FindUpgrades(rostered, available, "2026")
	if len(upgrades) != 1 {
		t.Fatalf("expected 1 upgrade, got %d", len(upgrades))
	}
	// The best FA (rank 5) should be paired; it's far away so NearTerm=false
	if upgrades[0].Add.Name != "far" {
		t.Errorf("expected best FA 'far', got %q", upgrades[0].Add.Name)
	}
	if upgrades[0].NearTerm {
		t.Error("expected NearTerm=false for ETA 2029")
	}

	// Now test with near-term FA being the best
	rostered2 := []RankedProspect{{Name: "drop2", Rank: 0}}
	available2 := []RankedProspect{{Name: "near2", Rank: 10, ETA: "2027"}}
	upgrades2 := FindUpgrades(rostered2, available2, "2026")
	if len(upgrades2) != 1 {
		t.Fatalf("expected 1 upgrade, got %d", len(upgrades2))
	}
	if !upgrades2[0].NearTerm {
		t.Error("expected NearTerm=true for ETA 2027 (next year from 2026)")
	}
}

func TestFindUpgrades_DeduplicatesRostered(t *testing.T) {
	rostered := []RankedProspect{{Name: "player", Rank: 80}}
	available := []RankedProspect{
		{Name: "fa1", Rank: 10},
		{Name: "fa2", Rank: 20},
	}
	upgrades := FindUpgrades(rostered, available, "2026")
	if len(upgrades) != 1 {
		t.Errorf("expected 1 upgrade (deduped), got %d", len(upgrades))
	}
	// Should pick best FA
	if upgrades[0].Add.Name != "fa1" {
		t.Errorf("expected best FA 'fa1', got %q", upgrades[0].Add.Name)
	}
}

func TestUpgradeThreshold_AllBuckets(t *testing.T) {
	tests := []struct {
		rank     int
		expected int
	}{
		{1, 5},    // top 10
		{10, 5},   // top 10 boundary
		{11, 15},  // 11-50
		{50, 15},  // 11-50 boundary
		{51, 25},  // 51-100
		{100, 25}, // 51-100 boundary
		{0, 1},    // unranked
	}
	for _, tt := range tests {
		got := upgradeThreshold(tt.rank)
		if got != tt.expected {
			t.Errorf("upgradeThreshold(%d) = %d, want %d", tt.rank, got, tt.expected)
		}
	}
}
