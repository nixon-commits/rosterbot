package fantrax

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
)

// DayPlayerFP is one player's per-day fantasy-points snapshot, derived by
// diffing consecutive YTD roster snapshots.
type DayPlayerFP struct {
	PlayerID      string   `json:"player_id"`
	Name          string   `json:"name"`
	MLBTeam       string   `json:"mlb_team"`
	PosShortNames string   `json:"pos_short_names"`
	SlotPosID     string   `json:"slot_pos_id"`
	StatusID      string   `json:"status_id"`
	FPts          float64  `json:"fpts"`
	Active        bool     `json:"active"`
	HadGame       bool     `json:"had_game"`
	IsPitcher     bool     `json:"is_pitcher"`
	Positions     []string `json:"positions,omitempty"`
	// NeedsBackfill is set when diffYTD couldn't compute a reliable delta —
	// either because the player first appeared on the team's snapshot mid-window
	// (delta zeroed to avoid leaking pre-team YTD) or because they crossed
	// between the hitter and pitcher tables (role-specific YTDs can't be
	// subtracted). BackfillDailyFPts uses this flag to fix the FPts value
	// from the MLB statsapi game log.
	NeedsBackfill bool `json:"needs_backfill,omitempty"`
}

// DayRoster bundles every player's per-day FPts snapshot for a single date.
type DayRoster struct {
	Date    time.Time     `json:"date"`
	Period  int           `json:"period"`
	Players []DayPlayerFP `json:"players"`
}

// playerYTD holds a single player's YTD values as extracted from one roster snapshot.
type playerYTD struct {
	PlayerID      string
	Name          string
	MLBTeam       string
	PosShortNames string
	Positions     []string
	SlotPosID     string
	StatusID      string
	FPts          float64
	GP            int
	IsPitcher     bool
}

// periodSnapshot is the cacheable extracted YTD view for one scoring period.
type periodSnapshot struct {
	Hitters  map[string]playerYTD `json:"hitters"`
	Pitchers map[string]playerYTD `json:"pitchers"`
}

