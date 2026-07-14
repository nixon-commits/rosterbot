package fantrax

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/teams"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
)

// ScoringPeriod represents a scoring period with its date range.
type ScoringPeriod struct {
	Number    int
	Caption   string
	StartDate time.Time
	EndDate   time.Time
}

var periodNumRe = regexp.MustCompile(`Scoring Period (\d+)`)
var dateRangeRe = regexp.MustCompile(`\(.*?(\w+ \w+ \d+, \d{4})\s*-\s*(\w+ \w+ \d+, \d{4})\)`)

// standingsURL is the Fantrax API endpoint for standings. Var for test overriding.
var standingsURL = "https://www.fantrax.com/fxpa/req"

// GetScoringPeriodsAndTeams fetches all scoring periods, the team ID→name
// map, and the team ID→logoURL map from a single getStandings call with
// view=SCHEDULE. The logos map may have empty values for teams that
// haven't set a logo (rare); callers should treat empty as "no avatar".
func (c *Client) GetScoringPeriodsAndTeams() ([]ScoringPeriod, map[string]string, map[string]string, error) {
	fullRequest := map[string]interface{}{
		"msgs": []auth_client.FantraxMessage{
			{
				Method: "getStandings",
				Data: map[string]string{
					"leagueId": c.leagueID,
					"view":     "SCHEDULE",
				},
			},
		},
		"uiv":    3,
		"refUrl": fmt.Sprintf("https://www.fantrax.com/fantasy/league/%s/standings", c.leagueID),
		"dt":     0,
		"at":     0,
		"av":     "0.0",
		"tz":     "UTC",
		"v":      "181.0.0",
	}

	jsonStr, err := json.Marshal(fullRequest)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal standings request: %w", err)
	}

	req, err := http.NewRequest("POST", standingsURL+"?leagueId="+c.leagueID, bytes.NewBuffer(jsonStr))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create standings request: %w", err)
	}

	resp, err := c.auth.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("send standings request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("standings API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read standings response: %w", err)
	}

	var standingsResp auth_client.StandingsResponse
	if err := json.Unmarshal(body, &standingsResp); err != nil {
		return nil, nil, nil, fmt.Errorf("unmarshal standings response: %w", err)
	}

	if len(standingsResp.Responses) == 0 {
		return nil, nil, nil, fmt.Errorf("no response data in standings")
	}

	data := standingsResp.Responses[0].Data

	teams := make(map[string]string, len(data.FantasyTeamInfo))
	logos := make(map[string]string, len(data.FantasyTeamInfo))
	for id, info := range data.FantasyTeamInfo {
		teams[id] = info.Name
		// Filter out Fantrax's stock placeholder icons (helmets, gloves,
		// jerseys served from /assets/images/icons/...) so the recap only
		// shows logos that owners actually uploaded. Custom uploads live
		// under /logos/{code}/tmLogo_{id}.
		if !strings.Contains(info.LogoURL512, "/assets/images/icons/") {
			logos[id] = info.LogoURL512
		}
	}

	var periods []ScoringPeriod
	for _, table := range data.TableList {
		m := periodNumRe.FindStringSubmatch(table.Caption)
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])

		dm := dateRangeRe.FindStringSubmatch(table.SubCaption)
		if dm == nil {
			continue
		}

		start, err := time.Parse("Mon Jan 2, 2006", dm[1])
		if err != nil {
			continue
		}
		end, err := time.Parse("Mon Jan 2, 2006", dm[2])
		if err != nil {
			continue
		}

		periods = append(periods, ScoringPeriod{
			Number:    num,
			Caption:   table.Caption,
			StartDate: start,
			EndDate:   end,
		})
	}

	return periods, teams, logos, nil
}

// playerGSSnapshot holds a pitcher's YTD GS, YTD fantasy points, name, MLB
// team abbreviation, and active-slot status. Field names are exported so
// the struct can round-trip through JSON when it's cached on disk.
type playerGSSnapshot struct {
	GS      int     `json:"gs"`
	FPts    float64 `json:"fpts"`
	Name    string  `json:"name"`
	MLBTeam string  `json:"mlb_team"`
	Active  bool    `json:"active"`
}

