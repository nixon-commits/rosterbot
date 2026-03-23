package prospects

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
	"golang.org/x/sync/errgroup"
)

// URL vars (overridable in tests).
var mlbPlayerSearchURL = "https://statsapi.mlb.com/api/v1/people/search?names=%s&sportIds=11,12,13,14,1"
var mlbGameLogURL = "https://statsapi.mlb.com/api/v1/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=11,12,13,14"

// Player ID cache file path.
var playerIDCacheFile = ".prospects-cache/player-ids.json"

// ---------------------------------------------------------------------------
// Player ID cache
// ---------------------------------------------------------------------------

func loadPlayerIDCache() map[string]int {
	cache := map[string]int{}
	data, err := os.ReadFile(playerIDCacheFile)
	if err != nil {
		return cache
	}
	_ = json.Unmarshal(data, &cache)
	return cache
}

func savePlayerIDCache(cache map[string]int) {
	dir := filepath.Dir(playerIDCacheFile)
	_ = os.MkdirAll(dir, 0o755)
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(playerIDCacheFile, data, 0o644)
}

// ---------------------------------------------------------------------------
// Resolve MLB player ID
// ---------------------------------------------------------------------------

func resolveMLBPlayerID(name, team string, cache map[string]int) (int, bool) {
	key := projections.NormalizeName(name) + "|" + strings.ToLower(projections.NormalizeTeam(team))
	if id, ok := cache[key]; ok {
		return id, true
	}

	url := fmt.Sprintf(mlbPlayerSearchURL, strings.ReplaceAll(name, " ", "%20"))
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("WARNING: MLB search API error for %q: %v", name, err)
		return 0, false
	}
	defer resp.Body.Close()

	var result struct {
		People []struct {
			ID          int    `json:"id"`
			FullName    string `json:"fullName"`
			CurrentTeam struct {
				Abbreviation string `json:"abbreviation"`
			} `json:"currentTeam"`
		} `json:"people"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("WARNING: MLB search API decode error for %q: %v", name, err)
		return 0, false
	}

	normName := projections.NormalizeName(name)
	normTeam := strings.ToLower(projections.NormalizeTeam(team))

	for _, p := range result.People {
		pName := projections.NormalizeName(p.FullName)
		pTeam := strings.ToLower(projections.NormalizeTeam(p.CurrentTeam.Abbreviation))
		if pName == normName && pTeam == normTeam {
			cache[key] = p.ID
			return p.ID, true
		}
	}

	log.Printf("WARNING: no MLB ID found for %q (%s) — skipping", name, team)
	return 0, false
}

// ---------------------------------------------------------------------------
// Game log types and fetching
// ---------------------------------------------------------------------------

type gameLogEntry struct {
	Date  string
	Level string // "AAA", "AA", "A+", "A"
	// Hitter fields
	AB, H, Doubles, Triples, HR, BB, HBP, SF int
	// Pitcher fields
	IP  float64
	ER  int
	SO  int
	BBA int // walks allowed
	HA  int // hits allowed
}

func fetchGameLogs(playerID int, group string, season int) ([]gameLogEntry, error) {
	url := fmt.Sprintf(mlbGameLogURL, playerID, group, season)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching game logs: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Stats []struct {
			Splits []struct {
				Date  string `json:"date"`
				Sport struct {
					Abbreviation string `json:"abbreviation"`
				} `json:"sport"`
				Stat struct {
					AtBats       int     `json:"atBats"`
					Hits         int     `json:"hits"`
					Doubles      int     `json:"doubles"`
					Triples      int     `json:"triples"`
					HomeRuns     int     `json:"homeRuns"`
					BaseOnBalls  int     `json:"baseOnBalls"`
					HitByPitch   int     `json:"hitByPitch"`
					SacFlies     int     `json:"sacFlies"`
					InningsPitched string `json:"inningsPitched"`
					EarnedRuns   int     `json:"earnedRuns"`
					StrikeOuts   int     `json:"strikeOuts"`
					HitsAllowed  int     `json:"hits"` // pitcher context
				} `json:"stat"`
			} `json:"splits"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding game logs: %w", err)
	}

	var entries []gameLogEntry
	for _, st := range result.Stats {
		for _, split := range st.Splits {
			level := sportAbbrevToLevel(split.Sport.Abbreviation)
			e := gameLogEntry{
				Date:     split.Date,
				Level:    level,
				AB:       split.Stat.AtBats,
				H:        split.Stat.Hits,
				Doubles:  split.Stat.Doubles,
				Triples:  split.Stat.Triples,
				HR:       split.Stat.HomeRuns,
				BB:       split.Stat.BaseOnBalls,
				HBP:      split.Stat.HitByPitch,
				SF:       split.Stat.SacFlies,
				ER:       split.Stat.EarnedRuns,
				SO:       split.Stat.StrikeOuts,
				BBA:      split.Stat.BaseOnBalls,
				HA:       split.Stat.HitsAllowed,
			}
			e.IP = parseIP(split.Stat.InningsPitched)
			entries = append(entries, e)
		}
	}
	return entries, nil
}

