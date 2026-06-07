package recap

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/schedule"
	"golang.org/x/sync/errgroup"
)

// Options configures a single recap run.
type Options struct {
	WeekStart   time.Time
	WeekEnd     time.Time
	WeekNumber  int    // optional; if zero, computed as ((weekStart - seasonStart) / 7) + 1
	WeekLabel   string // optional; defaults to "Week N"
	CacheDir    string
	CacheTTL    time.Duration // 0 → use default; pass 30 days for past weeks (immutable)
	TopPlayers  int           // top N for Players-of-Week (default 4)
	Concurrency int           // parallel team fetches; 0 → runtime.NumCPU()
}

// Run pulls the data for the matchup week, aggregates per-team performance,
// computes awards, and returns a Recap. The returned struct is the data model
// the renderer consumes.
func Run(ft *fantrax.Client, opts Options) (*Recap, error) {
	if opts.WeekEnd.Before(opts.WeekStart) {
		return nil, fmt.Errorf("week end %s before start %s",
			opts.WeekEnd.Format("2006-01-02"), opts.WeekStart.Format("2006-01-02"))
	}
	if opts.TopPlayers <= 0 {
		opts.TopPlayers = 5
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = runtime.NumCPU()
	}

	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		return nil, fmt.Errorf("season range: %w", err)
	}

	_, teamMap, teamLogos, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return nil, fmt.Errorf("teams: %w", err)
	}
	if len(teamMap) == 0 {
		return nil, fmt.Errorf("no teams found")
	}

	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		return nil, fmt.Errorf("hitter slots: %w", err)
	}
	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		return nil, fmt.Errorf("pitcher slots: %w", err)
	}

	allMatchups, err := ft.GetAllMatchupEntries()
	if err != nil {
		return nil, fmt.Errorf("matchups: %w", err)
	}

	// Determine the H2H pair for each team this week. Since matchups are
	// weekly but reported per-day, just take the first matchup entry whose
	// date falls inside [WeekStart, WeekEnd].
	weekPairs := pairsForWeek(allMatchups, opts.WeekStart, opts.WeekEnd)
	if len(weekPairs) == 0 {
		return nil, fmt.Errorf("no matchups in window %s..%s",
			opts.WeekStart.Format("2006-01-02"), opts.WeekEnd.Format("2006-01-02"))
	}

	results := make(map[string]*teamData, len(teamMap))
	var mu sync.Mutex

	g := new(errgroup.Group)
	g.SetLimit(opts.Concurrency)

	for teamID, teamName := range teamMap {
		teamID, teamName := teamID, teamName
		g.Go(func() error {
			td, err := collectTeam(ft, teamID, teamName, opts.WeekStart, opts.WeekEnd,
				seasonStart, hitterSlots, pitcherSlots, opts.CacheDir, opts.CacheTTL)
			if err != nil {
				return fmt.Errorf("team %s (%s): %w", teamName, teamID, err)
			}
			mu.Lock()
			results[teamID] = td
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	teamWeeks := make([]TeamWeek, 0, len(results))
	allActive := make([]PlayerLine, 0, 256)
	allStarts := make([]PitcherStartLine, 0, 64)
	for _, td := range results {
		teamWeeks = append(teamWeeks, td.team)
		allActive = append(allActive, td.active...)
		allStarts = append(allStarts, td.starts...)
	}

	// Attach each start's opponent via the MLB schedule. One fetch per unique
	// date in the week. Soft-fail if the schedule API is unreachable — the
	// label just won't render.
	annotateOpponents(allStarts, opts.CacheDir)

	// Pivot per-team daily home/away actuals keyed by team for WP curves.
	teamDailyByID := dailyByTeam(results)

	// Stable sort by efficiency descending so the rendered "Team Performance"
	// table always reads top-to-bottom by efficiency.
	sort.SliceStable(teamWeeks, func(i, j int) bool {
		if teamWeeks[i].Efficiency != teamWeeks[j].Efficiency {
			return teamWeeks[i].Efficiency > teamWeeks[j].Efficiency
		}
		return teamWeeks[i].TeamID < teamWeeks[j].TeamID
	})

	// Build matchup results from team scores + pairings.
	teamScore := make(map[string]float64, len(teamWeeks))
	teamName := make(map[string]string, len(teamWeeks))
	for _, t := range teamWeeks {
		teamScore[t.TeamID] = t.ActualPts
		teamName[t.TeamID] = t.TeamName
	}
	matchups := buildMatchups(weekPairs, teamScore, teamName)

	weekNum := opts.WeekNumber
	if weekNum == 0 {
		// Look up the actual Fantrax-aligned week number by date. Falls back
		// to a simple calendar approximation if the matchup data doesn't
		// contain the date (e.g., custom --dates window outside the season).
		if n, err := ft.GetMatchupWeekNumberForDate(opts.WeekStart); err == nil && n > 0 {
			weekNum = n
		} else {
			weekNum = matchupWeekNumber(seasonStart, opts.WeekStart)
		}
	}
	weekLabel := opts.WeekLabel
	if weekLabel == "" {
		weekLabel = fmt.Sprintf("Week %d", weekNum)
	}

	awards := Awards{
		MostEfficient:    MostEfficient(teamWeeks),
		LeastEfficient:   LeastEfficient(teamWeeks),
		HighestScore:     HighestScore(teamWeeks),
		LowestScore:      LowestScore(teamWeeks),
		BiggestBlowout:   BiggestBlowout(matchups),
		NarrowVictory:    NarrowVictory(matchups),
		HighestPtsInLoss: HighestPtsInLoss(matchups),
		LowestPtsInWin:   LowestPtsInWin(matchups),
		BestSingleStart:  BestSingleStart(allStarts),
		WorstSingleStart: WorstSingleStart(allStarts),
		TopBatters:       TopBatters(allActive, opts.TopPlayers),
		TopPitchers:      TopPitchers(allActive, opts.TopPlayers),
	}

	// League-wide season-to-date rate-stat leaders across all rostered players.
	// Soft-fails to nil internally; the section is omitted when empty.
	awards.WOBALeaders, awards.FIPLeaders = buildLeaders(
		ft, opts.WeekEnd.Year(), time.Now().UTC(), opts.CacheDir, opts.CacheTTL, opts.TopPlayers)

	// Season-to-date team means feed the WP simulation so the curve isn't
	// look-ahead biased by the very week we're plotting. Fall back to the
	// within-week mean per-team when season data is unavailable (Week 1, or
	// per-team fetch error — see fetchSeasonMeans).
	asOf := opts.WeekStart.AddDate(0, 0, -1)
	seasonMeans := fetchSeasonMeans(ft, teamMap, seasonStart, asOf, opts.CacheDir, opts.CacheTTL, opts.Concurrency)

	var curves []MatchupWPCurve
	for _, m := range matchups {
		h := teamDailyByID[m.HomeTeamID]
		a := teamDailyByID[m.AwayTeamID]
		if len(h.Actuals) != 7 || len(a.Actuals) != 7 {
			continue
		}
		hMean := seasonMeans[m.HomeTeamID]
		if hMean == 0 {
			hMean = mean(h.Actuals)
		}
		aMean := seasonMeans[m.AwayTeamID]
		if aMean == 0 {
			aMean = mean(a.Actuals)
		}
		curve := ComputeWPCurve(WPInputs{
			HomeTeamID:    m.HomeTeamID,
			AwayTeamID:    m.AwayTeamID,
			HomeMeanDaily: hMean,
			AwayMeanDaily: aMean,
			Dates:         h.Dates,
			HomeActuals:   h.Actuals,
			AwayActuals:   a.Actuals,
			WeekNumber:    weekNum,
		})
		curves = append(curves, curve)
	}
	awards.GameOfWeek = GameOfTheWeek(curves, matchups)
	awards.Comeback = Comeback(curves, matchups)

	return &Recap{
		Season:      opts.WeekStart.Year(),
		WeekNumber:  weekNum,
		WeekLabel:   weekLabel,
		StartDate:   opts.WeekStart,
		EndDate:     opts.WeekEnd,
		GeneratedAt: time.Now().UTC(),
		Teams:       teamWeeks,
		Matchups:    matchups,
		Awards:      awards,
		WPCurves:    curves,
		LogoURLs:    teamLogos,
	}, nil
}

// teamData bundles one team's per-week aggregate plus highlight inputs.
type teamData struct {
	team   TeamWeek
	active []PlayerLine
	starts []PitcherStartLine
}

// collectTeam fetches one team's daily roster snapshots for the week, runs the
// hindsight-optimal lineup analysis, and extracts player highlights + SP starts.
func collectTeam(
	ft *fantrax.Client,
	teamID, teamName string,
	weekStart, weekEnd, seasonStart time.Time,
	hitterSlots, pitcherSlots []fantrax.Slot,
	cacheDir string,
	cacheTTL time.Duration,
) (*teamData, error) {
	days, err := ft.DailyFantasyPoints(teamID, weekStart, weekEnd, seasonStart, cacheDir, cacheTTL)
	if err != nil {
		return nil, fmt.Errorf("daily fpts: %w", err)
	}
	if err := ft.BackfillDailyFPts(days); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: MLB backfill for %s: %v\n", teamName, err)
	}

	lineup := backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)

	var actual, optimal float64
	for _, d := range lineup {
		actual += d.ActualPts
		optimal += d.OptimalPts
	}

	eff := 0.0
	if optimal > 0 {
		eff = actual / optimal
	}

	tw := TeamWeek{
		TeamID:     teamID,
		TeamName:   teamName,
		ActualPts:  actual,
		OptimalPts: optimal,
		Efficiency: eff,
	}

	active := extractActivePlayerLines(days, teamName, teamID)

	starts, err := ft.GetTeamPitcherStarts(teamID, weekStart, weekEnd, seasonStart, cacheDir, cacheTTL)
	if err != nil {
		// Soft-fail: pitcher starts are nice-to-have. Log via stderr (caller
		// orchestrates output); recap still returns.
		fmt.Fprintf(os.Stderr, "WARNING: pitcher starts for %s: %v\n", teamName, err)
		starts = nil
	}
	startLines := make([]PitcherStartLine, 0, len(starts))
	for _, s := range starts {
		startLines = append(startLines, PitcherStartLine{
			Name:      s.PitcherName,
			Date:      s.Date,
			FPts:      s.FPts,
			OwnerTeam: teamName,
			MLBTeam:   s.MLBTeam,
		})
	}

	return &teamData{team: tw, active: active, starts: startLines}, nil
}

