package cmd

import (
	"strings"
	"testing"
	"time"
)

// The gate judges a command on the day its WORK covers, not on the day it
// launches. grade's window ends yesterday (cmd/grade.go's `end = today-1`) and
// gs-check resolves the period that ended yesterday (fantrax.FindJustEnded-
// Period), so for both the morning AFTER the final date is a live run — the
// only one that can ever cover the final day — while the morning after that is
// off-season. Keying both on today lost exactly one day per season, silently:
// the 2026-09-28 grade recorded SUCCESS/off_season and dt=2026-09-27 was never
// graded for any system (rosterbot-l53u), and the 12:00:41Z gs-check exited
// without evaluating period 25 (rosterbot-97gs).
//
// Both bounds move together, which is the point of referencing a date rather
// than special-casing the end: on opening day grade would grade the day BEFORE
// the opener, so it is correctly gated there and runs from the day after.
func TestSeasonGate_ReferenceDateMovesBothBoundaries(t *testing.T) {
	win := seasonWindow{Start: gateStart, End: gateEnd, Source: "fantrax"}
	day := func(base time.Time, n int) time.Time { return base.AddDate(0, 0, n) }

	cases := []struct {
		cmd   string
		today time.Time
		gated bool
		why   string
	}{
		{"grade", day(gateEnd, 1), false, "grades the final date"},
		{"grade", day(gateEnd, 2), true, "would grade the day after the final"},
		{"grade", gateStart, true, "would grade the day before the opener"},
		{"grade", day(gateStart, 1), false, "grades opening day"},

		{"gs-check", day(gateEnd, 1), false, "checks the period that ended on the final date"},
		{"gs-check", day(gateEnd, 2), true, "no in-season period ended yesterday"},
		{"gs-check", gateStart, true, "nothing ended before the opener"},

		// Today-referenced commands are untouched: they act on today, so the
		// final date is their last live day and the morning after is not.
		{"optimize", gateEnd, false, "sets the final day's lineup"},
		{"optimize", day(gateEnd, 1), true, "no lineup left to set"},
		{"optimize", gateStart, false, "opening day"},
		{"waivers", day(gateEnd, 1), true, "dead list"},
	}

	for _, tc := range cases {
		t.Run(tc.cmd+" on "+tc.today.Format("2006-01-02"), func(t *testing.T) {
			gated, line := seasonGate(tc.cmd, changedNone, tc.today, win, nil, "")
			if gated != tc.gated {
				t.Fatalf("gated = %v, want %v (%s) — line %q", gated, tc.gated, tc.why, line)
			}
		})
	}
}

// The stop line has to stay honest about WHICH day was judged. For a
// yesterday-referenced command "today is <date>" alone reads as a
// contradiction — the operator sees a job gated on a date that is one day past
// a window it ran inside yesterday — so the line names the covered day too.
func TestSeasonGate_StopLineNamesTheJudgedDay(t *testing.T) {
	win := seasonWindow{Start: gateStart, End: gateEnd, Source: "fantrax"}
	today := gateEnd.AddDate(0, 0, 2)

	gated, line := seasonGate("grade", changedNone, today, win, nil, "")
	if !gated {
		t.Fatalf("grade should be gated two days past the final; line %q", line)
	}
	for _, want := range []string{"2026-09-29", "2026-09-28", "2026-03-25", "2026-09-27", "fantrax"} {
		if !strings.Contains(line, want) {
			t.Errorf("stop line %q lacks %q (today, the day it would cover, and the window)", line, want)
		}
	}
}
