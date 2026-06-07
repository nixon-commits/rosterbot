package recap

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/waivers"
	"github.com/pmurley/go-fantrax/models"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestParseIP(t *testing.T) {
	cases := map[string]float64{
		"":      0,
		"45":    45,
		"45.0":  45,
		"45.1":  45 + 1.0/3,
		"45.2":  45 + 2.0/3,
		"100.1": 100 + 1.0/3,
	}
	for in, want := range cases {
		if got := parseIP(in); !approx(got, want) {
			t.Errorf("parseIP(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFIPAndConstant(t *testing.T) {
	// Two pitchers; constant makes pool aggregate FIP == aggregate ERA.
	stats := map[int]pitchSeason{
		1: {IP: 60, HR: 6, BB: 18, HBP: 2, SO: 70, ER: 20},
		2: {IP: 50, HR: 10, BB: 25, HBP: 3, SO: 40, ER: 35},
	}
	c := fipConstant(stats)

	// Aggregate FIP using the derived constant should equal aggregate ERA.
	var ip, er, core float64
	for _, s := range stats {
		ip += s.IP
		er += s.ER
		core += 13*s.HR + 3*(s.BB+s.HBP) - 2*s.SO
	}
	lgERA := er * 9 / ip
	aggFIP := core/ip + c
	if !approx(aggFIP, lgERA) {
		t.Errorf("aggregate FIP %v != aggregate ERA %v (constant %v)", aggFIP, lgERA, c)
	}

	// Pitcher 1 (more Ks, fewer HR) should out-FIP pitcher 2.
	if fip(stats[1], c) >= fip(stats[2], c) {
		t.Errorf("expected pitcher 1 FIP < pitcher 2: %v vs %v", fip(stats[1], c), fip(stats[2], c))
	}
}

func TestComputeFIPLeaders(t *testing.T) {
	rostered := []models.PoolPlayer{
		{Name: "Ace One", Positions: []string{"015"}, FantasyTeamID: "t1", FantasyTeamName: "Team1", MLBTeamShortName: "LAD"},
		{Name: "Mid Two", Positions: []string{"015"}, FantasyTeamID: "t2", FantasyTeamName: "Team2"},
		{Name: "Tiny Sample", Positions: []string{"016"}, FantasyTeamID: "t3", FantasyTeamName: "Team3"},
		{Name: "Some Hitter", Positions: []string{"012"}, FantasyTeamID: "t4", FantasyTeamName: "Team4"},
	}
	resolved := &playername.ResolvedPlayers{ByName: map[string]int{
		playername.Normalize("Ace One"):     1,
		playername.Normalize("Mid Two"):     2,
		playername.Normalize("Tiny Sample"): 3,
	}}
	stats := map[int]pitchSeason{
		1: {IP: 70, HR: 5, BB: 15, HBP: 1, SO: 90, ER: 18},
		2: {IP: 65, HR: 14, BB: 30, HBP: 4, SO: 45, ER: 40},
		3: {IP: 10, HR: 0, BB: 2, HBP: 0, SO: 18, ER: 1}, // below fipMinIP → excluded
	}
	leaders := computeFIPLeaders(rostered, resolved, stats, 5)
	if len(leaders) != 2 {
		t.Fatalf("want 2 FIP leaders (minIP filter drops tiny sample + hitter excluded), got %d", len(leaders))
	}
	if leaders[0].Name != "Ace One" {
		t.Errorf("expected Ace One first (lowest FIP), got %q", leaders[0].Name)
	}
	if leaders[0].Value >= leaders[1].Value {
		t.Errorf("FIP leaders not ascending: %v then %v", leaders[0].Value, leaders[1].Value)
	}
	if leaders[0].OwnerTeamID != "t1" || leaders[0].MLBTeam != "LAD" {
		t.Errorf("owner/mlb attribution wrong: %+v", leaders[0])
	}
}

func TestComputeWOBALeaders(t *testing.T) {
	rostered := []models.PoolPlayer{
		{Name: "Masher", Positions: []string{"012"}, FantasyTeamID: "t1", FantasyTeamName: "Team1"},
		{Name: "Average Joe", Positions: []string{"002"}, FantasyTeamID: "t2", FantasyTeamName: "Team2"},
		{Name: "Unqualified", Positions: []string{"005"}, FantasyTeamID: "t3", FantasyTeamName: "Team3"},
		{Name: "Just A Pitcher", Positions: []string{"015"}, FantasyTeamID: "t4", FantasyTeamName: "Team4"},
	}
	resolved := &playername.ResolvedPlayers{ByName: map[string]int{
		playername.Normalize("Masher"):         10,
		playername.Normalize("Average Joe"):    11,
		playername.Normalize("Just A Pitcher"): 12,
	}}
	savant := &waivers.SavantBundle{HitterExp: map[int]waivers.SavantHitterRow{
		10: {MLBAMID: 10, WOBA: 0.410},
		11: {MLBAMID: 11, WOBA: 0.330},
		12: {MLBAMID: 12, WOBA: 0.150}, // pitcher hitting — excluded (isHitter false)
	}}
	leaders := computeWOBALeaders(rostered, resolved, savant, 5)
	if len(leaders) != 2 {
		t.Fatalf("want 2 wOBA leaders, got %d", len(leaders))
	}
	if leaders[0].Name != "Masher" || !approx(leaders[0].Value, 0.410) {
		t.Errorf("expected Masher .410 first, got %+v", leaders[0])
	}
}

func TestRenderLeadersSection(t *testing.T) {
	r := &Recap{
		Season:    2026,
		WeekLabel: "Week 9",
		StartDate: time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC),
		LogoURLs:  map[string]string{"t1": "https://example.com/logo.png"},
		Awards: Awards{
			WOBALeaders: []LeaderLine{{Name: "Aaron Judge", MLBTeam: "NYY", OwnerTeam: "jimmydyl", OwnerTeamID: "t1", Value: 0.450}},
			FIPLeaders:  []LeaderLine{{Name: "Tarik Skubal", MLBTeam: "DET", OwnerTeam: "DillonP33", OwnerTeamID: "t2", Value: 2.31}},
		},
	}
	var buf bytes.Buffer
	if err := Render(&buf, r); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{"League Leaders", ">wOBA<", ">FIP<", "Aaron Judge", ".450", "Tarik Skubal", "2.31", "logo.png"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

func TestFetchSeasonPitching(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"people":[
			{"id":1,"stats":[{"splits":[{"stat":{"inningsPitched":"60.2","homeRuns":6,"baseOnBalls":18,"hitBatsmen":2,"strikeOuts":70,"earnedRuns":20}}]}]},
			{"id":2,"stats":[{"splits":[{"stat":{"inningsPitched":"50.0","homeRuns":10,"baseOnBalls":25,"hitBatsmen":3,"strikeOuts":40,"earnedRuns":35}}]}]}
		]}`))
	}))
	defer srv.Close()
	orig := mlbSeasonPitchingURL
	mlbSeasonPitchingURL = srv.URL + "?ids=%s&season=%d"
	defer func() { mlbSeasonPitchingURL = orig }()

	stats, err := fetchSeasonPitching([]int{1, 2}, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("want 2 pitchers, got %d", len(stats))
	}
	if !approx(stats[1].IP, 60+2.0/3) || stats[1].SO != 70 {
		t.Errorf("pitcher 1 parsed wrong: %+v", stats[1])
	}
}
