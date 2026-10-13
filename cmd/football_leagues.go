package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/spf13/cobra"
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
//   - Zero leagues RETURNED under those seasons is checked against the
//     PREVIOUS season before anything is concluded. Between Sleeper moving
//     `season` forward and the first commissioner renewing a league, every
//     league the operator has is still last season's object and the current
//     seasons list nothing; that is the off-season (the leagues found there
//     are complete, and the next outcome handles them), not a fault, and
//     without this check the hourly offers job failed and paged on every run
//     through that gap. Nothing under any season asked is an error, not an
//     empty success — a user id with no leagues anywhere is a configuration
//     fault (wrong id) and a job that exits 0 over it would read as a quiet
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
	fetch := func(season string) error {
		ls, err := sc.LeaguesForUser(ctx, cfg.SleeperUserID, "nfl", season)
		if err != nil {
			return fmt.Errorf("sleeper leagues for user %s (%s): %w", cfg.SleeperUserID, season, err)
		}
		for _, lg := range ls {
			if seen[lg.LeagueID] {
				continue
			}
			seen[lg.LeagueID] = true
			leagues = append(leagues, lg)
		}
		return nil
	}
	for _, season := range seasons {
		if err := fetch(season); err != nil {
			return nil, err
		}
	}
	if len(leagues) == 0 {
		// Nothing under the seasons being played or created into: ask last
		// season before calling it a fault (see the doc comment). Asked only
		// now, so an in-season run never pays for or lists last season's
		// complete leagues.
		if prev := previousSeason(state.Season); prev != "" && !slices.Contains(seasons, prev) {
			seasons = append(seasons, prev)
			if err := fetch(prev); err != nil {
				return nil, err
			}
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

// previousSeason is the season before s ("2027" -> "2026"), or "" when s is
// not a year, in which case discovery has no previous season to ask.
func previousSeason(s string) string {
	y, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || y <= 0 {
		return ""
	}
	return strconv.Itoa(y - 1)
}

// discoverLeaguesOrStop is discoverLeagues for a cobra command. On the
// off-season stop discovery has already printed its one line and recorded the
// ledger outcome, so cobra is silenced and the exit-0 path prints nothing
// else, exactly as checkSeasonGate does for the baseball commands. One helper
// rather than the same four lines in each football command.
func discoverLeaguesOrStop(cmd *cobra.Command, ctx context.Context, sc leagueLister, cfg *FootballConfig, state *sleeper.NFLState) ([]leagueContext, error) {
	leagues, err := discoverLeagues(ctx, sc, cfg, state, os.Stdout)
	if err != nil && errors.Is(err, errOffSeason) {
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
	}
	return leagues, err
}

// forEachLeague is the per-league loop every multi-league football job runs
// (cmd/archive.go's per-source pattern): load a league's inputs, then run it,
// isolating failures so one league's Sleeper hiccup never silences the rest.
// A league whose load fails is warned under job's prefix, named in failed and
// skipped; the caller exits non-zero at the end so ops alerting sees it.
//
// abort, when non-nil, names the one class of load error that ends the run
// instead -- football-offers' ErrUnauthorized, where every league would fail
// the same way and six warnings would read as six problems where there is
// one: the returned error names the job and the league, and no further league
// is loaded. The three pollers (pollLeagues, pollOffers, pollPickups) are
// closures over this; accumulation stays with each job's own result type.
func forEachLeague[I any](ctx context.Context, job string, leagues []leagueContext, load func(context.Context, leagueContext) (I, error), abort func(error) bool, run func(leagueContext, I)) (failed []string, err error) {
	for _, lc := range leagues {
		in, lerr := load(ctx, lc)
		if lerr != nil {
			if abort != nil && abort(lerr) {
				return failed, fmt.Errorf("%s: %s: %w", job, lc.League.Name, lerr)
			}
			warn("%s: %s: %v (continuing with the other leagues)", job, lc.League.Name, lerr)
			failed = append(failed, lc.League.Name)
			continue
		}
		run(lc, in)
	}
	return failed, nil
}
