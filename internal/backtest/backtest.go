// Package backtest grades past lineup moves and projections against actual
// fantasy-points outcomes. It answers two questions:
//
//  1. Lineup: for each past day, how much worse was the lineup we set vs. the
//     hindsight-optimal lineup (given what we now know actually scored)?
//  2. Projections: how close were the projected pts/game values the optimizer
//     used vs. the actual fantasy points each player earned?
package backtest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// PlayerPts is a simple name+points record for report output.
type PlayerPts struct {
	PlayerID string  `json:"player_id"`
	Name     string  `json:"name"`
	MLBTeam  string  `json:"mlb_team"`
	Pts      float64 `json:"pts"`
	Slot     string  `json:"slot,omitempty"`
}

// LineupDayResult is the lineup-grade output for a single date.
type LineupDayResult struct {
	Date       time.Time   `json:"date"`
	Period     int         `json:"period"`
	ActualPts  float64     `json:"actual_pts"`
	OptimalPts float64     `json:"optimal_pts"`
	Gap        float64     `json:"gap"`            // ActualPts - OptimalPts (negative = left on bench)
	Benched    []PlayerPts `json:"benched_points"` // players who scored but were not started
	Started    []PlayerPts `json:"started_points"` // players who were started (for context)
}

// PlayerProjection compares a single player-day's projection to actual FPts.
type PlayerProjection struct {
	Date      time.Time `json:"date"`
	PlayerID  string    `json:"player_id"`
	Name      string    `json:"name"`
	MLBTeam   string    `json:"mlb_team"`
	Projected float64   `json:"projected"`
	Actual    float64   `json:"actual"`
	Diff      float64   `json:"diff"`   // actual - projected
	Source    string    `json:"source"` // "snapshot" or "reconstructed"
	IsPitcher bool      `json:"is_pitcher"`
	Bucket    string    `json:"bucket,omitempty"` // position bucket: C/INF/OF/UT/SP/RP
}

// ProjectionDayResult aggregates projection grading for one date.
type ProjectionDayResult struct {
	Date    time.Time          `json:"date"`
	Players []PlayerProjection `json:"players"`
	MAE     float64            `json:"mae"`
	Bias    float64            `json:"bias"` // mean (actual - projected)
	RMSE    float64            `json:"rmse"`
	Source  string             `json:"source"` // "snapshot", "reconstructed", or "mixed"
}

// Report is the full serializable backtest output.
type Report struct {
	Start             time.Time             `json:"start"`
	End               time.Time             `json:"end"`
	Lineup            []LineupDayResult     `json:"lineup"`
	Projections       []ProjectionDayResult `json:"projections,omitempty"`
	TopBench          []PlayerPts           `json:"top_bench_cumulative"`
	ProjectionSummary *ProjectionSummary    `json:"projection_summary,omitempty"`
}

// ProjectionSummary rolls up projection grading across all days.
type ProjectionSummary struct {
	TotalPlayerDays   int           `json:"total_player_days"`
	SnapshotDays      int           `json:"snapshot_player_days"`
	ReconstructedDays int           `json:"reconstructed_player_days"`
	MAE               float64       `json:"mae"`
	Bias              float64       `json:"bias"`
	RMSE              float64       `json:"rmse"`
	ByPosition        []PositionMAE `json:"by_position,omitempty"`
}

// PositionMAE is per-bucket projection accuracy (C/INF/OF/UT/SP/RP).
type PositionMAE struct {
	Bucket string  `json:"bucket"`
	N      int     `json:"n"`
	MAE    float64 `json:"mae"`
	Bias   float64 `json:"bias"`
}

