package fantrax

import (
	"strconv"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/auth_client"
)

// gsCategoryName matches the pitcher games-started row in a GAMES_PER_POS
// category-limits list ("Games Started - Pitching (GS)" in this league).
const gsCategoryName = "Games Started"

// gsLimits is the cached shape for GetGSLimits — a small struct so the two
// *int results can share one FileCache entry.
type gsLimits struct {
	Min *int `json:"min"`
	Max *int `json:"max"`
}

// GetGSLimits returns the real Fantrax-configured min/max for the pitcher
// games-started category for the given team+period, straight from the
// league's own Min/Max position-limit settings (getTeamRosterInfo?view=
// GAMES_PER_POS) rather than a guessed constant. Fantrax scales this per
// period — a period spanning more than one calendar week (season opener,
// All-Star break) gets a proportionally larger min/max than a normal 7-day
// week, which a flat env var can't express. Either return value is nil if
// that limit isn't configured for the period.
//
// Cached under fantrax-gs-limits-<teamID>-<period> at pastPeriodTTL
// unconditionally — not via ttlForPeriod, which compares against the
// unrelated daily-period numbering (see period-drift-2026 memory). Once a
// period's min/max is set at league setup time it doesn't change again,
// past or current.
func (c *Client) GetGSLimits(teamID string, period int) (min, max *int, err error) {
	if c.cacheDir == "" {
		limits, ferr := c.fetchGSLimits(teamID, period)
		return limits.Min, limits.Max, ferr
	}
	fc := cache.New[gsLimits](c.cacheDir, pastPeriodTTL)
	key := cache.Key(keyGSLimits, teamID, strconv.Itoa(period))
	limits, ferr := fc.Get(key, func() (gsLimits, error) {
		return c.fetchGSLimits(teamID, period)
	})
	return limits.Min, limits.Max, ferr
}

func (c *Client) fetchGSLimits(teamID string, period int) (gsLimits, error) {
	gpp, err := c.auth.GetTeamRosterPositionCounts(teamID, strconv.Itoa(period))
	if err != nil {
		return gsLimits{}, err
	}
	return extractGSLimit(gpp.CategoryLimits), nil
}

// extractGSLimit finds the pitcher games-started row in a GAMES_PER_POS
// category-limits list. Returns a zero gsLimits (both nil) if the category
// isn't present.
func extractGSLimit(categories []auth_client.CategoryLimit) gsLimits {
	for _, cat := range categories {
		if strings.Contains(cat.Category, gsCategoryName) {
			return gsLimits{Min: cat.Min, Max: cat.Max}
		}
	}
	return gsLimits{}
}
