package fantrax

import (
	"errors"
	"fmt"
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
// once decided. A league with no playoffs configured yields an empty bracket
// (no rounds), not an error. It is the ONLY Fantrax read that sees the playoffs — the
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
		if errors.Is(err, auth_client.ErrNoPlayoffTree) {
			// A league with no playoffs configured: an empty bracket is the
			// answer, and it is cached like any other so the PLAYOFFS view
			// is not re-asked on every period lookup.
			return auth_client.PlayoffBracket{}, nil
		}
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

// bracketWithRetry is GetPlayoffBracket behind the same retry policy as the
// SCHEDULE matchups fetch it is merged with: a pure read, safe to repeat, and
// with no disk fallback on a miss.
func (c *Client) bracketWithRetry() (*auth_client.PlayoffBracket, error) {
	return withRetry("getPlayoffBracket", fantraxBackoff, c.GetPlayoffBracket)
}

// matchupDateLayout is the date format the SCHEDULE view's matchup rows carry
// and parseMatchupDate reads; playoff rows are minted in the same format so
// the week-grouping code needs no special case.
const matchupDateLayout = "Mon Jan 2, 2006"

// playoffPeriods renders each bracket round as a weekly ScoringPeriod on the
// same axis as the regular season, captioned the way Fantrax captions its own
// per-round tables and flagged Playoff.
func playoffPeriods(b *auth_client.PlayoffBracket) []ScoringPeriod {
	if b == nil {
		return nil
	}
	out := make([]ScoringPeriod, 0, len(b.Rounds))
	for _, r := range b.Rounds {
		out = append(out, ScoringPeriod{
			Number:    WeeklyPeriod(r.ScoringPeriod),
			Caption:   fmt.Sprintf("Playoffs - Round %d", r.Number),
			StartDate: r.StartDate,
			EndDate:   r.EndDate,
			Playoff:   true,
		})
	}
	return out
}

// playoffMatchups renders the bracket's REAL pairings — a team on both sides —
// as SCHEDULE-shaped matchup rows so the team-scoped week lookups
// (MatchupWeekBounds and friends) see playoff weeks. A bye has no opponent and
// an undrawn seed is not a matchup yet; neither becomes a row, which is what
// makes "no matchup week" the honest answer for a team on a bye or out.
func playoffMatchups(b *auth_client.PlayoffBracket) []auth_client.Matchup {
	if b == nil {
		return nil
	}
	var out []auth_client.Matchup
	for _, r := range b.Rounds {
		for _, m := range r.Matchups {
			if m.Home.Kind != auth_client.PlayoffSlotTeam || m.Away.Kind != auth_client.PlayoffSlotTeam {
				continue
			}
			out = append(out, auth_client.Matchup{
				ScoringPeriod: r.ScoringPeriod,
				Date:          r.StartDate.Format(matchupDateLayout),
				AwayTeam:      auth_client.MatchTeam{TeamID: m.Away.TeamID, Points: m.AwayScore, Total: m.AwayScore},
				HomeTeam:      auth_client.MatchTeam{TeamID: m.Home.TeamID, Points: m.HomeScore, Total: m.HomeScore},
			})
		}
	}
	return out
}

// playoffEntries renders the bracket for the recap: every real pairing plus
// each bye as an entry carrying the home side alone and Bye set. Undrawn
// seeds are still nothing — there is no team to name.
func playoffEntries(b *auth_client.PlayoffBracket) []MatchupEntry {
	if b == nil {
		return nil
	}
	var out []MatchupEntry
	for _, r := range b.Rounds {
		for _, m := range r.Matchups {
			e := MatchupEntry{ScoringPeriod: r.ScoringPeriod, Date: r.StartDate.Format(matchupDateLayout), Playoff: true}
			switch {
			case m.Home.Kind == auth_client.PlayoffSlotTeam && m.Away.Kind == auth_client.PlayoffSlotTeam:
				e.HomeID, e.AwayID = m.Home.TeamID, m.Away.TeamID
			case m.Home.Kind == auth_client.PlayoffSlotTeam && m.Away.Kind == auth_client.PlayoffSlotBye:
				e.HomeID, e.Bye = m.Home.TeamID, true
			case m.Away.Kind == auth_client.PlayoffSlotTeam && m.Home.Kind == auth_client.PlayoffSlotBye:
				e.HomeID, e.Bye = m.Away.TeamID, true
			default:
				continue
			}
			out = append(out, e)
		}
	}
	return out
}
