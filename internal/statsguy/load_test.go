package statsguy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadBundleJoinsPlayersAndPicks(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/api/v1/players":
			w.Write([]byte(`{
				"total": 2,
				"players": [
					{"id": "9509", "name": "Bijan Robinson", "team": "ATL", "position": "RB",
					 "value": {"sf_dynasty": 10000, "non_sf_dynasty": 10000, "sf_redraft": 9772, "non_sf_redraft": 9996}},
					{"id": "4984", "name": "Josh Allen", "team": "BUF", "position": "QB",
					 "value": {"sf_dynasty": 9480, "non_sf_dynasty": 5047, "sf_redraft": 9905, "non_sf_redraft": 4306}}
				]
			}`))
		case "/api/v1/picks":
			w.Write([]byte(`{
				"picks": [
					{"id": "pick:2026:1:early", "year": 2026, "round": 1, "variant": "early",
					 "value": {"sf_dynasty": 3748, "non_sf_dynasty": 3783}},
					{"id": "pick:2026:1.01", "year": 2026, "round": 1, "slot": 1,
					 "value": {"sf_dynasty": 5563, "non_sf_dynasty": 5635}}
				]
			}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	bundle, err := LoadBundle(t.Context(), t.TempDir(), CacheTTL)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}

	p, ok := bundle.Players["9509"]
	if !ok {
		t.Fatal("Players[9509] missing")
	}
	if p.Name != "Bijan Robinson" || p.Value.SFDynasty != 10000 || p.Value.SFRedraft != 9772 {
		t.Errorf("player = %+v, unexpected shape", p)
	}

	if len(bundle.Picks) != 2 {
		t.Fatalf("len(Picks) = %d, want 2", len(bundle.Picks))
	}
	if bundle.Picks[0].Variant != "early" || bundle.Picks[1].Slot != 1 {
		t.Errorf("picks = %+v, unexpected shape", bundle.Picks)
	}

	if calls["/api/v1/players"] != 1 || calls["/api/v1/picks"] != 1 {
		t.Errorf("calls = %+v, want exactly one of each", calls)
	}
}

func TestLoadBundleCachesSecondCall(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/v1/players":
			w.Write([]byte(`{"total": 1, "players": [{"id": "9509", "name": "Bijan Robinson", "value": {"sf_dynasty": 10000}}]}`))
		case "/api/v1/picks":
			w.Write([]byte(`{"picks": [{"id": "pick:2026:1:early", "year": 2026, "round": 1, "variant": "early", "value": {"sf_dynasty": 3748}}]}`))
		}
	}))
	defer srv.Close()
	orig := baseURL
	baseURL = srv.URL
	defer func() { baseURL = orig }()

	dir := t.TempDir()
	first, err := LoadBundle(t.Context(), dir, CacheTTL)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	second, err := LoadBundle(t.Context(), dir, CacheTTL)
	if err != nil {
		t.Fatalf("LoadBundle (cached): %v", err)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2 (one players + one picks, second run cached)", calls)
	}
	// The cached read must round-trip the same data, not just avoid a second fetch.
	if second.Players["9509"] != first.Players["9509"] {
		t.Errorf("cached Players[9509] = %+v, want %+v (round-trip mismatch)", second.Players["9509"], first.Players["9509"])
	}
	if len(second.Picks) != 1 || second.Picks[0] != first.Picks[0] {
		t.Errorf("cached Picks = %+v, want %+v (round-trip mismatch)", second.Picks, first.Picks)
	}
}

func TestFormatValuesGet(t *testing.T) {
	v := FormatValues{SFDynasty: 100, NonSFDynasty: 200, SFRedraft: 300, NonSFRedraft: 400}
	cases := map[string]int{
		"sf_dynasty":     100,
		"non_sf_dynasty": 200,
		"sf_redraft":     300,
		"non_sf_redraft": 400,
		"unknown_format": 0,
	}
	for format, want := range cases {
		if got := v.Get(format); got != want {
			t.Errorf("Get(%q) = %d, want %d", format, got, want)
		}
	}
}
