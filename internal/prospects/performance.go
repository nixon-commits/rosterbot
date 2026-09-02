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
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/scoring"
	"golang.org/x/sync/errgroup"
)

// URL vars (overridable in tests).
var mlbGameLogURL = "https://statsapi.mlb.com/api/v1/people/%d/stats?stats=gameLog&group=%s&season=%d&sportId=11,12,13,14"

// performanceCacheDir is the directory the prospects-performance caches
// (the shared playername MLBAM-ID resolution cache, game logs) live in.
// Package-level var so tests can swap it. The pre-existing
// `.cache/player-ids.json` ad-hoc bulk file from before the cache.FileCache
// migration, and the `.cache/mlb-player-id-<name>-<team>` entries this
// package wrote before it was routed through playername.ResolveMLBAMIDs
// (rosterbot-hdiu), are both orphaned and safe to delete.
var performanceCacheDir = ".cache"

// gameLogTTL: in-season game logs grow daily; an hour is a good compromise
// between freshness and reuse across the prospects daily run + same-day
// dev iteration.
const gameLogTTL = time.Hour

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
// Name→MLBAM ID resolution is done ONCE for the whole roster via
// playername.ResolveMLBAMIDs (the same bulk season-index-then-search path
// buildRankLookups uses), rather than one people-search per player — this
// used to be a sixth, independent resolution path that bypassed the season
// index entirely and so did not carry rosterbot-1x8's namesake-collision
// guarantee (rosterbot-hdiu). Game log fetches are still persisted under
// performanceCacheDir via cache.FileCache (see gameLogTTL).
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

	names := make([]string, len(prospects))
	for i, p := range prospects {
		names[i] = p.Name
	}
	resolved, resolveErr := playername.ResolveMLBAMIDs(names, performanceCacheDir)
	if resolveErr != nil {
		// A degraded (partial) result is returned out-of-band by
		// ResolveMLBAMIDs and used as-is (see its doc comment) — this branch
		// is defensive for any other failure shape, in which case every
		// prospect below falls through to the unresolved path exactly as a
		// dead resolver would have before this change.
		log.Printf("WARNING: MLBAM ID resolution failed: %v — every prospect will be treated as unresolved", resolveErr)
	}

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
			var id int
			var found bool
			if resolved != nil {
				id, found = resolved.ByName[playername.Normalize(p.Name)]
			}
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