// PitcherStart records a single active-slot pitcher game start with its fantasy points.
type PitcherStart struct {
	PitcherName string
	FPts        float64
}

// GetTeamGS returns the total Games Started for active-slot pitchers on a team
// within the given matchup scoring period, along with the pitcher starts that
// pushed the team over gsMax (the overage starts). It checks each day individually
// so that a pitcher's GS only counts on days they were in an active lineup slot.
// A pitcher moved to IL or bench mid-period won't have later starts counted,
// and a pitcher called up mid-period will have their starts counted from the
// day they entered an active slot.
func (c *Client) GetTeamGS(teamID, teamName string, sp ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []PitcherStart, error) {
	// Use yesterday as the last completed day. If the period hasn't started yet, return 0.
	yesterday := today.Truncate(24*time.Hour).AddDate(0, 0, -1)
	if yesterday.Before(sp.StartDate) {
		return 0, nil, nil
	}
	// Cap at the period end date.
	if yesterday.After(sp.EndDate) {
		yesterday = sp.EndDate
	}

	// The per-day snapshot diff is keyed by the *daily* scoring period (one
	// number per calendar day), anchored on Fantrax's authoritative current
	// daily period. See DailyPeriodFor / gsPeriodWalk for why this must not be
	// the weekly matchup number.
	currentPeriod, _ := c.GetCurrentPeriod()
	walkPeriods := gsPeriodWalk(sp, currentPeriod, seasonStart, today)

	// Get baseline YTD GS and FPts per player as of the day before the period started.
	// For the first period of the season, baseline is zero (no prior data).
	dayBeforePeriod := sp.StartDate.AddDate(0, 0, -1)
	prevGS := map[string]int{}
	prevFPts := map[string]float64{}
	if !dayBeforePeriod.Before(seasonStart) {
		baselinePeriod := DailyPeriodFor(currentPeriod, seasonStart, today, dayBeforePeriod)
		info, err := c.getPlayerGSSnapshotForPeriod(teamID, baselinePeriod)
		if err != nil {
			return 0, nil, fmt.Errorf("get baseline GS: %w", err)
		}
		for pid, snap := range info {
			prevGS[pid] = snap.GS
			prevFPts[pid] = snap.FPts
		}
	}

	// Walk each day of the period. For each day, fetch the roster snapshot to
	// determine which pitchers are active and compute per-day GS deltas.
	// Only collect starts that occur after the team has exceeded gsMax.
	//
	// prevGS / prevFPts are retained across days (not wiped to dayGS each
	// iteration). This keeps a pitcher's last known YTD available after he
	// temporarily vanishes from the pitcher table — e.g. a two-way player like
	// Ohtani who cycles between hitter and pitcher slots, an IL round trip,
	// or a brief drop-and-re-add. Without retention, the re-appearance day
	// computes delta against prev=0 and over-counts his pre-period YTD.
	//
	// On a pitcher's first-ever appearance with nothing in prevGS (either a
	// mid-period pickup or a two-way player slotted as hitter across the
	// entire baseline-through-earlier days stretch), cap the delta at 1 since
	// a pitcher cannot earn more than one GS per day. This eliminates the
	// phantom inflation from counting his pre-period or hitter-slot starts.
	totalGS := 0
	var overageStarts []PitcherStart
	for i, d := 0, sp.StartDate; !d.After(yesterday); i, d = i+1, d.AddDate(0, 0, 1) {
		period := walkPeriods[i]
		info, err := c.getPlayerGSSnapshotForPeriod(teamID, period)
		if err != nil {
			return 0, nil, fmt.Errorf("get GS for %s: %w", d.Format("2006-01-02"), err)
		}

		var dayStarts []PitcherStart
		for pid, snap := range info {
			// Only count this day's GS delta if the pitcher was in an active slot.
			if snap.Active {
				prev, existed := prevGS[pid]
				delta := snap.GS - prev
				capped := false
				if !existed && delta > 1 {
					delta = 1
					capped = true
				}
				if delta > 0 {
					totalGS += delta
					fptsDelta := snap.FPts - prevFPts[pid]
					dayStarts = append(dayStarts, PitcherStart{
						PitcherName: snap.Name,
						FPts:        fptsDelta,
					})
					if verbose {
						mark := ""
						if capped {
							mark = fmt.Sprintf(" [FIRST APPEARANCE — raw delta=+%d, capped to 1]", snap.GS-prev)
						} else if !existed {
							mark = " [FIRST APPEARANCE]"
						}
						fmt.Printf("      [%s] %s: prev=%d now=%d delta=+%d%s\n",
							d.Format("2006-01-02"), snap.Name, prev, snap.GS, delta, mark)
					}
				} else if verbose && !existed && snap.GS > 0 {
					fmt.Printf("      [%s] %s: first appearance (active, YTD=%d, no new start today)\n",
						d.Format("2006-01-02"), snap.Name, snap.GS)
				}
			}
			// Retain the latest known YTD for this pitcher regardless of active
			// status, so future days can diff against his real prior YTD.
			prevGS[pid] = snap.GS
			prevFPts[pid] = snap.FPts
		}

		// Collect starts from any day where the team is over gsMax. All starts
		// on that day are deduction candidates — the caller picks the top N by FPts.
		if gsMax > 0 && totalGS > gsMax {
			overageStarts = append(overageStarts, dayStarts...)
		}

		// Brief pause between API calls to avoid rate limiting.
		time.Sleep(200 * time.Millisecond)
	}

	return totalGS, overageStarts, nil
}

