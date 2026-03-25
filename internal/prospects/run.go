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
	"github.com/nixon-commits/rosterbot/internal/projections"
	"golang.org/x/sync/errgroup"
)

// ---------------------------------------------------------------------------
// Transaction cursor
// ---------------------------------------------------------------------------

var txnCursorFile = ".prospects-cache/last-transactions.json"

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
// RunProspectReport
// ---------------------------------------------------------------------------

// RunProspectReport orchestrates the full prospect report: fetches rankings,
// minors roster, available prospects, transactions, and performance data,
// then prints the report and optionally writes a GHA summary.
func RunProspectReport(ft *fantrax.Client, cfg config.Config, today time.Time) error {
	if err := os.MkdirAll(".prospects-cache", 0o755); err != nil {
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

	// Parallel: load FanGraphs rankings, Fantrax rankings, and available prospects
	var fgRankings []RankedProspect
	var ftxRankings []RankedProspect
	var availablePlayers []fantrax.Player

	g := new(errgroup.Group)

	g.Go(func() error {
		r, err := LoadRankings(&FanGraphsRankingSource{}, today.Year(), cfg.ProspectRankCacheHours)
		if err != nil {
			log.Printf("WARNING: FanGraphs rankings failed: %v", err)
			return nil // non-fatal
		}
		fgRankings = r
		return nil
	})

	g.Go(func() error {
		r, err := (&FantraxRankingSource{Client: ft}).GetTopProspects(today.Year())
		if err != nil {
			log.Printf("WARNING: Fantrax rankings failed: %v", err)
			return nil // non-fatal
		}
		ftxRankings = r
		return nil
	})

	g.Go(func() error {
		ap, err := ft.GetAvailableProspects()
		if err != nil {
			log.Printf("WARNING: failed to fetch available prospects: %v", err)
			return nil // non-fatal
		}
		availablePlayers = ap
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	// Use FanGraphs as primary rankings map for transaction/performance alerts.
	// Fall back to Fantrax if FanGraphs unavailable.
	primaryRankings := fgRankings
	if len(primaryRankings) == 0 {
		primaryRankings = ftxRankings
	}
	rankingsMap := make(map[string]int, len(primaryRankings))
	for _, r := range primaryRankings {
		rankingsMap[projections.NormalizeName(r.Name)] = r.Rank
	}

	// Transaction cursor
	cursorDate := loadTxnCursor()
	if cursorDate.IsZero() {
		cursorDate = today.AddDate(0, 0, -3)
	}

	// Fetch alerts (both non-fatal on error)
	var txnAlerts []ProspectAlert
	ta, err := FetchTransactionAlerts(cursorDate, today, myMinors, rankingsMap)
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

	// Compute upgrades from both ranking sources independently.
	currentYear := strconv.Itoa(today.Year())
	var upgradeSets []UpgradeSet

	// --- FanGraphs: rank-based upgrades ---
	if len(fgRankings) > 0 {
		srcMap := make(map[string]int, len(fgRankings))
		for _, r := range fgRankings {
			srcMap[projections.NormalizeName(r.Name)] = r.Rank
		}

		var myRanked []RankedProspect
		for _, p := range minorsRoster {
			norm := projections.NormalizeName(p.Name)
			if rank, ok := srcMap[norm]; ok && rank > 0 {
				myRanked = append(myRanked, RankedProspect{
					Name:    p.Name,
					MLBTeam: p.MLBTeam,
					Rank:    rank,
				})
			}
		}

		var faRanked []RankedProspect
		for _, p := range availablePlayers {
			norm := projections.NormalizeName(p.Name)
			if _, ok := srcMap[norm]; ok {
				for _, r := range fgRankings {
					if projections.NormalizeName(r.Name) == norm {
						faRanked = append(faRanked, r)
						break
					}
				}
			}
		}

		candidates := FindUpgrades(myRanked, faRanked, currentYear)
		if len(candidates) > 0 {
			upgradeSets = append(upgradeSets, UpgradeSet{
				Source:     "FanGraphs",
				Candidates: candidates,
			})
		}
	}

	// --- Fantrax: %Rostered-based upgrades ---
	if len(ftxRankings) > 0 {
		ftxMap := make(map[string]RankedProspect, len(ftxRankings))
		for _, r := range ftxRankings {
			ftxMap[projections.NormalizeName(r.Name)] = r
		}

		var myRanked []RankedProspect
		for _, p := range minorsRoster {
			norm := projections.NormalizeName(p.Name)
			if rp, ok := ftxMap[norm]; ok {
				rp.Name = p.Name // preserve original name casing
				rp.MLBTeam = p.MLBTeam
				myRanked = append(myRanked, rp)
			}
		}

		var faRanked []RankedProspect
		for _, p := range availablePlayers {
			norm := projections.NormalizeName(p.Name)
			if rp, ok := ftxMap[norm]; ok {
				faRanked = append(faRanked, rp)
			}
		}

		candidates := FindPctRosteredUpgrades(myRanked, faRanked, 15.0)
		if len(candidates) > 0 {
			upgradeSets = append(upgradeSets, UpgradeSet{
				Source:     "Fantrax",
				Candidates: candidates,
			})
		}
	}

	report := Report{
		Date:     today,
		Alerts:   allAlerts,
		Rankings: nil, // rankings displayed per-source in upgrades
		Upgrades: upgradeSets,
	}

	printReport(report)

	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		writeGHASummary(report, summaryPath)
	}

	if err := saveTxnCursor(today); err != nil {
		log.Printf("WARNING: failed to save transaction cursor: %v", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// printReport — stdout format
// ---------------------------------------------------------------------------

func alertKindLabel(kind AlertKind) string {
	switch kind {
	case CalledUp:
		return "CALLED UP"
	case Optioned:
		return "OPTIONED"
	case PerformanceHot:
		return "HOT STREAK"
	case PerformanceCold:
		return "COLD STREAK"
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

func printReport(r Report) {
	fmt.Printf("=== Prospect Report (%s) ===\n", r.Date.Format("2006-01-02"))

	if len(r.Alerts) == 0 && !hasUpgrades(r.Upgrades) {
		fmt.Println("No prospect alerts today.")
		fmt.Println("=== End Prospect Report ===")
		return
	}

	for _, a := range r.Alerts {
		prio := strings.ToUpper(a.Priority)
		label := alertKindLabel(a.Kind)
		team := a.MLBTeam
		if team == "" {
			team = "???"
		}
		fmt.Printf("[%-4s]  %-14s %-25s (%s)   %s\n", prio, label, a.PlayerName, team, a.Detail)
	}

	for _, set := range r.Upgrades {
		fmt.Printf("--- Upgrades (%s) ---\n", set.Source)
		for _, u := range set.Candidates {
			if u.PctGap > 0 {
				// %Rostered-based (Fantrax)
				fmt.Printf("  Drop %s (%.0f%%) → Add %s (%.0f%%) [+%.0f%%]\n",
					u.Drop.Name, u.Drop.PctRostered, u.Add.Name, u.Add.PctRostered, u.PctGap)
			} else {
				// Rank-based (FanGraphs)
				nearTerm := ""
				if u.NearTerm {
					nearTerm = fmt.Sprintf(", ETA %s", u.Add.ETA)
				}
				fmt.Printf("  Drop %s (#%d) → Add %s (#%d) [+%d spots%s]\n",
					u.Drop.Name, u.Drop.Rank, u.Add.Name, u.Add.Rank, u.RankGap, nearTerm)
			}
		}
	}

	fmt.Println("=== End Prospect Report ===")
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
		if set.Source == "Fantrax" {
			fmt.Fprintln(f, "| Drop | Add | %Rost Gap |")
			fmt.Fprintln(f, "|------|-----|-----------|")
			for _, u := range set.Candidates {
				fmt.Fprintf(f, "| %s (%.0f%%) | %s (%.0f%%) | +%.0f%% |\n",
					u.Drop.Name, u.Drop.PctRostered, u.Add.Name, u.Add.PctRostered, u.PctGap)
			}
		} else {
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
		}
		fmt.Fprintln(f)
	}
}
