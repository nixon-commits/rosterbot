package waivers

import (
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
	"golang.org/x/sync/errgroup"
)

const (
	defaultTopN     = 15
	defaultCacheTTL = 12 * time.Hour
	pushoverTitle   = "RosterBot: Waiver picks"
)

// Run executes the waivers report end-to-end. Mirrors prospects.RunProspectReport
// in shape: errgroup-parallel fetches, build the report, emit stdout / GHA
// markdown / Pushover (last one only when not in dry-run and creds are set).
func Run(ft FantraxClient, today time.Time, opts Options) error {
	if opts.TopN <= 0 {
		opts.TopN = defaultTopN
	}
	if opts.CacheDir == "" {
		opts.CacheDir = ".cache"
	}
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	ttl := defaultCacheTTL
	if opts.NoCache {
		ttl = 0
	}

	var (
		pool           []models.PoolPlayer
		batSrc         projections.Source
		pitSrc         projections.PitcherSource
		hitterScoring  fantrax.ScoringWeights
		pitcherScoring fantrax.ScoringWeights
		savant         *SavantBundle
		hitterRoster   []fantrax.Player
		pitcherRoster  []fantrax.Player
	)

	g := new(errgroup.Group)

	g.Go(func() error {
		p, err := ft.GetFullPlayerPool()
		if err != nil {
			return fmt.Errorf("get player pool: %w", err)
		}
		pool = p
		return nil
	})

	g.Go(func() error {
		src, _, err := projections.LoadBattingProjections(projections.ProjectionSteamer, opts.CacheDir, ttl)
		if err != nil {
			log.Printf("WARNING: batting projections unavailable: %v", err)
			return nil
		}
		batSrc = src
		return nil
	})

	g.Go(func() error {
		src, _, err := projections.LoadPitcherProjections(projections.ProjectionSteamer, opts.CacheDir, ttl)
		if err != nil {
			log.Printf("WARNING: pitcher projections unavailable: %v", err)
			return nil
		}
		pitSrc = src
		return nil
	})

	g.Go(func() error {
		w, err := ft.GetScoringWeights()
		if err != nil {
			return fmt.Errorf("get hitter scoring weights: %w", err)
		}
		hitterScoring = w
		return nil
	})

	g.Go(func() error {
		w, err := ft.GetPitcherScoringWeights()
		if err != nil {
			return fmt.Errorf("get pitcher scoring weights: %w", err)
		}
		pitcherScoring = w
		return nil
	})

	g.Go(func() error {
		b, err := LoadSavant(opts.CacheDir, today.Year(), today, ttl)
		if err != nil {
			log.Printf("WARNING: savant load failed: %v", err)
			return nil
		}
		savant = b
		return nil
	})

	g.Go(func() error {
		r, err := ft.GetHitterRoster()
		if err != nil {
			return fmt.Errorf("get hitter roster: %w", err)
		}
		hitterRoster = r
		return nil
	})

	g.Go(func() error {
		r, err := ft.GetPitcherRoster()
		if err != nil {
			return fmt.Errorf("get pitcher roster: %w", err)
		}
		pitcherRoster = r
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	freeAgents := filterFreeAgents(pool, opts.Positions)

	// Build name → MLBAM map from FanGraphs (free, no MLB API calls). Steamer
	// rows already carry xMLBAMID, so a FA who has a Steamer projection also
	// has the MLBAM ID needed to join Savant data. FAs without a Steamer
	// projection get skipped naturally — they wouldn't have a fantasy-points
	// projection anyway.
	mlbamByName := mergeMLBAMMaps(batSrc, pitSrc)

	hitterDrop := worstRosteredHitter(hitterRoster, batSrc, hitterScoring)
	pitcherDrop := worstRosteredPitcher(pitcherRoster, pitSrc, pitcherScoring)

	th := DefaultThresholds()
	candidates := buildCandidates(freeAgents, mlbamByName, savant, batSrc, pitSrc, hitterScoring, pitcherScoring, hitterDrop, pitcherDrop, th)

	report := Report{
		Date:  today,
		Total: len(candidates),
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		// Rank by upgrade gap (FA.FPG - drop.FPG) when a drop is known —
		// the user always has a full roster, so the relevant question is
		// "how much better than my worst rostered player is this FA?"
		if candidates[i].Gap != candidates[j].Gap {
			return candidates[i].Gap > candidates[j].Gap
		}
		if candidates[i].ProjectedFPG != candidates[j].ProjectedFPG {
			return candidates[i].ProjectedFPG > candidates[j].ProjectedFPG
		}
		return candidates[i].MLBAMID < candidates[j].MLBAMID
	})
	if len(candidates) > opts.TopN {
		report.Top = candidates[:opts.TopN]
	} else {
		report.Top = candidates
	}

	printReport(report)

	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		writeGHASummary(report, path)
	}

	if !opts.DryRun && opts.PushoverUserKey != "" && opts.PushoverAPIToken != "" {
		msg := formatPushover(report)
		if msg != "" {
			if err := notify.SendPushover(opts.PushoverUserKey, opts.PushoverAPIToken, pushoverTitle, msg); err != nil {
				log.Printf("WARNING: pushover failed: %v", err)
			}
		}
	}

	return nil
}

