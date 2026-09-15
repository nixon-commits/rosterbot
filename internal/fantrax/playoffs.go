package fantrax

import (
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/auth_client"
)

// fetchPlayoffBracketFn is the seam tests use to stub the PLAYOFFS-view fetch;
// production leaves it pointing at the real call.
var fetchPlayoffBracketFn = (*Client).fetchPlayoffBracket

func (c *Client) fetchPlayoffBracket() (*auth_client.PlayoffBracket, error) {
	return c.auth.GetPlayoffBracket()
}

// GetPlayoffBracket returns the league's playoff bracket: every round with its
// weekly scoring period number, calendar bounds and pairings, plus the champion
// once decided. It is the ONLY Fantrax read that sees the playoffs — the
// standings SCHEDULE view every other period lookup is parsed from stops at
// the last regular-season period, which is why GetSeasonDateRange's "end"
// used to be the regular-season end (rosterbot-0lyz).
//
// Cached under fantrax-playoffs-<leagueID>-<year> at tierToday (15 m): scores,
// seed resolution and the champion all move while a round is live, so the
// bracket is treated like every other "today" read — at most one fetch per
// run, never a stale champion. The key's year is the calendar year of the
// fetch rather than the season-range year, so this read never triggers a
// second Fantrax round-trip to name itself; a baseball season never straddles
// a calendar year, and at a TTL of minutes the year is a namespace, not a
// settlement guarantee.
func (c *Client) GetPlayoffBracket() (*auth_client.PlayoffBracket, error) {
	key := cache.Key(keyPlayoffs, c.leagueID, strconv.Itoa(time.Now().UTC().Year()))
	b, err := cached(c, key, tierToday, func() (auth_client.PlayoffBracket, error) {
		got, err := fetchPlayoffBracketFn(c)
		if err != nil {
			return auth_client.PlayoffBracket{}, err
		}
		return *got, nil
	})
	if err != nil {
		return nil, err
	}
	return &b, nil
}
