package prospects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TestTxnCursor
// ---------------------------------------------------------------------------

func TestTxnCursor_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	origFile := txnCursorFile
	txnCursorFile = filepath.Join(tmpDir, "cursor.json")
	defer func() { txnCursorFile = origFile }()

	// Missing file → zero time
	got := loadTxnCursor()
	if !got.IsZero() {
		t.Errorf("expected zero time for missing file, got %v", got)
	}

	// Save and reload
	now := time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC)
	if err := saveTxnCursor(now); err != nil {
		t.Fatalf("saveTxnCursor: %v", err)
	}
	got = loadTxnCursor()
	if !got.Equal(now) {
		t.Errorf("expected %v, got %v", now, got)
	}
}

func TestTxnCursor_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	origFile := txnCursorFile
	txnCursorFile = filepath.Join(tmpDir, "cursor.json")
	defer func() { txnCursorFile = origFile }()

	os.WriteFile(txnCursorFile, []byte("not json"), 0o644)
	got := loadTxnCursor()
	if !got.IsZero() {
		t.Errorf("expected zero time for invalid JSON, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// TestFormatReport_Markdown (writeGHASummary)
// ---------------------------------------------------------------------------

func TestFormatReport_Markdown(t *testing.T) {
	report := Report{
		Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
		Alerts: []ProspectAlert{
			{
				Kind:       CalledUp,
				Priority:   "high",
				PlayerName: "Jackson Chourio",
				MLBTeam:    "MIL",
				Detail:     "Called up — move from Minors slot",
				OnMyTeam:   true,
				Rank:       15,
			},
			{
				Kind:       FreeAgentBuzz,
				Priority:   "high",
				PlayerName: "Jasson Dominguez",
				MLBTeam:    "NYY",
				Detail:     "#8 prospect called up — available in your league?",
				Rank:       8,
			},
			{
				Kind:       PerformanceHot,
				Priority:   "medium",
				PlayerName: "Colton Cowser",
				MLBTeam:    "BAL",
				Position:   "OF",
				Detail:     "Breaking out at AAA — recent: .342/.401/.658 vs season: .280/.340/.450",
				Stats:      ".342/.401/.658",
				Rank:       22,
			},
		},
		Upgrades: []UpgradeSet{
			{
				Source: "FanGraphs",
				Candidates: []UpgradeCandidate{
					{
						Drop:     RankedProspect{Name: "Tyler Black", Rank: 92},
						Add:      RankedProspect{Name: "Ethan Salas", Rank: 18, ETA: "2026"},
						RankGap:  74,
						NearTerm: true,
					},
				},
			},
		},
	}

	tmpFile := filepath.Join(t.TempDir(), "summary.md")
	writeGHASummary(report, tmpFile)

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read summary file: %v", err)
	}
	content := string(data)

	// Check header
	if !strings.Contains(content, "## Prospect Report") {
		t.Error("missing '## Prospect Report' header")
	}

	// Check alerts table header
	if !strings.Contains(content, "| Priority | Type | Player | Team | Detail |") {
		t.Error("missing alerts table header")
	}

	// Check alert rows
	if !strings.Contains(content, "| HIGH | CALLED UP | Jackson Chourio | MIL |") {
		t.Error("missing Jackson Chourio alert row")
	}
	if !strings.Contains(content, "| HIGH | FA BUZZ | Jasson Dominguez | NYY |") {
		t.Error("missing Jasson Dominguez alert row")
	}
	if !strings.Contains(content, "| MEDIUM | HOT | Colton Cowser | BAL |") {
		t.Error("missing Colton Cowser alert row")
	}

	// Check upgrades table
	if !strings.Contains(content, "### Upgrades (FanGraphs)") {
		t.Error("missing upgrades section header")
	}
	if !strings.Contains(content, "| Tyler Black (#92) | Ethan Salas (#18) | +74 | yes |") {
		t.Error("missing upgrade row")
	}
}

func TestFormatReport_Markdown_NoAlerts(t *testing.T) {
	report := Report{
		Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
	}

	tmpFile := filepath.Join(t.TempDir(), "summary.md")
	writeGHASummary(report, tmpFile)

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read summary file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "No prospect alerts today.") {
		t.Error("expected 'No prospect alerts today.' for empty report")
	}
}

func TestFormatReport_Markdown_Appends(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "summary.md")

	// Write some pre-existing content
	os.WriteFile(tmpFile, []byte("## Previous Section\n\n"), 0o644)

	report := Report{
		Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
		Alerts: []ProspectAlert{
			{Kind: CalledUp, Priority: "high", PlayerName: "Test Player", MLBTeam: "NYY", Detail: "test"},
		},
	}
	writeGHASummary(report, tmpFile)

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read summary file: %v", err)
	}
	content := string(data)

	// Verify both sections exist (append mode)
	if !strings.Contains(content, "## Previous Section") {
		t.Error("pre-existing content was overwritten")
	}
	if !strings.Contains(content, "## Prospect Report") {
		t.Error("new content was not appended")
	}
}

