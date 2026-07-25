package fantrax

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// periodListEntryRe matches a Fantrax periodList dropdown entry like
// "104 (Mon Jul 6)" → group 1 = daily period number, group 2 = "Mon Jul 6".
var periodListEntryRe = regexp.MustCompile(`^(\d+)\s+\((\w+ \w+ \d+)\)$`)

// parsePeriodList turns Fantrax's periodList dropdown (DisplayedLists["periodList"])
// into a date→DailyPeriod map keyed by "2006-01-02". The label for period N is the
// calendar date whose roster snapshot lives at period N, so this is the
// authoritative daily-period numbering (self-correcting across Fantrax's mid-season
// period insertions). Year comes from seasonYear; an entry whose month precedes
// startMonth rolls to seasonYear+1 (defensive — MLB seasons don't cross a year
// boundary). Malformed/non-string entries are skipped, never fatal.
func parsePeriodList(entries []interface{}, seasonYear int, startMonth time.Month) map[string]DailyPeriod {
	out := make(map[string]DailyPeriod, len(entries))
	for _, e := range entries {
		s, ok := e.(string)
		if !ok {
			continue
		}
		m := periodListEntryRe.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		dt, err := time.Parse("Mon Jan 2 2006", fmt.Sprintf("%s %d", m[2], seasonYear))
		if err != nil {
			continue
		}
		if dt.Month() < startMonth {
			dt = dt.AddDate(1, 0, 0)
		}
		out[dt.Format("2006-01-02")] = DailyPeriod(num)
	}
	return out
}

// periodDateMap returns the authoritative date→DailyPeriod map for the season,
// fetched once (in-memory memoized like allMatchups, plus a season-stable file
// cache) from getTeamRosterInfo's DisplayedLists["periodList"]. cacheDir=="" (and
// a pre-seeded periodMapMemo, as in tests) skips the network entirely.
func (c *Client) periodDateMap(seasonStart time.Time) (map[string]DailyPeriod, error) {
	c.periodMapMu.Lock()
	defer c.periodMapMu.Unlock()
	if c.periodMapMemo != nil {
		return c.periodMapMemo, nil
	}
	build := func() (map[string]DailyPeriod, error) {
		cur, err := c.GetCurrentPeriod()
		if err != nil {
			return nil, err
		}
		raw, err := c.auth.GetTeamRosterInfoRaw(strconv.Itoa(int(cur)), c.teamID)
		if err != nil {
			return nil, err
		}
		if len(raw.Responses) == 0 {
			return nil, fmt.Errorf("period map: empty responses")
		}
		pl, _ := raw.Responses[0].Data.DisplayedLists["periodList"].([]interface{})
		if len(pl) == 0 {
			return nil, fmt.Errorf("period map: empty periodList")
		}
		return parsePeriodList(pl, seasonStart.Year(), seasonStart.Month()), nil
	}

	m, err := cached(c, cache.Key(keyPeriodDateMap, c.leagueID, strconv.Itoa(seasonStart.Year())), tierStable, build)
	if err != nil {
		return nil, err
	}
	c.periodMapMemo = m
	return m, nil
}

// (rosterbot-xyk) dailyPeriodForDate used to live here as a near-twin of
// DailyPeriodFor, differing only in that it skipped the anchor tier. With the
// anchor tier deleted the two were byte-identical, so the historical FP/GS
// walks (DailyFantasyPoints, GetTeamPitcherStarts — the rosterbot-ren callers)
// now call DailyPeriodFor in gs_period_walk.go directly. One daily resolver.