// getPlayerGSSnapshotForPeriod returns per-player YTD GS and active-slot status for a single daily period.
func (c *Client) getPlayerGSSnapshotForPeriod(teamID string, period int) (map[string]playerGSSnapshot, error) {
	rosterResp, err := c.auth.GetTeamRosterInfoRaw(strconv.Itoa(period), teamID,
		auth_client.WithScoringCategoryType("1"),
		auth_client.WithStatsType("2"))
	if err != nil {
		return nil, fmt.Errorf("get roster for period %d: %w", period, err)
	}
	if len(rosterResp.Responses) == 0 {
		return nil, fmt.Errorf("no response data in roster")
	}
	tables := rosterResp.Responses[0].Data.Tables
	return playerGSFromTables(tables)
}

// getPlayerGSSnapshotForPeriodCached wraps getPlayerGSSnapshotForPeriod with
// a TTL'd file cache. Pass snapCache=nil (or with TTL=0) to bypass caching;
// callers like the live gs-check path skip the cache because they always
// need fresh data, while the recap pipeline reuses immutable past-period
// snapshots across runs. The returned bool reports whether the upstream
// API was actually hit — callers can use this to skip throttle sleeps on
// cache hits.
func (c *Client) getPlayerGSSnapshotForPeriodCached(
	snapCache *cache.FileCache[map[string]playerGSSnapshot],
	teamID string,
	period int,
) (snap map[string]playerGSSnapshot, hitNetwork bool, err error) {
	if snapCache == nil {
		snap, err = c.getPlayerGSSnapshotForPeriod(teamID, period)
		return snap, true, err
	}
	key := cache.Key(keyPitcherGS, teamID, strconv.Itoa(period))
	snap, err = snapCache.Get(key, func() (map[string]playerGSSnapshot, error) {
		hitNetwork = true
		return c.getPlayerGSSnapshotForPeriod(teamID, period)
	})
	return snap, hitNetwork, err
}

