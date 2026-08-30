package cmd

import (
	"fmt"

	"github.com/nixon-commits/rosterbot/internal/projections"
)

// resolveProjectionSystems decides which projection system each role's lineup
// optimization uses (rosterbot-5qvs). Precedence: an explicitly typed
// --projections wins and sets BOTH roles — the person at the keyboard outranks
// the stored setting, and reproducing a tenant's run stays one flag away.
// Otherwise each role follows its own stored preference; empty follows the
// flag's default value, the one place the deployment default is stated.
//
// A preference that fails ValidBaseSystem falls back to the default WITH A
// WARNING, never an error: write-time validation makes this unreachable
// through the API, so a hit here is a hand-edited record or a retired system —
// cosmetic staleness that must not take down the job that writes lineups
// (degrade to noise, never to silence).
func resolveProjectionSystems(flagSet bool, flagVal, hitterPref, pitcherPref string) (hitter, pitcher string, warnings []string) {
	if flagSet {
		return flagVal, flagVal, nil
	}
	resolve := func(role, pref string) string {
		if pref == "" {
			return flagVal
		}
		if !projections.ValidBaseSystem(pref) {
			warnings = append(warnings, fmt.Sprintf(
				"%s projection preference %q unknown — using %s", role, pref, flagVal))
			return flagVal
		}
		return pref
	}
	return resolve("hitter", hitterPref), resolve("pitcher", pitcherPref), warnings
}
