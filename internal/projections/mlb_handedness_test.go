package projections

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchMLBHandedness(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/people" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"people": [
				{"id": 592450, "fullName": "Aaron Judge", "batSide": {"code": "R"}, "pitchHand": {"code": "R"}},
				{"id": 665742, "fullName": "Juan Soto", "batSide": {"code": "L"}, "pitchHand": {"code": "L"}},
				{"id": 645277, "fullName": "Ozzie Albies", "batSide": {"code": "S"}, "pitchHand": {"code": "R"}},
				{"id": 543037, "fullName": "Gerrit Cole", "batSide": {"code": "R"}, "pitchHand": {"code": "R"}},
				{"id": 579328, "fullName": "Yusei Kikuchi", "batSide": {"code": "L"}, "pitchHand": {"code": "L"}}
			]
		}`))
	}))
	defer srv.Close()

	old := mlbBaseURL
	mlbBaseURL = srv.URL
	defer func() { mlbBaseURL = old }()

	ids := map[string]int{
		"aaron judge":   592450,
		"juan soto":     665742,
		"ozzie albies":  645277,
		"gerrit cole":   543037,
		"yusei kikuchi": 579328,
	}

	bats, throws, err := FetchMLBHandedness(ids)
	if err != nil {
		t.Fatal(err)
	}

	// Hitter bat sides
	if bats["aaron judge"] != "R" {
		t.Errorf("Judge bats: got %q, want R", bats["aaron judge"])
	}
	if bats["juan soto"] != "L" {
		t.Errorf("Soto bats: got %q, want L", bats["juan soto"])
	}
	if bats["ozzie albies"] != "S" {
		t.Errorf("Albies bats: got %q, want S", bats["ozzie albies"])
	}

	// Pitcher throws
	if throws["gerrit cole"] != "R" {
		t.Errorf("Cole throws: got %q, want R", throws["gerrit cole"])
	}
	if throws["yusei kikuchi"] != "L" {
		t.Errorf("Kikuchi throws: got %q, want L", throws["yusei kikuchi"])
	}
}

func TestFetchMLBHandedness_Empty(t *testing.T) {
	bats, throws, err := FetchMLBHandedness(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bats) != 0 || len(throws) != 0 {
		t.Errorf("expected empty maps, got %d bats, %d throws", len(bats), len(throws))
	}
}

func TestFetchMLBHandedness_BatSideB(t *testing.T) {
	// MLB API sometimes returns "B" for switch hitters; we normalize to "S".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"people":[{"id":1,"fullName":"Test","batSide":{"code":"B"}}]}`))
	}))
	defer srv.Close()

	old := mlbBaseURL
	mlbBaseURL = srv.URL
	defer func() { mlbBaseURL = old }()

	bats, _, err := FetchMLBHandedness(map[string]int{"test": 1})
	if err != nil {
		t.Fatal(err)
	}
	if bats["test"] != "S" {
		t.Errorf("got %q, want S", bats["test"])
	}
}