// playerGSFromTables finds the pitching table (scGroup=20) and returns per-player
// YTD GS, YTD fantasy points, name, and active-slot status (keyed by ScorerID).
// StatusID "1" = active slot; "2" = reserve/bench, "3" = IL, "9" = minors.
// Fantasy points come from the "fpts" column (col.Key); if absent, fpts defaults to 0.
func playerGSFromTables(tables []models.RosterTable) (map[string]playerGSSnapshot, error) {
	for _, table := range tables {
		if !isPitchingGroup(table.SCGroup) {
			continue
		}

		gsIdx := -1
		fptsIdx := -1
		for i, col := range table.Header.Cells {
			if col.ShortName == "GS" {
				gsIdx = i
			}
			if col.Key == "fpts" {
				fptsIdx = i
			}
		}
		if gsIdx == -1 {
			return map[string]playerGSSnapshot{}, nil
		}

		result := map[string]playerGSSnapshot{}
		for _, row := range table.Rows {
			// Skip totals/summary rows (empty name, non-roster status, empty slots).
			if row.Scorer.Name == "" || row.IsEmptyRosterSlot || row.Scorer.ScorerID == "" {
				continue
			}
			if gsIdx >= len(row.Cells) {
				continue
			}
			raw := row.Cells[gsIdx].Content
			if raw == "" {
				continue
			}
			val, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}

			var fpts float64
			if fptsIdx >= 0 && fptsIdx < len(row.Cells) {
				if v, err := strconv.ParseFloat(row.Cells[fptsIdx].Content, 64); err == nil {
					fpts = v
				}
			}

			result[row.Scorer.ScorerID] = playerGSSnapshot{
				GS:      int(math.Round(val)),
				FPts:    fpts,
				Name:    row.Scorer.Name,
				MLBTeam: teams.Normalize(row.Scorer.TeamShortName),
				Active:  row.StatusID == "1",
			}
		}
		return result, nil
	}

	return map[string]playerGSSnapshot{}, nil
}

// isPitchingGroup checks if scGroup represents the pitching group (20).
// SCGroup is interface{} in the model — it may be string "20" or float64 20.
func isPitchingGroup(scGroup interface{}) bool {
	switch v := scGroup.(type) {
	case string:
		return v == "20"
	case float64:
		return v == 20
	case int:
		return v == 20
	default:
		return false
	}
}

// FindJustEndedPeriod returns the period whose end date is yesterday, or nil.
func FindJustEndedPeriod(periods []ScoringPeriod, today time.Time) *ScoringPeriod {
	yesterday := today.AddDate(0, 0, -1)
	ymd := yesterday.Format("2006-01-02")
	for i := range periods {
		if periods[i].EndDate.Format("2006-01-02") == ymd {
			return &periods[i]
		}
	}
	return nil
}

// FindCurrentPeriod returns the period containing date (start <= date <= end),
// or nil. Despite the name, it's a plain range-containment check with no
// dependency on "now" — most callers pass today (hence the name). periods is
// the weekly matchup "Scoring Period" list from GetScoringPeriodsAndTeams, so
// this answers "which weekly period contains date," not "what daily period is
// date" — callers needing the latter (roster/apply/GS-snapshot lookups) want
// DailyPeriodFor instead.
func FindCurrentPeriod(periods []ScoringPeriod, date time.Time) *ScoringPeriod {
	dateYMD := date.Format("2006-01-02")
	for i := range periods {
		if periods[i].StartDate.Format("2006-01-02") <= dateYMD && dateYMD <= periods[i].EndDate.Format("2006-01-02") {
			return &periods[i]
		}
	}
	return nil
}

// FindMostRecentPastPeriod returns the most recent period whose end date is before today.
func FindMostRecentPastPeriod(periods []ScoringPeriod, today time.Time) *ScoringPeriod {
	todayYMD := today.Format("2006-01-02")
	var best *ScoringPeriod
	for i := range periods {
		if periods[i].EndDate.Format("2006-01-02") < todayYMD {
			if best == nil || periods[i].EndDate.After(best.EndDate) {
				best = &periods[i]
			}
		}
	}
	return best
}
