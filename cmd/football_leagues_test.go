package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func TestLeagueContexts_SkipsCompleteAndSortsByName(t *testing.T) {
	in := []sleeper.League{
		{LeagueID: "3", Name: "Zeta", Status: "in_season", Settings: map[string]int{"type": 0}},
		{LeagueID: "1", Name: "Done", Status: "complete", Settings: map[string]int{"type": 2}},
		{LeagueID: "2", Name: "Alpha", Status: "pre_draft", Settings: map[string]int{"type": 2}, RosterPositions: []string{"SUPER_FLEX"}},
	}
	got := leagueContexts(in, nil)
	if len(got) != 2 || got[0].League.Name != "Alpha" || got[1].League.Name != "Zeta" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Profile.Kind != dynasty.KindDynasty || !got[0].Profile.Superflex {
		t.Errorf("profile not derived: %+v", got[0].Profile)
	}
}

func TestLeagueContexts_AppliesOverrides(t *testing.T) {
	in := []sleeper.League{{LeagueID: "9", Name: "K", Status: "in_season", Settings: map[string]int{"type": 1}}}
	got := leagueContexts(in, map[string]string{"9": "non_sf_redraft"})
	if got[0].Profile.Format != "non_sf_redraft" || got[0].Profile.FormatSource != "override" {
		t.Errorf("override not applied: %+v", got[0].Profile)
	}
}

// --- discovery across the season rollover (rosterbot-udkm) ---
//
// Sleeper's /state/nfl carries two seasons: `season` (the one being played)
// and `league_create_season` (the one new leagues are created into), which
// flips ahead in December. Discovery asked for `season` alone, dropped
// complete leagues and errored on zero left — so once the fantasy playoffs
// ended in early January every league was complete, the job FAILED every run
// until Sleeper moved `season` forward, and the renewed leagues living under
// `league_create_season` went unpolled for weeks. lineupapi.defaultSeason
// already handles the same rollover for the dashboard's league picker.

// fakeLeagueLister answers LeaguesForUser per season and records what it was
// asked, so the two-season query can be asserted rather than inferred.
type fakeLeagueLister struct {
	bySeason map[string][]sleeper.League
	errOn    map[string]error
	asked    []string
}

func (f *fakeLeagueLister) LeaguesForUser(_ context.Context, _, _, season string) ([]sleeper.League, error) {
	f.asked = append(f.asked, season)
	if err := f.errOn[season]; err != nil {
		return nil, err
	}
	return f.bySeason[season], nil
}

func withUser() *FootballConfig { return &FootballConfig{SleeperUserID: "u1"} }

func TestDiscoverySeasons_QueriesTheCreateSeasonOnlyWhenItDiffers(t *testing.T) {
	cases := []struct {
		name  string
		state sleeper.NFLState
		want  []string
	}{
		{"in season, both the same", sleeper.NFLState{Season: "2026", LeagueCreateSeason: "2026"}, []string{"2026"}},
		{"December: create season has flipped", sleeper.NFLState{Season: "2026", LeagueCreateSeason: "2027"}, []string{"2026", "2027"}},
		{"create season absent from the payload", sleeper.NFLState{Season: "2026"}, []string{"2026"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := discoverySeasons(&tc.state)
			if len(got) != len(tc.want) {
				t.Fatalf("seasons = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("seasons = %v, want %v (state.Season first)", got, tc.want)
				}
			}
		})
	}
}

