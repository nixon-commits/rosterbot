package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/projections"
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

// TestOptimizeDefaultProjectionIsTheDeploymentDefault pins the one place the
// deployment default is stated (the --projections flag's default value) to
// projections.DefaultSystem, and that default to steamer (rosterbot-jyt0.1).
// A fresh tenant has no stored preference, so this IS their system.
func TestOptimizeDefaultProjectionIsTheDeploymentDefault(t *testing.T) {
	if projections.DefaultSystem != "steamer" {
		t.Fatalf("DefaultSystem = %q, want steamer", projections.DefaultSystem)
	}
	if !projections.ValidBaseSystem(projections.DefaultSystem) {
		t.Fatalf("DefaultSystem %q must be a base family so the loader keeps its preseason fallback", projections.DefaultSystem)
	}
	if got := optimizeCmd.Flags().Lookup("projections").DefValue; got != projections.DefaultSystem {
		t.Fatalf("--projections default = %q, want %q", got, projections.DefaultSystem)
	}
}

type prefUserLookup struct {
	u   *lineupapi.User
	ok  bool
	err error
}

func (s prefUserLookup) GetUser(context.Context, lineupapi.UserID) (*lineupapi.User, bool, error) {
	return s.u, s.ok, s.err
}

// TestProductionSystemsFor pins the Projections tab's production caption input
// (rosterbot-gaa2): resolved RoS names, per role, with every failure landing
// on the deployment default rather than on an empty caption.
func TestProductionSystemsFor(t *testing.T) {
	cases := []struct {
		name             string
		dir              prefUserLookup
		wantHit, wantPit string
		wantWarn         bool
	}{
		{name: "stored preference resolves to the RoS system per role",
			dir:     prefUserLookup{ok: true, u: &lineupapi.User{HitterProjection: "depthcharts", PitcherProjection: "atc"}},
			wantHit: "depthcharts-ros", wantPit: "atc-ros"},
		{name: "no stored preference is the deployment default",
			dir:     prefUserLookup{ok: true, u: &lineupapi.User{}},
			wantHit: "steamer-ros", wantPit: "steamer-ros"},
		{name: "one role set, the other follows the default",
			dir:     prefUserLookup{ok: true, u: &lineupapi.User{PitcherProjection: "thebatx"}},
			wantHit: "steamer-ros", wantPit: "thebatx-ros"},
		{name: "unknown user is the deployment default, silently",
			dir:     prefUserLookup{},
			wantHit: "steamer-ros", wantPit: "steamer-ros"},
		{name: "a read failure is the default WITH a warning",
			dir:     prefUserLookup{err: errors.New("ddb down")},
			wantHit: "steamer-ros", wantPit: "steamer-ros", wantWarn: true},
		{name: "an invalid stored value is the default with a warning",
			dir:     prefUserLookup{ok: true, u: &lineupapi.User{HitterProjection: "zips"}},
			wantHit: "steamer-ros", wantPit: "steamer-ros", wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, p, warns := productionSystemsFor(context.Background(), tc.dir, "u1")
			if h != tc.wantHit || p != tc.wantPit {
				t.Fatalf("got (%q, %q), want (%q, %q)", h, p, tc.wantHit, tc.wantPit)
			}
			if (len(warns) > 0) != tc.wantWarn {
				t.Fatalf("warnings = %v, wantWarn = %v", warns, tc.wantWarn)
			}
		})
	}
}
