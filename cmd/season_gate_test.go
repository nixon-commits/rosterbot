package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	gateStart = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	gateEnd   = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
)

func changedNone(string) bool { return false }

// The gate stops an in-season-only command outside [start, end] and nothing
// else: a year-round command, an unknown command (fail open — the
// classification test below is what catches the omission), an explicit
// historical window, an operator override, and an unknowable window all run.
func TestSeasonGate_Decision(t *testing.T) {
	win := seasonWindow{Start: gateStart, End: gateEnd, Source: "fantrax"}
	changedDates := func(f string) bool { return f == "dates" }
	cases := []struct {
		name    string
		cmd     string
		changed func(string) bool
		today   time.Time
		win     seasonWindow
		winErr  error
		env     string
		gated   bool
	}{
		{"waivers after the final", "waivers", changedNone, gateEnd.AddDate(0, 0, 1), win, nil, "", true},
		{"waivers before the opener", "waivers", changedNone, gateStart.AddDate(0, 0, -1), win, nil, "", true},
		{"waivers on the final date", "waivers", changedNone, gateEnd, win, nil, "", false},
		{"waivers on opening day", "waivers", changedNone, gateStart, win, nil, "", false},
		{"optimize --dates off season", "optimize", changedDates, gateEnd.AddDate(0, 0, 30), win, nil, "", false},
		{"optimize --matchup off season", "optimize", func(f string) bool { return f == "matchup" }, gateEnd.AddDate(0, 0, 30), win, nil, "", true},
		{"recap --week off season", "recap", func(f string) bool { return f == "week" }, gateEnd.AddDate(0, 0, 30), win, nil, "", false},
		{"team-values off season", "team-values", changedNone, gateEnd.AddDate(0, 0, 30), win, nil, "", false},
		{"football-trades off season", "football-trades", changedNone, gateEnd.AddDate(0, 0, 30), win, nil, "", false},
		{"unknown command fails open", "frobnicate", changedNone, gateEnd.AddDate(0, 0, 30), win, nil, "", false},
		{"window unknowable fails open", "waivers", changedNone, gateEnd.AddDate(0, 0, 30), seasonWindow{}, errors.New("both sources down"), "", false},
		{"override", "waivers", changedNone, gateEnd.AddDate(0, 0, 30), win, nil, "off", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gated, line := seasonGate(tc.cmd, tc.changed, tc.today, tc.win, tc.winErr, tc.env)
			if gated != tc.gated {
				t.Fatalf("gated = %v, want %v (line %q)", gated, tc.gated, line)
			}
			if gated && (!strings.Contains(line, "2026-03-25") || !strings.Contains(line, "2026-09-27") || !strings.Contains(line, "fantrax")) {
				t.Errorf("stop line should name the window and its source: %q", line)
			}
		})
	}
}

// Every command the schedule table launches is classified, in-season-only
// or year-round — an unclassified command fails HERE, never the gate, which
// fails open on it. The table is read from infra/infra.go's jsii.Strings
// entries so a schedule added without a policy cannot slip through.
func TestSeasonGate_EveryScheduledCommandIsClassified(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "infra", "infra.go"))
	if err != nil {
		t.Fatalf("read infra/infra.go: %v", err)
	}
	re := regexp.MustCompile(`jsii\.Strings\("([a-z-]+)"`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		seen[m[1]] = true
	}
	if len(seen) < 15 {
		t.Fatalf("found only %d scheduled commands in infra/infra.go; the regex is not matching the table", len(seen))
	}
	for name := range seen {
		if _, ok := seasonPolicies[name]; !ok {
			t.Errorf("scheduled command %q has no season policy; add it to seasonPolicies (in-season-only or year-round)", name)
		}
	}
	// And every explicit-window flag a policy names must exist on the command,
	// so a renamed flag cannot silently turn a bypass into a gate.
	for name, pol := range seasonPolicies {
		c, _, err := rootCmd.Find([]string{name})
		if err != nil || c == nil || c.Name() != name {
			t.Errorf("policy names %q, which is not a registered command", name)
			continue
		}
		for _, f := range pol.ExplicitFlags {
			if c.Flags().Lookup(f) == nil {
				t.Errorf("policy for %q names flag --%s, which the command does not define", name, f)
			}
		}
	}
}

// The sentinel the gate returns is a clean exit, not a failure: Execute maps
// it to exit code 0 with nothing printed on stderr.
func TestSeasonGate_StopIsExitZero(t *testing.T) {
	if code := exitCodeFor(&offSeasonStop{line: "x"}); code != 0 {
		t.Errorf("exit code for an off-season stop = %d, want 0", code)
	}
	if code := exitCodeFor(errors.New("boom")); code != 1 {
		t.Errorf("exit code for an ordinary error = %d, want 1", code)
	}
	if !errors.Is(&offSeasonStop{line: "x"}, errOffSeason) {
		t.Error("offSeasonStop should satisfy errors.Is(err, errOffSeason)")
	}
}
