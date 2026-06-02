package fantrax

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/playername"
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
	IP      float64 `json:"ip"`
	HA      int     `json:"ha"`
	ER      int     `json:"er"`
	BBA     int     `json:"bba"`
	K       int     `json:"k"`
	HRA     int     `json:"hra"`
	W       int     `json:"w"`
	L       int     `json:"l"`
	SV      int     `json:"sv"`
	HLD     int     `json:"hld"`
	BS      int     `json:"bs"`
	PHBP    int     `json:"phbp"` // hit-by-pitch allowed (pitching)
	WP      int     `json:"wp"`
	BK      int     `json:"bk"`
	OutsP   int     `json:"outs"` // raw outs; IP = OutsP/3
	HasGame bool    `json:"has_game"`
}

// BackfillDailyFPts walks every DayRoster and resolves NeedsBackfill rows
// by fetching the player's MLB game log for that date and re-computing FPts
// from the raw stat line via league scoring weights. Mutates `days` in place.
//
// Soft-failing: any individual row that can't be resolved (no MLBAM ID, MLB
// API unreachable, no game on the date) keeps its current value. If the row
// is resolved to "no game that day", NeedsBackfill is cleared (genuine zero).
// Hard errors (e.g., ResolveMLBAMIDs returns an error) cause the function
// to return early with the error; the caller should log and proceed —
// the un-backfilled rows are the same defensive zero the recap had before.
func (c *Client) BackfillDailyFPts(days []DayRoster) error {
	targets := collectBackfillTargets(days)
	if len(targets) == 0 {
		return nil
	}

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
		return fmt.Errorf("resolve MLB IDs: %w", err)
	}

	hitterWeights, err := c.GetScoringWeights()
	if err != nil {
		return fmt.Errorf("hitter scoring weights: %w", err)
	}
	pitcherWeights, err := c.GetPitcherScoringWeights()
	if err != nil {
		return fmt.Errorf("pitcher scoring weights: %w", err)
	}

	var resolvedCount, noGameCount, failedCount int
	for _, t := range targets {
		mlbID, ok := resolved.ByName[playername.Normalize(t.Name)]
		if !ok || mlbID == 0 {
			failedCount++
			continue
		}
		group := "hitting"
		if t.IsPitcher {
			group = "pitching"
		}
		log, err := c.fetchMLBGameLog(mlbID, group, t.Date.Year())
		if err != nil {
			failedCount++
			continue
		}
		fpts, hadGame := computeFPtsFromGameLog(log, t.Date, t.IsPitcher, hitterWeights, pitcherWeights)
		days[t.DayIdx].Players[t.PlayerIdx].FPts = fpts
		days[t.DayIdx].Players[t.PlayerIdx].HadGame = hadGame
		days[t.DayIdx].Players[t.PlayerIdx].NeedsBackfill = false
		if hadGame {
			resolvedCount++
		} else {
			noGameCount++
		}
	}

	fmt.Fprintf(os.Stderr, "mlb backfill: %d flagged, %d resolved, %d no-game, %d failed\n",
		len(targets), resolvedCount, noGameCount, failedCount)
	return nil
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
			if !p.NeedsBackfill {
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
	key := cache.Key("mlb-game-log", strconv.Itoa(mlbamID), group, strconv.Itoa(season))
	fc := cache.New[[]mlbGameLogDay](c.cacheDir, mlbBackfillGameLogTTL)
	return fc.Get(key, func() ([]mlbGameLogDay, error) {
		return fetchMLBGameLogUncached(mlbamID, group, season)
	})
}

func fetchMLBGameLogUncached(mlbamID int, group string, season int) ([]mlbGameLogDay, error) {
	url := fmt.Sprintf(mlbBackfillGameLogURL, mlbamID, group, season)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch game log: %w", err)
	}
	defer resp.Body.Close()
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
	parts := strings.SplitN(s, ".", 2)
	full, _ := strconv.Atoi(parts[0])
	partial := 0
	if len(parts) == 2 {
		partial, _ = strconv.Atoi(parts[1])
	}
	return float64(full) + float64(partial)/3.0
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

// hitterFPtsFromGame computes single-game fantasy points from a hitter
// stat line × the league's hitter scoring weights. Mirrors the algebra in
// projections.ExpectedPtsFromProj but without per-game normalization
// (input is already a single game).
func hitterFPtsFromGame(g mlbGameLogDay, w ScoringWeights) float64 {
	singles := g.H - g.Doubles - g.Triples - g.HR
	if singles < 0 {
		singles = 0
	}
	xbh := g.Doubles + g.Triples + g.HR
	tb := singles + 2*g.Doubles + 3*g.Triples + 4*g.HR

	statMap := map[string]float64{
		"1B":   float64(singles),
		"2B":   float64(g.Doubles),
		"3B":   float64(g.Triples),
		"HR":   float64(g.HR),
		"R":    float64(g.R),
		"RBI":  float64(g.RBI),
		"BB":   float64(g.BB),
		"SO":   float64(g.SO),
		"SB":   float64(g.SB),
		"CS":   float64(g.CS),
		"HBP":  float64(g.HBP),
		"GIDP": float64(g.GIDP),
		"XBH":  float64(xbh),
		"TB":   float64(tb),
	}

	var total float64
	for stat, val := range statMap {
		if pts, ok := w[stat]; ok {
			total += val * pts
		}
	}
	return total
}

// pitcherFPtsFromGame computes single-game fantasy points from a pitcher
// stat line × the league's pitcher scoring weights. QS is derived
// (IP ≥ 6 AND ER ≤ 3). CG/SHO are not derived here — the MLB game log
// flags them via a separate API endpoint that isn't worth pulling for
// the rare events this backfill covers.
func pitcherFPtsFromGame(g mlbGameLogDay, w ScoringWeights) float64 {
	qs := 0
	if g.IP >= 6 && g.ER <= 3 {
		qs = 1
	}

	statMap := map[string]float64{
		"IP":  g.IP,
		"K":   float64(g.K),
		"BB":  float64(g.BBA),
		"H":   float64(g.HA),
		"ER":  float64(g.ER),
		"HR":  float64(g.HRA),
		"W":   float64(g.W),
		"L":   float64(g.L),
		"SV":  float64(g.SV),
		"HLD": float64(g.HLD),
		"BS":  float64(g.BS),
		"QS":  float64(qs),
		"HBP": float64(g.PHBP),
		"WP":  float64(g.WP),
		"BK":  float64(g.BK),
	}

	var total float64
	for stat, val := range statMap {
		if pts, ok := w[stat]; ok {
			total += val * pts
		}
	}
	return total
}
