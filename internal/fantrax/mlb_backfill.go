package fantrax

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/scoring"
)

// mlbBackfillGameLogURL is the MLB statsapi gameLog endpoint, scoped to
// sportId=1 (MLB only). Var so tests can swap in an httptest server.
var mlbBackfillGameLogURL = "https://statsapi.mlb.com/api/v1/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=1"

// mlbBackfillGameLogTTL — in-season game logs grow daily; same cadence as
// prospects (1h compromise between freshness and warm-cache reuse).
const mlbBackfillGameLogTTL = time.Hour

// resolveBackfillNames maps Fantrax player names → MLBAM IDs for the
// backfill. Indirected through a var so tests can inject a deterministic
// resolver without going through the live MLB statsapi.
var resolveBackfillNames = func(names []string, cacheDir string) (*playername.ResolvedPlayers, error) {
	return playername.ResolveMLBAMIDs(names, cacheDir)
}

// mlbGameLogDay holds one MLB game-log row decoded from the statsapi
// response. Only the fields the league scores are pulled in.
type mlbGameLogDay struct {
	Date string `json:"date"`

	// Hitting stats
	AB      int `json:"ab"`
	H       int `json:"h"`
	Doubles int `json:"doubles"`
	Triples int `json:"triples"`
	HR      int `json:"hr"`
	R       int `json:"r"`
	RBI     int `json:"rbi"`
	BB      int `json:"bb"`
	SO      int `json:"so"`
	SB      int `json:"sb"`
	CS      int `json:"cs"`
	HBP     int `json:"hbp"`
	GIDP    int `json:"gidp"`

	// Pitching stats
	IP    float64 `json:"ip"`
	HA    int     `json:"ha"`
	ER    int     `json:"er"`
	BBA   int     `json:"bba"`
	K     int     `json:"k"`
	HRA   int     `json:"hra"`
	W     int     `json:"w"`
	L     int     `json:"l"`
	SV    int     `json:"sv"`
	HLD   int     `json:"hld"`
	BS    int     `json:"bs"`
	PHBP  int     `json:"phbp"` // hit-by-pitch allowed (pitching)
	WP    int     `json:"wp"`
	BK    int     `json:"bk"`
	OutsP int     `json:"outs"` // raw outs; IP = OutsP/3
	// BattersFaced backs the perfect-game predicate (scoring.IsPerfectGame):
	// hits/walks/HBP allowed being zero can't rule out a runner reaching on a
	// fielding error or catcher's interference, and BattersFaced == OutsP is
	// what closes that gap. See internal/scoring/bonus.go for the identity.
	BattersFaced int `json:"battersFaced"`
	// GroundOutsP/AirOutsP join K to form BatterOuts — outs charged to a
	// batter's own plate appearance. The perfect-game predicate needs them to
	// tell a genuine perfect game from a no-hitter whose only baserunner was
	// erased on the bases; see internal/scoring/bonus.go.
	GroundOutsP int  `json:"ground_outs_p"`
	AirOutsP    int  `json:"air_outs_p"`
	HasGame     bool `json:"has_game"`
}

