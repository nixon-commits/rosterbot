package gscheck

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

// ViolationKind indicates whether a team exceeded the max or fell below the min.
type ViolationKind int

const (
	ViolationMax ViolationKind = iota // exceeded GS_MAX
	ViolationMin                      // below GS_MIN
)

// Violation represents a team that violated a GS limit.
type Violation struct {
	TeamName string
	GSUsed   int
	Kind     ViolationKind
}

// BuildReport creates the notification content for GS violations.
// Returns a title and a short summary suitable for Pushover.
func BuildReport(violations []Violation, periodLabel string, gsMax, gsMin int) (title, summary string) {
	title = fmt.Sprintf("GS Alert: %d violation(s) — %s", len(violations), periodLabel)

	var teamParts []string
	for _, v := range violations {
		switch v.Kind {
		case ViolationMax:
			teamParts = append(teamParts, fmt.Sprintf("%s (%d GS, +%d over max)", v.TeamName, v.GSUsed, v.GSUsed-gsMax))
		case ViolationMin:
			teamParts = append(teamParts, fmt.Sprintf("%s (%d GS, %d under min)", v.TeamName, v.GSUsed, gsMin-v.GSUsed))
		}
	}
	limParts := []string{}
	if gsMax > 0 {
		limParts = append(limParts, fmt.Sprintf("max %d", gsMax))
	}
	if gsMin > 0 {
		limParts = append(limParts, fmt.Sprintf("min %d", gsMin))
	}
	summary = fmt.Sprintf("%d GS violation(s) for %s (%s): %s", len(violations), periodLabel, strings.Join(limParts, ", "), strings.Join(teamParts, ", "))

	return
}

type teamGS struct {
	id   string
	name string
	gs   int
}

// RunGSCheck checks all teams for GS violations in the most recent scoring period.
func RunGSCheck(ft *fantrax.Client, cfg config.Config, force bool) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	fmt.Printf("Running GS check for date: %s\n", today.Format("2006-01-02"))

	fmt.Println("Fetching scoring periods and teams...")
	periods, teamMap, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return fmt.Errorf("fetch scoring periods: %w", err)
	}
	if len(periods) == 0 {
		return fmt.Errorf("no scoring periods found")
	}

	var period *fantrax.ScoringPeriod
	if force {
		period = fantrax.FindCurrentPeriod(periods, today)
		if period == nil {
			period = fantrax.FindMostRecentPastPeriod(periods, today)
		}
		if period == nil {
			return fmt.Errorf("no current or past scoring period found for %s", today.Format("2006-01-02"))
		}
		fmt.Printf("--force: using period %d (%s)\n", period.Number, period.Caption)
	} else {
		period = fantrax.FindJustEndedPeriod(periods, today)
		if period == nil {
			fmt.Println("Yesterday was not the end of a scoring period. Nothing to check.")
			return nil
		}
	}

	periodLabel := fmt.Sprintf("%s (%s – %s)", period.Caption, period.StartDate.Format("2006-01-02"), period.EndDate.Format("2006-01-02"))
	fmt.Printf("Checking: %s\n", periodLabel)
	fmt.Printf("GS max: %d\n", cfg.GSMax)
	if cfg.GSMin > 0 {
		fmt.Printf("GS min: %d\n", cfg.GSMin)
	}

	if len(teamMap) == 0 {
		return fmt.Errorf("no teams found")
	}

	// Derive season start from the earliest scoring period (period 1 = season opener).
	seasonStart := periods[0].StartDate
	for _, p := range periods {
		if p.StartDate.Before(seasonStart) {
			seasonStart = p.StartDate
		}
	}
	fmt.Printf("Found %d teams. Tallying GS for Period %d (days %s to %s)...\n",
		len(teamMap), period.Number, period.StartDate.Format("2006-01-02"), today.Format("2006-01-02"))

	var results []teamGS
	for teamID, teamName := range teamMap {
		gs, err := ft.GetTeamGS(teamID, *period, seasonStart, today)
		if err != nil {
			fmt.Printf("WARNING: failed to get GS for %s: %v\n", teamName, err)
			continue
		}
		fmt.Printf("  %s: %d GS\n", teamName, gs)
		results = append(results, teamGS{id: teamID, name: teamName, gs: gs})
		time.Sleep(500 * time.Millisecond)
	}

	// Find violations.
	var violations []Violation
	for _, r := range results {
		if r.gs > cfg.GSMax {
			violations = append(violations, Violation{TeamName: r.name, GSUsed: r.gs, Kind: ViolationMax})
		}
		if cfg.GSMin > 0 && r.gs < cfg.GSMin {
			violations = append(violations, Violation{TeamName: r.name, GSUsed: r.gs, Kind: ViolationMin})
		}
	}

	// Print report.
	sort.Slice(results, func(i, j int) bool { return results[i].gs > results[j].gs })
	fmt.Printf("\n--- GS Report: %s (max=%d", periodLabel, cfg.GSMax)
	if cfg.GSMin > 0 {
		fmt.Printf(", min=%d", cfg.GSMin)
	}
	fmt.Println(") ---")
	for _, r := range results {
		flag := ""
		if r.gs > cfg.GSMax {
			flag = " *** OVER MAX ***"
		} else if cfg.GSMin > 0 && r.gs < cfg.GSMin {
			flag = " *** UNDER MIN ***"
		}
		fmt.Printf("  %s: %d GS%s\n", r.name, r.gs, flag)
	}

	if len(violations) == 0 {
		fmt.Println("\nNo violations found.")
		return nil
	}

	fmt.Printf("\n%d violation(s) found.\n", len(violations))
	_, shortSummary := BuildReport(violations, periodLabel, cfg.GSMax, cfg.GSMin)

	if cfg.DryRun {
		fmt.Println("\n[DRY RUN] Would send Pushover notification:")
		fmt.Printf("  %s\n", shortSummary)
		return nil
	}

	// Send Pushover notification.
	if err := notify.SendPushover(cfg.PushoverUserKey, cfg.PushoverAPIToken, "Fantrax GS Alert", shortSummary); err != nil {
		return fmt.Errorf("send pushover: %w", err)
	}
	fmt.Println("Pushover notification sent.")

	return nil
}
