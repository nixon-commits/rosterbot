package fantrax

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"
)

func TestFindJustEndedPeriod(t *testing.T) {
	periods := []ScoringPeriod{
		{Number: 1, Caption: "Scoring Period 1", EndDate: time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)},
		{Number: 2, Caption: "Scoring Period 2", EndDate: time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)},
		{Number: 3, Caption: "Scoring Period 3", EndDate: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)},
	}

	// Today is April 6 → yesterday is April 5 → period 2 just ended.
	today := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC)
	p := FindJustEndedPeriod(periods, today)
	if p == nil {
		t.Fatal("expected period 2, got nil")
	}
	if p.Number != 2 {
		t.Errorf("expected period 2, got %d", p.Number)
	}

	// Today is April 7 → yesterday is April 6 → no period ended.
	today = time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)
	p = FindJustEndedPeriod(periods, today)
	if p != nil {
		t.Errorf("expected nil, got period %d", p.Number)
	}
}

func TestFindCurrentPeriod(t *testing.T) {
	periods := []ScoringPeriod{
		{Number: 1, Caption: "Scoring Period 1", StartDate: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)},
		{Number: 2, Caption: "Scoring Period 2", StartDate: time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)},
		{Number: 3, Caption: "Scoring Period 3", StartDate: time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)},
	}

	// Today is March 25 → within period 1.
	today := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	p := FindCurrentPeriod(periods, today)
	if p == nil {
		t.Fatal("expected period 1, got nil")
	}
	if p.Number != 1 {
		t.Errorf("expected period 1, got %d", p.Number)
	}

	// Today is March 29 → last day of period 1.
	today = time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)
	p = FindCurrentPeriod(periods, today)
	if p == nil {
		t.Fatal("expected period 1, got nil")
	}
	if p.Number != 1 {
		t.Errorf("expected period 1, got %d", p.Number)
	}

	// Today is March 30 → first day of period 2.
	today = time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	p = FindCurrentPeriod(periods, today)
	if p == nil {
		t.Fatal("expected period 2, got nil")
	}
	if p.Number != 2 {
		t.Errorf("expected period 2, got %d", p.Number)
	}

	// Today is March 20 → before any period.
	today = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	p = FindCurrentPeriod(periods, today)
	if p != nil {
		t.Errorf("expected nil, got period %d", p.Number)
	}
}

func TestFindMostRecentPastPeriod(t *testing.T) {
	periods := []ScoringPeriod{
		{Number: 1, Caption: "Scoring Period 1", EndDate: time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)},
		{Number: 2, Caption: "Scoring Period 2", EndDate: time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)},
		{Number: 3, Caption: "Scoring Period 3", EndDate: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)},
	}

	// Today is April 10 → periods 1 and 2 are past → most recent is period 2.
	today := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	p := FindMostRecentPastPeriod(periods, today)
	if p == nil {
		t.Fatal("expected period 2, got nil")
	}
	if p.Number != 2 {
		t.Errorf("expected period 2, got %d", p.Number)
	}

	// Today is March 25 → no periods have ended yet.
	today = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	p = FindMostRecentPastPeriod(periods, today)
	if p != nil {
		t.Errorf("expected nil, got period %d", p.Number)
	}
}

