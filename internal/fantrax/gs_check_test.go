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

func TestSumGSFromTables(t *testing.T) {
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
					{ShortName: "K"},
				},
			},
			Rows: []models.PlayerRow{
				{Scorer: models.Player{Name: "P1"}, Cells: []models.Cell{{Content: "1"}, {Content: "3"}, {Content: "20"}}},
				{Scorer: models.Player{Name: "P2"}, Cells: []models.Cell{{Content: "0"}, {Content: "5"}, {Content: "30"}}},
				{Scorer: models.Player{Name: "P3"}, Cells: []models.Cell{{Content: "0"}, {Content: ""}, {Content: "10"}}},   // empty GS
				{Scorer: models.Player{Name: "P4"}, Cells: []models.Cell{{Content: "0"}, {Content: "2.0"}, {Content: "5"}}}, // float GS
				{Cells: []models.Cell{{Content: "0"}, {Content: "10"}, {Content: "65"}}, StatusID: "y"},                     // totals row, should be skipped
			},
		},
	}

	total, err := sumGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 10 { // 3 + 5 + 0 + 2
		t.Errorf("expected 10, got %d", total)
	}
}

func TestSumGSFromTables_NoGSColumn(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: "20",
			Header: models.TableHeader{
				Cells: []models.Column{{ShortName: "W"}, {ShortName: "K"}},
			},
			Rows: []models.PlayerRow{
				{Cells: []models.Cell{{Content: "1"}, {Content: "20"}}},
			},
		},
	}

	total, err := sumGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0, got %d", total)
	}
}

func TestSumGSFromTables_NoPitchingTable(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: "10",
			Header:  models.TableHeader{Cells: []models.Column{{ShortName: "GS"}}},
			Rows:    []models.PlayerRow{{Cells: []models.Cell{{Content: "5"}}}},
		},
	}

	total, err := sumGSFromTables(tables)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0, got %d", total)
	}
}
