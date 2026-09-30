package recap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/positions"
	"github.com/nixon-commits/rosterbot/internal/scoring"
	"github.com/nixon-commits/rosterbot/internal/statcast"
	"github.com/pmurley/go-fantrax/models"
)

// fipMinIP is the season-to-date innings floor a rostered pitcher must clear to
// appear on the FIP leaderboard. Keeps a 5-inning reliever from topping the
// board on noise while still admitting most established arms by mid-season.
const fipMinIP = 30.0

// mlbSeasonPitchingURL is the statsapi people endpoint hydrated with
// season-to-date pitching splits. var (not const) so tests can point it at an
// httptest server, matching the convention in internal/schedule and waivers.
var mlbSeasonPitchingURL = "https://statsapi.mlb.com/api/v1/people?personIds=%s&hydrate=stats(group=[pitching],type=[season],season=%d,sportId=1)"

// pitchSeason is the subset of a pitcher's season-to-date stat line needed to
// compute FIP.
type pitchSeason struct {
	IP  float64
	HR  float64
	BB  float64
	HBP float64
	SO  float64
	ER  float64
}

// leadersClient is the fantrax subset buildLeaders needs (the rest of the
// leaderboard data comes from statcast + statsapi, not fantrax).
type leadersClient interface {
	GetFullPlayerPool() ([]models.PoolPlayer, error)
}

// buildLeaders produces the league wOBA (hitters) and FIP (pitchers)
// leaderboards across all rostered players. Soft-fails to nil on any data
// error — the leaderboards are nice-to-have, the rest of the recap still
// renders.
func buildLeaders(ctx context.Context, ft leadersClient, year int, today time.Time, cacheDir string, cacheTTL time.Duration, n int) (wobaLeaders, fipLeaders []LeaderLine) {
	pool, err := ft.GetFullPlayerPool()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: league leaders (player pool): %v\n", err)
		return nil, nil
	}
	rostered := rosteredPlayers(pool)
	if len(rostered) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(rostered))
	for _, p := range rostered {
		names = append(names, p.Name)
	}
	resolved, err := playername.ResolveMLBAMIDs(names, cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: league leaders (id resolve): %v\n", err)
		return nil, nil
	}

	// Hitters → season wOBA via the Savant expected-stats CSV (qualified only).
	if savant, err := statcast.LoadBundle(ctx, cacheDir, year, today, cacheTTL); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: league leaders (savant): %v\n", err)
	} else {
		wobaLeaders = computeWOBALeaders(rostered, resolved, savant, n)
	}

	// Pitchers → season-to-date actual FIP from MLB statsapi.
	pitcherIDs := pitcherMLBAMIDs(rostered, resolved)
	if stats, err := fetchSeasonPitching(ctx, pitcherIDs, year); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: league leaders (pitching): %v\n", err)
	} else {
		fipLeaders = computeFIPLeaders(rostered, resolved, stats, n)
	}

	return wobaLeaders, fipLeaders
}

// rosteredPlayers filters the league player pool to players currently on a
// fantasy roster (FantasyTeamID set).
func rosteredPlayers(pool []models.PoolPlayer) []models.PoolPlayer {
	out := make([]models.PoolPlayer, 0, len(pool))
	for _, p := range pool {
		if p.FantasyTeamID != "" {
			out = append(out, p)
		}
	}
	return out
}

// isPitcher reports whether any of the player's eligible positions is a pitcher
// slot. Two-way players (Ohtani) are both a hitter and a pitcher and so appear
// on both boards if they qualify.
func isPitcher(p models.PoolPlayer) bool {
	for _, pos := range p.Positions {
		if positions.IsPitcherSlot(pos) {
			return true
		}
	}
	return false
}

// isHitter reports whether the player has any non-pitcher eligible position.
func isHitter(p models.PoolPlayer) bool {
	for _, pos := range p.Positions {
		if !positions.IsPitcherSlot(pos) {
			return true
		}
	}
	return false
}

