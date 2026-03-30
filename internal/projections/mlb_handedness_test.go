package projections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchMLBHandedness(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mlbPeopleResponse{
			People: []mlbPerson{
				{ID: 592450, FullName: "Aaron Judge", BatSide: mlbHandSide{Code: "R"}, PitchHand: mlbHandSide{Code: "R"}},
				{ID: 665742, FullName: "Juan Soto", BatSide: mlbHandSide{Code: "L"}, PitchHand: mlbHandSide{Code: "L"}},
				{ID: 645277, FullName: "Ozzie Albies", BatSide: mlbHandSide{Code: "S"}, PitchHand: mlbHandSide{Code: "R"}},
				{ID: 543037, FullName: "Gerrit Cole", BatSide: mlbHandSide{Code: "R"}, PitchHand: mlbHandSide{Code: "R"}},
				{ID: 579328, FullName: "Yusei Kikuchi", BatSide: mlbHandSide{Code: "L"}, PitchHand: mlbHandSide{Code: "L"}},
			},
		})
	}))
	defer srv.Close()

	old := mlbPeopleURL
	mlbPeopleURL = srv.URL
	defer func() { mlbPeopleURL = old }()

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
