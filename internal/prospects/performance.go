package prospects

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/scoring"
	"github.com/nixon-commits/rosterbot/internal/teams"
	"golang.org/x/sync/errgroup"
)

// URL vars (overridable in tests).
var mlbPlayerSearchURL = "https://statsapi.mlb.com/api/v1/people/search?names=%s&sportIds=11,12,13,14,1"
var mlbGameLogURL = "https://statsapi.mlb.com/api/v1/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=11,12,13,14"

// performanceCacheDir is the directory the prospects-performance caches
// (player IDs, game logs) live in. Package-level var so tests can swap it.
// The pre-existing `.cache/player-ids.json` ad-hoc bulk file from before
// the cache.FileCache migration is orphaned; safe to delete.
var performanceCacheDir = ".cache"

// playerIDTTL: MLB player IDs are immutable, so a 30d TTL is plenty.
const playerIDTTL = 30 * 24 * time.Hour

// gameLogTTL: in-season game logs grow daily; an hour is a good compromise
// between freshness and reuse across the prospects daily run + same-day
// dev iteration.
const gameLogTTL = time.Hour

// ---------------------------------------------------------------------------
// Resolve MLB player ID
// ---------------------------------------------------------------------------

func resolveMLBPlayerID(name, team string) (int, bool) {
	fc := cache.New[int](performanceCacheDir, playerIDTTL)
	normName := projections.NormalizeName(name)
	normTeam := strings.ToLower(teams.Normalize(team))
	key := cache.Key("mlb-player-id", normName, normTeam)

	id, err := fc.Get(key, func() (int, error) {
		got, ok := fetchMLBPlayerID(name, team, normName, normTeam)
		if !ok {
			// Don't cache misses — return a sentinel error so the cache
			// layer skips the save and the next run retries the upstream.
			return 0, fmt.Errorf("not found")
		}
		return got, nil
	})
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

func fetchMLBPlayerID(name, team, normName, normTeam string) (int, bool) {
	url := fmt.Sprintf(mlbPlayerSearchURL, strings.ReplaceAll(name, " ", "%20"))
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("WARNING: MLB search API error for %q: %v", name, err)
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()

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

	// First pass: exact name + team match.
	// Second pass: name-only match for prospects whose currentTeam is missing
	// from the API (common for players without MLB service time).
	var nameOnlyMatch int
	var nameOnlyCount int
	for _, p := range result.People {
		pName := projections.NormalizeName(p.FullName)
		if pName != normName {
			continue
		}
		pTeam := strings.ToLower(teams.Normalize(p.CurrentTeam.Abbreviation))
		if pTeam == normTeam {
			return p.ID, true
		}
		if pTeam == "" {
			nameOnlyMatch = p.ID
			nameOnlyCount++
		}
	}
	// Accept a name-only match when exactly one result had no team.
	if nameOnlyCount == 1 && nameOnlyMatch != 0 {
		return nameOnlyMatch, true
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
	fc := cache.New[[]gameLogEntry](performanceCacheDir, gameLogTTL)
	key := cache.Key("mlb-game-logs", strconv.Itoa(playerID), group, strconv.Itoa(season))
	return fc.Get(key, func() ([]gameLogEntry, error) {
		return fetchGameLogsUncached(playerID, group, season)
	})
}

func fetchGameLogsUncached(playerID int, group string, season int) ([]gameLogEntry, error) {
	url := fmt.Sprintf(mlbGameLogURL, playerID, group, season)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching game logs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Stats []struct {
			Splits []struct {
				Date  string `json:"date"`
				Sport struct {
					Abbreviation string `json:"abbreviation"`
				} `json:"sport"`
				Stat struct {
					AtBats         int    `json:"atBats"`
					Hits           int    `json:"hits"`
					Doubles        int    `json:"doubles"`
					Triples        int    `json:"triples"`
					HomeRuns       int    `json:"homeRuns"`
					BaseOnBalls    int    `json:"baseOnBalls"`
					HitByPitch     int    `json:"hitByPitch"`
					SacFlies       int    `json:"sacFlies"`
					InningsPitched string `json:"inningsPitched"`
					EarnedRuns     int    `json:"earnedRuns"`
					StrikeOuts     int    `json:"strikeOuts"`
					HitsAllowed    int    `json:"hitsAllowed"`
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
			level := split.Sport.Abbreviation
			e := gameLogEntry{
				Date:    split.Date,
				Level:   level,
				AB:      split.Stat.AtBats,
				H:       split.Stat.Hits,
				Doubles: split.Stat.Doubles,
				Triples: split.Stat.Triples,
				HR:      split.Stat.HomeRuns,
				BB:      split.Stat.BaseOnBalls,
				HBP:     split.Stat.HitByPitch,
				SF:      split.Stat.SacFlies,
				ER:      split.Stat.EarnedRuns,
				SO:      split.Stat.StrikeOuts,
				BBA:     split.Stat.BaseOnBalls,
				HA:      split.Stat.HitsAllowed,
			}
			e.IP = scoring.ParseIP(split.Stat.InningsPitched)
			entries = append(entries, e)
		}
	}
	return entries, nil
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
	k9Delta := recentK9 - seasonK9    // positive = improvement

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

// PerformanceCoverage records what the scan SAW, as distinct from what it
// found: how many prospects were handed to it, and which of them never
// resolved to an MLB player ID and were therefore silently dropped before a
// single game log was read (rosterbot-2onc).
//
// It exists because an unresolved name is not an error and must not become
// one — the job still exits 0, the run ledger still records SUCCESS, and
// opsalert still stays quiet, all correctly. What was missing is any way to
// tell a roster the scan genuinely covered from one it was structurally blind
// to: both produce the same short alert list. Only the counts separate them.
type PerformanceCoverage struct {
	// Scanned is the number of prospects handed to FetchPerformanceAlerts.
	Scanned int
	// Unresolved names the prospects whose MLB ID lookup failed, sorted.
	// Naming them rather than counting them is what turns this line into a
	// ten-second check — the same reason coverImportedHitters prints its
	// unmatched hitters instead of only a ratio. A name here is not
	// automatically a defect: a prospect too new for MLB's people-search
	// legitimately has no ID, and naming him is what makes that a decision
	// rather than an assumption.
	Unresolved []string
	// NoGameLog names the prospects who resolved to an MLB ID and were then
	// dropped anyway because the game-log fetch failed, sorted.
	//
	// This is the SECOND silent drop in the same function and it must be
	// reported separately, because the two failures point at different
	// upstreams and are not interchangeable. The people-search endpoint and
	// the game-log endpoint fail independently: an outage confined to game
	// logs leaves every ID resolving perfectly, so a coverage line counting
	// only Unresolved reports a fully-covered scan while the report it
	// produced is empty — exactly the blindness this type exists to expose,
	// reached one step later. Unresolved says "we don't know who this is";
	// NoGameLog says "we know who he is and could not read his season".
	NoGameLog []string
}

// Resolved is the count that reached a game-log fetch. Derived rather than
// stored so the counts can never disagree with themselves.
func (c PerformanceCoverage) Resolved() int { return c.Scanned - len(c.Unresolved) }

// Read is the count whose game log was actually fetched — the only figure that
// answers "did this scan look at anything", which is the question a silent
// empty report raises. Resolved() is not that figure: it counts prospects that
// merely got as far as having an ID.
func (c PerformanceCoverage) Read() int { return c.Resolved() - len(c.NoGameLog) }

// String renders the one-line coverage summary the caller prints.
//
// The caller prints it UNCONDITIONALLY, including the all-zero case, on the
// same reasoning as the "mlb recency coverage:" and "il-start check:" lines:
// this path's normal output is silence, so without the counts a fully covered
// roster and a scan that resolved nothing look identical. Scanned=0 means
// there was nothing to check; Unresolved=Scanned means the resolver is dead.
// Those are different problems and only this line tells them apart.
func (c PerformanceCoverage) String() string {
	line := fmt.Sprintf("prospect scan coverage: %d scanned, %d game logs read (%d unresolved, %d no game log)",
		c.Scanned, c.Read(), len(c.Unresolved), len(c.NoGameLog))
	if len(c.Unresolved) > 0 {
		line += "; unresolved: " + strings.Join(c.Unresolved, ", ")
	}
	if len(c.NoGameLog) > 0 {
		line += "; no game log: " + strings.Join(c.NoGameLog, ", ")
	}
	return line
}

// FetchPerformanceAlerts checks MiLB game logs for breakout/cold streaks.
// Player ID lookups and game log fetches are persisted under
// performanceCacheDir via cache.FileCache (see playerIDTTL / gameLogTTL).
//
// It returns a PerformanceCoverage alongside the alerts. Every per-prospect
// failure is deliberately soft (the closure returns nil on both drop paths),
// so g.Wait() cannot fail today and the error is returned only so a future
// hard failure cannot reach the caller unnoticed. Coverage is built and
// returned before that error is consulted, because a run that failed part-way
// still covered whatever it covered.
func FetchPerformanceAlerts(prospects []fantrax.Player, rankings map[string]int, season, rollingDays, minGames int) ([]ProspectAlert, PerformanceCoverage, error) {
	var mu sync.Mutex
	var alerts []ProspectAlert
	var unresolved, noGameLog []string

	g := new(errgroup.Group)
	// Each prospect makes up to two MLB statsapi calls (player-id resolve +
	// game-log fetch). The MLB API tolerates well above 5 concurrent
	// connections; cap at NumCPU * 2 (or 16 floor) so cold runs aren't
	// bottlenecked on a serial-by-default rate. Once cached, this loop is
	// pure file I/O and the concurrency cost is trivial.
	maxConcurrent := runtime.NumCPU() * 2
	if maxConcurrent < 16 {
		maxConcurrent = 16
	}
	g.SetLimit(maxConcurrent)

	for _, p := range prospects {
		g.Go(func() error {
			id, found := resolveMLBPlayerID(p.Name, p.MLBTeam)
			if !found {
				// Still not an error, and deliberately so: one unresolvable
				// name must not fail the prospects job or drop the rest of
				// the roster. rosterbot-2onc is about the SILENCE, not the
				// miss — record the name under the mutex that already guards
				// alerts, and let the caller report it.
				mu.Lock()
				unresolved = append(unresolved, fmt.Sprintf("%s (%s)", p.Name, p.MLBTeam))
				mu.Unlock()
				return nil
			}

			// Determine group
			group := "hitting"
			if strings.Contains(p.PosShortNames, "SP") || strings.Contains(p.PosShortNames, "RP") {
				group = "pitching"
			}

			logs, err := fetchGameLogs(id, group, season)
			if err != nil {
				// Soft for the same reason the unresolved branch is: one bad
				// game log must not fail the job. But it is a DIFFERENT drop
				// — this prospect resolved fine — so it is counted separately.
				// A game-log outage otherwise leaves every ID resolving and
				// the coverage line reporting a clean scan that read nothing.
				log.Printf("WARNING: game log fetch failed for %s: %v", p.Name, err)
				mu.Lock()
				noGameLog = append(noGameLog, fmt.Sprintf("%s (%s)", p.Name, p.MLBTeam))
				mu.Unlock()
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

	err := g.Wait()

	// Sorted here, once, because the errgroup completes in whatever order the
	// upstream answers: without this the same roster prints a different line
	// every run and a diff between two runs is unreadable.
	sort.Strings(unresolved)
	sort.Strings(noGameLog)
	cov := PerformanceCoverage{Scanned: len(prospects), Unresolved: unresolved, NoGameLog: noGameLog}

	return alerts, cov, err
}
