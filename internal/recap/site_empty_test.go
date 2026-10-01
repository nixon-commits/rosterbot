package recap

import (
	"errors"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
)

// stubSiteClient knows only the league's scoring periods. RunSite reads those
// first and returns before touching anything else when no week is complete, so
// every other method on this path is unreachable and answers empty.
type stubSiteClient struct{ periods []fantrax.ScoringPeriod }

func (s stubSiteClient) GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error) {
	return s.periods, nil, nil, nil
}
func (stubSiteClient) GetFullPlayerPool() ([]models.PoolPlayer, error) { return nil, nil }
func (stubSiteClient) DailyFantasyPoints(string, time.Time, time.Time, time.Time, string, time.Duration) ([]fantrax.DayRoster, error) {
	return nil, nil
}
func (stubSiteClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, nil
}
func (stubSiteClient) GetActiveSlots() ([]fantrax.Slot, error)               { return nil, nil }
func (stubSiteClient) GetPitcherSlots() ([]fantrax.Slot, error)              { return nil, nil }
func (stubSiteClient) GetAllMatchupEntries() ([]fantrax.MatchupEntry, error) { return nil, nil }
func (stubSiteClient) GetTeamPitcherStarts(string, time.Time, time.Time, time.Time, string, time.Duration) ([]fantrax.DatedPitcherStart, error) {
	return nil, nil
}
func (stubSiteClient) GetPlayoffBracket() (*auth_client.PlayoffBracket, error) { return nil, nil }
func (stubSiteClient) GetStandings() ([]fantrax.StandingRow, error)            { return nil, nil }
func (stubSiteClient) GetRecentTrades(time.Time) ([]models.Transaction, error) { return nil, nil }

// A pre-season run has nothing to render yet, and that is not a failure. The
// season gate used to hide this case behind its START bound; now that
// recap-site is year-round, a bare error here is a FAILED ledger row every
// Monday from the new league's creation until its first week completes, which
// opsalert.leadingFailures turns into a page. RunSite says "nothing yet" with a
// sentinel and lets the caller decide what that means.
func TestRunSite_NoCompletedWeeksIsASentinel(t *testing.T) {
	ft := stubSiteClient{periods: []fantrax.ScoringPeriod{
		{Number: 1, StartDate: ymd("2027-03-29"), EndDate: ymd("2027-04-04")},
	}}

	err := RunSite(t.Context(), ft, SiteOptions{OutDir: t.TempDir(), Today: ymd("2027-02-15")})

	if !errors.Is(err, ErrNoCompletedWeeks) {
		t.Fatalf("err = %v, want it to wrap ErrNoCompletedWeeks", err)
	}
}
