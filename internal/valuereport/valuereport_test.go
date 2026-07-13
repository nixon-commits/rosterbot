package valuereport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/teamvalue"
)

func fixtureRows() []teamvalue.Row {
	return []teamvalue.Row{
		// day 1
		{Dt: "2026-07-12", TeamID: "t2", TeamName: "Beta", LogoURL: "https://x/b.png",
			HitterMLBValue: 100, PitcherMLBValue: 50, HitterMinorsValue: 20, RosteredCount: 10, MatchedCount: 9},
		{Dt: "2026-07-12", TeamID: "t1", TeamName: "Alpha", LogoURL: "https://x/a.png",
			HitterMLBValue: 200, PitcherMLBValue: 80, HitterMinorsValue: 40, RosteredCount: 12, MatchedCount: 12},
		// day 2
		{Dt: "2026-07-13", TeamID: "t1", TeamName: "Alpha", LogoURL: "https://x/a.png",
			HitterMLBValue: 210, PitcherMLBValue: 80, HitterMinorsValue: 40, RosteredCount: 12, MatchedCount: 12},
		{Dt: "2026-07-13", TeamID: "t2", TeamName: "Beta", LogoURL: "https://x/b.png",
			HitterMLBValue: 100, PitcherMLBValue: 55, HitterMinorsValue: 20, RosteredCount: 10, MatchedCount: 8},
	}
}

func TestBuildModel_Empty(t *testing.T) {
	m := BuildModel(nil)
	if !m.Empty {
		t.Fatal("want Empty=true for no rows")
	}
}

func TestBuildModel_Shape(t *testing.T) {
	m := BuildModel(fixtureRows())
	if m.Empty {
		t.Fatal("want non-empty")
	}
	if len(m.Dates) != 2 || m.Dates[0] != "2026-07-12" || m.Dates[1] != "2026-07-13" {
		t.Fatalf("dates wrong: %v", m.Dates)
	}
	if m.FirstDate != "2026-07-12" || m.LastDate != "2026-07-13" {
		t.Fatalf("first/last wrong: %s %s", m.FirstDate, m.LastDate)
	}
	if len(m.Teams) != 2 || m.Teams[0].ID != "t1" || m.Teams[1].ID != "t2" {
		t.Fatalf("teams not sorted by ID: %+v", m.Teams)
	}
	// Color is stable per sorted position.
	if m.Teams[0].Color != palette[0] || m.Teams[1].Color != palette[1] {
		t.Fatalf("colors not assigned deterministically: %+v", m.Teams)
	}
	if len(m.Series) != 4 {
		t.Fatalf("want 4 series points, got %d", len(m.Series))
	}
}

func TestBuildModel_LatestSortedByTotalDesc(t *testing.T) {
	m := BuildModel(fixtureRows())
	if len(m.Latest) != 2 {
		t.Fatalf("want 2 latest rows (last day only), got %d", len(m.Latest))
	}
	// Day 2: Alpha total = 210+80+40 = 330, Beta = 100+55+20 = 175.
	if m.Latest[0].TeamID != "t1" || m.Latest[0].Total != 330 {
		t.Fatalf("latest[0] wrong: %+v", m.Latest[0])
	}
	if m.Latest[1].TeamID != "t2" || m.Latest[1].Total != 175 {
		t.Fatalf("latest[1] wrong: %+v", m.Latest[1])
	}
	// Coverage carried through: Beta on day 2 matched 8/10.
	if m.Latest[1].MatchedCount != 8 || m.Latest[1].RosteredCount != 10 {
		t.Fatalf("coverage not carried: %+v", m.Latest[1])
	}
}

func TestRender_SelfContainedHTML(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, BuildModel(fixtureRows())); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"<canvas id=\"valueChart\"",
		"const DATA =",
		"\"Alpha\"", "\"Beta\"",
		"chart.js@4.4.7", // pinned CDN + SRI
		"index.html",     // cross-link to accuracy dashboard
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

// A malicious team name must not break out of the <script> data blob (Go's
// json.Marshal escapes <>&), and the runtime innerHTML build must route
// DATA-derived strings through esc(). Team names are league-member-controlled.
func TestRender_XSSTeamNameNeutralized(t *testing.T) {
	rows := []teamvalue.Row{{
		Dt: "2026-07-12", TeamID: "t1",
		TeamName:       `<img src=x onerror=alert(1)>`,
		HitterMLBValue: 100, RosteredCount: 1, MatchedCount: 1,
	}}
	var buf bytes.Buffer
	if err := Render(&buf, BuildModel(rows)); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	// The raw payload must not appear verbatim (it is <-escaped in the blob).
	if strings.Contains(out, "<img src=x onerror=alert(1)>") {
		t.Error("raw <img> payload leaked into rendered HTML — script-context escaping failed")
	}
	if !strings.Contains(out, "\\u003cimg src=x onerror=alert(1)\\u003e") {
		t.Error("expected the team name to be unicode-escaped inside the JSON data blob")
	}
	// The runtime guard must be present and applied to the team-name cell.
	if !strings.Contains(out, "function esc(") {
		t.Error("esc() helper missing from template")
	}
	if !strings.Contains(out, "esc(r.name || r.team)") {
		t.Error("team-name cell is not routed through esc()")
	}
}

func TestRender_EmptyState(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, BuildModel(nil)); err != nil {
		t.Fatalf("render empty: %v", err)
	}
	if !strings.Contains(buf.String(), "Team Value Store is empty") {
		t.Error("empty-state note missing")
	}
}
