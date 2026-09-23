package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// leagueContext is one discovered league plus everything the multi-league
// football jobs derive from it once per run.
type leagueContext struct {
	League  sleeper.League
	Profile dynasty.LeagueProfile
}

// leagueContexts filters and orders the leagues a discovery returned and
// derives each profile. Pure, so the filter and the sort are testable without
// a Sleeper client.
//
// A "complete" league is skipped: Sleeper rolls a dynasty league into a NEW
// league id for the next season, so offseason activity lives there, and the
// finished one only accumulates nothing. Order is by name then id so the run
// output and the alert order are stable across runs.
func leagueContexts(leagues []sleeper.League, overrides map[string]string) []leagueContext {
	out := make([]leagueContext, 0, len(leagues))
	for _, lg := range leagues {
		if lg.Status == "complete" {
			continue
		}
		out = append(out, leagueContext{League: lg, Profile: dynasty.DeriveProfile(lg, overrides)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].League.Name != out[j].League.Name {
			return out[i].League.Name < out[j].League.Name
		}
		return out[i].League.LeagueID < out[j].League.LeagueID
	})
	return out
}

// discoverLeagues lists the operator's NFL leagues for season and prints one
// line per league naming the value column it resolved to and how. The line
// is unconditional: settings.type is undocumented, and a wrong column renders
// as a confident number, so the resolution must be visible on every run.
//
// Zero leagues is an error, not an empty success — a user id with no leagues
// this season is a configuration fault (wrong id, wrong season) and a job
// that exits 0 over it would read as a quiet week forever.
func discoverLeagues(ctx context.Context, sc *sleeper.Client, cfg *FootballConfig, season string, out io.Writer) ([]leagueContext, error) {
	if err := cfg.requireSleeperUserID(); err != nil {
		return nil, err
	}
	leagues, err := sc.LeaguesForUser(ctx, cfg.SleeperUserID, "nfl", season)
	if err != nil {
		return nil, fmt.Errorf("sleeper leagues for user %s (%s): %w", cfg.SleeperUserID, season, err)
	}
	lcs := leagueContexts(leagues, cfg.FormatOverrides)
	if len(lcs) == 0 {
		return nil, fmt.Errorf("sleeper user %s has no non-complete NFL leagues for %s (%d returned)", cfg.SleeperUserID, season, len(leagues))
	}
	for _, lc := range lcs {
		p := lc.Profile
		fmt.Fprintf(out, "football: league %q (%s) kind=%s superflex=%v format=%s (%s)\n",
			p.Name, p.LeagueID, p.Kind, p.Superflex, p.Format, p.FormatSource)
	}
	return lcs, nil
}
