package gscheck

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
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
	TeamName   string
	GSUsed     int
	Kind       ViolationKind
	Deductions []fantrax.PitcherStart // top N highest-scoring starts to deduct (ViolationMax only)
}

// BuildReport creates the notification content for GS violations.
// Returns a title and an HTML-formatted body suitable for Pushover.
func BuildReport(violations []Violation, periodLabel string, gsMax, gsMin int) (title, body string) {
	title = fmt.Sprintf("GS Alert — %s", periodLabel)

	var limParts []string
	if gsMax > 0 {
		limParts = append(limParts, fmt.Sprintf("max %d", gsMax))
	}
	if gsMin > 0 {
		limParts = append(limParts, fmt.Sprintf("min %d", gsMin))
	}

	var overLines, underLines []string
	for _, v := range violations {
		switch v.Kind {
		case ViolationMax:
			line := fmt.Sprintf("  %s — <b>%d GS</b> (+%d)", v.TeamName, v.GSUsed, v.GSUsed-gsMax)
			if len(v.Deductions) > 0 {
				var parts []string
				for _, d := range v.Deductions {
					parts = append(parts, fmt.Sprintf("%s (%.1f pts)", d.PitcherName, d.FPts))
				}
				line += fmt.Sprintf("\n    Deduct: %s", strings.Join(parts, ", "))
			}
			overLines = append(overLines, line)
		case ViolationMin:
			underLines = append(underLines, fmt.Sprintf("  %s — <b>%d GS</b> (-%d)", v.TeamName, v.GSUsed, gsMin-v.GSUsed))
		}
	}

	var sections []string
	if len(overLines) > 0 {
		sections = append(sections, fmt.Sprintf("<b>Over Max (%d):</b>\n%s", gsMax, strings.Join(overLines, "\n")))
	}
	if len(underLines) > 0 {
		sections = append(sections, fmt.Sprintf("<b>Under Min (%d):</b>\n%s", gsMin, strings.Join(underLines, "\n")))
	}

	body = fmt.Sprintf("%d violation(s) · %s\n\n%s", len(violations), strings.Join(limParts, ", "), strings.Join(sections, "\n\n"))

	return
}

type teamGS struct {
	id     string
	name   string
	gs     int
	starts []fantrax.PitcherStart
}

