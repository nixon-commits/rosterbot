package sleeperdir

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

type stubClient struct {
	user    *sleeper.User
	userErr error
	leagues []sleeper.League
}

func (s stubClient) UserByName(context.Context, string) (*sleeper.User, error) {
	return s.user, s.userErr
}
func (s stubClient) LeaguesForUser(_ context.Context, _, _, _ string) ([]sleeper.League, error) {
	return s.leagues, nil
}

func TestLeaguesForUsernameStampsResolvedUserIDAsTeamID(t *testing.T) {
	d := &Directory{client: stubClient{
		user:    &sleeper.User{UserID: "456"},
		leagues: []sleeper.League{{LeagueID: "1", Name: "Dynasty", Season: "2026", TotalRosters: 12}},
	}}

	got, err := d.LeaguesForUsername(context.Background(), "jnixon", "nfl", "2026")
	if err != nil {
		t.Fatalf("LeaguesForUsername: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].TeamID != "456" {
		t.Errorf("TeamID = %q, want the resolved user id 456", got[0].TeamID)
	}
	if got[0].Name != "Dynasty" {
		t.Errorf("Name = %q, want Dynasty", got[0].Name)
	}
}

// The client's not-found must arrive as the API layer's not-found, or the
// handler answers 502 for a typo'd username.
func TestLeaguesForUsernameTranslatesNotFound(t *testing.T) {
	d := &Directory{client: stubClient{userErr: sleeper.ErrUserNotFound}}
	_, err := d.LeaguesForUsername(context.Background(), "nobody", "nfl", "2026")
	if !errors.Is(err, lineupapi.ErrSleeperUserUnknown) {
		t.Errorf("err = %v, want lineupapi.ErrSleeperUserUnknown", err)
	}
}

// Status and Avatar are projected because the picker renders both: the
// subtitle reads "12 teams · 2026 · In season" and the row carries the
// league's Sleeper avatar. They are DOCUMENTED fields — unlike
// settings.type, which the Sleeper docs render as an empty object and which
// this repo deliberately does not turn into a "Redraft"/"Dynasty" label.
//
// Worth a test of its own rather than folding into the case above: a
// projection is exactly the kind of code where a field is silently dropped
// while every other assertion still passes.
func TestLeaguesForUsernameProjectsStatusAndAvatar(t *testing.T) {
	d := &Directory{client: stubClient{
		user: &sleeper.User{UserID: "456"},
		leagues: []sleeper.League{{
			LeagueID:     "1",
			Name:         "Palm Trees & Promethazine",
			Season:       "2026",
			Status:       "in_season",
			TotalRosters: 12,
			Avatar:       "20f43a91a85db31c773e6fa2b88c5362",
		}},
	}}

	got, err := d.LeaguesForUsername(context.Background(), "nix0n", "nfl", "2026")
	if err != nil {
		t.Fatalf("LeaguesForUsername: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Status != "in_season" {
		t.Errorf("Status = %q, want in_season", got[0].Status)
	}
	if got[0].Avatar != "20f43a91a85db31c773e6fa2b88c5362" {
		t.Errorf("Avatar = %q, want the league's avatar id", got[0].Avatar)
	}
	// Re-asserted here so a refactor that drops a field while adding these two
	// fails in the test that added them, not only in the older case.
	if got[0].TotalRosters != 12 || got[0].Season != "2026" {
		t.Errorf("TotalRosters/Season = %d/%q, want 12/2026", got[0].TotalRosters, got[0].Season)
	}
}
