package fantrax

import (
	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/positions"
	"github.com/pmurley/go-fantrax/models"
)

// isPitcher returns true if the player has at least one pitcher position
// and no hitter positions (excludes two-way players).
func isPitcher(rp models.RosterPlayer) bool {
	hasPitcherPos := false
	for _, pos := range rp.Positions {
		if positions.IsPitcherSlot(pos) {
			hasPitcherPos = true
		}
	}
	return hasPitcherPos && !isHitter(rp)
}

// GetPitcherRoster returns all pitchers on the team (active + reserve;
// excludes IL/minors). Cached under fantrax-pitcher-roster-<teamID> with
// todayTTL when SetCache is on.
func (c *Client) GetPitcherRoster() ([]Player, error) {
	return cached(c, cache.Key(keyPitcherRoster, c.teamID), tierToday,
		func() ([]Player, error) { return c.fetchPitcherRosterForPeriod(0) })
}

// GetPitcherRosterForPeriod returns all pitchers for the given scoring period.
// Pass 0 to use the current period. Settled past-period rosters are cached at
// PastPeriodTTL via cachedForPeriod/periodCachePolicy; the current period and
// the still-settling ones use todayTTL.
func (c *Client) GetPitcherRosterForPeriod(period DailyPeriod) ([]Player, error) {
	// period==0 is "current" — let GetPitcherRoster handle the today-keyed cache.
	if period == 0 {
		return c.GetPitcherRoster()
	}
	return cachedForPeriod(c, keyPitcherRoster, c.teamID, period,
		func() ([]Player, error) { return c.fetchPitcherRosterForPeriod(period) })
}

func (c *Client) fetchPitcherRosterForPeriod(period DailyPeriod) ([]Player, error) {
	return c.fetchRosterForPeriod(period, isPitcher)
}

// GetPitcherSlots returns the ordered list of active pitcher slots for the
// league. Cached under fantrax-pitcher-slots-<leagueID> with stableTTL.
func (c *Client) GetPitcherSlots() ([]Slot, error) {
	return cached(c, cache.Key(keyPitcherSlots, c.leagueID), tierStable, c.fetchPitcherSlots)
}

func (c *Client) fetchPitcherSlots() ([]Slot, error) {
	// Ordered: specific slots first, generic last.
	order := []string{"SP", "RP", "P"}
	return c.fetchSlots(order, pitcherPosNameToID)
}

// GetPitcherScoringWeights returns pitching stat short-names → point values.
// Cached under fantrax-pitcher-scoring-<leagueID> with stableTTL.
func (c *Client) GetPitcherScoringWeights() (ScoringWeights, error) {
	return cached(c, cache.Key(keyPitcherScoring, c.leagueID), tierStable, c.fetchPitcherScoringWeights)
}

func (c *Client) fetchPitcherScoringWeights() (ScoringWeights, error) {
	return c.fetchWeightsForGroup("BASEBALL_PITCHING")
}