// extractActivePlayerLines turns per-day per-player FPts into PlayerLine
// records for active-slot players. Players with zero FPts and no game are
// skipped to keep the highlight set tight.
func extractActivePlayerLines(days []fantrax.DayRoster, ownerTeam, ownerTeamID string) []PlayerLine {
	var active []PlayerLine
	for _, d := range days {
		for _, p := range d.Players {
			if !p.Active {
				continue
			}
			if p.FPts == 0 && !p.HadGame {
				continue
			}
			active = append(active, PlayerLine{
				PlayerID:    p.PlayerID,
				Name:        p.Name,
				MLBTeam:     p.MLBTeam,
				FPts:        p.FPts,
				Date:        d.Date,
				OwnerTeam:   ownerTeam,
				OwnerTeamID: ownerTeamID,
				IsPitcher:   p.IsPitcher,
			})
		}
	}
	return active
}

// annotateOpponents fills the Opponent field on each start by looking up the
// MLB schedule for that date. One schedule fetch per unique date. Past-date
// schedules are persisted to cacheDir (key `mlb-schedule-<YYYY-MM-DD>`) so
// rebuilds reuse them. Soft-fails silently — a missing opponent just renders
// blank.
func annotateOpponents(starts []PitcherStartLine, cacheDir string) {
	if len(starts) == 0 {
		return
	}
	sched := schedule.NewClient()
	sched.CacheDir = cacheDir
	cache := map[string]map[string]string{}
	for i := range starts {
		s := &starts[i]
		if s.MLBTeam == "" || s.Date.IsZero() {
			continue
		}
		key := s.Date.Format("2006-01-02")
		opp, fetched := cache[key]
		if !fetched {
			fetched := false
			if got, err := sched.OpponentsOn(s.Date); err == nil {
				opp = got
			}
			_ = fetched
			cache[key] = opp
		}
		if opp == nil {
			continue
		}
		s.Opponent = opp[s.MLBTeam]
	}
}