// computeWOBALeaders ranks rostered hitters by season-to-date wOBA descending.
func computeWOBALeaders(rostered []models.PoolPlayer, resolved *playername.ResolvedPlayers, savant *statcast.Bundle, n int) []LeaderLine {
	var out []LeaderLine
	for _, p := range rostered {
		if !isHitter(p) {
			continue
		}
		id, ok := resolved.ByName[playername.Normalize(p.Name)]
		if !ok {
			continue
		}
		row, ok := savant.HitterExp[id]
		if !ok || row.WOBA <= 0 {
			continue
		}
		out = append(out, LeaderLine{
			Name:        p.Name,
			MLBTeam:     p.MLBTeamShortName,
			OwnerTeam:   p.FantasyTeamName,
			OwnerTeamID: p.FantasyTeamID,
			Value:       row.WOBA,
		})
	}
	sortLeaders(out, true)
	return topN(out, n)
}

// computeFIPLeaders ranks rostered pitchers by season-to-date FIP ascending
// (lower is better). The FIP constant is derived from the rostered-pitcher
// pool itself so displayed values land near league average without a separate
// league-wide fetch (ranking is unaffected by the constant).
func computeFIPLeaders(rostered []models.PoolPlayer, resolved *playername.ResolvedPlayers, stats map[int]pitchSeason, n int) []LeaderLine {
	cFIP := fipConstant(stats)
	var out []LeaderLine
	for _, p := range rostered {
		if !isPitcher(p) {
			continue
		}
		id, ok := resolved.ByName[playername.Normalize(p.Name)]
		if !ok {
			continue
		}
		s, ok := stats[id]
		if !ok || s.IP < fipMinIP {
			continue
		}
		out = append(out, LeaderLine{
			Name:        p.Name,
			MLBTeam:     p.MLBTeamShortName,
			OwnerTeam:   p.FantasyTeamName,
			OwnerTeamID: p.FantasyTeamID,
			Value:       fip(s, cFIP),
		})
	}
	sortLeaders(out, false)
	return topN(out, n)
}

// fip computes Fielding Independent Pitching for one season line.
func fip(s pitchSeason, constant float64) float64 {
	if s.IP <= 0 {
		return 0
	}
	return (13*s.HR+3*(s.BB+s.HBP)-2*s.SO)/s.IP + constant
}

// fipConstant solves for the additive constant that makes the pool's aggregate
// FIP equal its aggregate ERA: c = lgERA - (13*HR + 3*(BB+HBP) - 2*SO) / lgIP.
func fipConstant(stats map[int]pitchSeason) float64 {
	var ip, hr, bb, hbp, so, er float64
	for _, s := range stats {
		ip += s.IP
		hr += s.HR
		bb += s.BB
		hbp += s.HBP
		so += s.SO
		er += s.ER
	}
	if ip <= 0 {
		return 3.10 // reasonable modern-era fallback
	}
	lgERA := er * 9 / ip
	return lgERA - (13*hr+3*(bb+hbp)-2*so)/ip
}

// sortLeaders orders leaders by Value (desc when higherBetter, asc otherwise),
// with a stable Name tiebreaker for deterministic output.
func sortLeaders(ls []LeaderLine, higherBetter bool) {
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Value != ls[j].Value {
			if higherBetter {
				return ls[i].Value > ls[j].Value
			}
			return ls[i].Value < ls[j].Value
		}
		return ls[i].Name < ls[j].Name
	})
}

func topN(ls []LeaderLine, n int) []LeaderLine {
	if n > 0 && len(ls) > n {
		return ls[:n]
	}
	return ls
}