// SnapshotPlayer is one player's archived projection for a given date.
// Written by the optimizer for every rostered player, read here for exact
// grading. The extra eligibility/slot/locked fields exist so future look-back
// analysis can slice projection error by position, by whether we started the
// player, and by whether their game was already locked when we snapshotted.
type SnapshotPlayer struct {
	PlayerID       string   `json:"player_id"`
	Name           string   `json:"name"`
	MLBTeam        string   `json:"mlb_team"`
	ProjPtsPerGame float64  `json:"proj_pts_per_game"`
	HasGame        bool     `json:"has_game"`
	WasStarted     bool     `json:"was_started"`
	IsPitcher      bool     `json:"is_pitcher"`
	IsStarter      bool     `json:"is_starter,omitempty"`
	Role           string   `json:"role,omitempty"`        // "SP" / "RP" for pitchers
	Slot           string   `json:"slot,omitempty"`        // active slot occupied, e.g. "OF"; "" if benched
	Locked         bool     `json:"locked,omitempty"`      // game in progress/final at snapshot time
	Eligibility    []string `json:"eligibility,omitempty"` // position IDs the player is eligible for
}

// Snapshot is the serialized per-date snapshot file format.
type Snapshot struct {
	Date             string           `json:"date"`
	ProjectionSystem string           `json:"projection_system"`
	GeneratedAt      time.Time        `json:"generated_at"`
	Hitters          []SnapshotPlayer `json:"hitters"`
	Pitchers         []SnapshotPlayer `json:"pitchers"`
}

// RunLineupAnalysis grades each day's actual lineup against the hindsight
// optimal, using the existing optimizer with a hindsight points source.
func RunLineupAnalysis(
	days []fantrax.DayRoster,
	hitterSlots, pitcherSlots []fantrax.Slot,
) []LineupDayResult {
	results := make([]LineupDayResult, 0, len(days))

	for _, day := range days {
		hitters, pitchers := splitPlayers(day.Players)

		// Actual points: sum FPts for everyone who was started (StatusID=="1")
		// and who actually had a game.
		var actualPts float64
		started := []PlayerPts{}
		for _, p := range day.Players {
			if p.Active && p.HadGame {
				actualPts += p.FPts
				slot := slotShortName(p.SlotPosID, p.IsPitcher)
				started = append(started, PlayerPts{
					PlayerID: p.PlayerID, Name: p.Name, MLBTeam: p.MLBTeam,
					Pts: p.FPts, Slot: slot,
				})
			}
		}

		// Hindsight-optimal: feed actual FPts into the optimizer as projections.
		hitterResult := optimizeHitters(hitters, hitterSlots)
		pitcherResult := optimizePitchers(pitchers, pitcherSlots)

		optimalPts := hitterOptimalPts(hitterResult) + pitcherOptimalPts(pitcherResult)

		// Top bench misses: players not in the optimal lineup who scored — but
		// here we want "players who actually scored but were benched by us".
		benchedPts := benchedPoints(day.Players)

		results = append(results, LineupDayResult{
			Date:       day.Date,
			Period:     day.Period,
			ActualPts:  actualPts,
			OptimalPts: optimalPts,
			Gap:        actualPts - optimalPts,
			Benched:    benchedPts,
			Started:    started,
		})
	}

	return results
}

// optimizeHitters runs the hitter optimizer with a hindsight source.
func optimizeHitters(players []fantrax.DayPlayerFP, slots []fantrax.Slot) optimizer.Result {
	roster := toPlayers(players)
	playing := teamsWithGames(players)
	src := newHindsightHitterSource(players)
	// Scoring weights are not used when PtsPerGameSource returns a value, so
	// an empty map is fine.
	return optimizer.OptimizeLineup(roster, playing, src, fantrax.ScoringWeights{}, slots, nil)
}

// optimizePitchers runs the pitcher optimizer with a hindsight source. All
// pitchers who actually appeared in a game are treated as "probable starters"
// if they're SP-eligible — that way the optimizer doesn't apply the 0.10x
// non-starter discount and can rank them by actual FPts.
func optimizePitchers(players []fantrax.DayPlayerFP, slots []fantrax.Slot) optimizer.PitcherResult {
	roster := toPlayers(players)
	playing := teamsWithGames(players)
	probable := probableStartersFromActuals(players)
	src := newHindsightPitcherSource(players)
	return optimizer.OptimizePitcherLineup(roster, playing, probable, src, fantrax.ScoringWeights{}, slots, nil)
}