// pairsForWeek returns one (homeID, awayID) pair per matchup that touches the
// week. Each team appears exactly once because the league plays weekly head to
// head and matchups are reported per daily period — we dedupe on the team-pair
// canonical key.
type teamPair struct {
	HomeID, AwayID string
}

func pairsForWeek(matchups []fantrax.MatchupEntry, weekStart, weekEnd time.Time) []teamPair {
	seen := map[string]bool{}
	var pairs []teamPair
	startYMD := weekStart.Format("2006-01-02")
	endYMD := weekEnd.Format("2006-01-02")

	for _, m := range matchups {
		// Upstream sometimes emits header/placeholder rows where one side has
		// no TeamID. Skip those so they don't dedupe-collide as ("", real).
		if m.HomeID == "" || m.AwayID == "" {
			continue
		}
		t, err := time.Parse("Mon Jan 2, 2006", m.Date)
		if err != nil {
			continue
		}
		ymd := t.Format("2006-01-02")
		if ymd < startYMD || ymd > endYMD {
			continue
		}
		key := canonPair(m.HomeID, m.AwayID)
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, teamPair{HomeID: m.HomeID, AwayID: m.AwayID})
	}
	return pairs
}

func canonPair(a, b string) string {
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

func buildMatchups(pairs []teamPair, scores map[string]float64, names map[string]string) []MatchupResult {
	out := make([]MatchupResult, 0, len(pairs))
	for _, p := range pairs {
		homePts := scores[p.HomeID]
		awayPts := scores[p.AwayID]
		margin := homePts - awayPts
		if margin < 0 {
			margin = -margin
		}
		m := MatchupResult{
			HomeTeamID:   p.HomeID,
			HomeTeamName: names[p.HomeID],
			HomePts:      homePts,
			AwayTeamID:   p.AwayID,
			AwayTeamName: names[p.AwayID],
			AwayPts:      awayPts,
			Margin:       margin,
		}
		switch {
		case homePts > awayPts:
			m.WinnerID, m.LoserID = p.HomeID, p.AwayID
		case awayPts > homePts:
			m.WinnerID, m.LoserID = p.AwayID, p.HomeID
		default:
			m.IsTie = true
		}
		out = append(out, m)
	}
	// Stable sort by margin descending for consistent rendering.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Margin != out[j].Margin {
			return out[i].Margin > out[j].Margin
		}
		return out[i].HomeTeamID < out[j].HomeTeamID
	})
	return out
}

