package sleeper

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestUserByNameParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/jnixon" {
			t.Errorf("path = %q, want /user/jnixon", r.URL.Path)
		}
		w.Write([]byte(`{"user_id":"456","username":"jnixon","display_name":"Jon"}`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	u, err := NewClient().UserByName("jnixon")
	if err != nil {
		t.Fatalf("UserByName: %v", err)
	}
	if u.UserID != "456" {
		t.Errorf("UserID = %q, want 456", u.UserID)
	}
	if u.DisplayName != "Jon" {
		t.Errorf("DisplayName = %q, want Jon", u.DisplayName)
	}
}

// Sleeper answers an unknown username with a 200 and a literal `null` body
// rather than a 404, so the status check in get() lets it through and the
// decode succeeds into a zero User. An empty UserID is therefore the only
// reliable signal, and it must not be reported as success — a caller would
// go on to request leagues for the empty string.
func TestUserByNameUnknownUsernameIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`null`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	if _, err := NewClient().UserByName("nobody"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

// The 404 spelling of the same outcome, in case Sleeper changes its mind.
func TestUserByNameNotFoundStatusIsAlsoNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	if _, err := NewClient().UserByName("nobody"); err == nil {
		t.Error("err = nil, want an error for a 404")
	}
}

func TestLeaguesForUserParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/456/leagues/nfl/2026" {
			t.Errorf("path = %q, want /user/456/leagues/nfl/2026", r.URL.Path)
		}
		w.Write([]byte(`[
			{"league_id":"1","name":"Dynasty","season":"2026","total_rosters":12},
			{"league_id":"2","name":"Redraft","season":"2026","total_rosters":10}
		]`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	ls, err := NewClient().LeaguesForUser("456", "nfl", "2026")
	if err != nil {
		t.Fatalf("LeaguesForUser: %v", err)
	}
	if len(ls) != 2 {
		t.Fatalf("len = %d, want 2", len(ls))
	}
	if ls[0].Name != "Dynasty" {
		t.Errorf("Name = %q, want Dynasty", ls[0].Name)
	}
	if ls[1].TotalRosters != 10 {
		t.Errorf("TotalRosters = %d, want 10", ls[1].TotalRosters)
	}
}

// A username with a space must not escape the path and address a different
// endpoint. It is a space rather than a slash because a slash is now refused
// outright before the URL is ever built — see the traversal test below — but
// every other character still has to survive the trip verbatim.
func TestUserByNameEscapesUsername(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`{"user_id":"1"}`))
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	if _, err := NewClient().UserByName("a b"); err != nil {
		t.Fatalf("UserByName: %v", err)
	}
	if gotPath != "/user/a%20b" {
		t.Errorf("path = %q, want /user/a%%20b", gotPath)
	}
}

// A cache key becomes a FILE PATH: internal/cache resolves it as
// <root>/<key>.json, so an argument carrying a path separator reads and writes
// outside the cache root. In Lambda that root is /tmp/sleeper-cache and the
// username arrives from a tenant's query string, which made
// "/../../../../../../tmp/pwned" resolve to /tmp/pwned.json. The read half runs
// unconditionally, before any fetch, so merely calling this was enough.
//
// The handler validates too, but this package must refuse on its own — it is a
// public leaf and the next caller will not know the rule.
func TestUserByNameRejectsPathTraversal(t *testing.T) {
	srv, hits := countingServer(t, `{"user_id":"1","username":"x"}`)
	defer srv.Close()

	outside := t.TempDir()
	c := NewClient()
	c.CacheDir = filepath.Join(outside, "cache")

	for _, username := range []string{"../pwned", "a/b", `a\b`, "/../../pwned"} {
		if _, err := c.UserByName(username); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("UserByName(%q) = %v, want ErrInvalidArgument", username, err)
		}
	}
	assertNoEscape(t, outside, *hits)
}

func TestLeaguesForUserRejectsPathTraversal(t *testing.T) {
	srv, hits := countingServer(t, `[]`)
	defer srv.Close()

	outside := t.TempDir()
	c := NewClient()
	c.CacheDir = filepath.Join(outside, "cache")

	// Every argument is joined into the key, so every one of them is a way out.
	for _, args := range [][3]string{
		{"../pwned", "nfl", "2026"},
		{"456", "../pwned", "2026"},
		{"456", "nfl", "../pwned"},
	} {
		if _, err := c.LeaguesForUser(args[0], args[1], args[2]); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("LeaguesForUser%v = %v, want ErrInvalidArgument", args, err)
		}
	}
	assertNoEscape(t, outside, *hits)
}

// countingServer stands in for Sleeper and counts requests, so a rejected
// argument can be shown to cost no upstream call at all.
func countingServer(t *testing.T, body string) (*httptest.Server, *int) {
	t.Helper()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(body))
	}))
	orig := baseURL
	baseURL = srv.URL
	t.Cleanup(func() { baseURL = orig })
	return srv, &hits
}

func assertNoEscape(t *testing.T, outside string, hits int) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(outside, "pwned.json")); err == nil {
		t.Error("a cache entry was written OUTSIDE the cache root: the key is a file path, " +
			"so a separator in an argument escapes it")
	}
	if hits != 0 {
		t.Errorf("upstream was called %d times for a rejected argument, want 0", hits)
	}
}
