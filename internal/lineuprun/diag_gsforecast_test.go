//go:build diag

package lineuprun

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/schedule"
)

// TestDiagGSForecastAgainstLiveSchedule measures buildGSForecast against the
// real MLB statsapi for the remaining days of a matchup week, and reports the
// per-club partition it produced. Never runs in CI; this is the harness that
// reproduced rosterbot-1jvj and verifies the fix against production inputs.
//
// The roster is supplied as club abbreviations rather than fetched from
// Fantrax, so the check needs no credentials and no chromedp login:
//
//	DIAG_SP_TEAMS=ARI,BAL,CHW,CIN,KC,MIA,STL,TEX,WSH \
//	DIAG_WEEK_END=2026-08-23 \
//	go test -tags diag -run TestDiagGSForecast -v ./internal/lineuprun/
func TestDiagGSForecastAgainstLiveSchedule(t *testing.T) {
	teamsCSV := os.Getenv("DIAG_SP_TEAMS")
	if teamsCSV == "" {
		t.Skip("set DIAG_SP_TEAMS=ARI,BAL,... to run")
	}
	weekEnd, perr := time.Parse("2006-01-02", os.Getenv("DIAG_WEEK_END"))
	if perr != nil {
		t.Fatalf("DIAG_WEEK_END must be YYYY-MM-DD: %v", perr)
	}

	spNames := map[string]fantrax.Player{}
	for i, club := range strings.Split(teamsCSV, ",") {
		club = strings.TrimSpace(club)
		name := "Diag Pitcher " + string(rune('A'+i))
		spNames[projections.NormalizeName(name)] = fantrax.Player{
			ID: club, Name: name, MLBTeam: club, Status: "Active", PosShortNames: "SP",
		}
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	sched := schedule.NewClient()

	forecast, err := buildGSForecast(sched, spNames, 6, today, weekEnd,
		func(fantrax.Player) float64 { return 12 })
	if err != nil {
		t.Fatalf("forecast failed: %v", err)
	}

	var total float64
	for _, f := range forecast {
		// Re-derive the partition for the report; buildGSForecast keeps only
		// the totals.
		playing, _ := sched.TeamsPlayingOn(f.Date)
		probs, _ := sched.ProbableStarters(f.Date)
		announced := map[string]bool{}
		for _, tm := range probs {
			announced[tm] = true
		}
		var play, unann int
		for _, p := range spNames {
			if playing[p.MLBTeam] {
				play++
				if !announced[p.MLBTeam] {
					unann++
				}
			}
		}
		total += float64(len(f.ConfirmedStarters)) + f.Estimated
		t.Logf("%s  leagueProbables=%-3d ourClubsPlaying=%d unannounced=%d -> confirmed=%d estimated=%.2f",
			f.Date.Format("2006-01-02"), len(probs), play, unann, len(f.ConfirmedStarters), f.Estimated)
	}
	t.Logf("TOTAL FutureDemand = %.2f over %d days", total, len(forecast))
	if total == 0 {
		t.Errorf("future demand is 0.0 across %d remaining days — the forecast is blind", len(forecast))
	}
}
