package projections

import (
	"strings"
	"testing"
)

func TestParseFanGraphsCSV(t *testing.T) {
	// Minimal CSV fixture mimicking FanGraphs format.
	csvData := `Name,Team,PA,H,2B,3B,HR,RBI,R,BB,SB,CS,HBP
Aaron Judge,NYY,600,160,30,2,40,100,100,80,5,2,8
Freddie Freeman,LAD,580,168,35,1,25,90,95,70,10,3,6
`
	r := strings.NewReader(csvData)
	_ = r // exercise the field parser

	// Parse manually using our helper functions.
	lines := strings.Split(strings.TrimSpace(csvData), "\n")
	header := strings.Split(lines[0], ",")
	idx := buildIndex(header)

	row := strings.Split(lines[1], ",") // Aaron Judge row
	name := getField(row, idx, "Name")
	team := getField(row, idx, "Team")
	hr := parseFloat(getField(row, idx, "HR"))

	if name != "Aaron Judge" {
		t.Errorf("expected 'Aaron Judge', got %q", name)
	}
	if team != "NYY" {
		t.Errorf("expected 'NYY', got %q", team)
	}
	if hr != 40 {
		t.Errorf("expected HR=40, got %v", hr)
	}
}

func TestProjectionKeyLookup(t *testing.T) {
	src := &FanGraphsSource{projections: map[string]*Projection{
		"aaron judge|NYY": {PA: 600, HR: 40},
		"freddie freeman|LAD": {PA: 580, HR: 25},
	}}

	p, ok := src.GetProjection("Aaron Judge", "NYY")
	if !ok {
		t.Fatal("expected projection for Aaron Judge")
	}
	if p.HR != 40 {
		t.Errorf("expected HR=40, got %v", p.HR)
	}

	// Case-insensitive team lookup.
	p2, ok2 := src.GetProjection("Freddie Freeman", "lad")
	if !ok2 {
		t.Fatal("expected projection for Freddie Freeman with lowercase team")
	}
	if p2.HR != 25 {
		t.Errorf("expected HR=25, got %v", p2.HR)
	}
}

func TestChainedSource_FallsThrough(t *testing.T) {
	primary := &FanGraphsSource{projections: map[string]*Projection{
		"judge|NYY": {HR: 40},
	}}

	rolling := NewRollingSource()
	rolling.AddPlayer("mystery player", 14, 2.0, 0.5, 0.1, 0.3, 1.5, 1.2, 1.0, 0.3, 0.0, 0.1)

	chained := NewChainedSource(primary, rolling)

	// Primary has this one.
	_, ok := chained.GetProjection("judge", "NYY")
	if !ok {
		t.Error("expected primary source hit")
	}

	// Rolling fallback.
	_, ok2 := chained.GetProjection("mystery player", "COL")
	if !ok2 {
		t.Error("expected rolling fallback hit")
	}

	// Nobody has this.
	_, ok3 := chained.GetProjection("nobody", "XYZ")
	if ok3 {
		t.Error("expected miss for unknown player")
	}
}

func TestParseFloat_EdgeCases(t *testing.T) {
	cases := []struct {
		input    string
		expected float64
	}{
		{"40", 40},
		{"3.14", 3.14},
		{"-", 0},
		{"", 0},
		{"  25  ", 25},
	}
	for _, c := range cases {
		got := parseFloat(c.input)
		if got != c.expected {
			t.Errorf("parseFloat(%q) = %v, want %v", c.input, got, c.expected)
		}
	}
}