func sportAbbrevToLevel(abbrev string) string {
	switch abbrev {
	case "AAA":
		return "AAA"
	case "AA":
		return "AA"
	case "A+":
		return "A+"
	case "A":
		return "A"
	default:
		return abbrev
	}
}

func parseIP(s string) float64 {
	if s == "" {
		return 0
	}
	var ip float64
	fmt.Sscanf(s, "%f", &ip)
	return ip
}

// ---------------------------------------------------------------------------
// Level-adjusted thresholds
// ---------------------------------------------------------------------------

var opsHotThreshold = map[string]float64{"AAA": 0.150, "AA": 0.200, "A+": 0.250, "A": 0.250}
var opsColdThreshold = -0.200 // uniform
var eraHotThreshold = map[string]float64{"AAA": -1.00, "AA": -1.50, "A+": -2.00, "A": -2.00}
var k9HotThreshold = map[string]float64{"AAA": 2.0, "AA": 2.5, "A+": 3.0, "A": 3.0}

// ---------------------------------------------------------------------------
// Hitter breakout detection
// ---------------------------------------------------------------------------

func computeOPS(logs []gameLogEntry) (ops, avg, obp, slg float64) {
	var totalAB, totalH, totalBB, totalHBP, totalSF int
	var totalDoubles, totalTriples, totalHR int
	for _, g := range logs {
		totalAB += g.AB
		totalH += g.H
		totalBB += g.BB
		totalHBP += g.HBP
		totalSF += g.SF
		totalDoubles += g.Doubles
		totalTriples += g.Triples
		totalHR += g.HR
	}
	if totalAB == 0 {
		return 0, 0, 0, 0
	}
	avg = float64(totalH) / float64(totalAB)
	denom := float64(totalAB + totalBB + totalHBP + totalSF)
	if denom > 0 {
		obp = float64(totalH+totalBB+totalHBP) / denom
	}
	singles := totalH - totalDoubles - totalTriples - totalHR
	tb := singles + 2*totalDoubles + 3*totalTriples + 4*totalHR
	slg = float64(tb) / float64(totalAB)
	ops = obp + slg
	return
}

func formatSlashLine(avg, obp, slg float64) string {
	return fmt.Sprintf(".%03.0f/.%03.0f/.%03.0f", avg*1000, obp*1000, slg*1000)
}

func computeHitterBreakout(logs []gameLogEntry, minGames int, level string) (hot, cold bool, recentLine, seasonLine string) {
	if len(logs) < minGames {
		return false, false, "", ""
	}

	recent := logs[len(logs)-minGames:]
	seasonOPS, sAvg, sOBP, sSLG := computeOPS(logs)
	recentOPS, rAvg, rOBP, rSLG := computeOPS(recent)

	delta := recentOPS - seasonOPS

	threshold, ok := opsHotThreshold[level]
	if !ok {
		threshold = 0.200
	}
	if delta > threshold {
		hot = true
	}
	if delta < opsColdThreshold {
		cold = true
	}

	recentLine = formatSlashLine(rAvg, rOBP, rSLG)
	seasonLine = formatSlashLine(sAvg, sOBP, sSLG)
	return
}

// ---------------------------------------------------------------------------
// Pitcher breakout detection
// ---------------------------------------------------------------------------