// matchupWeekNumber returns 1-based "weeks since season start" for a date.
// Matchup weeks in this league are 7 days (per CLAUDE.md), so simple division
// suffices. Caller should only invoke this with a confirmed week-start date.
func matchupWeekNumber(seasonStart, weekStart time.Time) int {
	days := int(weekStart.Truncate(24*time.Hour).Sub(seasonStart.Truncate(24*time.Hour)).Hours() / 24)
	if days < 0 {
		return 1
	}
	return (days / 7) + 1
}

// teamDaily holds one team's per-day actuals for the matchup window.
// Length 7, chronological.
type teamDaily struct {
	Dates   []time.Time
	Actuals []float64
}

// dailyByTeam pivots the per-team teamData (which has actual FPts via the
// existing backtest analysis) into a teamID → teamDaily map. The orchestrator
// uses this to feed the WP simulation per matchup.
func dailyByTeam(results map[string]*teamData) map[string]teamDaily {
	out := make(map[string]teamDaily, len(results))
	for teamID, td := range results {
		// The active player lines carry per-day per-player FPts. Aggregate by
		// date (active starters only — same definition the optimizer uses for
		// the actual-points side of efficiency).
		byDate := map[string]float64{}
		for _, p := range td.active {
			byDate[p.Date.Format("2006-01-02")] += p.FPts
		}
		// Materialize into chronological slices. We need the canonical week
		// dates — pull them from the active list's distinct Dates.
		dates := uniqueDates(td.active)
		actuals := make([]float64, len(dates))
		for i, d := range dates {
			actuals[i] = byDate[d.Format("2006-01-02")]
		}
		out[teamID] = teamDaily{Dates: dates, Actuals: actuals}
	}
	return out
}