// backfillDailyFPts walks every DayRoster and resolves needsBackfill rows
// by fetching the player's MLB game log for that date and re-computing FPts
// from the raw stat line via league scoring weights. Mutates `days` in place.
//
// It is called internally by DailyFantasyPoints so callers never see the
// needsBackfill placeholder — see that method's doc for the soft-fail contract.
//
// Soft-failing: any individual row that can't be resolved (no MLBAM ID, MLB
// API unreachable, no game on the date) keeps its current value. If the row
// is resolved to "no game that day", needsBackfill is cleared (genuine zero).
// Hard errors (e.g., ResolveMLBAMIDs returns an error) cause the function
// to return early with the error; DailyFantasyPoints logs and proceeds —
// the un-backfilled rows are the same defensive zero the recap had before.
func (c *Client) backfillDailyFPts(days []DayRoster) (backfillStats, error) {
	var stats backfillStats
	targets := collectBackfillTargets(days)
	if len(targets) == 0 {
		return stats, nil
	}
	stats.Flagged = len(targets)

	// Resolve all unique names in one batch.
	nameSet := map[string]bool{}
	var names []string
	for _, t := range targets {
		if nameSet[t.Name] {
			continue
		}
		nameSet[t.Name] = true
		names = append(names, t.Name)
	}
	resolved, err := resolveBackfillNames(names, c.cacheDir)
	if err != nil {
		return stats, fmt.Errorf("resolve MLB IDs: %w", err)
	}

	hitterWeights, err := c.GetScoringWeights()
	if err != nil {
		return stats, fmt.Errorf("hitter scoring weights: %w", err)
	}
	pitcherWeights, err := c.GetPitcherScoringWeights()
	if err != nil {
		return stats, fmt.Errorf("pitcher scoring weights: %w", err)
	}

	for _, t := range targets {
		mlbID, ok := resolved.ByName[playername.Normalize(t.Name)]
		if !ok || mlbID == 0 {
			stats.Unresolved++
			continue
		}
		group := "hitting"
		if t.IsPitcher {
			group = "pitching"
		}
		log, err := c.fetchMLBGameLog(mlbID, group, t.Date.Year())
		if err != nil {
			stats.FetchFailed++
			stats.LastFetchErr = err
			continue
		}
		fpts, hadGame := computeFPtsFromGameLog(log, t.Date, t.IsPitcher, hitterWeights, pitcherWeights)
		days[t.DayIdx].Players[t.PlayerIdx].FPts = fpts
		days[t.DayIdx].Players[t.PlayerIdx].HadGame = hadGame
		days[t.DayIdx].Players[t.PlayerIdx].needsBackfill = false
		if hadGame {
			stats.Resolved++
		} else {
			stats.NoGame++
		}
	}

	fmt.Fprintln(os.Stderr, stats.String())
	return stats, nil
}

// backfillStats is the outcome of one backfill pass.
//
// Unresolved and FetchFailed are counted SEPARATELY on purpose. They were one
// `failed` counter, which made the operator-facing log line unable to
// distinguish a name that resolved to no MLBAM ID — the poisoned-resolution
// failure of rosterbot-i52 — from an upstream game-log outage. The two have
// opposite fixes, and for months the log could not tell them apart, which is
// exactly why the throttling hypothesis went untested (rosterbot-i52 defect 3).
type backfillStats struct {
	Flagged     int
	Resolved    int
	NoGame      int
	Unresolved  int // name → MLBAM ID lookup produced nothing; no request was made
	FetchFailed int // had an ID, but the game-log request failed

	// LastFetchErr is reported alongside the counts so a fetch failure names its
	// cause instead of only its count.
	LastFetchErr error
}

func (s backfillStats) String() string {
	msg := fmt.Sprintf("mlb backfill: %d flagged, %d resolved, %d no-game, %d unresolved-name, %d fetch-failed",
		s.Flagged, s.Resolved, s.NoGame, s.Unresolved, s.FetchFailed)
	if s.LastFetchErr != nil {
		msg += fmt.Sprintf(" (last fetch error: %v)", s.LastFetchErr)
	}
	return msg
}

// MLBPlayerRef identifies one player for a roster-independent MLB game-log
// read. PlayerID is the Fantrax ID, carried through so returned rows key the
// same way DailyFantasyPoints' rows do; Name is what resolves to an MLBAM ID.
type MLBPlayerRef struct {
	PlayerID  string
	Name      string
	IsPitcher bool
}