// RunGSCheck checks all teams for GS violations in the most recent scoring period.
func RunGSCheck(ft *fantrax.Client, cfg config.Config) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	fmt.Printf("Running GS check for date: %s\n", today.Format("2006-01-02"))

	fmt.Println("Fetching scoring periods and teams...")
	periods, teamMap, _, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return fmt.Errorf("fetch scoring periods: %w", err)
	}
	if len(periods) == 0 {
		return fmt.Errorf("no scoring periods found")
	}

	period := fantrax.FindJustEndedPeriod(periods, today)
	if period == nil {
		fmt.Println("Yesterday was not the end of a scoring period. Nothing to check.")
		return nil
	}

	// Prefer the real Fantrax-configured min/max for this period over the
	// static GS_MAX/GS_MIN env vars — Fantrax scales the real min/max
	// whenever a period spans more than one calendar week (season opener,
	// All-Star break), which a flat env var can't express. Falls back to
	// the configured values if the live fetch fails for any reason.
	gsMax, gsMin := cfg.GSMax, cfg.GSMin
	if liveMin, liveMax, gerr := ft.GetGSLimits(cfg.TeamID, period.Number); gerr != nil {
		fmt.Printf("WARNING: live GS limit fetch failed (%v) — using configured GS_MAX=%d/GS_MIN=%d\n", gerr, cfg.GSMax, cfg.GSMin)
	} else {
		if liveMax != nil {
			gsMax = *liveMax
		} else {
			fmt.Printf("WARNING: no GS max configured for period %d — using configured GS_MAX=%d\n", period.Number, cfg.GSMax)
		}
		if liveMin != nil {
			gsMin = *liveMin
		} else if cfg.GSMin > 0 {
			fmt.Printf("WARNING: no GS min configured for period %d — using configured GS_MIN=%d\n", period.Number, cfg.GSMin)
		}
	}

	periodLabel := fmt.Sprintf("%s (%s – %s)", period.Caption, period.StartDate.Format("2006-01-02"), period.EndDate.Format("2006-01-02"))
	fmt.Printf("Checking: %s\n", periodLabel)
	fmt.Printf("GS max: %d\n", gsMax)
	if gsMin > 0 {
		fmt.Printf("GS min: %d\n", gsMin)
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
		if cfg.DryRun {
			fmt.Printf("  --- %s (per-day GS deltas) ---\n", teamName)
		}
		gs, starts, err := ft.GetTeamGS(teamID, teamName, *period, seasonStart, today, gsMax, cfg.DryRun)
		if err != nil {
			fmt.Printf("WARNING: failed to get GS for %s: %v\n", teamName, err)
			continue
		}
		fmt.Printf("  %s: %d GS\n", teamName, gs)
		results = append(results, teamGS{id: teamID, name: teamName, gs: gs, starts: starts})
		time.Sleep(500 * time.Millisecond)
	}

	// Min violations are only meaningful once the period is complete; suppress
	// them mid-week so an in-progress period doesn't generate false alerts.
	periodComplete := period.EndDate.Before(today)

	// Find violations.
	var violations []Violation
	for _, r := range results {
		if r.gs > gsMax {
			v := Violation{TeamName: r.name, GSUsed: r.gs, Kind: ViolationMax}
			// Deduct the N highest-scoring starts where N = overage.
			overage := r.gs - gsMax
			if len(r.starts) > 0 {
				sorted := make([]fantrax.PitcherStart, len(r.starts))
				copy(sorted, r.starts)
				sort.Slice(sorted, func(i, j int) bool { return sorted[i].FPts > sorted[j].FPts })
				if overage > len(sorted) {
					overage = len(sorted)
				}
				v.Deductions = sorted[:overage]
			}
			violations = append(violations, v)
		}
		if periodComplete && gsMin > 0 && r.gs < gsMin {
			violations = append(violations, Violation{TeamName: r.name, GSUsed: r.gs, Kind: ViolationMin})
		}
	}

	lineupapi.RecordOutput("gs-check", toWireResult(violations, periodLabel, gsMax, gsMin))

	// Print report.
	sort.Slice(results, func(i, j int) bool { return results[i].gs > results[j].gs })
	fmt.Printf("\n--- GS Report: %s (max=%d", periodLabel, gsMax)
	if gsMin > 0 {
		fmt.Printf(", min=%d", gsMin)
	}
	fmt.Println(") ---")
	for _, r := range results {
		flag := ""
		if r.gs > gsMax {
			flag = " *** OVER MAX ***"
		} else if periodComplete && gsMin > 0 && r.gs < gsMin {
			flag = " *** UNDER MIN ***"
		}
		fmt.Printf("  %s: %d GS%s\n", r.name, r.gs, flag)
	}

	if len(violations) == 0 {
		fmt.Println("\nNo violations found.")
		return nil
	}

	fmt.Printf("\n%d violation(s) found.\n", len(violations))
	_, shortSummary := BuildReport(violations, periodLabel, gsMax, gsMin)

	if cfg.DryRun {
		fmt.Println("\n[DRY RUN] Would send Pushover notification:")
		fmt.Printf("  %s\n", shortSummary)
		return nil
	}

	// Send Pushover notification.
	if err := notify.SendPushover(cfg.PushoverGroupKey, cfg.PushoverAPIToken, "Fantrax GS Alert", shortSummary); err != nil {
		return fmt.Errorf("send pushover: %w", err)
	}
	fmt.Println("Pushover notification sent.")

	return nil
}
