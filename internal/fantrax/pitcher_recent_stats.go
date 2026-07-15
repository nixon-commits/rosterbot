package fantrax

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/models"
)

// extractPitcherStats extracts per-player pitching stats from a single roster snapshot.
// The Fantrax API returns cumulative YTD stats regardless of period requested.
//
// Note: the Fantrax roster API may return batting-keyed stat columns for all players
// (including pitchers), so Stats.Pitching can be nil even for pitchers. We fall back
// to Stats.Batting for FP/G and GP when Stats.Pitching is unavailable.
func extractPitcherStats(roster []models.RosterPlayer, fptsMap map[string]float64) map[string]RecentStat {
	result := make(map[string]RecentStat)

	for _, rp := range roster {
		if rp.Stats == nil {
			continue
		}

		// Only extract stats for pitchers.
		if !isPitcherByName(rp.PosShortNames) {
			continue
		}

		var gp int
		var fpg float64

		if rp.Stats.Pitching != nil && rp.Stats.Pitching.GamesPlayed != nil {
			// Preferred: use pitching stats when available.
			gp = *rp.Stats.Pitching.GamesPlayed
			if rp.Stats.Pitching.FantasyPointsPerGame != nil {
				fpg = *rp.Stats.Pitching.FantasyPointsPerGame
			}
		} else if rp.Stats.Batting != nil && rp.Stats.Batting.GamesPlayed != nil {
			gp = *rp.Stats.Batting.GamesPlayed
			if rp.Stats.Batting.FantasyPointsPerGame != nil {
				fpg = *rp.Stats.Batting.FantasyPointsPerGame
			}
		} else {
			// The roster API returned batting-keyed stat columns for this pitcher.
			// FP/G is available via fptsPerGame but GP is not. Derive GP from
			// total FPts (extracted from raw response) and FP/G: GP = round(FPts / FP/G).
			fpg = getFPG(rp)
			if fpg == 0 {
				continue
			}
			if fpts, ok := fptsMap[rp.PlayerID]; ok && fpts != 0 {
				gp = int(math.Round(fpts / fpg))
			}
			if gp <= 0 {
				continue
			}
		}

		result[rp.PlayerID] = RecentStat{GamesPlayed: gp, FPtsPerGame: fpg}
	}

	return result
}

// getFPG extracts FP/G from whichever stat struct is available.
func getFPG(rp models.RosterPlayer) float64 {
	if rp.Stats.Pitching != nil && rp.Stats.Pitching.FantasyPointsPerGame != nil {
		return *rp.Stats.Pitching.FantasyPointsPerGame
	}
	if rp.Stats.Batting != nil && rp.Stats.Batting.FantasyPointsPerGame != nil {
		return *rp.Stats.Batting.FantasyPointsPerGame
	}
	return 0
}

// isPitcherByName returns true if PosShortNames contains "SP" or "RP".
func isPitcherByName(posShortNames string) bool {
	return strings.Contains(posShortNames, "SP") || strings.Contains(posShortNames, "RP")
}

// GetRecentPitcherStats fetches the most recent completed period's roster and
// returns the cumulative season-to-date pitching stats for each player.
//
// It uses the raw API response to extract total FPts per pitcher, then derives
// GP = round(FPts / FP/G) for accurate blend weight calculation.
//
// Cached under fantrax-recent-stats-pitcher-<teamID>-<period> with a TTL
// determined by ttlForPeriod (30d for past, todayTTL otherwise).
func (c *Client) GetRecentPitcherStats(currentPeriod DailyPeriod, _ int) (map[string]RecentStat, error) {
	period := currentPeriod - 1
	if period < 1 {
		return nil, fmt.Errorf("no completed periods (current=%d)", currentPeriod)
	}

	if c.cacheDir == "" {
		return c.fetchRecentPitcherStats(period)
	}
	fc := cache.New[map[string]RecentStat](c.cacheDir, c.ttlForPeriod(period))
	key := cache.Key(keyRecentStatsPitcher, c.teamID, strconv.Itoa(int(period)))
	return fc.Get(key, func() (map[string]RecentStat, error) {
		return c.fetchRecentPitcherStats(period)
	})
}

func (c *Client) fetchRecentPitcherStats(period DailyPeriod) (map[string]RecentStat, error) {
	raw, err := c.auth.GetTeamRosterInfoRaw(strconv.Itoa(int(period)), c.teamID)
	if err != nil {
		return nil, fmt.Errorf("fetch roster for period %d: %w", period, err)
	}

	// Extract total FPts per player from the raw response cells.
	// The parsed roster doesn't include total FPts, only FP/G.
	fptsMap := extractRawFPts(raw)

	// Also parse the roster normally for FP/G and position info.
	roster, err := c.auth.GetTeamRosterInfo(strconv.Itoa(int(period)), c.teamID)
	if err != nil {
		return nil, fmt.Errorf("parse roster for period %d: %w", period, err)
	}

	players := append(roster.ActiveRoster, roster.ReserveRoster...)
	return extractPitcherStats(players, fptsMap), nil
}

// extractRawFPts scans the raw roster response for total fantasy points per player.
// It looks for the "fpts" column key in each table and maps scorerID → total FPts.
func extractRawFPts(raw *models.TeamRosterResponse) map[string]float64 {
	result := make(map[string]float64)
	if raw == nil || len(raw.Responses) == 0 {
		return result
	}

	data := raw.Responses[0].Data
	for _, table := range data.Tables {
		// Find the "fpts" column index.
		fptsIdx := -1
		for i, col := range table.Header.Cells {
			if col.Key == "fpts" {
				fptsIdx = i
				break
			}
		}
		if fptsIdx < 0 {
			continue
		}

		for _, row := range table.Rows {
			if row.IsEmptyRosterSlot || row.Scorer.ScorerID == "" {
				continue
			}
			if fptsIdx < len(row.Cells) {
				if v, err := strconv.ParseFloat(row.Cells[fptsIdx].Content, 64); err == nil {
					result[row.Scorer.ScorerID] = v
				}
			}
		}
	}
	return result
}
