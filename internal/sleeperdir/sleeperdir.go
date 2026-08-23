// Package sleeperdir adapts internal/sleeper to lineupapi.SleeperDirectory.
//
// It exists so lineupapi never imports the HTTP client: the API package stays
// hermetic and its tests network-free, while this package — which nothing but
// wiring imports — owns the type translation and the error mapping.
package sleeperdir

import (
	"context"
	"errors"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// client is the slice of *sleeper.Client this adapter uses, named as an
// interface so the test can stub it without an httptest server.
type client interface {
	UserByName(username string) (*sleeper.User, error)
	LeaguesForUser(userID, sport, season string) ([]sleeper.League, error)
}

// Directory resolves a Sleeper username to the leagues that account is in.
type Directory struct{ client client }

// New returns a Directory over a fresh Sleeper client. cacheDir may be empty
// to disable caching, matching sleeper.Client's own rule.
func New(cacheDir string) *Directory {
	c := sleeper.NewClient()
	c.CacheDir = cacheDir
	return &Directory{client: c}
}

// LeaguesForUsername resolves the username, then lists that account's leagues.
//
// Two calls rather than one because Sleeper's league listing is keyed by user
// id, not username. The resolved id is stamped onto every row as TeamID: on
// Sleeper the account id IS the roster owner id, so this is the value that
// answers "which team in this league is yours" and it saves the client a
// second round trip when it stores the membership.
func (d *Directory) LeaguesForUsername(_ context.Context, username, sport, season string) ([]lineupapi.SleeperLeague, error) {
	u, err := d.client.UserByName(username)
	if err != nil {
		if errors.Is(err, sleeper.ErrUserNotFound) {
			return nil, lineupapi.ErrSleeperUserUnknown
		}
		return nil, err
	}
	ls, err := d.client.LeaguesForUser(u.UserID, sport, season)
	if err != nil {
		return nil, err
	}
	out := make([]lineupapi.SleeperLeague, 0, len(ls))
	for _, l := range ls {
		out = append(out, lineupapi.SleeperLeague{
			LeagueID:     l.LeagueID,
			Name:         l.Name,
			Season:       l.Season,
			TotalRosters: l.TotalRosters,
			TeamID:       u.UserID,
		})
	}
	return out, nil
}