// uniqueDates returns the distinct Dates from a PlayerLine slice in
// chronological order. Used to derive the canonical 7-day window.
func uniqueDates(lines []PlayerLine) []time.Time {
	seen := map[string]time.Time{}
	for _, l := range lines {
		key := l.Date.Format("2006-01-02")
		if _, ok := seen[key]; !ok {
			seen[key] = l.Date
		}
	}
	out := make([]time.Time, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// computeSeasonMeanFromDays returns (FPts-per-day, days-played) for one
// team across the supplied DayRoster slice. Active-starter definition
// matches extractActivePlayerLines / buildTeamDays: a player counts when
// they were started AND either scored points or had a game. A day counts
// toward the denominator only if at least one player on that day qualified.
func computeSeasonMeanFromDays(days []fantrax.DayRoster) (float64, int) {
	var totalFPts float64
	playedDays := 0
	for _, d := range days {
		var dayFPts float64
		hasActivity := false
		for _, p := range d.Players {
			if !p.Active {
				continue
			}
			if p.FPts == 0 && !p.HadGame {
				continue
			}
			dayFPts += p.FPts
			hasActivity = true
		}
		if !hasActivity {
			continue
		}
		totalFPts += dayFPts
		playedDays++
	}
	if playedDays == 0 {
		return 0, 0
	}
	return totalFPts / float64(playedDays), playedDays
}

// seasonToDateTeamMean fetches one team's daily snapshots from seasonStart
// through asOf (inclusive) and returns the FPts-per-day. Returns (0, 0, nil)
// when asOf < seasonStart (i.e., no history yet — caller falls back to the
// within-week mean).
func seasonToDateTeamMean(ft *fantrax.Client, teamID string, seasonStart, asOf time.Time, cacheDir string, cacheTTL time.Duration) (float64, int, error) {
	if asOf.Before(seasonStart) {
		return 0, 0, nil
	}
	days, err := ft.DailyFantasyPoints(teamID, seasonStart, asOf, seasonStart, cacheDir, cacheTTL)
	if err != nil {
		return 0, 0, err
	}
	mean, played := computeSeasonMeanFromDays(days)
	return mean, played, nil
}

// fetchSeasonMeans returns a teamID → FPts-per-day map by fetching every
// team's season-to-date data in parallel (capped by concurrency). Per-team
// errors are logged to stderr and surfaced as 0.0 in the output map; the
// orchestrator falls back to the within-week mean for those teams.
//
// When asOf precedes seasonStart (Week 1), returns nil so all teams fall
// back without making any HTTP calls.
func fetchSeasonMeans(ft *fantrax.Client, teamMap map[string]string, seasonStart, asOf time.Time, cacheDir string, cacheTTL time.Duration, concurrency int) map[string]float64 {
	if asOf.Before(seasonStart) {
		return nil
	}
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	out := make(map[string]float64, len(teamMap))
	var mu sync.Mutex
	g := new(errgroup.Group)
	g.SetLimit(concurrency)
	for teamID, teamName := range teamMap {
		teamID, teamName := teamID, teamName
		g.Go(func() error {
			m, _, err := seasonToDateTeamMean(ft, teamID, seasonStart, asOf, cacheDir, cacheTTL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: season-to-date mean for %s (%s): %v\n", teamName, teamID, err)
				return nil
			}
			mu.Lock()
			out[teamID] = m
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return out
}