// MLBDailyFPts returns per-day FPts for the given players over [start, end],
// read from their MLB statsapi game logs and scored with the league's weights.
//
// It is the roster-independent sibling of DailyFantasyPoints: that method
// answers "what did MY team score on this day" by diffing our own roster
// snapshots, and is therefore blind to anything a player did before we acquired
// them — correct for recap/backtest/grade, which must not credit us with
// production earned on someone else's roster, and wrong for a recency window,
// which is asking about the player's recent form regardless of who rostered
// them. Days on which a player did not appear are omitted rather than reported
// as zero, matching the DayRoster shape the recency collapse expects.
func (c *Client) MLBDailyFPts(players []MLBPlayerRef, start, end time.Time) ([]DayRoster, error) {
	if len(players) == 0 || end.Before(start) {
		return nil, nil
	}

	seen := map[string]bool{}
	var names []string
	for _, p := range players {
		if p.Name == "" || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		names = append(names, p.Name)
	}
	resolved, err := resolveBackfillNames(names, c.cacheDir)
	if err != nil {
		return nil, fmt.Errorf("resolve MLB IDs: %w", err)
	}
	hitterWeights, err := c.GetScoringWeights()
	if err != nil {
		return nil, fmt.Errorf("hitter scoring weights: %w", err)
	}
	pitcherWeights, err := c.GetPitcherScoringWeights()
	if err != nil {
		return nil, fmt.Errorf("pitcher scoring weights: %w", err)
	}

	byDate := map[time.Time][]DayPlayerFP{}
	for _, p := range players {
		// An unresolvable name is skipped, not fatal: the caller keeps whatever
		// roster-derived signal it already had for that player.
		mlbID, ok := resolved.ByName[playername.Normalize(p.Name)]
		if !ok || mlbID == 0 {
			continue
		}
		group := "hitting"
		if p.IsPitcher {
			group = "pitching"
		}
		// The game log is fetched per season, so a window straddling New Year
		// needs both. In practice a 30-day in-season window never does.
		for season := start.Year(); season <= end.Year(); season++ {
			log, err := c.fetchMLBGameLog(mlbID, group, season)
			if err != nil {
				continue
			}
			for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
				if d.Year() != season {
					continue
				}
				fpts, hadGame := computeFPtsFromGameLog(log, d, p.IsPitcher, hitterWeights, pitcherWeights)
				if !hadGame {
					continue
				}
				byDate[d] = append(byDate[d], DayPlayerFP{
					PlayerID:  p.PlayerID,
					Name:      p.Name,
					FPts:      fpts,
					HadGame:   true,
					Active:    true,
					IsPitcher: p.IsPitcher,
				})
			}
		}
	}

	out := make([]DayRoster, 0, len(byDate))
	for d, ps := range byDate {
		out = append(out, DayRoster{Date: d, Players: ps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// backfillTarget points at a single flagged DayPlayerFP row that the backfill
// needs to refresh, plus the metadata needed to fetch its MLB game log.
type backfillTarget struct {
	PlayerID  string
	Name      string
	Date      time.Time
	IsPitcher bool
	DayIdx    int
	PlayerIdx int
}

func collectBackfillTargets(days []DayRoster) []backfillTarget {
	var out []backfillTarget
	for di, d := range days {
		for pi, p := range d.Players {
			if !p.needsBackfill {
				continue
			}
			out = append(out, backfillTarget{
				PlayerID:  p.PlayerID,
				Name:      p.Name,
				Date:      d.Date,
				IsPitcher: p.IsPitcher,
				DayIdx:    di,
				PlayerIdx: pi,
			})
		}
	}
	return out
}

// fetchMLBGameLog pulls a player's full season game log for one role
// (hitting or pitching). The MLB statsapi returns the entire season in
// one request; we cache it at 1h TTL and the caller picks the target date.
func (c *Client) fetchMLBGameLog(mlbamID int, group string, season int) ([]mlbGameLogDay, error) {
	key := cache.Key(keyMLBGameLog, strconv.Itoa(mlbamID), group, strconv.Itoa(season))
	fc := cache.New[[]mlbGameLogDay](c.cacheDir, mlbBackfillGameLogTTL)
	return fc.Get(key, func() ([]mlbGameLogDay, error) {
		return fetchMLBGameLogUncached(mlbamID, group, season)
	})
}

// context.Background() is deliberate here, not an oversight: *Client's whole
// exported surface (DailyFantasyPoints, MLBDailyFPts, GetTeamGS, ApplyLineup,
// ...) is called from ~19 files across cmd/, internal/lineuprun,
// internal/recap and internal/gscheck with no ctx threaded through any of
// them today. Giving this one leaf fetch a real ctx would mean ctx-enabling
// the entire Client method surface — ScheduleClient's rosterbot-6fpv-shaped
// refactor, but an order of magnitude larger — which is out of scope for a
// noctx lint-compliance pass. This is the outermost point that can name that
// tradeoff, so the explicit Background() lives here rather than silently
// deep inside http.Get.
func fetchMLBGameLogUncached(mlbamID int, group string, season int) ([]mlbGameLogDay, error) {
	url := fmt.Sprintf(mlbBackfillGameLogURL, mlbamID, group, season)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build game log request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch game log: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("game log status %d", resp.StatusCode)
	}

	// The MLB statsapi shares JSON keys between hitting and pitching contexts
	// ("hits" = hits as batter when group=hitting, hits allowed when
	// group=pitching; same for "homeRuns", "baseOnBalls", "strikeOuts").
	// Decode into one stat struct and the caller's `group` decides which
	// fields carry meaning.
	var raw struct {
		Stats []struct {
			Splits []struct {
				Date string `json:"date"`
				Stat struct {
					// Shared keys (hitting OR pitching context)
					Hits        int `json:"hits"`
					HomeRuns    int `json:"homeRuns"`
					BaseOnBalls int `json:"baseOnBalls"`
					StrikeOuts  int `json:"strikeOuts"`
					// Hitting-only
					AtBats         int `json:"atBats"`
					Doubles        int `json:"doubles"`
					Triples        int `json:"triples"`
					Runs           int `json:"runs"`
					RBI            int `json:"rbi"`
					StolenBases    int `json:"stolenBases"`
					CaughtStealing int `json:"caughtStealing"`
					HitByPitch     int `json:"hitByPitch"`
					GIDP           int `json:"groundIntoDoublePlay"`
					// Pitching-only
					InningsPitched string `json:"inningsPitched"`
					Outs           int    `json:"outs"`
					EarnedRuns     int    `json:"earnedRuns"`
					Wins           int    `json:"wins"`
					Losses         int    `json:"losses"`
					Saves          int    `json:"saves"`
					Holds          int    `json:"holds"`
					BlownSaves     int    `json:"blownSaves"`
					HitBatsmen     int    `json:"hitBatsmen"`
					WildPitches    int    `json:"wildPitches"`
					Balks          int    `json:"balks"`
					BattersFaced   int    `json:"battersFaced"`
					GroundOuts     int    `json:"groundOuts"`
					AirOuts        int    `json:"airOuts"`
				} `json:"stat"`
			} `json:"splits"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode game log: %w", err)
	}

	var out []mlbGameLogDay
	for _, st := range raw.Stats {
		for _, sp := range st.Splits {
			d := mlbGameLogDay{Date: sp.Date, HasGame: true}
			if group == "hitting" {
				d.AB = sp.Stat.AtBats
				d.H = sp.Stat.Hits
				d.Doubles = sp.Stat.Doubles
				d.Triples = sp.Stat.Triples
				d.HR = sp.Stat.HomeRuns
				d.R = sp.Stat.Runs
				d.RBI = sp.Stat.RBI
				d.BB = sp.Stat.BaseOnBalls
				d.SO = sp.Stat.StrikeOuts
				d.SB = sp.Stat.StolenBases
				d.CS = sp.Stat.CaughtStealing
				d.HBP = sp.Stat.HitByPitch
				d.GIDP = sp.Stat.GIDP
			} else {
				d.IP = parseInningsPitched(sp.Stat.InningsPitched, sp.Stat.Outs)
				d.HA = sp.Stat.Hits
				d.ER = sp.Stat.EarnedRuns
				d.BBA = sp.Stat.BaseOnBalls
				d.K = sp.Stat.StrikeOuts
				d.HRA = sp.Stat.HomeRuns
				d.W = sp.Stat.Wins
				d.L = sp.Stat.Losses
				d.SV = sp.Stat.Saves
				d.HLD = sp.Stat.Holds
				d.BS = sp.Stat.BlownSaves
				d.PHBP = sp.Stat.HitBatsmen
				d.WP = sp.Stat.WildPitches
				d.BK = sp.Stat.Balks
				d.OutsP = sp.Stat.Outs
				d.BattersFaced = sp.Stat.BattersFaced
				d.GroundOutsP = sp.Stat.GroundOuts
				d.AirOutsP = sp.Stat.AirOuts
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// parseInningsPitched converts MLB notation ("6.1" = 6 IP + 1 out = 6.333)
// to a float. Falls back to outs/3 if the string form is empty.
func parseInningsPitched(s string, outs int) float64 {
	if s == "" {
		return float64(outs) / 3.0
	}
	return scoring.ParseIP(s)
}

// computeFPtsFromGameLog finds every game-log entry on `date` (multiple entries
// = doubleheader) and sums the FPts contributions from each, using the
// configured league scoring weights. Returns (fpts, hadGame). hadGame is false
// when no entries match the date.
func computeFPtsFromGameLog(log []mlbGameLogDay, date time.Time, isPitcher bool, hitterWeights, pitcherWeights ScoringWeights) (float64, bool) {
	targetYMD := date.Format("2006-01-02")
	var total float64
	hadGame := false
	for _, g := range log {
		if g.Date != targetYMD {
			continue
		}
		hadGame = true
		if isPitcher {
			total += pitcherFPtsFromGame(g, pitcherWeights)
		} else {
			total += hitterFPtsFromGame(g, hitterWeights)
		}
	}
	return total, hadGame
}

// hitterFPtsFromGame computes single-game fantasy points from a hitter stat
// line by adapting it to a scoring.HitterLine. No per-game division — the
// input is already a single game. The cycle bonus is added here rather than
// inside ApplyHitter (rosterbot-ec1): ApplyHitter's line can also represent a
// season/window aggregate, where "has >=1 single/double/triple/HR" is true
// for nearly every regular and would falsely fire every time.
func hitterFPtsFromGame(g mlbGameLogDay, w ScoringWeights) float64 {
	line := scoring.HitterLine{
		H: float64(g.H), Doubles: float64(g.Doubles), Triples: float64(g.Triples),
		HR: float64(g.HR), RBI: float64(g.RBI), R: float64(g.R),
		BB: float64(g.BB), SB: float64(g.SB), CS: float64(g.CS),
		HBP: float64(g.HBP), SO: float64(g.SO), GIDP: float64(g.GIDP),
	}
	total := scoring.ApplyHitter(line, w)
	if scoring.IsCycle(line) {
		total += w["CYC"]
	}
	return total
}

// pitcherFPtsFromGame computes single-game fantasy points from a pitcher stat
// line by adapting it to a scoring.PitcherLine. QS is derived here
// (IP ≥ 6 AND ER ≤ 3). CG/SHO/PKO aren't tracked in the MLB game log, so they
// are left zero — the rare events aren't worth a separate API call. NH/PG are
// event bonuses layered on afterward, same reasoning as the cycle bonus
// above: they only make sense for a single outing, never a season aggregate.
func pitcherFPtsFromGame(g mlbGameLogDay, w ScoringWeights) float64 {
	var qs float64
	if g.IP >= 6 && g.ER <= 3 {
		qs = 1
	}
	total := scoring.ApplyPitcher(scoring.PitcherLine{
		IP: g.IP, K: float64(g.K), BB: float64(g.BBA), H: float64(g.HA),
		ER: float64(g.ER), HR: float64(g.HRA), W: float64(g.W), L: float64(g.L),
		QS: qs, SV: float64(g.SV), HLD: float64(g.HLD), BS: float64(g.BS),
		HBP: float64(g.PHBP), WP: float64(g.WP), BK: float64(g.BK),
	}, w)

	gameLine := scoring.PitcherGameLine{
		Outs: g.OutsP, Hits: g.HA, Walks: g.BBA, HitBatsmen: g.PHBP,
		BattersFaced: g.BattersFaced,
		BatterOuts:   g.GroundOutsP + g.AirOutsP + g.K,
	}
	if scoring.IsNoHitter(gameLine) {
		total += w["NH"]
	}
	if scoring.IsPerfectGame(gameLine) {
		total += w["PG"]
	}
	return total
}
