package sleeper

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLeagueParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/league/1312135439800356864" {
			t.Errorf("path = %q, want /league/1312135439800356864", r.URL.Path)
		}
		w.Write([]byte(`{
			"league_id": "1312135439800356864",
			"name": "Palm Trees & Promethazine",
			"season": "2026",
			"status": "in_season",
			"total_rosters": 12,
			"roster_positions": ["QB", "RB", "RB", "WR", "WR", "TE", "FLEX", "SUPER_FLEX", "BN"],
			"previous_league_id": "999",
			"settings": {"draft_rounds": 5}
		}`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	lg, err := c.League("1312135439800356864")
	if err != nil {
		t.Fatalf("League: %v", err)
	}
	if lg.Name != "Palm Trees & Promethazine" {
		t.Errorf("Name = %q, want Palm Trees & Promethazine", lg.Name)
	}
	if lg.TotalRosters != 12 {
		t.Errorf("TotalRosters = %d, want 12", lg.TotalRosters)
	}
	if len(lg.RosterPositions) != 9 {
		t.Errorf("RosterPositions len = %d, want 9", len(lg.RosterPositions))
	}
	if lg.Settings["draft_rounds"] != 5 {
		t.Errorf("Settings[draft_rounds] = %d, want 5", lg.Settings["draft_rounds"])
	}
}

func TestLeagueErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	if _, err := c.League("nope"); err == nil {
		t.Fatal("League: expected error on 404, got nil")
	}
}

func TestRostersParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/league/lg1/rosters" {
			t.Errorf("path = %q, want /league/lg1/rosters", r.URL.Path)
		}
		w.Write([]byte(`[
			{"roster_id": 1, "owner_id": "u1", "league_id": "lg1", "players": ["4984", "9509"], "starters": ["4984"], "reserve": [], "taxi": ["9509"]},
			{"roster_id": 2, "owner_id": "u2", "league_id": "lg1", "players": ["1234"], "starters": ["1234"]}
		]`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	rosters, err := c.Rosters("lg1")
	if err != nil {
		t.Fatalf("Rosters: %v", err)
	}
	if len(rosters) != 2 {
		t.Fatalf("len(rosters) = %d, want 2", len(rosters))
	}
	if rosters[0].OwnerID != "u1" || len(rosters[0].Players) != 2 || rosters[0].Taxi[0] != "9509" {
		t.Errorf("rosters[0] = %+v, unexpected shape", rosters[0])
	}
}

func TestUsersParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/league/lg1/users" {
			t.Errorf("path = %q, want /league/lg1/users", r.URL.Path)
		}
		w.Write([]byte(`[
			{"user_id": "u1", "username": "jon", "display_name": "Jon", "metadata": {"team_name": "Palm Trees"}}
		]`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	users, err := c.Users("lg1")
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if len(users) != 1 || users[0].Metadata.TeamName != "Palm Trees" {
		t.Errorf("users = %+v, unexpected shape", users)
	}
}

func TestTradedPicksParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/league/lg1/traded_picks" {
			t.Errorf("path = %q, want /league/lg1/traded_picks", r.URL.Path)
		}
		w.Write([]byte(`[
			{"season": "2027", "round": 1, "roster_id": 3, "previous_owner_id": 3, "owner_id": 7}
		]`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	picks, err := c.TradedPicks("lg1")
	if err != nil {
		t.Fatalf("TradedPicks: %v", err)
	}
	if len(picks) != 1 || picks[0].OwnerID != 7 || picks[0].Season != "2027" {
		t.Errorf("picks = %+v, unexpected shape", picks)
	}
}

func TestTransactionsParsesResponseAndWeekPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/league/lg1/transactions/3" {
			t.Errorf("path = %q, want /league/lg1/transactions/3", r.URL.Path)
		}
		w.Write([]byte(`[
			{"transaction_id": "t1", "type": "trade", "status": "complete", "roster_ids": [1, 2],
			 "adds": {"4984": 1}, "drops": {"1234": 2},
			 "draft_picks": [{"season": "2027", "round": 1, "roster_id": 1, "previous_owner_id": 1, "owner_id": 2}],
			 "waiver_budget": [{"sender": 2, "receiver": 1, "amount": 15}],
			 "created": 1723334400000}
		]`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	txns, err := c.Transactions("lg1", 3)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txns) != 1 || txns[0].Type != "trade" || txns[0].Adds["4984"] != 1 || len(txns[0].DraftPicks) != 1 {
		t.Errorf("txns = %+v, unexpected shape", txns)
	}
	if len(txns[0].WaiverBudget) != 1 || txns[0].WaiverBudget[0].Sender != 2 || txns[0].WaiverBudget[0].Receiver != 1 || txns[0].WaiverBudget[0].Amount != 15 {
		t.Errorf("WaiverBudget = %+v, unexpected shape", txns[0].WaiverBudget)
	}
}

func TestStateParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/state/nfl" {
			t.Errorf("path = %q, want /state/nfl", r.URL.Path)
		}
		w.Write([]byte(`{"week": 1, "season": "2026", "season_type": "pre", "leg": 1}`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	st, err := c.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.Week != 1 || st.SeasonType != "pre" {
		t.Errorf("state = %+v, unexpected shape", st)
	}
}

func TestPlayersNFLParsesResponseAndCaches(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/players/nfl" {
			t.Errorf("path = %q, want /players/nfl", r.URL.Path)
		}
		w.Write([]byte(`{
			"4984": {"player_id": "4984", "first_name": "Josh", "last_name": "Allen", "position": "QB", "team": "BUF", "fantasy_positions": ["QB"], "search_rank": 42}
		}`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	c := NewClient()
	c.CacheDir = t.TempDir()

	players, err := c.PlayersNFL()
	if err != nil {
		t.Fatalf("PlayersNFL: %v", err)
	}
	if p, ok := players["4984"]; !ok || p.LastName != "Allen" || p.SearchRank != 42 {
		t.Errorf("players[4984] = %+v, ok=%v, unexpected shape", p, ok)
	}

	// Second call within TTL must hit the cache, never the server.
	if _, err := c.PlayersNFL(); err != nil {
		t.Fatalf("PlayersNFL (cached): %v", err)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want 1 (second call should be cached)", calls)
	}
}
