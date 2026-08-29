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

// Pin the real client against the interface here rather than leaving it to
// New()'s struct assignment. A refactor of New() — swapping in a wrapper,
// taking the client as a parameter — would otherwise drop the only thing
// checking that *sleeper.Client still satisfies this, with no signal at the
// declaration a reader is looking at.
var _ client = (*sleeper.Client)(nil)

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
//
// The ctx is DROPPED, and the interface promising otherwise is why that is
// worth saying out loud. internal/sleeper is deliberately non-context, like
// internal/schedule — so a caller's cancellation, including an HTTP client
// hanging up, reaches nothing here. The only bound is sleeper.Client's own 10s
// timeout, applied per call: two sequential calls means a wedged Sleeper can
// hold a request for ~20s and there is no way to cut it short. Threading a ctx
// through means giving internal/sleeper context-aware methods, which is a
// change to that package's shape rather than to this adapter.
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
			Status:       l.Status,
			Avatar:       l.Avatar,
			TeamID:       u.UserID,
		})
	}
	return out, nil
}