// DailyFantasyPoints walks every day in [start, end] (inclusive) and returns a
// per-day snapshot of each rostered player's fantasy points. FPts are derived
// by diffing consecutive YTD roster snapshots fetched in MLB stats mode
// (StatsType=1) so that bench-day production is visible to hindsight callers.
// Players seen for the first time in the window have their delta zeroed so the
// cumulative pre-window YTD doesn't leak in as same-day production.
func (c *Client) DailyFantasyPoints(
	teamID string,
	start, end time.Time,
	seasonStart time.Time,
	cacheDir string,
	cacheTTL time.Duration,
) ([]DayRoster, error) {
	if end.Before(start) {
		return nil, fmt.Errorf("end %s before start %s", end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	snapCache := cache.New[periodSnapshot](cacheDir, cacheTTL)

	// Baseline YTD from the day before `start` so the first day in range
	// yields a single-day delta.
	prevHitters := map[string]playerYTD{}
	prevPitchers := map[string]playerYTD{}
	dayBefore := start.AddDate(0, 0, -1)
	if !dayBefore.Before(seasonStart) {
		basePeriod := PeriodForDate(seasonStart, dayBefore)
		if basePeriod >= 1 {
			base, _, err := c.getPeriodSnapshotCached(snapCache, teamID, basePeriod)
			if err != nil {
				return nil, fmt.Errorf("baseline snapshot period %d: %w", basePeriod, err)
			}
			prevHitters = base.Hitters
			prevPitchers = base.Pitchers
		}
	}

	var days []DayRoster
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		period := PeriodForDate(seasonStart, d)
		snap, hitNetwork, err := c.getPeriodSnapshotCached(snapCache, teamID, period)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s (period %d): %w", d.Format("2006-01-02"), period, err)
		}

		var players []DayPlayerFP
		players = append(players, diffYTD(snap.Hitters, prevHitters, prevPitchers, false)...)
		players = append(players, diffYTD(snap.Pitchers, prevPitchers, prevHitters, true)...)

		days = append(days, DayRoster{
			Date:    d,
			Period:  period,
			Players: players,
		})

		prevHitters = snap.Hitters
		prevPitchers = snap.Pitchers

		// Rate-limit only on real upstream calls; cache hits don't need pacing.
		if hitNetwork {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return days, nil
}

// diffYTD computes per-day FPts from current vs. prior YTD. The prevSame map is
// the prior snapshot for the same kind (hitters or pitchers); prevOther is the
// other kind. When a player isn't in prevSame the code falls back to prevOther,
// but a fallback hit means the player crossed between hitter/pitcher tables —
// the prior YTD is for a different role and subtracting it produces a phantom
// delta. In that case the delta is zeroed and NeedsBackfill is set so the
// MLB-statsapi backfill step can compute the real same-day points.
//
// Genuinely new players (waiver pickup or roster addition) are also zeroed +
// flagged: their pre-team YTD would otherwise leak in as same-day production.
func diffYTD(cur, prevSame, prevOther map[string]playerYTD, isPitcher bool) []DayPlayerFP {
	out := make([]DayPlayerFP, 0, len(cur))
	for id, now := range cur {
		pr, existed := prevSame[id]
		crossedKinds := false
		if !existed {
			pr, existed = prevOther[id]
			if existed {
				crossedKinds = true
			}
		}
		deltaFP := now.FPts - pr.FPts
		deltaGP := now.GP - pr.GP
		needsBackfill := false
		if !existed {
			deltaFP = 0
			deltaGP = 0
			needsBackfill = true
		} else if crossedKinds {
			deltaFP = 0
			deltaGP = 0
			needsBackfill = true
		}
		had := deltaFP != 0 || deltaGP > 0
		out = append(out, DayPlayerFP{
			PlayerID:      id,
			Name:          now.Name,
			MLBTeam:       now.MLBTeam,
			PosShortNames: now.PosShortNames,
			Positions:     now.Positions,
			SlotPosID:     now.SlotPosID,
			StatusID:      now.StatusID,
			FPts:          deltaFP,
			Active:        now.StatusID == "1",
			HadGame:       had,
			IsPitcher:     isPitcher,
			NeedsBackfill: needsBackfill,
		})
	}
	return out
}

// getPeriodSnapshotCached fetches a single period's YTD snapshot via the cache.
// The returned bool reports whether the upstream API was actually hit —
// callers use this to skip throttle sleeps on cache hits.
func (c *Client) getPeriodSnapshotCached(
	snapCache *cache.FileCache[periodSnapshot],
	teamID string,
	period int,
) (snap periodSnapshot, hitNetwork bool, err error) {
	key := cache.Key("fantrax-roster-stats", teamID, strconv.Itoa(period))
	snap, err = snapCache.Get(key, func() (periodSnapshot, error) {
		hitNetwork = true
		return c.fetchPeriodSnapshot(teamID, period)
	})
	return snap, hitNetwork, err
}

// fetchPeriodSnapshot pulls the raw team roster info for a period and extracts
// hitter + pitcher YTD values.
func (c *Client) fetchPeriodSnapshot(teamID string, period int) (periodSnapshot, error) {
	data := gsRosterRequest{
		LeagueID:            c.leagueID,
		Reload:              "1",
		Period:              strconv.Itoa(period),
		TeamID:              teamID,
		View:                "STATS",
		ScoringCategoryType: "1",
		StatsType:           "1",
	}
	fullRequest := map[string]interface{}{
		"msgs": []auth_client.FantraxMessage{
			{Method: "getTeamRosterInfo", Data: data},
		},
		"uiv":    3,
		"refUrl": fmt.Sprintf("https://www.fantrax.com/fantasy/league/%s/team/roster;reload=1;period=%d;teamId=%s", c.leagueID, period, teamID),
		"dt":     0,
		"at":     0,
		"av":     "0.0",
		"tz":     "UTC",
		"v":      "179.0.1",
	}
	jsonStr, err := json.Marshal(fullRequest)
	if err != nil {
		return periodSnapshot{}, fmt.Errorf("marshal roster request: %w", err)
	}
	req, err := http.NewRequest("POST", standingsURL+"?leagueId="+c.leagueID, bytes.NewBuffer(jsonStr))
	if err != nil {
		return periodSnapshot{}, fmt.Errorf("create roster request: %w", err)
	}
	resp, err := c.auth.Do(req)
	if err != nil {
		return periodSnapshot{}, fmt.Errorf("send roster request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return periodSnapshot{}, fmt.Errorf("roster API returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return periodSnapshot{}, fmt.Errorf("read roster response: %w", err)
	}
	var rosterResp models.TeamRosterResponse
	if err := json.Unmarshal(body, &rosterResp); err != nil {
		return periodSnapshot{}, fmt.Errorf("unmarshal roster response: %w", err)
	}
	if len(rosterResp.Responses) == 0 {
		return periodSnapshot{}, fmt.Errorf("no response data in roster")
	}
	tables := rosterResp.Responses[0].Data.Tables
	return playerStatsFromTables(tables), nil
}

// playerStatsFromTables walks the roster tables and extracts YTD snapshots
// for both hitters (scGroup=10) and pitchers (scGroup=20). Rows without an
// `fpts` column or without a scorer ID are skipped.
func playerStatsFromTables(tables []models.RosterTable) periodSnapshot {
	snap := periodSnapshot{
		Hitters:  map[string]playerYTD{},
		Pitchers: map[string]playerYTD{},
	}
	for _, table := range tables {
		isHitGroup := isScGroup(table.SCGroup, 10)
		isPitGroup := isScGroup(table.SCGroup, 20)
		if !isHitGroup && !isPitGroup {
			continue
		}

		fptsIdx := -1
		gpIdx := -1
		for i, col := range table.Header.Cells {
			if col.Key == "fpts" {
				fptsIdx = i
			}
			if col.ShortName == "GP" {
				gpIdx = i
			}
		}

		for _, row := range table.Rows {
			if row.Scorer.Name == "" || row.IsEmptyRosterSlot || row.Scorer.ScorerID == "" {
				continue
			}
			var fpts float64
			if fptsIdx >= 0 && fptsIdx < len(row.Cells) {
				if v, err := strconv.ParseFloat(row.Cells[fptsIdx].Content, 64); err == nil {
					fpts = v
				}
			}
			var gp int
			if gpIdx >= 0 && gpIdx < len(row.Cells) {
				if v, err := strconv.ParseFloat(row.Cells[gpIdx].Content, 64); err == nil {
					gp = int(v)
				}
			}
			y := playerYTD{
				PlayerID:      row.Scorer.ScorerID,
				Name:          row.Scorer.Name,
				MLBTeam:       row.Scorer.TeamShortName,
				PosShortNames: row.Scorer.PosShortNames,
				Positions:     append([]string(nil), row.Scorer.PosIDs...),
				SlotPosID:     row.PosID,
				StatusID:      row.StatusID,
				FPts:          fpts,
				GP:            gp,
				IsPitcher:     isPitGroup,
			}
			if isPitGroup {
				snap.Pitchers[y.PlayerID] = y
			} else {
				snap.Hitters[y.PlayerID] = y
			}
		}
	}
	return snap
}

// isScGroup returns true when the interface-typed scGroup field equals want.
// The go-fantrax model leaves it as interface{} — the wire value may be
// string "10"/"20", float64 10/20, or int 10/20.
func isScGroup(scGroup interface{}, want int) bool {
	switch v := scGroup.(type) {
	case string:
		n, err := strconv.Atoi(v)
		return err == nil && n == want
	case float64:
		return int(v) == want
	case int:
		return v == want
	}
	return false
}