func TestDiscoverLeagues_QueriesBothSeasonsAndDedupesByLeagueID(t *testing.T) {
	// Palm Trees renewed: the 2026 league is complete, the 2027 one exists
	// only under league_create_season. Mad Lux is still in season under both
	// (Sleeper lists a league under every season it spans).
	f := &fakeLeagueLister{bySeason: map[string][]sleeper.League{
		"2026": {
			{LeagueID: "palm-2026", Name: "Palm Trees", Status: "complete", Settings: map[string]int{"type": 2}},
			{LeagueID: "madlux", Name: "Mad Lux", Status: "in_season", Settings: map[string]int{"type": 2}},
		},
		"2027": {
			{LeagueID: "madlux", Name: "Mad Lux", Status: "in_season", Settings: map[string]int{"type": 2}},
			{LeagueID: "palm-2027", Name: "Palm Trees", Status: "pre_draft", Settings: map[string]int{"type": 2}},
		},
	}}
	var out strings.Builder
	got, err := discoverLeagues(context.Background(), f, withUser(), &sleeper.NFLState{Season: "2026", LeagueCreateSeason: "2027"}, &out)
	if err != nil {
		t.Fatalf("discoverLeagues: %v", err)
	}
	if len(f.asked) != 2 || f.asked[0] != "2026" || f.asked[1] != "2027" {
		t.Fatalf("must query state.Season then league_create_season; asked %v", f.asked)
	}
	ids := make([]string, 0, len(got))
	for _, lc := range got {
		ids = append(ids, lc.League.LeagueID)
	}
	if len(got) != 2 || ids[0] != "madlux" || ids[1] != "palm-2027" {
		t.Fatalf("want madlux once and palm-2027, without the complete palm-2026; got %v", ids)
	}
}

func TestDiscoverLeagues_EveryLeagueCompleteIsAnOffSeasonStop(t *testing.T) {
	outcome := filepath.Join(t.TempDir(), "outcome")
	t.Setenv(runOutcomeFileEnv, outcome)
	f := &fakeLeagueLister{bySeason: map[string][]sleeper.League{
		"2026": {{LeagueID: "a", Name: "A", Status: "complete"}, {LeagueID: "b", Name: "B", Status: "complete"}},
		"2027": {{LeagueID: "a", Name: "A", Status: "complete"}},
	}}
	var out strings.Builder
	got, err := discoverLeagues(context.Background(), f, withUser(), &sleeper.NFLState{Season: "2026", LeagueCreateSeason: "2027"}, &out)
	if got != nil {
		t.Errorf("no leagues to run over; got %v", got)
	}
	if !errors.Is(err, errOffSeason) {
		t.Fatalf("every league complete is the off-season, not a failure: err = %v", err)
	}
	if exitCodeFor(err) != 0 {
		t.Errorf("an off-season stop exits 0; got %d", exitCodeFor(err))
	}
	if !strings.Contains(out.String(), "Off-season") || !strings.Contains(out.String(), "2 league(s)") {
		t.Errorf("the one printed line must say off-season and how many complete leagues were seen; got:\n%s", out.String())
	}
	if b, rerr := os.ReadFile(outcome); rerr != nil || string(b) != lineupapi.RunOutcomeOffSeason {
		t.Errorf("ledger outcome must be %q like the baseball gate; got %q (%v)", lineupapi.RunOutcomeOffSeason, b, rerr)
	}
}

// Zero leagues RETURNED is still a configuration fault (wrong id, wrong
// seasons), never a quiet off-season: a job exiting 0 over it would read as a
// quiet week forever.
func TestDiscoverLeagues_ZeroLeaguesReturnedStaysAnError(t *testing.T) {
	f := &fakeLeagueLister{bySeason: map[string][]sleeper.League{}}
	var out strings.Builder
	_, err := discoverLeagues(context.Background(), f, withUser(), &sleeper.NFLState{Season: "2026", LeagueCreateSeason: "2027"}, &out)
	if err == nil || errors.Is(err, errOffSeason) {
		t.Fatalf("zero returned must be an ordinary error, got %v", err)
	}
	if exitCodeFor(err) != 1 {
		t.Errorf("zero returned exits non-zero; got %d", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "2026") || !strings.Contains(err.Error(), "2027") {
		t.Errorf("the error must name both seasons it asked for: %v", err)
	}
}

// A failed season fetch is a failure, not half a discovery: proceeding on the
// one season that answered would silently drop every league under the other.
func TestDiscoverLeagues_ASeasonFetchErrorFailsTheRun(t *testing.T) {
	f := &fakeLeagueLister{
		bySeason: map[string][]sleeper.League{"2026": {{LeagueID: "a", Name: "A", Status: "in_season"}}},
		errOn:    map[string]error{"2027": errors.New("sleeper GET: status 502")},
	}
	var out strings.Builder
	if _, err := discoverLeagues(context.Background(), f, withUser(), &sleeper.NFLState{Season: "2026", LeagueCreateSeason: "2027"}, &out); err == nil || errors.Is(err, errOffSeason) {
		t.Fatalf("a failed season fetch must fail discovery, got %v", err)
	}
}
