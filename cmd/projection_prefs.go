package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/statestore"

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

// productionSystems maps a tenant's stored per-role preference to the RESOLVED
// in-season system names the Analysis Store grades are keyed on, for the
// Projections tab's "production" caption (rosterbot-gaa2). It goes through
// resolveProjectionSystems so an empty or invalid preference lands on the same
// deployment default the hourly optimize run uses — the caption and the run
// cannot disagree about what "default" means.
func productionSystems(hitterPref, pitcherPref string) (hitter, pitcher string, warnings []string) {
	h, p, warnings := resolveProjectionSystems(false, projections.DefaultSystem, hitterPref, pitcherPref)
	return projections.RoSVariant(h), projections.RoSVariant(p), warnings
}

// tenantProductionSystems reads the ambient tenant's stored preference and
// resolves it with productionSystems. With no identity table or no tenant
// (local dev, single-tenant) it answers the deployment default.
//
// A failed read ALSO answers the default, with a warning: the caption is
// decoration on a report whose numbers do not depend on it, so it must never
// fail the publish. The warning is what keeps a wrong caption from being
// silent.
func tenantProductionSystems(ctx context.Context) (hitter, pitcher string, warnings []string) {
	table, uid := os.Getenv("IDENTITY_TABLE"), statestore.Tenant()
	if table == "" || uid == "" {
		return productionSystems("", "")
	}
	store, err := ddbuser.New(ctx, table)
	if err != nil {
		h, p, w := productionSystems("", "")
		return h, p, append(w, fmt.Sprintf("identity store unavailable (%v) — captioning the deployment default as production", err))
	}
	return productionSystemsFor(ctx, store, lineupapi.UserID(uid))
}

// productionSystemsFor is tenantProductionSystems past the store construction,
// split out so the read-failure and missing-user branches are testable.
func productionSystemsFor(ctx context.Context, dir userLookup, uid lineupapi.UserID) (hitter, pitcher string, warnings []string) {
	u, ok, err := dir.GetUser(ctx, uid)
	if err != nil {
		h, p, w := productionSystems("", "")
		return h, p, append(w, fmt.Sprintf("could not read the projection preference (%v) — captioning the deployment default as production", err))
	}
	if !ok {
		return productionSystems("", "")
	}
	return productionSystems(u.HitterProjection, u.PitcherProjection)
}
