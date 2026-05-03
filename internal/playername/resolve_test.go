package playername

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveMLBAMIDs_BridgesNicknames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/people/search", "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"people": [
					{"id": 815888, "fullName": "Leo De Vries", "firstName": "Leodalis", "lastName": "De Vries", "useName": "Leo"}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := mlbBaseURL
	mlbBaseURL = srv.URL
	defer func() { mlbBaseURL = old }()

	rp, err := ResolveMLBAMIDsNoCache([]string{"Leo De Vries", "Leodalis De Vries"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leoID, ok := rp.ByName[Normalize("Leo De Vries")]
	if !ok || leoID != 815888 {
		t.Errorf("expected Leo De Vries → 815888, got %d (ok=%v)", leoID, ok)
	}

	leodalisID, ok := rp.ByName[Normalize("Leodalis De Vries")]
	if !ok || leodalisID != 815888 {
		t.Errorf("expected Leodalis De Vries → 815888, got %d (ok=%v)", leodalisID, ok)
	}

	if rp.ByID[815888] != "Leo De Vries" {
		t.Errorf("expected ByID[815888] = Leo De Vries, got %q", rp.ByID[815888])
	}
}

func TestResolveMLBAMIDs_DeduplicatesNames(t *testing.T) {
	searchCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/people/search":
			searchCalls++
			fallthrough
		case "/api/v1/people":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"people": [
					{"id": 100, "fullName": "Mike Trout", "firstName": "Michael", "lastName": "Trout", "useName": "Mike"}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := mlbBaseURL
	mlbBaseURL = srv.URL
	defer func() { mlbBaseURL = old }()

	// Same name twice — should only search once after dedup.
	_, err := ResolveMLBAMIDsNoCache([]string{"Mike Trout", "mike trout"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searchCalls != 1 {
		t.Errorf("expected 1 search call, got %d", searchCalls)
	}
}
