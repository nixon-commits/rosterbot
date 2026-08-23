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

func (s stubClient) UserByName(string) (*sleeper.User, error) { return s.user, s.userErr }
func (s stubClient) LeaguesForUser(_, _, _ string) ([]sleeper.League, error) {
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
