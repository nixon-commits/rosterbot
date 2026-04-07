package prospects

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/pmurley/go-fantrax/models"
	"golang.org/x/sync/errgroup"
)

// ---------------------------------------------------------------------------
// Transaction cursor
// ---------------------------------------------------------------------------

var txnCursorFile = ".cache/last-transactions.json"

type txnCursor struct {
	LastChecked time.Time `json:"lastChecked"`
}

func loadTxnCursor() time.Time {
	data, err := os.ReadFile(txnCursorFile)
	if err != nil {
		return time.Time{}
	}
	var c txnCursor
	if err := json.Unmarshal(data, &c); err != nil {
		return time.Time{}
	}
	if c.LastChecked.IsZero() {
		return time.Time{}
	}
	return c.LastChecked
}

func saveTxnCursor(date time.Time) error {
	c := txnCursor{LastChecked: date}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(txnCursorFile, data, 0o644)
}

// ---------------------------------------------------------------------------
// hkbRanks
// ---------------------------------------------------------------------------

// hkbRanks builds a name → rank map from HKB players.
// Includes all non-MLB players with a rank — Fantrax minors designation
// is the source of truth for who is a prospect, HKB just enriches data.
func hkbRanks(players []hkb.Player) map[string]int {
	m := make(map[string]int)
	for _, p := range players {
		if p.AssetType == "PLAYER" && p.Level != "MLB" && p.Rank > 0 {
			m[projections.NormalizeName(p.Name)] = p.Rank
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// RunProspectReport
// ---------------------------------------------------------------------------

// RunProspectReport orchestrates the full prospect report: fetches rankings
// from multiple sources, minors roster, available prospects, transactions,
// and performance data, then prints the report and optionally writes a GHA summary.
func RunProspectReport(ft *fantrax.Client, cfg config.Config, today time.Time) error {
	if err := os.MkdirAll(".cache", 0o755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	// Get minors roster
	minorsRoster, err := ft.GetMinorsRoster()
	if err != nil {
		return fmt.Errorf("fetching minors roster: %w", err)
	}
	myMinors := make(map[string]bool, len(minorsRoster))
	for _, p := range minorsRoster {
		myMinors[projections.NormalizeName(p.Name)] = true
	}

	var fgRankings []RankedProspect
	var hkbPlayers []hkb.Player
	var availablePlayers []fantrax.Player

	g := new(errgroup.Group)

	g.Go(func() error {
		r, err := LoadRankings(&FanGraphsRankingSource{}, today.Year(), cfg.ProspectRankCacheHours)
		if err != nil {
			log.Printf("WARNING: FanGraphs rankings failed: %v", err)
			return nil
		}
		fgRankings = r
		return nil
	})

	g.Go(func() error {
		p, err := hkb.GetPlayers(".cache")
		if err != nil {
			log.Printf("WARNING: HKB data failed: %v", err)
			return nil
		}
		hkbPlayers = p
		return nil
	})

	g.Go(func() error {
		ap, err := ft.GetAvailableProspects()
		if err != nil {
			log.Printf("WARNING: failed to fetch available prospects: %v", err)
			return nil
		}
		availablePlayers = ap
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	fgMap := make(map[string]int, len(fgRankings))
	for _, r := range fgRankings {
		fgMap[projections.NormalizeName(r.Name)] = r.Rank
	}
	hkbMap := hkbRanks(hkbPlayers)

	// Resolve MLBAM IDs to bridge nickname/legal name mismatches.
	// Collect all names from all sources, resolve via MLB API (cached),
	// then build MLBAM-keyed maps for cross-source matching.
	var allNames []string
	for _, p := range minorsRoster {
		allNames = append(allNames, p.Name)
	}
	for _, r := range fgRankings {
		allNames = append(allNames, r.Name)
	}
	for _, p := range hkbPlayers {
		if p.AssetType == "PLAYER" && p.Level != "MLB" {
			allNames = append(allNames, p.Name)
		}
	}
	resolved, resolveErr := playername.ResolveMLBAMIDs(allNames, ".cache")
	if resolveErr != nil {
		log.Printf("WARNING: MLBAM ID resolution failed: %v — using name-only matching", resolveErr)
	}

	// Build MLBAM-keyed ranking maps for ID-based matching.
	fgByMLBAM := make(map[int]int)
	hkbByMLBAM := make(map[int]int)
	if resolved != nil {
		for _, r := range fgRankings {
			if id, ok := resolved.ByName[projections.NormalizeName(r.Name)]; ok {
				fgByMLBAM[id] = r.Rank
			}
		}
		for name, rank := range hkbMap {
			if id, ok := resolved.ByName[name]; ok {
				hkbByMLBAM[id] = rank
			}
		}
	}

	rankingsMap := fgMap

	// Transaction cursor
	cursorDate := loadTxnCursor()
	if cursorDate.IsZero() {
		cursorDate = today.AddDate(0, 0, -3)
	}

	// Build available-player set for transaction alert enrichment.
	var availableSet map[string]bool
	if len(availablePlayers) > 0 {
		availableSet = make(map[string]bool, len(availablePlayers))
		for _, p := range availablePlayers {
			availableSet[projections.NormalizeName(p.Name)] = true
		}
	}

	// Fetch alerts (both non-fatal on error)
	var txnAlerts []ProspectAlert
	ta, err := FetchTransactionAlerts(cursorDate, today, myMinors, rankingsMap, availableSet)
	if err != nil {
		log.Printf("WARNING: transaction alerts failed: %v", err)
	} else {
		txnAlerts = ta
	}

	var perfAlerts []ProspectAlert
	pa, err := FetchPerformanceAlerts(minorsRoster, rankingsMap, today.Year(), cfg.ProspectRollingDays, cfg.ProspectMinGames)
	if err != nil {
		log.Printf("WARNING: performance alerts failed: %v", err)
	} else {
		perfAlerts = pa
	}

	// Combine alerts
	allAlerts := append(txnAlerts, perfAlerts...)

	// Sort alerts by priority: high → medium → low
	priorityOrder := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(allAlerts, func(i, j int) bool {
		return priorityOrder[allAlerts[i].Priority] < priorityOrder[allAlerts[j].Priority]
	})

	currentYear := strconv.Itoa(today.Year())
	var upgradeSets []UpgradeSet

	// FanGraphs upgrades
	if len(fgRankings) > 0 {
		var myRanked []RankedProspect
		for _, p := range minorsRoster {
			if rank, ok := lookupRank(p.Name, fgMap, fgByMLBAM, resolved); ok {
				myRanked = append(myRanked, RankedProspect{Name: p.Name, MLBTeam: p.MLBTeam, Rank: rank})
			}
		}

		fgByName := make(map[string]RankedProspect, len(fgRankings))
		for _, r := range fgRankings {
			fgByName[projections.NormalizeName(r.Name)] = r
		}
		var faRanked []RankedProspect
		for _, p := range availablePlayers {
			if r, ok := fgByName[projections.NormalizeName(p.Name)]; ok {
				faRanked = append(faRanked, r)
			}
		}

		if candidates := FindUpgrades(myRanked, faRanked, currentYear); len(candidates) > 0 {
			upgradeSets = append(upgradeSets, UpgradeSet{Source: "FanGraphs", Candidates: candidates})
		}
	}

	// HKB upgrades
	if len(hkbMap) > 0 {
		var myRanked []RankedProspect
		for _, p := range minorsRoster {
			if rank, ok := lookupRank(p.Name, hkbMap, hkbByMLBAM, resolved); ok {
				myRanked = append(myRanked, RankedProspect{Name: p.Name, MLBTeam: p.MLBTeam, Rank: rank})
			}
		}

		var faRanked []RankedProspect
		for _, p := range availablePlayers {
			if rank, ok := lookupRank(p.Name, hkbMap, hkbByMLBAM, resolved); ok {
				faRanked = append(faRanked, RankedProspect{Name: p.Name, MLBTeam: p.MLBTeam, Rank: rank})
			}
		}

		if candidates := FindUpgrades(myRanked, faRanked, currentYear); len(candidates) > 0 {
			upgradeSets = append(upgradeSets, UpgradeSet{Source: "HKB", Candidates: candidates})
		}
	}

	// Build roster ranking info for display.
	sourceNames := []string{"FanGraphs", "HKB"}
	var rosterRanked []rosterRankEntry
	for _, p := range minorsRoster {
		entry := rosterRankEntry{Name: p.Name, Team: p.MLBTeam}
		if rank, ok := lookupRank(p.Name, fgMap, fgByMLBAM, resolved); ok {
			entry.Ranks = append(entry.Ranks, sourceRank{Source: "FanGraphs", Rank: rank})
		}
		if rank, ok := lookupRank(p.Name, hkbMap, hkbByMLBAM, resolved); ok {
			entry.Ranks = append(entry.Ranks, sourceRank{Source: "HKB", Rank: rank})
		}
		rosterRanked = append(rosterRanked, entry)
	}

	report := Report{
		Date:     today,
		Alerts:   allAlerts,
		Upgrades: upgradeSets,
	}

	printReport(report, rosterRanked, sourceNames)

	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		writeGHASummary(report, summaryPath)
	}

	if err := saveTxnCursor(today); err != nil {
		log.Printf("WARNING: failed to save transaction cursor: %v", err)
	}

	return nil
}

// lookupRank tries name-based lookup first, then falls back to MLBAM ID matching.
func lookupRank(name string, nameMap map[string]int, mlbamMap map[int]int, resolved *playername.ResolvedPlayers) (int, bool) {
	norm := projections.NormalizeName(name)
	if rank, ok := nameMap[norm]; ok && rank > 0 {
		return rank, true
	}
	if resolved != nil {
		if id, ok := resolved.ByName[norm]; ok {
			if rank, ok := mlbamMap[id]; ok && rank > 0 {
				return rank, true
			}
		}
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Display types
// ---------------------------------------------------------------------------

type sourceRank struct {
	Source string
	Rank   int
}

type rosterRankEntry struct {
	Name  string
	Team  string
	Ranks []sourceRank
}

// ---------------------------------------------------------------------------
// printReport — styled stdout format
// ---------------------------------------------------------------------------

const (
	colW  = 60 // single column width
	green = "\033[32m"
	red   = "\033[31m"
	gray  = "\033[90m"
	reset = "\033[0m"
)

func alertKindLabel(kind AlertKind) string {
	switch kind {
	case CalledUp:
		return "CALLED UP"
	case Optioned:
		return "OPTIONED"
	case PerformanceHot:
		return "HOT"
	case PerformanceCold:
		return "COLD"
	case FreeAgentBuzz:
		return "FA BUZZ"
	case UpgradeAvail:
		return "UPGRADE"
	default:
		return string(kind)
	}
}

func hasUpgrades(sets []UpgradeSet) bool {
	for _, s := range sets {
		if len(s.Candidates) > 0 {
			return true
		}
	}
	return false
}

func priorityColor(prio string) string {
	switch prio {
	case "high":
		return red
	case "medium":
		return "\033[33m" // yellow
	default:
		return gray
	}
}

func printReport(r Report, roster []rosterRankEntry, sourceNames []string) {
	// Header
	title := "Prospect Report · " + r.Date.Format("2006-01-02")
	fmt.Printf("\n  %s\n\n", title)

	// Minors roster with rankings from each source
	if len(roster) > 0 {
		fmt.Printf("  Minors Roster %s\n", strings.Repeat("─", colW-15))

		// Header row
		fmt.Printf("  %-22s%-5s", "Player", "Team")
		for _, name := range sourceNames {
			fmt.Printf("  %-8s", name)
		}
		fmt.Println()
		fmt.Printf("  %s\n", strings.Repeat("─", colW))

		// Sort by first available rank ascending
		sorted := make([]rosterRankEntry, len(roster))
		copy(sorted, roster)
		sort.Slice(sorted, func(i, j int) bool {
			ri, rj := 9999, 9999
			if len(sorted[i].Ranks) > 0 {
				ri = sorted[i].Ranks[0].Rank
			}
			if len(sorted[j].Ranks) > 0 {
				rj = sorted[j].Ranks[0].Rank
			}
			return ri < rj
		})

		for _, entry := range sorted {
			name := entry.Name
			if len([]rune(name)) > 20 {
				name = string([]rune(name)[:20])
			}
			fmt.Printf("  %-22s%-5s", name, entry.Team)
			for _, srcName := range sourceNames {
				found := false
				for _, sr := range entry.Ranks {
					if sr.Source == srcName {
						fmt.Printf("  %s%-8s%s", green, fmt.Sprintf("#%d", sr.Rank), reset)
						found = true
						break
					}
				}
				if !found {
					fmt.Printf("  %s%-8s%s", gray, "-", reset)
				}
			}
			fmt.Println()
		}
		fmt.Println()
	}

	// Alerts
	if len(r.Alerts) > 0 {
		fmt.Printf("  Alerts %s\n", strings.Repeat("─", colW-8))
		for _, a := range r.Alerts {
			color := priorityColor(a.Priority)
			label := alertKindLabel(a.Kind)
			team := a.MLBTeam
			if team == "" {
				team = "???"
			}
			fmt.Printf("  %s%-10s%s %-22s %-4s %s\n",
				color, label, reset, a.PlayerName, team, a.Detail)
		}
		fmt.Println()
	}

	// Upgrades
	if hasUpgrades(r.Upgrades) {
		for _, set := range r.Upgrades {
			header := fmt.Sprintf("Upgrades (%s)", set.Source)
			fmt.Printf("\n  %s %s\n", header, strings.Repeat("─", colW-len(header)-1))
			fmt.Printf("  %-20s %6s    %-20s %6s  %s\n",
				"Drop", "Rank", "Add", "Rank", "Gap")
			fmt.Printf("  %s\n", strings.Repeat("─", colW))
			for _, u := range set.Candidates {
				nearTerm := ""
				if u.NearTerm {
					nearTerm = fmt.Sprintf(" (ETA %s)", u.Add.ETA)
				}
				dropName := u.Drop.Name
				if len([]rune(dropName)) > 18 {
					dropName = string([]rune(dropName)[:18])
				}
				addName := u.Add.Name
				if len([]rune(addName)) > 18 {
					addName = string([]rune(addName)[:18])
				}
				fmt.Printf("  %-20s %s%6s%s  → %-20s %s%6s%s  %s%-4s%s%s\n",
					dropName, gray, fmt.Sprintf("#%d", u.Drop.Rank), reset,
					addName, green, fmt.Sprintf("#%d", u.Add.Rank), reset,
					green, fmt.Sprintf("+%d", u.RankGap), reset, nearTerm)
			}
		}
		fmt.Println()
	}

	if len(r.Alerts) == 0 && !hasUpgrades(r.Upgrades) && len(roster) == 0 {
		fmt.Println("  No prospect alerts today.")
	}
}

// ---------------------------------------------------------------------------
// ListAllProspects
// ---------------------------------------------------------------------------

// ListAllProspects fetches all minors-eligible players in the league, cross-
// references them against ranking sources, and prints a table showing each
// player's rank and which fantasy team owns them.
func ListAllProspects(ft *fantrax.Client, cfg config.Config, today time.Time) error {
	if err := os.MkdirAll(".cache", 0o755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	var fgRankings []RankedProspect
	var hkbPlayers []hkb.Player
	var pool []models.PoolPlayer

	g := new(errgroup.Group)

	g.Go(func() error {
		r, err := LoadRankings(&FanGraphsRankingSource{}, today.Year(), cfg.ProspectRankCacheHours)
		if err != nil {
			log.Printf("WARNING: FanGraphs rankings failed: %v", err)
			return nil
		}
		fgRankings = r
		return nil
	})

	g.Go(func() error {
		p, err := hkb.GetPlayers(".cache")
		if err != nil {
			log.Printf("WARNING: HKB data failed: %v", err)
			return nil
		}
		hkbPlayers = p
		return nil
	})

	g.Go(func() error {
		p, err := ft.GetFullPlayerPool()
		if err != nil {
			return fmt.Errorf("fetching player pool: %w", err)
		}
		pool = p
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	fgMap := make(map[string]int, len(fgRankings))
	for _, r := range fgRankings {
		fgMap[projections.NormalizeName(r.Name)] = r.Rank
	}
	hkbMap := hkbRanks(hkbPlayers)

	// Resolve MLBAM IDs for cross-source matching (uses cached data).
	var allNames []string
	for _, pp := range pool {
		if pp.MinorsEligible {
			allNames = append(allNames, pp.Name)
		}
	}
	for _, r := range fgRankings {
		allNames = append(allNames, r.Name)
	}
	for _, p := range hkbPlayers {
		if p.AssetType == "PLAYER" && p.Level != "MLB" {
			allNames = append(allNames, p.Name)
		}
	}
	resolved, resolveErr := playername.ResolveMLBAMIDs(allNames, ".cache")
	if resolveErr != nil {
		log.Printf("WARNING: MLBAM ID resolution failed: %v — using name-only matching", resolveErr)
	}

	fgByMLBAM := make(map[int]int)
	hkbByMLBAM := make(map[int]int)
	if resolved != nil {
		for _, r := range fgRankings {
			if id, ok := resolved.ByName[projections.NormalizeName(r.Name)]; ok {
				fgByMLBAM[id] = r.Rank
			}
		}
		for name, rank := range hkbMap {
			if id, ok := resolved.ByName[name]; ok {
				hkbByMLBAM[id] = rank
			}
		}
	}

	sourceNames := []string{"FanGraphs", "HKB"}

	type row struct {
		name     string
		team     string
		owner    string
		ranks    []int
		bestRank int
	}
	var rows []row
	for _, pp := range pool {
		if !pp.MinorsEligible {
			continue
		}
		if pp.FantasyStatus == "FA" || pp.FantasyStatus == "" {
			continue
		}
		owner := pp.FantasyStatus
		if strings.HasPrefix(owner, "W") {
			owner = green + "WAIVER    " + reset
		}
		fgRank, _ := lookupRank(pp.Name, fgMap, fgByMLBAM, resolved)
		hkbRank, _ := lookupRank(pp.Name, hkbMap, hkbByMLBAM, resolved)
		best := 9999
		if fgRank > 0 && fgRank < best {
			best = fgRank
		}
		if hkbRank > 0 && hkbRank < best {
			best = hkbRank
		}
		rows = append(rows, row{
			name:     pp.Name,
			team:     pp.MLBTeamShortName,
			owner:    owner,
			ranks:    []int{fgRank, hkbRank},
			bestRank: best,
		})
	}

	// Sort by HKB rank ascending (index 1), unranked at bottom
	sort.Slice(rows, func(i, j int) bool {
		ri, rj := rows[i].ranks[1], rows[j].ranks[1]
		if ri == 0 {
			ri = 9999
		}
		if rj == 0 {
			rj = 9999
		}
		return ri < rj
	})

	fmt.Printf("\n  All Minors-Eligible Rostered Players (%d)\n", len(rows))
	fmt.Printf("  %s\n", strings.Repeat("─", 72))
	fmt.Printf("  %-22s%-5s %-10s", "Player", "Team", "Owner")
	for _, name := range sourceNames {
		fmt.Printf("  %-8s", name)
	}
	fmt.Println()
	fmt.Printf("  %s\n", strings.Repeat("─", 72))

	for _, r := range rows {
		name := r.name
		if len([]rune(name)) > 20 {
			name = string([]rune(name)[:20])
		}
		fmt.Printf("  %-22s%-5s %-10s", name, r.team, r.owner)
		for _, rank := range r.ranks {
			if rank > 0 {
				fmt.Printf("  %s%-8s%s", green, fmt.Sprintf("#%d", rank), reset)
			} else {
				fmt.Printf("  %s%-8s%s", gray, "-", reset)
			}
		}
		fmt.Println()
	}
	fmt.Println()

	return nil
}

// ---------------------------------------------------------------------------
// writeGHASummary — GHA markdown output
// ---------------------------------------------------------------------------

func writeGHASummary(r Report, path string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		log.Printf("WARNING: failed to open GHA summary file: %v", err)
		return
	}
	defer f.Close()

	fmt.Fprintln(f, "## Prospect Report")
	fmt.Fprintln(f)

	if len(r.Alerts) == 0 && !hasUpgrades(r.Upgrades) {
		fmt.Fprintln(f, "No prospect alerts today.")
		return
	}

	if len(r.Alerts) > 0 {
		fmt.Fprintln(f, "### Alerts")
		fmt.Fprintln(f, "| Priority | Type | Player | Team | Detail |")
		fmt.Fprintln(f, "|----------|------|--------|------|--------|")
		for _, a := range r.Alerts {
			prio := strings.ToUpper(a.Priority)
			label := alertKindLabel(a.Kind)
			team := a.MLBTeam
			if team == "" {
				team = "???"
			}
			fmt.Fprintf(f, "| %s | %s | %s | %s | %s |\n", prio, label, a.PlayerName, team, a.Detail)
		}
		fmt.Fprintln(f)
	}

	for _, set := range r.Upgrades {
		fmt.Fprintf(f, "### Upgrades (%s)\n", set.Source)
		fmt.Fprintln(f, "| Drop | Add | Rank Gap | Near-Term |")
		fmt.Fprintln(f, "|------|-----|----------|-----------|")
		for _, u := range set.Candidates {
			nearTerm := ""
			if u.NearTerm {
				nearTerm = "yes"
			}
			fmt.Fprintf(f, "| %s (#%d) | %s (#%d) | +%d | %s |\n",
				u.Drop.Name, u.Drop.Rank, u.Add.Name, u.Add.Rank, u.RankGap, nearTerm)
		}
		fmt.Fprintln(f)
	}
}