// filterFreeAgents reduces the league pool to MLB free agents (FA + waivers),
// excluding minors-eligible-only players and applying the optional --positions
// filter (case-insensitive substring match against MultiPositions).
func filterFreeAgents(pool []models.PoolPlayer, posFilter []string) []models.PoolPlayer {
	var out []models.PoolPlayer
	for _, p := range pool {
		if !isUnowned(p.FantasyStatus) {
			continue
		}
		if p.MinorsEligible {
			continue
		}
		if len(posFilter) > 0 && !matchesPositionFilter(p, posFilter) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isUnowned(status string) bool {
	if status == "FA" || status == "" {
		return true
	}
	return strings.HasPrefix(status, "W")
}

func matchesPositionFilter(p models.PoolPlayer, filters []string) bool {
	hay := strings.ToUpper(p.MultiPositions)
	for _, f := range filters {
		f = strings.ToUpper(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		for _, tok := range strings.Split(hay, ",") {
			if strings.TrimSpace(tok) == f {
				return true
			}
		}
	}
	return false
}

// isSPEligible returns true when the player has SP eligibility.
func isSPEligible(positions []string) bool {
	for _, pos := range positions {
		if pos == auth_client.PosSP {
			return true
		}
	}
	return false
}

// isHitterEligible returns true when the player has at least one non-pitcher
// position (i.e. could fill a hitter slot).
func isHitterEligible(positions []string) bool {
	for _, pos := range positions {
		switch pos {
		case auth_client.PosSP, auth_client.PosRP, auth_client.PosP, auth_client.PosRP2, auth_client.PosRP3:
			continue
		default:
			return true
		}
	}
	return false
}

// mlbamLooker is implemented by FanGraphsSource / FanGraphsPitcherSource.
type mlbamLooker interface {
	MLBAMIDs() map[string]int
}

// mergeMLBAMMaps merges the MLBAM ID maps exposed by the batting and pitching
// FanGraphs sources into one normalized-name → MLBAM lookup.
func mergeMLBAMMaps(batSrc projections.Source, pitSrc projections.PitcherSource) map[string]int {
	out := map[string]int{}
	if l, ok := batSrc.(mlbamLooker); ok && l != nil {
		for k, v := range l.MLBAMIDs() {
			out[k] = v
		}
	}
	if l, ok := pitSrc.(mlbamLooker); ok && l != nil {
		for k, v := range l.MLBAMIDs() {
			out[k] = v
		}
	}
	return out
}

// rosteredPlayer is a (name, FPG) pair used as a drop target.
type rosteredPlayer struct {
	Name string
	FPG  float64
}

// worstRosteredHitter returns the lowest-projected active+reserve hitter as
// the implicit drop target for any FA hitter. UT slot eligibility makes any
// hitter mutually substitutable, so the floor is the global worst-projected
// rostered hitter.
func worstRosteredHitter(roster []fantrax.Player, src projections.Source, scoring fantrax.ScoringWeights) rosteredPlayer {
	if src == nil {
		return rosteredPlayer{}
	}
	worst := rosteredPlayer{FPG: math.Inf(1)}
	for _, p := range roster {
		if p.IsInjured || p.InMinors {
			continue
		}
		proj, ok := src.GetProjection(p.Name, p.MLBTeam)
		if !ok {
			continue
		}
		fpg := projections.ExpectedPtsFromProj(proj, scoring)
		if fpg < worst.FPG {
			worst = rosteredPlayer{Name: p.Name, FPG: fpg}
		}
	}
	if math.IsInf(worst.FPG, 1) {
		return rosteredPlayer{}
	}
	return worst
}

// worstRosteredPitcher mirrors worstRosteredHitter for pitchers. The P slot
// accepts any pitcher, so the global worst-projected rostered pitcher is the
// natural drop target.
func worstRosteredPitcher(roster []fantrax.Player, src projections.PitcherSource, scoring fantrax.ScoringWeights) rosteredPlayer {
	if src == nil {
		return rosteredPlayer{}
	}
	worst := rosteredPlayer{FPG: math.Inf(1)}
	for _, p := range roster {
		if p.IsInjured || p.InMinors {
			continue
		}
		proj, ok := src.GetPitcherProjection(p.Name, p.MLBTeam)
		if !ok {
			continue
		}
		fpg := projections.PitcherExpectedPtsFromProj(proj, scoring)
		if fpg < worst.FPG {
			worst = rosteredPlayer{Name: p.Name, FPG: fpg}
		}
	}
	if math.IsInf(worst.FPG, 1) {
		return rosteredPlayer{}
	}
	return worst
}

// buildCandidates joins each FA against Steamer (for projection) and Savant
// (for signal). When a drop target is supplied, only FAs whose projected FPG
// exceeds it survive — the report becomes "concrete swaps that improve the
// roster" rather than "FAs with a Statcast signal in absolute terms."
func buildCandidates(
	freeAgents []models.PoolPlayer,
	mlbamByName map[string]int,
	savant *SavantBundle,
	batSrc projections.Source,
	pitSrc projections.PitcherSource,
	hitterScoring fantrax.ScoringWeights,
	pitcherScoring fantrax.ScoringWeights,
	hitterDrop rosteredPlayer,
	pitcherDrop rosteredPlayer,
	th Thresholds,
) []Candidate {
	var out []Candidate
	for _, p := range freeAgents {
		mlbamID := mlbamByName[projections.NormalizeName(p.Name)]
		if mlbamID == 0 {
			continue
		}

		spEligible := isSPEligible(p.Positions)
		hitter := isHitterEligible(p.Positions)

		// Pitcher path (SP-only — RP-only is excluded per the user's scope).
		if spEligible && pitSrc != nil {
			sig, c := TagPitcher(savant, mlbamID, th)
			if sig != SignalNone {
				proj, ok := pitSrc.GetPitcherProjection(p.Name, p.MLBTeamShortName)
				if ok {
					c.ProjectedFPG = projections.PitcherExpectedPtsFromProj(proj, pitcherScoring)
					c.Name = p.Name
					c.MLBTeam = p.MLBTeamShortName
					c.Position = primaryPosition(p)
					if pitcherDrop.Name != "" {
						c.DropName = pitcherDrop.Name
						c.DropFPG = pitcherDrop.FPG
						c.Gap = c.ProjectedFPG - pitcherDrop.FPG
						if c.Gap <= 0 {
							continue
						}
					}
					out = append(out, c)
					continue
				}
			}
		}

		// Hitter path. (Two-way players: prefer pitcher signal when SP-eligible
		// and a pitcher signal fired; otherwise fall through to hitter.)
		if hitter && batSrc != nil {
			sig, c := TagHitter(savant, mlbamID, th)
			if sig != SignalNone {
				proj, ok := batSrc.GetProjection(p.Name, p.MLBTeamShortName)
				if ok {
					c.ProjectedFPG = projections.ExpectedPtsFromProj(proj, hitterScoring)
					c.Name = p.Name
					c.MLBTeam = p.MLBTeamShortName
					c.Position = primaryPosition(p)
					if hitterDrop.Name != "" {
						c.DropName = hitterDrop.Name
						c.DropFPG = hitterDrop.FPG
						c.Gap = c.ProjectedFPG - hitterDrop.FPG
						if c.Gap <= 0 {
							continue
						}
					}
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// primaryPosition returns a clean comma-separated position label for display.
// Falls back to PosShortNames with HTML stripped if MultiPositions is empty.
func primaryPosition(p models.PoolPlayer) string {
	if p.MultiPositions != "" {
		return p.MultiPositions
	}
	return stripHTMLTags(p.PosShortNames)
}

func stripHTMLTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

const (
	colorReset = "\033[0m"
	colorBold  = "\033[1m"
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
	colorCyan  = "\033[36m"
	colorGray  = "\033[90m"
)

func signalColor(s Signal) string {
	switch s {
	case SignalBuyLow:
		return colorRed
	case SignalHot:
		return colorGreen
	case SignalBoth:
		return colorBold + colorCyan
	default:
		return ""
	}
}

func printReport(r Report) {
	title := "Waiver Picks · " + r.Date.Format("2006-01-02")
	fmt.Printf("\n  %s\n\n", title)
	if len(r.Top) == 0 {
		fmt.Println("  No upgrade candidates surfaced today.")
		fmt.Println()
		return
	}
	fmt.Printf("  %-9s %-20s %-5s %-7s %5s   %-20s %5s   %5s   %s\n",
		"Signal", "Add", "Team", "Pos", "FPG", "Drop", "FPG", "+Gap", "Detail")
	fmt.Printf("  %s\n", strings.Repeat("─", 110))
	for _, c := range r.Top {
		clr := signalColor(c.Signal)
		add := truncate(c.Name, 18)
		drop := truncate(c.DropName, 18)
		pos := truncate(c.Position, 6)
		fmt.Printf("  %s%-9s%s %-20s %-5s %-7s %5.2f   %-20s %5.2f   %s+%4.2f%s   %s\n",
			clr, c.Signal.String(), colorReset,
			add, c.MLBTeam, pos, c.ProjectedFPG,
			drop, c.DropFPG,
			colorGreen, c.Gap, colorReset,
			candidateDetail(c))
	}
	fmt.Println()
	if r.Total > len(r.Top) {
		fmt.Printf("  %s(%d more upgrades found — showing top %d)%s\n\n",
			colorGray, r.Total-len(r.Top), len(r.Top), colorReset)
	}
}

func truncate(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// candidateDetail returns a one-line description of the signal evidence.
func candidateDetail(c Candidate) string {
	if c.IsPitcher {
		return pitcherDetail(c)
	}
	return hitterDetail(c)
}

func hitterDetail(c Candidate) string {
	parts := []string{}
	if c.Signal == SignalBuyLow || c.Signal == SignalBoth {
		parts = append(parts, fmt.Sprintf("xwOBA %s", signedDelta(c.BuyLowDelta, 3)))
	}
	if c.Signal == SignalHot || c.Signal == SignalBoth {
		parts = append(parts, fmt.Sprintf("14d wOBA %s", omitLeadZero(c.HotHitter.Window14dWOBA)))
	}
	if c.Barrel > 0 {
		parts = append(parts, fmt.Sprintf("%.1f%% Brl", c.Barrel))
	}
	if c.HardHit > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% HH", c.HardHit))
	}
	return strings.Join(parts, " · ")
}

func pitcherDetail(c Candidate) string {
	parts := []string{}
	if c.Signal == SignalBuyLow || c.Signal == SignalBoth {
		parts = append(parts, fmt.Sprintf("ERA %.2f / xERA %.2f", c.ERA, c.XERA))
	}
	if c.Signal == SignalHot || c.Signal == SignalBoth {
		parts = append(parts, fmt.Sprintf("30d ERA %.2f", c.HotPitcher.Window30dERA))
	}
	if c.XwOBA > 0 {
		parts = append(parts, fmt.Sprintf("xwOBA %s", omitLeadZero(c.XwOBA)))
	}
	return strings.Join(parts, " · ")
}

func signedDelta(v float64, prec int) string {
	sign := "+"
	if v < 0 {
		sign = "-"
		v = -v
	}
	return sign + omitLeadZeroPrec(v, prec)
}

func omitLeadZero(v float64) string {
	return omitLeadZeroPrec(v, 3)
}

func omitLeadZeroPrec(v float64, prec int) string {
	s := fmt.Sprintf("%.*f", prec, v)
	if strings.HasPrefix(s, "0.") {
		return s[1:]
	}
	if strings.HasPrefix(s, "-0.") {
		return "-" + s[2:]
	}
	return s
}

// ---------------------------------------------------------------------------
// GHA markdown summary
// ---------------------------------------------------------------------------

func writeGHASummary(r Report, path string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		log.Printf("WARNING: failed to open GHA summary file: %v", err)
		return
	}
	defer f.Close()

	fmt.Fprintln(f, "## Waiver Picks")
	fmt.Fprintln(f)
	if len(r.Top) == 0 {
		fmt.Fprintln(f, "No upgrade candidates surfaced today.")
		return
	}
	fmt.Fprintln(f, "| Signal | Add | Team | Pos | FPG | Drop | DropFPG | +Gap | Detail |")
	fmt.Fprintln(f, "|--------|-----|------|-----|-----|------|---------|------|--------|")
	for _, c := range r.Top {
		fmt.Fprintf(f, "| %s | %s | %s | %s | %.2f | %s | %.2f | +%.2f | %s |\n",
			c.Signal.String(), c.Name, c.MLBTeam, c.Position, c.ProjectedFPG,
			c.DropName, c.DropFPG, c.Gap, candidateDetail(c))
	}
	if r.Total > len(r.Top) {
		fmt.Fprintf(f, "\n_%d more upgrades found — showing top %d._\n", r.Total-len(r.Top), len(r.Top))
	}
}

// ---------------------------------------------------------------------------
// Pushover
// ---------------------------------------------------------------------------

// signalEmoji returns a compact emoji for each signal type.
func signalEmoji(s Signal) string {
	switch s {
	case SignalHot:
		return "🔥"
	case SignalBuyLow:
		return "📉"
	case SignalBoth:
		return "⚡"
	default:
		return ""
	}
}

// formatPushover builds an HTML-friendly body that fits Pushover's 1024-char
// limit. Each candidate is two lines: action (add → drop + FPG gain) and
// evidence (stat details), separated by a blank line for scannability.
func formatPushover(r Report) string {
	if len(r.Top) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Waiver Picks · %s</b>\n", r.Date.Format("Jan 2"))
	for _, c := range r.Top {
		action := fmt.Sprintf("\n%s <b>%s</b> (%s) → drop %s · +%.2f FPG\n%s\n",
			signalEmoji(c.Signal), shortName(c.Name), c.MLBTeam,
			shortName(c.DropName), c.Gap, candidateDetail(c))
		if b.Len()+len(action) > 1000 {
			break
		}
		b.WriteString(action)
	}
	if r.Total > len(r.Top) {
		extra := fmt.Sprintf("+%d more", r.Total-len(r.Top))
		if b.Len()+len(extra) <= 1024 {
			b.WriteString(extra)
		}
	}
	return b.String()
}

// shortName collapses "First Last" to "F. Last" to save Pushover bytes.
func shortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	first := []rune(parts[0])
	if len(first) == 0 {
		return name
	}
	return string(first[:1]) + ". " + strings.Join(parts[1:], " ")
}
