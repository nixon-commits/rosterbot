package schedule

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTeamsPlayingOn_ParsesResponse(t *testing.T) {
	fixture := map[string]interface{}{
		"dates": []map[string]interface{}{
			{
				"games": []map[string]interface{}{
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "NYY"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "BOS"},
							},
						},
					},
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "LAD"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "SF"},
							},
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	// Patch the URL constant via a test-friendly helper.
	origURL := mlbScheduleURL
	mlbScheduleURL = srv.URL + "?date=%s"
	defer func() { mlbScheduleURL = origURL }()

	c := NewClient()
	playing, err := c.TeamsPlayingOn(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, team := range []string{"NYY", "BOS", "LAD", "SF"} {
		if !playing[team] {
			t.Errorf("expected %s to be playing", team)
		}
	}
	if playing["COL"] {
		t.Error("COL should not be playing")
	}
}

func TestGameVenues_ParsesResponse(t *testing.T) {
	fixture := map[string]interface{}{
		"dates": []map[string]interface{}{
			{
				"games": []map[string]interface{}{
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "NYY"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "BOS"},
							},
						},
					},
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "LAD"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "SF"},
							},
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbScheduleURL
	mlbScheduleURL = srv.URL + "?date=%s"
	defer func() { mlbScheduleURL = origURL }()

	c := NewClient()
	venues, err := c.GameVenues(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// NYY is away at BOS.
	if venues["NYY"] != "BOS" {
		t.Errorf("expected NYY venue=BOS, got %s", venues["NYY"])
	}
	// BOS is home.
	if venues["BOS"] != "BOS" {
		t.Errorf("expected BOS venue=BOS, got %s", venues["BOS"])
	}
	// LAD is away at SF.
	if venues["LAD"] != "SF" {
		t.Errorf("expected LAD venue=SF, got %s", venues["LAD"])
	}
	// SF is home.
	if venues["SF"] != "SF" {
		t.Errorf("expected SF venue=SF, got %s", venues["SF"])
	}
	// COL not playing.
	if _, ok := venues["COL"]; ok {
		t.Error("COL should not have a venue entry")
	}
}