func computePitcherStats(logs []gameLogEntry) (era, k9 float64) {
	var totalIP float64
	var totalER, totalSO int
	for _, g := range logs {
		totalIP += g.IP
		totalER += g.ER
		totalSO += g.SO
	}
	if totalIP == 0 {
		return 0, 0
	}
	era = 9.0 * float64(totalER) / totalIP
	k9 = 9.0 * float64(totalSO) / totalIP
	return
}

func computePitcherBreakout(logs []gameLogEntry, minGames int, level string) (hot, cold bool, recentLine, seasonLine string) {
	if len(logs) < minGames {
		return false, false, "", ""
	}

	recent := logs[len(logs)-minGames:]
	seasonERA, seasonK9 := computePitcherStats(logs)
	recentERA, recentK9 := computePitcherStats(recent)

	eraDelta := recentERA - seasonERA // negative = improvement
	k9Delta := recentK9 - seasonK9   // positive = improvement

	eraThresh, ok := eraHotThreshold[level]
	if !ok {
		eraThresh = -1.50
	}
	k9Thresh, ok := k9HotThreshold[level]
	if !ok {
		k9Thresh = 2.5
	}

	if eraDelta < eraThresh || k9Delta > k9Thresh {
		hot = true
	}
	if eraDelta > 1.50 {
		cold = true
	}

	recentLine = fmt.Sprintf("%.2f ERA, %.1f K/9", recentERA, recentK9)
	seasonLine = fmt.Sprintf("%.2f ERA, %.1f K/9", seasonERA, seasonK9)
	return
}

// ---------------------------------------------------------------------------
// FetchPerformanceAlerts
// ---------------------------------------------------------------------------

// FetchPerformanceAlerts checks MiLB game logs for breakout/cold streaks.
func FetchPerformanceAlerts(prospects []fantrax.Player, rankings map[string]int, season, rollingDays, minGames int) ([]ProspectAlert, error) {
	cache := loadPlayerIDCache()
	var mu sync.Mutex
	var alerts []ProspectAlert

	g := new(errgroup.Group)
	sem := make(chan struct{}, 5)

	for _, p := range prospects {
		p := p
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			id, found := resolveMLBPlayerID(p.Name, p.MLBTeam, cache)
			mu.Unlock()
			if !found {
				return nil
			}

			// Determine group
			group := "hitting"
			if strings.Contains(p.PosShortNames, "SP") || strings.Contains(p.PosShortNames, "RP") {
				group = "pitching"
			}

			logs, err := fetchGameLogs(id, group, season)
			if err != nil {
				log.Printf("WARNING: game log fetch failed for %s: %v", p.Name, err)
				return nil
			}

			rank := rankings[projections.NormalizeName(p.Name)]

			var hot, cold bool
			var recentLine, seasonLine string
			isPitcher := group == "pitching"
			level := ""
			if len(logs) > 0 {
				level = logs[len(logs)-1].Level
			}

			if isPitcher {
				hot, cold, recentLine, seasonLine = computePitcherBreakout(logs, minGames, level)
			} else {
				hot, cold, recentLine, seasonLine = computeHitterBreakout(logs, minGames, level)
			}

			mu.Lock()
			defer mu.Unlock()

			if hot {
				alerts = append(alerts, ProspectAlert{
					Kind:       PerformanceHot,
					Priority:   "medium",
					PlayerName: p.Name,
					MLBTeam:    p.MLBTeam,
					Position:   p.PosShortNames,
					Detail:     fmt.Sprintf("Breaking out at %s — recent: %s vs season: %s", level, recentLine, seasonLine),
					Stats:      recentLine,
					Rank:       rank,
					IsPitcher:  isPitcher,
				})
			}
			if cold && rank > 0 && rank <= 50 {
				alerts = append(alerts, ProspectAlert{
					Kind:       PerformanceCold,
					Priority:   "low",
					PlayerName: p.Name,
					MLBTeam:    p.MLBTeam,
					Position:   p.PosShortNames,
					Detail:     fmt.Sprintf("Struggling at %s — recent: %s vs season: %s", level, recentLine, seasonLine),
					Stats:      recentLine,
					Rank:       rank,
					IsPitcher:  isPitcher,
				})
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return alerts, err
	}

	mu.Lock()
	savePlayerIDCache(cache)
	mu.Unlock()

	return alerts, nil
}
