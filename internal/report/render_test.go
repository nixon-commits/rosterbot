package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func TestRender_EmbedsModelAndPanels(t *testing.T) {
	rows := []analysis.GradeRow{
		{Dt: "2026-06-15", PlayerID: "1", Name: "Tester", Bucket: "OF", Projected: 5, Actual: 7, Diff: 2},
	}
	m := Aggregate(rows, time.Now().UTC(), time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC))
	var buf bytes.Buffer
	if err := Render(&buf, m); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"const MODEL =", "chart.js", "id=\"scorecard\"", "id=\"calib\"", "id=\"insights\"", "id=\"misses\"",
		"sortMisses(", "id: \"vLine\"", "per projected bucket",
		"id=\"comparePanel\"", "id=\"compareChart\"", "function renderCompare("} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered HTML missing %q", want)
		}
	}
	// The embedded JSON must be valid: extract between the markers and parse.
	start := strings.Index(html, "const MODEL = ") + len("const MODEL = ")
	end := strings.Index(html[start:], ";\n")
	if end < 0 {
		t.Fatalf("could not locate embedded JSON terminator")
	}
	var got Model
	if err := json.Unmarshal([]byte(html[start:start+end]), &got); err != nil {
		t.Fatalf("embedded JSON invalid: %v", err)
	}
	if got.LatestDate != "2026-06-15" || len(got.Views) != 12 {
		t.Fatalf("round-tripped model wrong: %+v", got.LatestDate)
	}
}
