package fantrax

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/models"
)

// RecentStat holds season-to-date fantasy-points-per-game and games-played for
// a player, extracted from a single Fantrax roster snapshot.
type RecentStat struct {
	FPtsPerGame float64
	GamesPlayed int
}

// extractHitterStats extracts per-player batting stats from a single roster snapshot.
// The Fantrax getTeamRosterInfo API returns cumulative season-to-date stats regardless
// of which period is requested (the period parameter only controls the roster arrangement).
// Nil Stats / Batting / FantasyPointsPerGame are skipped safely.
func extractHitterStats(roster []models.RosterPlayer) map[string]RecentStat {
	result := make(map[string]RecentStat)

	for _, rp := range roster {
		if rp.Stats == nil || rp.Stats.Batting == nil {
			continue
		}
		b := rp.Stats.Batting
		if b.GamesPlayed == nil {
			continue
		}

		gp := *b.GamesPlayed
		stat := RecentStat{GamesPlayed: gp}

		if b.FantasyPointsPerGame != nil && gp > 0 {
			stat.FPtsPerGame = *b.FantasyPointsPerGame
		}

		result[rp.PlayerID] = stat
	}

	return result
}

// GetCurrentPeriod returns the current Fantrax scoring period number.
// Cached under fantrax-current-period-<leagueID>-<YYYY-MM-DD> with a 15m
// TTL when SetCache has been called.
func (c *Client) GetCurrentPeriod() (DailyPeriod, error) {
	key := cache.Key(keyCurrentPeriod, c.leagueID, time.Now().UTC().Format("2006-01-02"))
	return cached(c, key, tierToday, func() (DailyPeriod, error) {
		p, err := c.auth.GetCurrentPeriod()
		return DailyPeriod(p), err
	})
}

// seasonDateRange describes the season's first and last dates.
type seasonDateRange struct {
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// GetSeasonDateRange returns the first and last dates of the Fantrax season
// by using the scoring periods endpoint which has actual start/end dates.
// Cached under fantrax-season-range-<leagueID> with a 7d TTL when SetCache
// has been called — the season schedule is set at draft time and doesn't
// shift mid-season.
func (c *Client) GetSeasonDateRange() (time.Time, time.Time, error) {
	r, err := cached(c, cache.Key(keySeasonRange, c.leagueID), tierStable,
		func() (seasonDateRange, error) {
			first, last, err := c.fetchSeasonDateRange()
			return seasonDateRange{First: first, Last: last}, err
		})
	return r.First, r.Last, err
}

// fetchSeasonDateRange is the uncached upstream fetch.
func (c *Client) fetchSeasonDateRange() (time.Time, time.Time, error) {
	periods, _, _, err := c.GetScoringPeriodsAndTeams()
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("get scoring periods: %w", err)
	}
	if len(periods) == 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("no scoring periods found")
	}

	first := periods[0].StartDate
	last := periods[0].EndDate
	for _, p := range periods[1:] {
		if p.StartDate.Before(first) {
			first = p.StartDate
		}
		if p.EndDate.After(last) {
			last = p.EndDate
		}
	}
	return first, last, nil
}

// parseMatchupDate parses the date string from a Matchup (e.g. "Sat Apr 19, 2025").
func parseMatchupDate(s string) (time.Time, error) {
	return time.Parse("Mon Jan 2, 2006", s)
}

// PeriodForDate returns the daily scoring period number for the given date,
// counting one period per calendar day from the season start (period 1 =
// seasonStart).
//
// This is the fallback tier of fantrax.DailyPeriodFor, used when the
// authoritative periodList map is unavailable (no credentials, fetch failure,
// hermetic tests) or doesn't cover the date. Verified 2026-07-25 to agree with
// that map on all 187 dates of the 2026 season — the daily axis carries no
// merged or inserted periods, so the arithmetic is exact rather than
// approximate. Prefer DailyPeriodFor at call sites; this is its implementation
// detail, exported only because tests and cached.go's tierForPeriod use it.
//
// (rosterbot-xyk deleted the former AnchorPeriodForDate, which offset from
// Fantrax's reported current period instead of the season start. It existed to
// absorb mid-season period insertions that have never occurred, and its input
// lagged a day on 2026-07-16 — see DailyPeriodFor.)
func PeriodForDate(seasonStart, date time.Time) DailyPeriod {
	days := int(date.Truncate(24*time.Hour).Sub(seasonStart.Truncate(24*time.Hour)).Hours() / 24)
	return 1 + DailyPeriod(days)
}

// Note: the former GetRecentStats (unbounded season-to-date hitter FP/G from a
// single roster snapshot) was retired when the optimizer switched to a bounded
// trailing-30-day recency window (see cmd/optimize.go windowedHitterRecent,
// rosterbot-2nd). The window is built from the daily FPts series instead of one
// cumulative snapshot. extractHitterStats is retained for its unit tests and as
// the shared snapshot→RecentStat adapter.
