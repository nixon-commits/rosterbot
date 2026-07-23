package recap

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/models"
)

// fakeSeasonMeanClient returns a fixed day series per team for DailyFantasyPoints.
type fakeSeasonMeanClient struct {
	daysByTeam map[string][]fantrax.DayRoster
}

func (f *fakeSeasonMeanClient) DailyFantasyPoints(teamID string, _, _, _ time.Time, _ string, _ time.Duration) ([]fantrax.DayRoster, error) {
	return f.daysByTeam[teamID], nil
}

func activeDay(d time.Time, activeFPts ...float64) fantrax.DayRoster {
	var ps []fantrax.DayPlayerFP
	for _, fp := range activeFPts {
		ps = append(ps, fantrax.DayPlayerFP{Active: true, HadGame: true, FPts: fp})
	}
	return fantrax.DayRoster{Date: d, Players: ps}
}

// seasonToDateTeamMean sums active FPts per day and divides by played days.
func TestSeasonToDateTeamMean_MeanOfActiveDailyFPts(t *testing.T) {
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	asOf := seasonStart.AddDate(0, 0, 2)
	f := &fakeSeasonMeanClient{daysByTeam: map[string][]fantrax.DayRoster{
		"t1": {
			activeDay(seasonStart, 10, 20),              // day total 30
			activeDay(seasonStart.AddDate(0, 0, 1)),     // no activity → skipped
			activeDay(seasonStart.AddDate(0, 0, 2), 40), // day total 40
		},
	}}
	mean, played, err := seasonToDateTeamMean(f, "t1", seasonStart, asOf, "", 0)
	if err != nil {
		t.Fatalf("seasonToDateTeamMean: %v", err)
	}
	if played != 2 {
		t.Errorf("played = %d, want 2 (empty day skipped)", played)
	}
	if math.Abs(mean-35.0) > 1e-9 { // (30 + 40) / 2
		t.Errorf("mean = %v, want 35", mean)
	}
}

// fetchSeasonMeans returns nil (no HTTP) before season start.
func TestFetchSeasonMeans_PreSeasonNil(t *testing.T) {
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	got := fetchSeasonMeans(&fakeSeasonMeanClient{}, map[string]string{"t1": "Alpha"}, seasonStart, seasonStart.AddDate(0, 0, -1), "", 0, 1)
	if got != nil {
		t.Errorf("pre-season fetchSeasonMeans = %v, want nil", got)
	}
}

func TestFetchSeasonMeans_PerTeamMap(t *testing.T) {
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	asOf := seasonStart.AddDate(0, 0, 1)
	f := &fakeSeasonMeanClient{daysByTeam: map[string][]fantrax.DayRoster{
		"t1": {activeDay(seasonStart, 50)},
		"t2": {activeDay(seasonStart, 10), activeDay(seasonStart.AddDate(0, 0, 1), 30)},
	}}
	got := fetchSeasonMeans(f, map[string]string{"t1": "Alpha", "t2": "Beta"}, seasonStart, asOf, "", 0, 2)
	if math.Abs(got["t1"]-50.0) > 1e-9 {
		t.Errorf("t1 mean = %v, want 50", got["t1"])
	}
	if math.Abs(got["t2"]-20.0) > 1e-9 { // (10 + 30)/2
		t.Errorf("t2 mean = %v, want 20", got["t2"])
	}
}

// fakeLeadersClient drives buildLeaders' fantrax seam.
type fakeLeadersClient struct {
	pool []models.PoolPlayer
	err  error
}

func (f *fakeLeadersClient) GetFullPlayerPool() ([]models.PoolPlayer, error) {
	return f.pool, f.err
}

// buildLeaders soft-fails to nil when no players are rostered (early return,
// before any statcast/statsapi network call).
func TestBuildLeaders_EmptyRosteredNil(t *testing.T) {
	f := &fakeLeadersClient{pool: []models.PoolPlayer{{FantasyTeamID: ""}}} // unrostered
	woba, fip := buildLeaders(f, 2026, time.Now().UTC(), "", 0, 5)
	if woba != nil || fip != nil {
		t.Errorf("want nil leaders for empty rostered pool, got woba=%v fip=%v", woba, fip)
	}
}

func TestBuildLeaders_PoolErrorNil(t *testing.T) {
	f := &fakeLeadersClient{err: errors.New("boom")}
	woba, fip := buildLeaders(f, 2026, time.Now().UTC(), "", 0, 5)
	if woba != nil || fip != nil {
		t.Errorf("want nil leaders on pool error, got woba=%v fip=%v", woba, fip)
	}
}
