package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
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

// leagueLister is the one Sleeper call discovery needs; *sleeper.Client
// satisfies it, and a test answers it per season without a network.
type leagueLister interface {
	LeaguesForUser(ctx context.Context, userID, sport, season string) ([]sleeper.League, error)
}

// discoverySeasons names the seasons discovery must ask about: Sleeper's
// current `season`, plus `league_create_season` when it has moved ahead.
// Sleeper scopes LeaguesForUser by season and flips the create season forward
// in December, so from then until the new season starts a renewed league is
// listed ONLY under the create season; asking about `season` alone cannot see
// it (rosterbot-udkm). state.Season stays first so the in-play season's
// listing wins the dedup below.
func discoverySeasons(state *sleeper.NFLState) []string {
	seasons := []string{state.Season}
	if cs := state.LeagueCreateSeason; cs != "" && cs != state.Season {
		seasons = append(seasons, cs)
	}
	return seasons
}

// discoverLeagues lists the operator's NFL leagues across discoverySeasons,
// de-duplicated by league id (a league spanning both seasons is listed under
// both), and prints one line per league naming the value column it resolved
// to and how. The line is unconditional: settings.type is undocumented, and a
// wrong column renders as a confident number, so the resolution must be
// visible on every run.
//
// Three outcomes are told apart, because they need different responses:
//   - Zero leagues RETURNED is an error, not an empty success — a user id
//     with no leagues in either season is a configuration fault (wrong id,
//     wrong seasons) and a job that exits 0 over it would read as a quiet
//     week forever.
//   - Leagues returned but every one COMPLETE is the off-season: the fantasy
//     playoffs have ended and Sleeper has not yet moved `season` forward. It
//     prints one line, records ledger outcome off_season and exits 0 — the
//     baseball season gate's shape (cmd/season_gate.go) — where it used to
//     fail every run from early January until the rollover.
//   - A failed fetch for EITHER season fails discovery. Proceeding on the
//     season that answered would silently drop every league under the other,
//     which is the exact blind spot this two-season query exists to close.
func discoverLeagues(ctx context.Context, sc leagueLister, cfg *FootballConfig, state *sleeper.NFLState, out io.Writer) ([]leagueContext, error) {
	if err := cfg.requireSleeperUserID(); err != nil {
		return nil, err
	}
	seasons := discoverySeasons(state)
	var (
		leagues []sleeper.League
		seen    = map[string]bool{}
	)
	for _, season := range seasons {
		ls, err := sc.LeaguesForUser(ctx, cfg.SleeperUserID, "nfl", season)
		if err != nil {
			return nil, fmt.Errorf("sleeper leagues for user %s (%s): %w", cfg.SleeperUserID, season, err)
		}
		for _, lg := range ls {
			if seen[lg.LeagueID] {
				continue
			}
			seen[lg.LeagueID] = true
			leagues = append(leagues, lg)
		}
	}
	label := strings.Join(seasons, ", ")
	if len(leagues) == 0 {
		return nil, fmt.Errorf("sleeper user %s has no NFL leagues for %s (0 returned)", cfg.SleeperUserID, label)
	}
	lcs := leagueContexts(leagues, cfg.FormatOverrides)
	if len(lcs) == 0 {
		line := fmt.Sprintf("Off-season: every Sleeper NFL league for user %s is complete (%d league(s) returned across %s). Nothing to do.",
			cfg.SleeperUserID, len(leagues), label)
		fmt.Fprintln(out, line)
		recordRunOutcome(lineupapi.RunOutcomeOffSeason)
		return nil, &offSeasonStop{line: line}
	}
	for _, lc := range lcs {
		p := lc.Profile
		fmt.Fprintf(out, "football: league %q (%s) kind=%s superflex=%v format=%s (%s)\n",
			p.Name, p.LeagueID, p.Kind, p.Superflex, p.Format, p.FormatSource)
	}
	return lcs, nil
}