// hitterOptimalPts sums the ExpectedPts (= actual FPts in hindsight) of the
// players in the optimizer's chosen active lineup. The optimal lineup is
// derived from the Result: currently-active players not in ToBench plus any
// players in ToActivate.
func hitterOptimalPts(r optimizer.Result) float64 {
	benched := make(map[string]bool, len(r.ToBench))
	for _, id := range r.ToBench {
		benched[id] = true
	}
	activated := make(map[string]bool, len(r.ToActivate))
	for _, ps := range r.ToActivate {
		activated[ps.PlayerID] = true
	}
	var total float64
	for _, sp := range r.Scored {
		inOptimal := (sp.Player.Status == "Active" && !benched[sp.Player.ID]) || activated[sp.Player.ID]
		if !inOptimal {
			continue
		}
		if !sp.HasGame {
			continue
		}
		total += sp.ExpectedPts
	}
	return total
}

// pitcherOptimalPts is the pitcher analogue. In backtest the hindsight source
// marks every SP who actually appeared as IsStarter=true, so the 0.10x
// non-starter discount does not apply to hindsight-optimal pitchers. We still
// apply it to any residual SP-eligible pitchers placed in active slots without
// IsStarter set (e.g. an SP whose team played but who didn't actually pitch).
func pitcherOptimalPts(r optimizer.PitcherResult) float64 {
	benched := make(map[string]bool, len(r.ToBench))
	for _, id := range r.ToBench {
		benched[id] = true
	}
	activated := make(map[string]bool, len(r.ToActivate))
	for _, ps := range r.ToActivate {
		activated[ps.PlayerID] = true
	}
	var total float64
	for _, sp := range r.Scored {
		inOptimal := (sp.Player.Status == "Active" && !benched[sp.Player.ID]) || activated[sp.Player.ID]
		if !inOptimal {
			continue
		}
		if !sp.HasGame {
			continue
		}
		isRP := !strings.Contains(sp.Player.PosShortNames, "SP")
		if sp.IsStarter || isRP {
			total += sp.ExpectedPts
		} else {
			total += sp.ExpectedPts * 0.10
		}
	}
	return total
}

