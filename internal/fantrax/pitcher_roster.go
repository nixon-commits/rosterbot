package fantrax

import (
	"fmt"
	"strconv"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/models"
)

// isPitcher returns true if the player has at least one pitcher position
// and no hitter positions (excludes two-way players).
func isPitcher(rp models.RosterPlayer) bool {
	hasPitcherPos := false
	for _, pos := range rp.Positions {
		if pitcherPosIDs[pos] {
			hasPitcherPos = true
		}
	}
	return hasPitcherPos && !isHitter(rp)
}

// GetPitcherRoster returns all pitchers on the team (active + reserve;
// excludes IL/minors). Cached under fantrax-pitcher-roster-<teamID> with
// todayTTL when SetCache is on.
func (c *Client) GetPitcherRoster() ([]Player, error) {
	if c.cacheDir == "" {
		return c.fetchPitcherRosterForPeriod(0)
	}
	fc := cache.New[[]Player](c.cacheDir, c.todayTTL)
	key := cache.Key("fantrax-pitcher-roster", c.teamID)
	return fc.Get(key, func() ([]Player, error) {
		return c.fetchPitcherRosterForPeriod(0)
	})
}

// GetPitcherRosterForPeriod returns all pitchers for the given scoring period.
// Pass 0 to use the current period. Past-period rosters are cached at 30d
// TTL via ttlForPeriod; current/future use todayTTL.
func (c *Client) GetPitcherRosterForPeriod(period int) ([]Player, error) {
	if c.cacheDir == "" || period == 0 {
		if period == 0 {
			return c.GetPitcherRoster()
		}
		return c.fetchPitcherRosterForPeriod(period)
	}
	fc := cache.New[[]Player](c.cacheDir, c.ttlForPeriod(period))
	key := cache.Key("fantrax-pitcher-roster", c.teamID, strconv.Itoa(period))
	return fc.Get(key, func() ([]Player, error) {
		return c.fetchPitcherRosterForPeriod(period)
	})
}

func (c *Client) fetchPitcherRosterForPeriod(period int) ([]Player, error) {
	var roster *models.TeamRoster
	var err error
	if period == 0 {
		roster, err = c.auth.GetCurrentPeriodTeamRosterInfo(c.teamID)
	} else {
		roster, err = c.auth.GetTeamRosterInfo(fmt.Sprintf("%d", period), c.teamID)
	}
	if err != nil {
		return nil, fmt.Errorf("get team roster: %w", err)
	}

	var players []Player
	for _, rp := range append(roster.ActiveRoster, roster.ReserveRoster...) {
		if !isPitcher(rp) {
			continue
		}
		players = append(players, toPlayer(rp))
	}
	return players, nil
}

// GetPitcherSlots returns the ordered list of active pitcher slots for the
// league. Cached under fantrax-pitcher-slots-<leagueID> with stableTTL.
func (c *Client) GetPitcherSlots() ([]Slot, error) {
	if c.cacheDir == "" {
		return c.fetchPitcherSlots()
	}
	fc := cache.New[[]Slot](c.cacheDir, c.stableTTL)
	key := cache.Key("fantrax-pitcher-slots", c.leagueID)
	return fc.Get(key, func() ([]Slot, error) {
		return c.fetchPitcherSlots()
	})
}

func (c *Client) fetchPitcherSlots() ([]Slot, error) {
	info, err := c.getLeagueInfo()
	if err != nil {
		return nil, fmt.Errorf("get league info: %w", err)
	}

	// Ordered: specific slots first, generic last.
	order := []string{"SP", "RP", "P"}

	var slots []Slot
	for _, name := range order {
		posID, ok := pitcherPosNameToID[name]
		if !ok {
			continue
		}
		constraint, ok := info.RosterInfo.PositionConstraints[name]
		if !ok {
			continue
		}
		for i := 0; i < constraint.MaxActive; i++ {
			slots = append(slots, Slot{PosID: posID, PosName: name})
		}
	}
	return slots, nil
}

// GetPitcherScoringWeights returns pitching stat short-names → point values.
// Cached under fantrax-pitcher-scoring-<leagueID> with stableTTL.
func (c *Client) GetPitcherScoringWeights() (ScoringWeights, error) {
	if c.cacheDir == "" {
		return c.fetchPitcherScoringWeights()
	}
	fc := cache.New[ScoringWeights](c.cacheDir, c.stableTTL)
	key := cache.Key("fantrax-pitcher-scoring", c.leagueID)
	return fc.Get(key, func() (ScoringWeights, error) {
		return c.fetchPitcherScoringWeights()
	})
}

func (c *Client) fetchPitcherScoringWeights() (ScoringWeights, error) {
	info, err := c.getLeagueInfo()
	if err != nil {
		return nil, fmt.Errorf("get league info: %w", err)
	}

	weights := make(ScoringWeights)
	for _, setting := range info.ScoringSystem.ScoringCategorySettings {
		if setting.Group.Code != "BASEBALL_PITCHING" {
			continue
		}
		for _, cfg := range setting.Configs {
			if cfg.Points != 0 {
				weights[cfg.ScoringCategory.ShortName] = cfg.Points
			}
		}
	}
	return weights, nil
}
