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
	"time"

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
		"v":      "179.0.1",
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
		logos[id] = info.LogoURL512
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

// gsRosterRequest adds scoringCategoryType and statsType to the standard roster request.
type gsRosterRequest struct {
	LeagueID            string `json:"leagueId"`
	Reload              string `json:"reload"`
	Period              string `json:"period"`
	TeamID              string `json:"teamId"`
	View                string `json:"view"`
	ScoringCategoryType string `json:"scoringCategoryType"`
	StatsType           string `json:"statsType"`
}

// playerGSSnapshot holds a pitcher's YTD GS, YTD fantasy points, name, MLB
// team abbreviation, and active-slot status.
type playerGSSnapshot struct {
	gs      int
	fpts    float64
	name    string
	mlbTeam string
	active  bool
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

	// Get baseline YTD GS and FPts per player as of the day before the period started.
	// For the first period of the season, baseline is zero (no prior data).
	dayBeforePeriod := sp.StartDate.AddDate(0, 0, -1)
	prevGS := map[string]int{}
	prevFPts := map[string]float64{}
	if !dayBeforePeriod.Before(seasonStart) {
		baselinePeriod := PeriodForDate(seasonStart, dayBeforePeriod)
		info, err := c.getPlayerGSSnapshotForPeriod(teamID, baselinePeriod)
		if err != nil {
			return 0, nil, fmt.Errorf("get baseline GS: %w", err)
		}
		for pid, snap := range info {
			prevGS[pid] = snap.gs
			prevFPts[pid] = snap.fpts
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
	for d := sp.StartDate; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
		period := PeriodForDate(seasonStart, d)
		info, err := c.getPlayerGSSnapshotForPeriod(teamID, period)
		if err != nil {
			return 0, nil, fmt.Errorf("get GS for %s: %w", d.Format("2006-01-02"), err)
		}

		var dayStarts []PitcherStart
		for pid, snap := range info {
			// Only count this day's GS delta if the pitcher was in an active slot.
			if snap.active {
				prev, existed := prevGS[pid]
				delta := snap.gs - prev
				capped := false
				if !existed && delta > 1 {
					delta = 1
					capped = true
				}
				if delta > 0 {
					totalGS += delta
					fptsDelta := snap.fpts - prevFPts[pid]
					dayStarts = append(dayStarts, PitcherStart{
						PitcherName: snap.name,
						FPts:        fptsDelta,
					})
					if verbose {
						mark := ""
						if capped {
							mark = fmt.Sprintf(" [FIRST APPEARANCE — raw delta=+%d, capped to 1]", snap.gs-prev)
						} else if !existed {
							mark = " [FIRST APPEARANCE]"
						}
						fmt.Printf("      [%s] %s: prev=%d now=%d delta=+%d%s\n",
							d.Format("2006-01-02"), snap.name, prev, snap.gs, delta, mark)
					}
				} else if verbose && !existed && snap.gs > 0 {
					fmt.Printf("      [%s] %s: first appearance (active, YTD=%d, no new start today)\n",
						d.Format("2006-01-02"), snap.name, snap.gs)
				}
			}
			// Retain the latest known YTD for this pitcher regardless of active
			// status, so future days can diff against his real prior YTD.
			prevGS[pid] = snap.gs
			prevFPts[pid] = snap.fpts
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
	data := gsRosterRequest{
		LeagueID:            c.leagueID,
		Reload:              "1",
		Period:              strconv.Itoa(period),
		TeamID:              teamID,
		View:                "STATS",
		ScoringCategoryType: "1",
		StatsType:           "2",
	}

	fullRequest := map[string]interface{}{
		"msgs": []auth_client.FantraxMessage{
			{
				Method: "getTeamRosterInfo",
				Data:   data,
			},
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
		return nil, fmt.Errorf("marshal roster request: %w", err)
	}

	req, err := http.NewRequest("POST", standingsURL+"?leagueId="+c.leagueID, bytes.NewBuffer(jsonStr))
	if err != nil {
		return nil, fmt.Errorf("create roster request: %w", err)
	}

	resp, err := c.auth.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send roster request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("roster API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read roster response: %w", err)
	}

	var rosterResp models.TeamRosterResponse
	if err := json.Unmarshal(body, &rosterResp); err != nil {
		return nil, fmt.Errorf("unmarshal roster response: %w", err)
	}

	if len(rosterResp.Responses) == 0 {
		return nil, fmt.Errorf("no response data in roster")
	}

	tables := rosterResp.Responses[0].Data.Tables
	return playerGSFromTables(tables)
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
				gs:      int(math.Round(val)),
				fpts:    fpts,
				name:    row.Scorer.Name,
				mlbTeam: row.Scorer.TeamShortName,
				active:  row.StatusID == "1",
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

// FindCurrentPeriod returns the period that contains today (start <= today <= end), or nil.
func FindCurrentPeriod(periods []ScoringPeriod, today time.Time) *ScoringPeriod {
	todayYMD := today.Format("2006-01-02")
	for i := range periods {
		if periods[i].StartDate.Format("2006-01-02") <= todayYMD && todayYMD <= periods[i].EndDate.Format("2006-01-02") {
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