// benchedPoints returns the players who scored points but were not in active
// slots (StatusID != "1"). These are the real "points left on bench".
func benchedPoints(all []fantrax.DayPlayerFP) []PlayerPts {
	var out []PlayerPts
	for _, p := range all {
		if p.Active {
			continue
		}
		if p.FPts == 0 {
			continue
		}
		out = append(out, PlayerPts{
			PlayerID: p.PlayerID, Name: p.Name, MLBTeam: p.MLBTeam, Pts: p.FPts,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pts != out[j].Pts {
			return out[i].Pts > out[j].Pts
		}
		return out[i].PlayerID < out[j].PlayerID
	})
	return out
}

// splitPlayers separates a day's players into hitters and pitchers. Only
// healthy roster players (StatusID 1 or 2) are returned — IL and minors
// are excluded from optimizer input.
func splitPlayers(players []fantrax.DayPlayerFP) (hitters, pitchers []fantrax.DayPlayerFP) {
	for _, p := range players {
		if p.StatusID != "1" && p.StatusID != "2" {
			continue
		}
		if p.IsPitcher {
			pitchers = append(pitchers, p)
		} else {
			hitters = append(hitters, p)
		}
	}
	return
}

// toPlayers converts DayPlayerFP rows into fantrax.Player records for the
// optimizer. Status is set to "Active" / "Reserve" based on StatusID. InMinors
// and IsInjured are always false here since we filtered them upstream.
func toPlayers(players []fantrax.DayPlayerFP) []fantrax.Player {
	out := make([]fantrax.Player, 0, len(players))
	for _, p := range players {
		status := "Reserve"
		if p.Active {
			status = "Active"
		}
		out = append(out, fantrax.Player{
			ID:             p.PlayerID,
			Name:           p.Name,
			MLBTeam:        p.MLBTeam,
			Positions:      p.Positions,
			PosShortNames:  p.PosShortNames,
			RosterPosition: p.SlotPosID,
			Status:         status,
		})
	}
	return out
}

// teamsWithGames returns a set of MLB team abbreviations where at least one
// rostered player had a game.
func teamsWithGames(players []fantrax.DayPlayerFP) map[string]bool {
	m := make(map[string]bool)
	for _, p := range players {
		if p.HadGame {
			m[p.MLBTeam] = true
		}
	}
	return m
}

// probableStartersFromActuals builds the `probableStarters` input for
// OptimizePitcherLineup, treating every SP-eligible pitcher who actually
// earned points as a confirmed starter. This suppresses the 0.10x non-starter
// discount in hindsight (we already know who started).
func probableStartersFromActuals(pitchers []fantrax.DayPlayerFP) map[string]string {
	m := make(map[string]string)
	for _, p := range pitchers {
		if !p.HadGame {
			continue
		}
		if !strings.Contains(p.PosShortNames, "SP") {
			continue
		}
		m[projections.NormalizeName(p.Name)] = p.MLBTeam
	}
	return m
}

// slotShortName returns the display-friendly slot name ("OF", "SP", etc.) for
// a given posId. Returns "" when unknown.
func slotShortName(posID string, isPitcher bool) string {
	// We don't have the full slot definitions passed in; look up common ones.
	// (Unused for analysis, only for optional display.)
	switch posID {
	case "001":
		return "C"
	case "002":
		return "1B"
	case "003":
		return "2B"
	case "004":
		return "3B"
	case "005":
		return "SS"
	case "008":
		return "INF"
	case "012":
		return "OF"
	case "014":
		return "UT"
	case "015":
		return "SP"
	case "016", "043", "044":
		return "RP"
	case "017":
		return "P"
	}
	return ""
}

// --- Hindsight sources ---

// hindsightHitterSource returns actual FPts as projected pts/game.
type hindsightHitterSource struct {
	byID   map[string]float64
	byName map[string]float64
}

func newHindsightHitterSource(players []fantrax.DayPlayerFP) *hindsightHitterSource {
	s := &hindsightHitterSource{
		byID:   make(map[string]float64),
		byName: make(map[string]float64),
	}
	for _, p := range players {
		s.byID[p.PlayerID] = p.FPts
		s.byName[projections.NormalizeName(p.Name)] = p.FPts
	}
	return s
}

// GetProjection returns a minimal projection so the optimizer's fallback path
// (when PtsPerGameSource isn't used) still works. G=1 means per-game value is
// the single-day value.
func (s *hindsightHitterSource) GetProjection(name, _ string) (*projections.Projection, bool) {
	pts, ok := s.byName[projections.NormalizeName(name)]
	if !ok {
		return nil, false
	}
	// ExpectedPtsFromProj ignores GamesPlayed when G>0 — we can't round-trip
	// a raw point total into stat counts, but we can bypass this path by
	// also implementing PtsPerGameSource (which the optimizer prefers).
	_ = pts
	return &projections.Projection{G: 1}, true
}

// GetPtsPerGame returns the actual FPts directly.
func (s *hindsightHitterSource) GetPtsPerGame(name, _ string, _ fantrax.ScoringWeights) (float64, bool) {
	pts, ok := s.byName[projections.NormalizeName(name)]
	if !ok {
		return 0, false
	}
	return pts, true
}

// hindsightPitcherSource is the pitcher analogue.
type hindsightPitcherSource struct {
	byID   map[string]float64
	byName map[string]float64
}

func newHindsightPitcherSource(players []fantrax.DayPlayerFP) *hindsightPitcherSource {
	s := &hindsightPitcherSource{
		byID:   make(map[string]float64),
		byName: make(map[string]float64),
	}
	for _, p := range players {
		s.byID[p.PlayerID] = p.FPts
		s.byName[projections.NormalizeName(p.Name)] = p.FPts
	}
	return s
}

func (s *hindsightPitcherSource) GetPitcherProjection(name, _ string) (*projections.PitcherProjection, bool) {
	_, ok := s.byName[projections.NormalizeName(name)]
	if !ok {
		return nil, false
	}
	return &projections.PitcherProjection{G: 1}, true
}

func (s *hindsightPitcherSource) GetPitcherPtsPerGame(name, _ string, _ fantrax.ScoringWeights) (float64, bool) {
	pts, ok := s.byName[projections.NormalizeName(name)]
	if !ok {
		return 0, false
	}
	return pts, true
}

// --- Projection analysis ---

// RunProjectionAnalysis grades projections against actual FPts for each day.
// For each date, it first checks snapshotDir/<YYYY-MM-DD>.json. Rows found
// there use "snapshot" as their source. Otherwise, the player is skipped
// (reconstruction is implemented as a separate path; see LoadSnapshot).
func RunProjectionAnalysis(days []fantrax.DayRoster, snapshotDir string) []ProjectionDayResult {
	results := make([]ProjectionDayResult, 0, len(days))
	for _, day := range days {
		snap, snapOK := LoadSnapshot(snapshotDir, day.Date)
		if !snapOK {
			results = append(results, ProjectionDayResult{
				Date:   day.Date,
				Source: "missing",
			})
			continue
		}
		byID := make(map[string]SnapshotPlayer, len(snap.Hitters)+len(snap.Pitchers))
		for _, s := range snap.Hitters {
			byID[s.PlayerID] = s
		}
		for _, s := range snap.Pitchers {
			byID[s.PlayerID] = s
		}

		var players []PlayerProjection
		for _, p := range day.Players {
			if !p.HadGame {
				continue
			}
			archived, ok := byID[p.PlayerID]
			if !ok {
				continue
			}
			players = append(players, PlayerProjection{
				Date:      day.Date,
				PlayerID:  p.PlayerID,
				Name:      p.Name,
				MLBTeam:   p.MLBTeam,
				Projected: archived.ProjPtsPerGame,
				Actual:    p.FPts,
				Diff:      p.FPts - archived.ProjPtsPerGame,
				Source:    "snapshot",
				IsPitcher: p.IsPitcher,
				// Bucket from the snapshot's eligibility/role — the live-roster
				// source is authoritative, unlike the historical actuals feed.
				Bucket: positionBucket(archived.IsPitcher, archived.Role, archived.Eligibility),
			})
		}

		mae, bias, rmse := accuracyStats(players)
		results = append(results, ProjectionDayResult{
			Date:    day.Date,
			Players: players,
			MAE:     mae,
			Bias:    bias,
			RMSE:    rmse,
			Source:  "snapshot",
		})
	}
	return results
}

// accuracyStats computes MAE, mean bias (actual-projected), and RMSE.
func accuracyStats(players []PlayerProjection) (mae, bias, rmse float64) {
	if len(players) == 0 {
		return 0, 0, 0
	}
	var absSum, biasSum, sqSum float64
	for _, p := range players {
		absSum += math.Abs(p.Diff)
		biasSum += p.Diff
		sqSum += p.Diff * p.Diff
	}
	n := float64(len(players))
	mae = absSum / n
	bias = biasSum / n
	rmse = math.Sqrt(sqSum / n)
	return
}

// bucketOrder is the canonical display order for per-position accuracy.
var bucketOrder = []string{"C", "INF", "OF", "UT", "SP", "RP"}

// positionBucket assigns a player to one of six accuracy buckets. Pitchers are
// bucketed by role (SP/RP). Hitters are bucketed by eligibility with the
// precedence C > INF > OF > UT, so the scarcest defensive role a player
// qualifies for wins (a C/OF lands in C; a 3B/OF lands in INF). `positions`
// holds Fantrax position-ID strings (001=C, 002/003/004/005/008=infield,
// 012=OF, 014=UT). A hitter with no eligibility falls back to UT.
func positionBucket(isPitcher bool, role string, positions []string) string {
	if isPitcher {
		if role == "SP" {
			return "SP"
		}
		return "RP"
	}
	has := func(id string) bool {
		for _, p := range positions {
			if p == id {
				return true
			}
		}
		return false
	}
	switch {
	case has("001"):
		return "C"
	case has("002"), has("003"), has("004"), has("005"), has("008"):
		return "INF"
	case has("012"):
		return "OF"
	default:
		return "UT"
	}
}

// topSignedMisses returns the n player-days with the largest absolute
// projection error, ordered for display by signed diff ascending (most
// over-projected first) so systematic ramp-up patterns — a pitcher we kept
// projecting high who delivered low — cluster at the top. Ties break on
// PlayerID for deterministic output.
func topSignedMisses(players []PlayerProjection, n int) []PlayerProjection {
	sorted := make([]PlayerProjection, len(players))
	copy(sorted, players)
	// Select the biggest misses by magnitude.
	sort.Slice(sorted, func(i, j int) bool {
		ai, aj := math.Abs(sorted[i].Diff), math.Abs(sorted[j].Diff)
		if ai != aj {
			return ai > aj
		}
		return sorted[i].PlayerID < sorted[j].PlayerID
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	// Display order: most over-projected (most negative diff) first.
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Diff != sorted[j].Diff {
			return sorted[i].Diff < sorted[j].Diff
		}
		return sorted[i].PlayerID < sorted[j].PlayerID
	})
	return sorted
}

// LoadSnapshot reads a snapshot JSON from snapshotDir for the given date.
// Returns (snapshot, true) on success or (_, false) if the file is missing
// or malformed.
func LoadSnapshot(dir string, date time.Time) (Snapshot, bool) {
	path := filepath.Join(dir, date.Format("2006-01-02")+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, false
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, false
	}
	return s, true
}

// WriteSnapshot serializes a snapshot to snapshotDir/<YYYY-MM-DD>.json.
func WriteSnapshot(dir string, s Snapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, s.Date+".json")
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// BuildReport assembles a Report from lineup + projection results.
func BuildReport(start, end time.Time, lineup []LineupDayResult, proj []ProjectionDayResult) Report {
	r := Report{
		Start:       start,
		End:         end,
		Lineup:      lineup,
		Projections: proj,
		TopBench:    topBenchCumulative(lineup),
	}
	if len(proj) > 0 {
		r.ProjectionSummary = summarizeProjections(proj)
	}
	return r
}

// topBenchCumulative aggregates bench-player points across the whole window.
func topBenchCumulative(lineup []LineupDayResult) []PlayerPts {
	cum := make(map[string]*PlayerPts)
	for _, day := range lineup {
		for _, b := range day.Benched {
			if existing, ok := cum[b.PlayerID]; ok {
				existing.Pts += b.Pts
			} else {
				p := b
				cum[p.PlayerID] = &p
			}
		}
	}
	out := make([]PlayerPts, 0, len(cum))
	for _, p := range cum {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pts != out[j].Pts {
			return out[i].Pts > out[j].Pts
		}
		return out[i].PlayerID < out[j].PlayerID
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func summarizeProjections(proj []ProjectionDayResult) *ProjectionSummary {
	var all []PlayerProjection
	var snapCount, reconCount int
	for _, d := range proj {
		all = append(all, d.Players...)
		for _, p := range d.Players {
			if p.Source == "snapshot" {
				snapCount++
			} else if p.Source == "reconstructed" {
				reconCount++
			}
		}
	}
	if len(all) == 0 {
		return &ProjectionSummary{}
	}
	mae, bias, rmse := accuracyStats(all)
	return &ProjectionSummary{
		TotalPlayerDays:   len(all),
		SnapshotDays:      snapCount,
		ReconstructedDays: reconCount,
		MAE:               mae,
		Bias:              bias,
		RMSE:              rmse,
		ByPosition:        accuracyByPosition(all),
	}
}

// accuracyByPosition groups player-days by bucket and returns per-bucket
// accuracy in canonical order, skipping empty buckets.
func accuracyByPosition(all []PlayerProjection) []PositionMAE {
	byBucket := make(map[string][]PlayerProjection)
	for _, p := range all {
		byBucket[p.Bucket] = append(byBucket[p.Bucket], p)
	}
	var out []PositionMAE
	for _, b := range bucketOrder {
		ps := byBucket[b]
		if len(ps) == 0 {
			continue
		}
		mae, bias, _ := accuracyStats(ps)
		out = append(out, PositionMAE{Bucket: b, N: len(ps), MAE: mae, Bias: bias})
	}
	return out
}

// FormatReport renders the report as human-readable text.
func FormatReport(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Backtest: %s to %s (%d days) ===\n\n",
		r.Start.Format("2006-01-02"), r.End.Format("2006-01-02"), len(r.Lineup))

	// Lineup table.
	fmt.Fprintf(&b, "%-14s %10s %10s %10s\n", "Day", "Actual", "Optimal", "Gap")
	fmt.Fprintln(&b, strings.Repeat("-", 48))
	var totalActual, totalOptimal float64
	for _, d := range r.Lineup {
		fmt.Fprintf(&b, "%-14s %10.2f %10.2f %10.2f\n",
			d.Date.Format("Mon Jan 2"), d.ActualPts, d.OptimalPts, d.Gap)
		totalActual += d.ActualPts
		totalOptimal += d.OptimalPts
	}
	fmt.Fprintln(&b, strings.Repeat("-", 48))
	fmt.Fprintf(&b, "%-14s %10.2f %10.2f %10.2f\n",
		"Total", totalActual, totalOptimal, totalActual-totalOptimal)

	// Top bench misses.
	if len(r.TopBench) > 0 {
		fmt.Fprintln(&b, "\nTop points left on bench (cumulative):")
		for _, p := range r.TopBench {
			fmt.Fprintf(&b, "  %-24s %+7.2f\n", truncate(p.Name, 22), p.Pts)
		}
	}

	// Projection summary.
	if r.ProjectionSummary != nil && r.ProjectionSummary.TotalPlayerDays > 0 {
		s := r.ProjectionSummary
		fmt.Fprintf(&b, "\nProjection accuracy (%d player-days, %d reconstructed / %d snapshot):\n",
			s.TotalPlayerDays, s.ReconstructedDays, s.SnapshotDays)
		fmt.Fprintf(&b, "  MAE:  %6.2f FP/game\n", s.MAE)
		fmt.Fprintf(&b, "  Bias: %+6.2f FP/game (negative = over-projection)\n", s.Bias)
		fmt.Fprintf(&b, "  RMSE: %6.2f FP/game\n", s.RMSE)

		// Per-position MAE.
		if len(s.ByPosition) > 0 {
			fmt.Fprintln(&b, "\nMAE by position:")
			for _, pb := range s.ByPosition {
				fmt.Fprintf(&b, "  %-4s n=%-5d MAE %6.2f  Bias %+6.2f\n", pb.Bucket, pb.N, pb.MAE, pb.Bias)
			}
		}

		// Top-10 signed-error misses (most over-projected first), so
		// ramp-up patterns surface.
		var all []PlayerProjection
		for _, d := range r.Projections {
			all = append(all, d.Players...)
		}
		misses := topSignedMisses(all, 10)
		if len(misses) > 0 {
			fmt.Fprintln(&b, "\nBiggest projection misses (signed; over-projected first):")
			for _, p := range misses {
				fmt.Fprintf(&b, "  %-24s %-4s %-6s  proj=%6.2f  actual=%6.2f  diff=%+6.2f\n",
					truncate(p.Name, 22),
					p.Bucket,
					p.Date.Format("Jan 2"),
					p.Projected, p.Actual, p.Diff)
			}
		}
	} else if len(r.Projections) > 0 {
		fmt.Fprintln(&b, "\nNo projection snapshots found for this window.")
		fmt.Fprintln(&b, "Use --archive-projections on optimize to enable exact grading.")
	}

	return b.String()
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
