package recap

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func sampleRecap() *Recap {
	d := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	return &Recap{
		Season:     2026,
		WeekNumber: 4,
		WeekLabel:  "Week 4",
		StartDate:  d,
		EndDate:    d.AddDate(0, 0, 6),
		Teams: []TeamWeek{
			{TeamID: "1", TeamName: "Alpha", ActualPts: 220, OptimalPts: 250, Efficiency: 0.88},
		},
	}
}

func TestRenderNoNav(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleRecap()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, `<div class="week-picker">`) {
		t.Errorf("Render() should not emit week-picker div when nav is absent")
	}
	if strings.Contains(got, "<select") {
		t.Errorf("Render() should not emit a <select> element when nav is absent")
	}
}

func TestRenderSiteWithNav(t *testing.T) {
	nav := []WeekLink{
		{WeekNumber: 5, WeekLabel: "Week 5", Filename: "week-05.html"},
		{WeekNumber: 4, WeekLabel: "Week 4", Filename: "week-04.html", IsCurrent: true},
		{WeekNumber: 3, WeekLabel: "Week 3", Filename: "week-03.html"},
	}
	var buf bytes.Buffer
	if err := RenderSite(&buf, sampleRecap(), nav, nil); err != nil {
		t.Fatalf("RenderSite: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		`class="week-picker"`,
		`id="week-select"`,
		`selected>Week 4<`, // current week's option is preselected
		`>Week 5<`,
		`>Week 3<`,
		// Navigation targets are a server-rendered trusted literal, indexed by
		// the option position — no DOM value flows to location (CodeQL
		// js/xss-through-dom). html/template JS-escapes each filename.
		`var files = ["week-05.html", "week-04.html", "week-03.html", ]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderSite output missing %q", want)
		}
	}
	// The old DOM-value navigation must be gone — a javascript: option value
	// could otherwise reach location.
	for _, unwanted := range []string{"this.value", "onchange", `value="week-`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("RenderSite output should not contain %q (DOM-value navigation removed)", unwanted)
		}
	}
}

func TestRenderSiteNilNavSameAsRender(t *testing.T) {
	var a, b bytes.Buffer
	if err := Render(&a, sampleRecap()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := RenderSite(&b, sampleRecap(), nil, nil); err != nil {
		t.Fatalf("RenderSite: %v", err)
	}
	if a.String() != b.String() {
		t.Errorf("RenderSite(nil nav) should match Render() byte-for-byte")
	}
}

func TestNavWithCurrent(t *testing.T) {
	nav := []WeekLink{
		{WeekNumber: 3, Filename: "week-03.html"},
		{WeekNumber: 4, Filename: "week-04.html"},
		{WeekNumber: 5, Filename: "week-05.html"},
	}
	got := navWithCurrent(nav, 4)
	if got[0].IsCurrent || !got[1].IsCurrent || got[2].IsCurrent {
		t.Errorf("navWithCurrent: only week 4 should be current, got %+v", got)
	}
	// Original slice must be untouched.
	if nav[1].IsCurrent {
		t.Errorf("navWithCurrent must not mutate input slice")
	}
}
