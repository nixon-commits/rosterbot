package cmd

import (
	"strings"
	"testing"
)

// TestResolveProjectionSystems pins the precedence decided in rosterbot-5qvs:
// an explicitly typed --projections outranks the stored preference and sets
// BOTH roles (the person at the keyboard wins, and reproducing a tenant's run
// stays one flag away); otherwise each role follows its own preference; empty
// or unresolvable falls back to the flag's default value — the one place the
// deployment default is stated.
func TestResolveProjectionSystems(t *testing.T) {
	cases := []struct {
		name               string
		flagSet            bool
		flagVal            string
		hitPref, pitPref   string
		wantHit, wantPit   string
		wantWarnContaining string
	}{
		{name: "explicit flag wins and sets both roles",
			flagSet: true, flagVal: "atc", hitPref: "steamer", pitPref: "thebatx",
			wantHit: "atc", wantPit: "atc"},
		{name: "no flag, per-role preferences apply",
			flagVal: "depthcharts", hitPref: "steamer", pitPref: "thebatx",
			wantHit: "steamer", wantPit: "thebatx"},
		{name: "no flag, no preference, deployment default",
			flagVal: "depthcharts",
			wantHit: "depthcharts", wantPit: "depthcharts"},
		{name: "one role set, the other follows the default",
			flagVal: "depthcharts", pitPref: "atc",
			wantHit: "depthcharts", wantPit: "atc"},
		{name: "unresolvable preference falls back with a warning",
			flagVal: "depthcharts", hitPref: "zips", pitPref: "atc",
			wantHit: "depthcharts", wantPit: "atc", wantWarnContaining: `"zips"`},
		{name: "a -ros value is outside the stored contract",
			flagVal: "depthcharts", hitPref: "depthcharts-ros",
			wantHit: "depthcharts", wantPit: "depthcharts",
			wantWarnContaining: `"depthcharts-ros"`},
	}
	for _, c := range cases {
		hit, pit, warns := resolveProjectionSystems(c.flagSet, c.flagVal, c.hitPref, c.pitPref)
		if hit != c.wantHit || pit != c.wantPit {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, hit, pit, c.wantHit, c.wantPit)
		}
		if c.wantWarnContaining == "" {
			if len(warns) != 0 {
				t.Errorf("%s: unexpected warnings %v", c.name, warns)
			}
			continue
		}
		joined := strings.Join(warns, "\n")
		if !strings.Contains(joined, c.wantWarnContaining) {
			t.Errorf("%s: warnings %v do not name the bad value %s",
				c.name, warns, c.wantWarnContaining)
		}
	}
}