// ---------------------------------------------------------------------------
// TestPrintReport (stdout capture)
// ---------------------------------------------------------------------------

func TestPrintReport_NoAlerts(t *testing.T) {
	// Just verify it doesn't panic with an empty report
	report := Report{
		Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
	}
	// printReport writes to stdout — we just verify no panic
	printReport(report, nil, nil)
}

func TestPrintReport_WithAlerts(t *testing.T) {
	report := Report{
		Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
		Alerts: []ProspectAlert{
			{Kind: CalledUp, Priority: "high", PlayerName: "Test Player", MLBTeam: "MIL", Detail: "Called up"},
		},
		Upgrades: []UpgradeSet{
			{
				Source: "FanGraphs",
				Candidates: []UpgradeCandidate{
					{
						Drop:     RankedProspect{Name: "Drop Guy", Rank: 90},
						Add:      RankedProspect{Name: "Add Guy", Rank: 10, ETA: "2026"},
						RankGap:  80,
						NearTerm: true,
					},
				},
			},
		},
	}
	// printReport writes to stdout — we just verify no panic
	printReport(report, nil, nil)
}

// ---------------------------------------------------------------------------
// TestAlertKindLabel
// ---------------------------------------------------------------------------

func TestAlertKindLabel(t *testing.T) {
	tests := []struct {
		kind     AlertKind
		expected string
	}{
		{CalledUp, "CALLED UP"},
		{Optioned, "OPTIONED"},
		{PerformanceHot, "HOT"},
		{PerformanceCold, "COLD"},
		{FreeAgentBuzz, "FA BUZZ"},
		{UpgradeAvail, "UPGRADE"},
		{AlertKind("unknown"), "unknown"},
	}
	for _, tt := range tests {
		got := alertKindLabel(tt.kind)
		if got != tt.expected {
			t.Errorf("alertKindLabel(%q) = %q, want %q", tt.kind, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// TestRunProspectReport_DegradedNoRankings
// ---------------------------------------------------------------------------

// Since RunProspectReport takes *fantrax.Client which is hard to construct in
// tests, we test the degraded path by verifying individual components compose
// correctly and the report formatting handles nil/empty data gracefully.
func TestRunProspectReport_DegradedNoRankings(t *testing.T) {
	tmpDir := t.TempDir()

	// Override cursor file
	origCursor := txnCursorFile
	txnCursorFile = filepath.Join(tmpDir, "cursor.json")
	defer func() { txnCursorFile = origCursor }()

	// Test that with no rankings, an empty report is well-formed
	report := Report{
		Date:     time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
		Alerts:   nil,
		Upgrades: nil,
	}

	// printReport should handle empty data gracefully
	printReport(report, nil, nil)

	// writeGHASummary should handle empty data gracefully
	summaryFile := filepath.Join(tmpDir, "summary.md")
	writeGHASummary(report, summaryFile)

	data, err := os.ReadFile(summaryFile)
	if err != nil {
		t.Fatalf("failed to read summary: %v", err)
	}
	if !strings.Contains(string(data), "No prospect alerts today.") {
		t.Error("expected 'No prospect alerts today.' in degraded summary")
	}

	// Verify cursor save/load still works in degraded mode
	now := time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC)
	if err := saveTxnCursor(now); err != nil {
		t.Fatalf("saveTxnCursor: %v", err)
	}
	loaded := loadTxnCursor()
	if !loaded.Equal(now) {
		t.Errorf("cursor round-trip failed: expected %v, got %v", now, loaded)
	}

	// Verify FindUpgrades with nil inputs returns nil
	upgrades := FindUpgrades(nil, nil, "2026")
	if upgrades != nil {
		t.Errorf("expected nil upgrades with nil inputs, got %v", upgrades)
	}

	// Verify alert sorting with empty slice doesn't panic
	var empty []ProspectAlert
	priorityOrder := map[string]int{"high": 0, "medium": 1, "low": 2}
	sortAlerts(empty, priorityOrder)
}

// sortAlerts is a helper to test the sort logic used in RunProspectReport.
func sortAlerts(alerts []ProspectAlert, order map[string]int) {
	if len(alerts) > 1 {
		for i := 0; i < len(alerts)-1; i++ {
			if order[alerts[i].Priority] > order[alerts[i+1].Priority] {
				alerts[i], alerts[i+1] = alerts[i+1], alerts[i]
			}
		}
	}
}
