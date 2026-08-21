package gscheck

import (
	"context"
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
	ViolationMax ViolationKind = iota // exceeded the period's GS max
	ViolationMin                      // below the period's GS min
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
//
// unchecked names the teams whose GS could not be fetched. It is rendered into
// the summary line rather than a trailing section because that line is what
// survives Pushover's 1024-char truncation, and a violation count that silently
// covers 9 of 10 teams is exactly the thing this report must never imply
// (rosterbot-xit).
func BuildReport(violations []Violation, periodLabel string, gsMax, gsMin int, unchecked []string) (title, body string) {
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

	coverage := ""
	if len(unchecked) > 0 {
		coverage = fmt.Sprintf(" · <b>%d team(s) NOT CHECKED</b>: %s", len(unchecked), strings.Join(unchecked, ", "))
	}

	body = fmt.Sprintf("%d violation(s) · %s%s\n\n%s", len(violations), strings.Join(limParts, ", "), coverage, strings.Join(sections, "\n\n"))

	return
}

type teamGS struct {
	id     string
	name   string
	gs     int
	starts []fantrax.PitcherStart
}

// RunGSCheck checks all teams for GS violations in the most recent scoring period.
// GSCheckClient is the narrow subset of *fantrax.Client that RunGSCheck needs,
// isolated for testability (mirrors waivers.FantraxClient). *fantrax.Client
// satisfies it implicitly — internal/fantrax is not modified.
type GSCheckClient interface {
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
	GetGSLimits(teamID string, period fantrax.WeeklyPeriod) (min, max *int, err error)
	GetTeamGS(teamID, teamName string, sp fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []fantrax.PitcherStart, error)
}

// teamFetchAttempts bounds the per-team retry. The 2026-08-17 drop was
// transient — the same command re-run minutes later returned the missing team —
// so a couple of cheap retries absorb the common case rather than escalating it.
const teamFetchAttempts = 3

// teamFetchBackoff is the base delay between per-team retries; attempt N waits
// N × this. A var so tests can zero it, matching how the schedule URLs are
// overridden elsewhere in the tree.
var teamFetchBackoff = 750 * time.Millisecond

// skippedTeam records a team whose GS could not be fetched, so a partial tally
// can be reported as partial instead of being silently dropped from results.
type skippedTeam struct {
	name string
	err  error
}

// fetchTeamGS retries a per-team GS fetch a bounded number of times before
// giving up. The tally loop already paces itself at 500ms per team, so over ten
// teams the retries cost at most a second or two — cheap against the
// alternative, which is a report that quietly omits a team.
func fetchTeamGS(ctx context.Context, ft GSCheckClient, teamID, teamName string, period fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, dryRun bool) (int, []fantrax.PitcherStart, error) {
	var err error
	for attempt := 1; attempt <= teamFetchAttempts; attempt++ {
		var (
			gs     int
			starts []fantrax.PitcherStart
		)
		gs, starts, err = ft.GetTeamGS(teamID, teamName, period, seasonStart, today, gsMax, dryRun)
		if err == nil {
			return gs, starts, nil
		}
		if attempt == teamFetchAttempts {
			break
		}
		fmt.Printf("  WARNING: GS fetch for %s failed (attempt %d/%d): %v — retrying\n", teamName, attempt, teamFetchAttempts, err)
		select {
		case <-ctx.Done():
			return 0, nil, fmt.Errorf("%w (retry abandoned: %v)", err, ctx.Err())
		case <-time.After(time.Duration(attempt) * teamFetchBackoff):
		}
	}
	return 0, nil, err
}

// coverageErr turns an incomplete tally into a run failure. It is returned at
// every terminal exit *after* the report and the notifications have gone out:
// the violations we did find are still worth delivering, and aborting early
// would trade a visible gap for an invisible one — rosterbot-chs's direction,
// degrade to noise rather than to silence.
//
// The error is what makes the gap visible to infrastructure. gs-check otherwise
// exits 0, the S3 run ledger records SUCCESS, and neither opsalert's streak nor
// its heartbeat can tell a 9-team report from a 10-team one (rosterbot-xit).
func coverageErr(skipped []skippedTeam) error {
	if len(skipped) == 0 {
		return nil
	}
	names := make([]string, 0, len(skipped))
	for _, sk := range skipped {
		names = append(names, sk.name)
	}
	return fmt.Errorf("incomplete GS check: %d team(s) could not be fetched: %s", len(skipped), strings.Join(names, ", "))
}

func RunGSCheck(ctx context.Context, ft GSCheckClient, cfg config.Config) error {
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

	// The real GS min/max come straight from Fantrax's own per-period
	// configuration — it scales the limit whenever a period spans more than
	// one calendar week (season opener, All-Star break), which a flat
	// constant can't express. There's no static fallback: if the live fetch
	// fails, or Fantrax has no GS max configured for this period, there's
	// nothing to check against, so alert (on a real fetch error) and stop.
	liveMin, liveMax, gerr := ft.GetGSLimits(cfg.TeamID, period.Number)
	if gerr != nil {
		msg := fmt.Sprintf("gs-check: live GS limit fetch failed for period %d (%v) — could not run violation check", period.Number, gerr)
		fmt.Println(msg)
		if cfg.PushoverUserKey != "" && cfg.PushoverAPIToken != "" {
			if perr := notify.SendPushover(cfg.PushoverUserKey, cfg.PushoverAPIToken, "gs-check: GS limit fetch failed", msg); perr != nil {
				fmt.Printf("WARNING: failed to send failure Pushover: %v\n", perr)
			}
		}
		return fmt.Errorf("fetch GS limits: %w", gerr)
	}
	if liveMax == nil {
		fmt.Printf("No GS max configured by Fantrax for period %d — nothing to check.\n", period.Number)
		return nil
	}
	gsMax := *liveMax
	gsMin := 0
	if liveMin != nil {
		gsMin = *liveMin
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
	// The banner reports the PERIOD's span, not today's date. gsPeriodWalk caps
	// the walk at sp.EndDate, so printing today here claimed an 8-day walk over a
	// 7-day period and read as an off-by-one GS overcount to anyone auditing a
	// completed period (rosterbot-6zn). today is still the right argument to pass
	// to GetTeamGS — the walk needs it to bound an in-progress period.
	fmt.Printf("Found %d teams. Tallying GS for Period %d (days %s to %s)...\n",
		len(teamMap), period.Number, period.StartDate.Format("2006-01-02"), period.EndDate.Format("2006-01-02"))

	var (
		results []teamGS
		skipped []skippedTeam
	)
	for teamID, teamName := range teamMap {
		if cfg.DryRun {
			fmt.Printf("  --- %s (per-day GS deltas) ---\n", teamName)
		}
		gs, starts, err := fetchTeamGS(ctx, ft, teamID, teamName, *period, seasonStart, today, gsMax, cfg.DryRun)
		if err != nil {
			fmt.Printf("WARNING: failed to get GS for %s: %v\n", teamName, err)
			skipped = append(skipped, skippedTeam{name: teamName, err: err})
			continue
		}
		fmt.Printf("  %s: %d GS\n", teamName, gs)
		results = append(results, teamGS{id: teamID, name: teamName, gs: gs, starts: starts})
		time.Sleep(500 * time.Millisecond)
	}

	// teamMap iterates in random order, so sort the skips to keep the report and
	// the returned error byte-identical across runs with the same failures.
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].name < skipped[j].name })
	uncheckedNames := make([]string, 0, len(skipped))
	for _, sk := range skipped {
		uncheckedNames = append(uncheckedNames, sk.name)
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
	if len(skipped) > 0 {
		fmt.Printf("\n*** INCOMPLETE: %d of %d team(s) could not be checked ***\n", len(skipped), len(teamMap))
		for _, sk := range skipped {
			fmt.Printf("  %s: %v\n", sk.name, sk.err)
		}
	}

	if len(violations) == 0 {
		fmt.Println("\nNo violations found.")
		return coverageErr(skipped)
	}

	fmt.Printf("\n%d violation(s) found.\n", len(violations))
	_, shortSummary := BuildReport(violations, periodLabel, gsMax, gsMin, uncheckedNames)

	if cfg.DryRun {
		fmt.Println("\n[DRY RUN] Would send Pushover notification:")
		fmt.Printf("  %s\n", shortSummary)
		return coverageErr(skipped)
	}

	// The league broadcast goes FIRST, and the ordering is load-bearing. It
	// reaches managers who are not app users and therefore cannot be ported
	// to APNs — only narrowed, kept, or duplicated (D3). It is duplicated,
	// permanently: anyone on both the Pushover group and the app hears GS
	// alerts twice, which is inherent to keeping the broadcast, not a defect
	// to fix later. Were the dispatcher call first, a transient feed-write
	// failure would abort this function before the broadcast — degrading to
	// silence for the whole league over an S3 hiccup unrelated to Pushover's
	// health. This way the worst case of either half failing is a duplicate
	// on the rerun, never a missed alert (rosterbot-chs's direction).
	if err := notify.SendPushover(cfg.PushoverGroupKey, cfg.PushoverAPIToken, "Fantrax GS Alert", shortSummary); err != nil {
		return fmt.Errorf("send pushover: %w", err)
	}
	fmt.Println("Pushover notification sent.")

	// Dispatcher half: the durable feed record plus APNs to app users. Errors
	// only on a failed feed write — fatal so the run is marked failed and
	// rerun, since the feed record is the durable obligation.
	if err := notify.Send(ctx, notify.Event{Kind: "gs-check", Title: "Fantrax GS Alert", Message: shortSummary}); err != nil {
		return fmt.Errorf("record gs alert: %w", err)
	}

	return coverageErr(skipped)
}