func TestLockedTeams_LiveAndFinal(t *testing.T) {
	fixture := map[string]interface{}{
		"dates": []map[string]interface{}{
			{
				"games": []map[string]interface{}{
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "NYY"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "BOS"},
							},
						},
						"status": map[string]interface{}{
							"abstractGameState": "Live",
						},
					},
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "LAD"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "SF"},
							},
						},
						"status": map[string]interface{}{
							"abstractGameState": "Final",
						},
					},
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "CHC"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "MIL"},
							},
						},
						"status": map[string]interface{}{
							"abstractGameState": "Preview",
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbScheduleURL
	mlbScheduleURL = srv.URL + "?date=%s"
	defer func() { mlbScheduleURL = origURL }()

	c := NewClient()
	locked, err := c.LockedTeams(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Live game: NYY @ BOS should be locked.
	for _, team := range []string{"NYY", "BOS"} {
		if !locked[team] {
			t.Errorf("expected %s to be locked (Live)", team)
		}
	}
	// Final game: LAD @ SF should be locked.
	for _, team := range []string{"LAD", "SF"} {
		if !locked[team] {
			t.Errorf("expected %s to be locked (Final)", team)
		}
	}
	// Preview game: CHC @ MIL should NOT be locked.
	for _, team := range []string{"CHC", "MIL"} {
		if locked[team] {
			t.Errorf("expected %s NOT to be locked (Preview)", team)
		}
	}
}

func TestTeamsPlayingOn_EmptySchedule(t *testing.T) {
	fixture := map[string]interface{}{"dates": []interface{}{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbScheduleURL
	mlbScheduleURL = srv.URL + "?date=%s"
	defer func() { mlbScheduleURL = origURL }()

	c := NewClient()
	playing, err := c.TeamsPlayingOn(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(playing) != 0 {
		t.Errorf("expected empty map, got %v", playing)
	}
}

func TestBenchedPlayers_LineupsPosted(t *testing.T) {
	fixture := map[string]interface{}{
		"dates": []map[string]interface{}{
			{
				"games": []map[string]interface{}{
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "NYY"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "BOS"},
							},
						},
						"lineups": map[string]interface{}{
							"awayPlayers": []map[string]interface{}{
								{"fullName": "Aaron Judge"},
								{"fullName": "Juan Soto"},
							},
							"homePlayers": []map[string]interface{}{
								{"fullName": "Rafael Devers"},
								{"fullName": "Masataka Yoshida"},
							},
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbLineupsURL
	mlbLineupsURL = srv.URL + "?date=%s"
	defer func() { mlbLineupsURL = origURL }()

	roster := map[string]string{
		"aaron judge":      "NYY",
		"anthony volpe":    "NYY", // not in lineup
		"rafael devers":    "BOS",
		"masataka yoshida": "BOS",
	}

	c := NewClient()
	benched, err := c.BenchedPlayers(time.Now(), roster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !benched["anthony volpe"] {
		t.Error("expected anthony volpe to be benched (not in lineup)")
	}
	if benched["aaron judge"] {
		t.Error("aaron judge should NOT be benched (in lineup)")
	}
	if benched["rafael devers"] {
		t.Error("rafael devers should NOT be benched (in lineup)")
	}
}

func TestBenchedPlayers_LineupsNotPosted(t *testing.T) {
	// Game exists but lineups field is null (not yet posted).
	fixture := map[string]interface{}{
		"dates": []map[string]interface{}{
			{
				"games": []map[string]interface{}{
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "NYY"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "BOS"},
							},
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbLineupsURL
	mlbLineupsURL = srv.URL + "?date=%s"
	defer func() { mlbLineupsURL = origURL }()

	roster := map[string]string{
		"aaron judge":   "NYY",
		"anthony volpe": "NYY",
	}

	c := NewClient()
	benched, err := c.BenchedPlayers(time.Now(), roster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(benched) != 0 {
		t.Errorf("expected no benched players when lineups not posted, got %v", benched)
	}
}

func TestBenchedPlayers_EmptyLineups(t *testing.T) {
	// Lineups field exists but with empty player lists (not yet posted).
	fixture := map[string]interface{}{
		"dates": []map[string]interface{}{
			{
				"games": []map[string]interface{}{
					{
						"teams": map[string]interface{}{
							"away": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "NYY"},
							},
							"home": map[string]interface{}{
								"team": map[string]interface{}{"abbreviation": "BOS"},
							},
						},
						"lineups": map[string]interface{}{
							"awayPlayers": []map[string]interface{}{},
							"homePlayers": []map[string]interface{}{},
						},
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbLineupsURL
	mlbLineupsURL = srv.URL + "?date=%s"
	defer func() { mlbLineupsURL = origURL }()

	roster := map[string]string{
		"aaron judge": "NYY",
	}

	c := NewClient()
	benched, err := c.BenchedPlayers(time.Now(), roster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(benched) != 0 {
		t.Errorf("expected no benched players with empty lineups, got %v", benched)
	}
}

func TestGameIsDone(t *testing.T) {
	cases := []struct {
		abstract, detailed string
		want               bool
	}{
		{"Final", "Final", true},
		{"Live", "In Progress", false},
		{"Preview", "Scheduled", false},
		{"Preview", "Postponed", true},
		{"Preview", "Cancelled", true},
		{"Live", "Suspended: Rain", false}, // resumes later → not done
	}
	for _, c := range cases {
		if got := gameIsDone(c.abstract, c.detailed); got != c.want {
			t.Errorf("gameIsDone(%q,%q)=%v want %v", c.abstract, c.detailed, got, c.want)
		}
	}
}

func TestAllGamesFinalOn(t *testing.T) {
	serve := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}))
	}

	// All final → done.
	srv := serve(`{"dates":[{"games":[
		{"status":{"abstractGameState":"Final","detailedState":"Final"}},
		{"status":{"abstractGameState":"Final","detailedState":"Final"}}
	]}]}`)
	mlbScheduleURL = srv.URL + "?date=%s"
	if done, err := NewClient().AllGamesFinalOn(time.Now()); err != nil || !done {
		t.Errorf("all-final: want true, got %v (err %v)", done, err)
	}
	srv.Close()

	// One still live → not done.
	srv = serve(`{"dates":[{"games":[
		{"status":{"abstractGameState":"Final","detailedState":"Final"}},
		{"status":{"abstractGameState":"Live","detailedState":"In Progress"}}
	]}]}`)
	mlbScheduleURL = srv.URL + "?date=%s"
	if done, err := NewClient().AllGamesFinalOn(time.Now()); err != nil || done {
		t.Errorf("one-live: want false, got %v (err %v)", done, err)
	}
	srv.Close()

	// No games scheduled → vacuously done.
	srv = serve(`{"dates":[]}`)
	mlbScheduleURL = srv.URL + "?date=%s"
	if done, err := NewClient().AllGamesFinalOn(time.Now()); err != nil || !done {
		t.Errorf("no-games: want true, got %v (err %v)", done, err)
	}
	srv.Close()
}
