package playername

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveMLBAMIDs_BridgesNicknames(t *testing.T) {
	// Mock MLB search API — returns the player when searched by either name.
	searchHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"people": []map[string]any{
				{"id": 815888, "fullName": "Leo De Vries", "firstName": "Leodalis", "lastName": "De Vries", "useName": "Leo"},
			},
		})
	})

	// Mock MLB people bulk API.
	peopleHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"people": []map[string]any{
				{"id": 815888, "fullName": "Leo De Vries", "firstName": "Leodalis", "lastName": "De Vries", "useName": "Leo"},
			},
		})
	})

	searchSrv := httptest.NewServer(searchHandler)
	defer searchSrv.Close()
	peopleSrv := httptest.NewServer(peopleHandler)
	defer peopleSrv.Close()

	origSearch := mlbSearchURL
	origPeople := mlbPeopleURL
	mlbSearchURL = searchSrv.URL + "?names=%s"
	mlbPeopleURL = peopleSrv.URL + "?personIds=%s"
	defer func() {
		mlbSearchURL = origSearch
		mlbPeopleURL = origPeople
	}()

	rp, err := ResolveMLBAMIDsNoCache([]string{"Leo De Vries", "Leodalis De Vries"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both name variants should resolve to the same MLBAM ID.
	leoID, ok := rp.ByName[Normalize("Leo De Vries")]
	if !ok || leoID != 815888 {
		t.Errorf("expected Leo De Vries → 815888, got %d (ok=%v)", leoID, ok)
	}

	leodalisID, ok := rp.ByName[Normalize("Leodalis De Vries")]
	if !ok || leodalisID != 815888 {
		t.Errorf("expected Leodalis De Vries → 815888, got %d (ok=%v)", leodalisID, ok)
	}

	// Both should map to the same display name.
	if rp.ByID[815888] != "Leo De Vries" {
		t.Errorf("expected ByID[815888] = Leo De Vries, got %q", rp.ByID[815888])
	}
}

func TestResolveMLBAMIDs_DeduplicatesNames(t *testing.T) {
	callCount := 0
	searchHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]any{
			"people": []map[string]any{
				{"id": 100, "fullName": "Mike Trout", "firstName": "Michael", "lastName": "Trout", "useName": "Mike"},
			},
		})
	})

	peopleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"people": []map[string]any{
				{"id": 100, "fullName": "Mike Trout", "firstName": "Michael", "lastName": "Trout", "useName": "Mike"},
			},
		})
	}))
	defer peopleSrv.Close()

	searchSrv := httptest.NewServer(searchHandler)
	defer searchSrv.Close()

	origSearch := mlbSearchURL
	origPeople := mlbPeopleURL
	mlbSearchURL = searchSrv.URL + "?names=%s"
	mlbPeopleURL = peopleSrv.URL + "?personIds=%s"
	defer func() {
		mlbSearchURL = origSearch
		mlbPeopleURL = origPeople
	}()

	// Same name twice — should only search once.
	_, err := ResolveMLBAMIDsNoCache([]string{"Mike Trout", "mike trout"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 search call, got %d", callCount)
	}
}