func TestIsPitchingGroup(t *testing.T) {
	tests := []struct {
		input interface{}
		want  bool
	}{
		{"20", true},
		{float64(20), true},
		{20, true},
		{"10", false},
		{float64(10), false},
		{nil, false},
		{true, false},
	}

	for _, tt := range tests {
		got := isPitchingGroup(tt.input)
		if got != tt.want {
			t.Errorf("isPitchingGroup(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestPlayerGSFromTables(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: "10", // hitting group, should be skipped
			Header:  models.TableHeader{Cells: []models.Column{{ShortName: "GS"}}},
			Rows: []models.PlayerRow{
				{Cells: []models.Cell{{Content: "5"}}},
			},
		},
		{
			SCGroup: "20", // pitching group
			Header: models.TableHeader{
				Cells: []models.Column{
					{ShortName: "W"},
					{ShortName: "GS"},
					{ShortName: "K", Key: "fpts"},
				},
			},
			Rows: []models.PlayerRow{
				{Scorer: models.Player{Name: "Ace Pitcher", ScorerID: "p1"}, StatusID: "1", Cells: []models.Cell{{Content: "1"}, {Content: "3"}, {Content: "45.5"}}},
				{Scorer: models.Player{Name: "Setup Guy", ScorerID: "p2"}, StatusID: "1", Cells: []models.Cell{{Content: "0"}, {Content: "5"}, {Content: "30.0"}}},
				{Scorer: models.Player{Name: "No GS", ScorerID: "p3"}, StatusID: "1", Cells: []models.Cell{{Content: "0"}, {Content: ""}, {Content: "10"}}},         // empty GS
				{Scorer: models.Player{Name: "Bench SP", ScorerID: "p4"}, StatusID: "2", Cells: []models.Cell{{Content: "0"}, {Content: "2.0"}, {Content: "12.0"}}}, // bench pitcher
				{Cells: []models.Cell{{Content: "0"}, {Content: "10"}, {Content: "65"}}, StatusID: "y"},                                                             // totals row, should be skipped
			},
		},
	}

	result, err := playerGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// P1=3 (active), P2=5 (active), P3=empty GS, P4=2 (bench)
	if result["p1"].GS != 3 {
		t.Errorf("expected p1 gs=3, got %d", result["p1"].GS)
	}
	if result["p1"].FPts != 45.5 {
		t.Errorf("expected p1 fpts=45.5, got %f", result["p1"].FPts)
	}
	if result["p1"].Name != "Ace Pitcher" {
		t.Errorf("expected p1 name='Ace Pitcher', got %q", result["p1"].Name)
	}
	if !result["p1"].Active {
		t.Error("expected p1 active=true")
	}
	if result["p2"].GS != 5 {
		t.Errorf("expected p2 gs=5, got %d", result["p2"].GS)
	}
	if result["p2"].FPts != 30.0 {
		t.Errorf("expected p2 fpts=30.0, got %f", result["p2"].FPts)
	}
	if result["p4"].GS != 2 {
		t.Errorf("expected p4 gs=2, got %d", result["p4"].GS)
	}
	if result["p4"].Active {
		t.Error("expected p4 active=false (bench)")
	}
	if _, ok := result["p3"]; ok {
		t.Errorf("expected p3 absent (empty GS), got %v", result["p3"])
	}
}

func TestPlayerGSFromTables_ActiveOnly(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: float64(20),
			Header: models.TableHeader{
				Cells: []models.Column{{ShortName: "GS"}, {Key: "fpts"}},
			},
			Rows: []models.PlayerRow{
				{Scorer: models.Player{Name: "Active SP", ScorerID: "a1"}, StatusID: "1", Cells: []models.Cell{{Content: "4"}, {Content: "50.0"}}},
				{Scorer: models.Player{Name: "Bench SP", ScorerID: "b1"}, StatusID: "2", Cells: []models.Cell{{Content: "3"}, {Content: "30.0"}}},
				{Scorer: models.Player{Name: "IL SP", ScorerID: "i1"}, StatusID: "3", Cells: []models.Cell{{Content: "2"}, {Content: "20.0"}}},
				{Scorer: models.Player{Name: "Minors SP", ScorerID: "m1"}, StatusID: "9", Cells: []models.Cell{{Content: "1"}, {Content: "10.0"}}},
			},
		},
	}

	result, err := playerGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 4 {
		t.Fatalf("expected 4 players, got %d", len(result))
	}
	// All players returned, but only a1 is active
	if !result["a1"].Active {
		t.Error("expected a1 active")
	}
	if result["a1"].FPts != 50.0 {
		t.Errorf("expected a1 fpts=50.0, got %f", result["a1"].FPts)
	}
	if result["a1"].Name != "Active SP" {
		t.Errorf("expected a1 name='Active SP', got %q", result["a1"].Name)
	}
	if result["b1"].Active || result["i1"].Active || result["m1"].Active {
		t.Error("expected bench/IL/minors to be inactive")
	}
}

func TestPlayerGSFromTables_NoFPtsColumn(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: "20",
			Header: models.TableHeader{
				Cells: []models.Column{{ShortName: "GS"}}, // no fpts column
			},
			Rows: []models.PlayerRow{
				{Scorer: models.Player{Name: "SP1", ScorerID: "s1"}, StatusID: "1", Cells: []models.Cell{{Content: "3"}}},
			},
		},
	}

	result, err := playerGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["s1"].GS != 3 {
		t.Errorf("expected gs=3, got %d", result["s1"].GS)
	}
	if result["s1"].FPts != 0 {
		t.Errorf("expected fpts=0 when column missing, got %f", result["s1"].FPts)
	}
	if result["s1"].Name != "SP1" {
		t.Errorf("expected name='SP1', got %q", result["s1"].Name)
	}
}

func TestPlayerGSFromTables_NoGSColumn(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: "20",
			Header: models.TableHeader{
				Cells: []models.Column{{ShortName: "W"}, {ShortName: "K"}},
			},
			Rows: []models.PlayerRow{
				{Scorer: models.Player{Name: "P1", ScorerID: "p1"}, Cells: []models.Cell{{Content: "1"}, {Content: "20"}}},
			},
		},
	}

	result, err := playerGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestPlayerGSFromTables_NoPitchingTable(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: "10",
			Header:  models.TableHeader{Cells: []models.Column{{ShortName: "GS"}}},
			Rows:    []models.PlayerRow{{Scorer: models.Player{Name: "P1", ScorerID: "p1"}, Cells: []models.Cell{{Content: "5"}}}},
		},
	}

	result, err := playerGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}
