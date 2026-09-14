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
// an undrawn seed is not a matchup yet; neither becomes a pairing row, which is
// what makes "no matchup week" the honest answer for a team on a bye or out.
//
// One exception is load-bearing. Fantrax intermittently renders an
// in-progress round as seed placeholders (measured 2026-09-14: Round 2 was
// named at 17:40Z, "seed 4 @ seed 1" at 18:10Z, named again at 19:57Z), and
// in that state an alive team is named nowhere in its round. Without a row it
// would read as having no matchup week, and the lineup path would stop an
// alive team's lineup for the day. So every team that advanced out of the
// previous round (playoffAdvancers) and is not named in an undrawn round gets
// a PENDING row — its own id on the home side, no opponent — which gives it
// the week without inventing a pairing; the recap's pair builder ignores
// one-sided rows, and playoffEntries never emits them.
func playoffMatchups(b *auth_client.PlayoffBracket) []auth_client.Matchup {
	if b == nil {
		return nil
	}
	var out []auth_client.Matchup
	for i, r := range b.Rounds {
		named := map[string]bool{}
		for _, m := range r.Matchups {
			if m.Home.Kind == auth_client.PlayoffSlotTeam {
				named[m.Home.TeamID] = true
			}
			if m.Away.Kind == auth_client.PlayoffSlotTeam {
				named[m.Away.TeamID] = true
			}
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
		if i == 0 || roundDrawn(r) {
			continue
		}
		for _, id := range playoffAdvancers(b.Rounds[i-1]) {
			if named[id] {
				continue
			}
			out = append(out, auth_client.Matchup{
				ScoringPeriod: r.ScoringPeriod,
				Date:          r.StartDate.Format(matchupDateLayout),
				HomeTeam:      auth_client.MatchTeam{TeamID: id},
			})
		}
	}
	return out
}

// roundDrawn reports whether every pairing in the round names real teams on
// both sides or a bye — no seed placeholders.
func roundDrawn(r auth_client.PlayoffRound) bool {
	for _, m := range r.Matchups {
		if m.Home.Kind == auth_client.PlayoffSlotSeed || m.Away.Kind == auth_client.PlayoffSlotSeed {
			return false
		}
	}
	return true
}

// playoffAdvancers returns the team ids that come out of the round alive: a
// bye team, the winner of a decided game, and BOTH sides of a game that is
// unscored or tied — a game that has not decided anything cannot have
// eliminated anyone, and the safe misreading is "alive" (a lineup gets set
// for nothing) rather than "out" (a lineup goes unset for a live round).
func playoffAdvancers(r auth_client.PlayoffRound) []string {
	var out []string
	for _, m := range r.Matchups {
		home, away := m.Home.Kind == auth_client.PlayoffSlotTeam, m.Away.Kind == auth_client.PlayoffSlotTeam
		switch {
		case home && m.Away.Kind == auth_client.PlayoffSlotBye:
			out = append(out, m.Home.TeamID)
		case away && m.Home.Kind == auth_client.PlayoffSlotBye:
			out = append(out, m.Away.TeamID)
		case home && away:
			switch {
			case !m.Scored || m.HomeScore == m.AwayScore:
				out = append(out, m.Home.TeamID, m.Away.TeamID)
			case m.HomeScore > m.AwayScore:
				out = append(out, m.Home.TeamID)
			default:
				out = append(out, m.Away.TeamID)
			}
		}
	}
	return out
}

// PlayoffStanding is a team's standing in one playoff round.
type PlayoffStanding int

const (
	// PlayoffNotSeeded: the team appears in no round of the bracket.
	PlayoffNotSeeded PlayoffStanding = iota
	// PlayoffAlive: paired in the round (Opponent set), or advanced into a
	// round Fantrax has not drawn yet (Undrawn set, Opponent empty).
	PlayoffAlive
	// PlayoffBye: in the bracket this round with no opponent.
	PlayoffBye
	// PlayoffEliminated: lost in an earlier round (Round, Opponent and the
	// score line say where).
	PlayoffEliminated
)

// PlayoffStatus is what PlayoffStatusFor reports.
type PlayoffStatus struct {
	Standing PlayoffStanding
	// Round is the 1-based round the standing refers to: the round asked
	// about for Alive/Bye, the round lost for Eliminated, 0 for NotSeeded.
	Round int
	// Opponent is the opposing team's name when known.
	Opponent string
	// ScoreFor/ScoreAgainst carry the losing score line for Eliminated.
	ScoreFor, ScoreAgainst float64
	// Undrawn marks an Alive standing inferred from advancement because the
	// round shows seed placeholders rather than names.
	Undrawn bool
}

// PlayoffStatusFor reads one team's standing in the round played in the given
// weekly period off the bracket. It is the wording behind a clean stop: the
// merged matchup list already answers WHETHER the team has a scoring week
// (see playoffMatchups); this answers WHY not, so a stop can say "eliminated
// in Round 1 (lost to jimmydyl 515-605)" rather than "bye or eliminated".
func PlayoffStatusFor(b *auth_client.PlayoffBracket, teamID string, period int) PlayoffStatus {
	if b == nil {
		return PlayoffStatus{}
	}
	idx := -1
	for i, r := range b.Rounds {
		if r.ScoringPeriod == period {
			idx = i
			break
		}
	}
	if idx < 0 {
		return PlayoffStatus{}
	}
	// Named in this round?
	for _, m := range b.Rounds[idx].Matchups {
		me, other := m.Home, m.Away
		if other.Kind == auth_client.PlayoffSlotTeam && other.TeamID == teamID {
			me, other = m.Away, m.Home
		}
		if me.Kind != auth_client.PlayoffSlotTeam || me.TeamID != teamID {
			continue
		}
		switch other.Kind {
		case auth_client.PlayoffSlotBye:
			return PlayoffStatus{Standing: PlayoffBye, Round: b.Rounds[idx].Number}
		case auth_client.PlayoffSlotTeam:
			return PlayoffStatus{Standing: PlayoffAlive, Round: b.Rounds[idx].Number, Opponent: other.TeamName}
		default:
			return PlayoffStatus{Standing: PlayoffAlive, Round: b.Rounds[idx].Number, Undrawn: true}
		}
	}
	// Not named here. Walk back to the last round that names the team.
	for j := idx - 1; j >= 0; j-- {
		for _, m := range b.Rounds[j].Matchups {
			me, other, myScore, theirScore := m.Home, m.Away, m.HomeScore, m.AwayScore
			if other.Kind == auth_client.PlayoffSlotTeam && other.TeamID == teamID {
				me, other, myScore, theirScore = m.Away, m.Home, m.AwayScore, m.HomeScore
			}
			if me.Kind != auth_client.PlayoffSlotTeam || me.TeamID != teamID {
				continue
			}
			lost := other.Kind == auth_client.PlayoffSlotTeam && m.Scored && myScore < theirScore
			if lost {
				return PlayoffStatus{Standing: PlayoffEliminated, Round: b.Rounds[j].Number,
					Opponent: other.TeamName, ScoreFor: myScore, ScoreAgainst: theirScore}
			}
			// Won, sat out, or undecided: the team is alive going forward,
			// and its absence from the asked-about round means that round
			// (or one between) is not drawn yet.
			return PlayoffStatus{Standing: PlayoffAlive, Round: b.Rounds[idx].Number, Undrawn: true}
		}
	}
	return PlayoffStatus{Standing: PlayoffNotSeeded}
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