// pitcherMLBAMIDs collects the resolved MLBAM IDs for all rostered pitchers.
func pitcherMLBAMIDs(rostered []models.PoolPlayer, resolved *playername.ResolvedPlayers) []int {
	seen := make(map[int]bool)
	var ids []int
	for _, p := range rostered {
		if !isPitcher(p) {
			continue
		}
		if id, ok := resolved.ByName[playername.Normalize(p.Name)]; ok && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// fetchSeasonPitching pulls season-to-date pitching splits for the given MLBAM
// IDs from statsapi, chunked to keep URLs short. Returns id → season line.
func fetchSeasonPitching(ctx context.Context, ids []int, year int) (map[int]pitchSeason, error) {
	out := make(map[int]pitchSeason, len(ids))
	const chunk = 100
	for i := 0; i < len(ids); i += chunk {
		end := i + chunk
		if end > len(ids) {
			end = len(ids)
		}
		parts := make([]string, 0, end-i)
		for _, id := range ids[i:end] {
			parts = append(parts, strconv.Itoa(id))
		}
		url := fmt.Sprintf(mlbSeasonPitchingURL, strings.Join(parts, ","), year)
		if err := fetchPitchingChunk(ctx, url, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// seasonPitchingHTTPTimeout bounds one season-pitching chunk fetch. 15s matches
// the other statsapi fetches. Must never be zero: http.Client reads that as
// "no timeout", which is the rosterbot-5zp1 hang. A var so tests can shrink it.
var seasonPitchingHTTPTimeout = 15 * time.Second

// seasonPitchingBackoff is the pause before each RETRY, so len+1 is the
// attempt budget. A var so tests need not sleep.
var seasonPitchingBackoff = []time.Duration{time.Second, 4 * time.Second}

// retryableStatsAPIStatus reports whether a statsapi status is worth another
// attempt. Same rule as the game-log fetch in internal/fantrax, for the same
// upstream; each package owns its own policy here the way projections and
// fantrax already do, rather than sharing one across a package boundary for
// two call sites.
func retryableStatsAPIStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

// fetchPitchingChunk GETs one chunk of season pitching and merges it into out,
// retrying the failures that clear on their own.
//
// The client timeout makes a stalled statsapi detectable (rosterbot-5zp1); the
// retry is what keeps one stall from costing the render its leaders board. A
// read, so repeating an attempt is always safe.
func fetchPitchingChunk(ctx context.Context, url string, out map[int]pitchSeason) error {
	var lastErr error
	for attempt := 0; attempt <= len(seasonPitchingBackoff); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(seasonPitchingBackoff[attempt-1]):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		// Not http.DefaultClient: it has no Timeout, and the ctx handed down
		// here has no deadline either (cmd/root.go calls Execute(), not
		// ExecuteContext()), so this fetch was unbounded and could stall a
		// render indefinitely -- rosterbot-5zp1, whose observed instance was
		// the sibling fetch in internal/fantrax. Both bounds apply, so a
		// caller that does pass a tighter deadline still wins.
		resp, err := (&http.Client{Timeout: seasonPitchingHTTPTimeout}).Do(req)
		if err != nil {
			lastErr = err
			continue // transport error, including the timeout: always retry
		}
		if resp.StatusCode != http.StatusOK {
			// Drain before closing so a retry reuses the connection.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("statsapi pitching: status %d", resp.StatusCode)
			if !retryableStatsAPIStatus(resp.StatusCode) {
				return lastErr
			}
			continue
		}

		var payload struct {
			People []struct {
				ID    int `json:"id"`
				Stats []struct {
					Splits []struct {
						Stat struct {
							InningsPitched string `json:"inningsPitched"`
							HomeRuns       int    `json:"homeRuns"`
							BaseOnBalls    int    `json:"baseOnBalls"`
							HitBatsmen     int    `json:"hitBatsmen"`
							StrikeOuts     int    `json:"strikeOuts"`
							EarnedRuns     int    `json:"earnedRuns"`
						} `json:"stat"`
					} `json:"splits"`
				} `json:"stats"`
			} `json:"people"`
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if err != nil {
			// A truncated body is retryable; a malformed one fails the same
			// way on the last attempt and surfaces then.
			lastErr = err
			continue
		}

		for _, person := range payload.People {
			for _, st := range person.Stats {
				for _, sp := range st.Splits {
					out[person.ID] = pitchSeason{
						IP:  scoring.ParseIP(sp.Stat.InningsPitched),
						HR:  float64(sp.Stat.HomeRuns),
						BB:  float64(sp.Stat.BaseOnBalls),
						HBP: float64(sp.Stat.HitBatsmen),
						SO:  float64(sp.Stat.StrikeOuts),
						ER:  float64(sp.Stat.EarnedRuns),
					}
				}
			}
		}
		return nil
	}
	return lastErr
}
