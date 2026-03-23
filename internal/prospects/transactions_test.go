package prospects

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchTransactionAlerts_CalledUpOnMyTeam(t *testing.T) {
	fixture := map[string]interface{}{
		"transactions": []map[string]interface{}{
			{
				"person":          map[string]interface{}{"fullName": "Jackson Chourio"},
				"toTeam":          map[string]interface{}{"abbreviation": "MIL"},
				"fromTeam":        map[string]interface{}{"abbreviation": "MIL"},
				"typeCode":        "CU",
				"date":            "2026-03-22T00:00:00",
				"description":     "Called up",
				"transactionType": "Call Up",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbTransactionsURL
	mlbTransactionsURL = srv.URL + "?startDate=%s&endDate=%s"
	defer func() { mlbTransactionsURL = origURL }()

	myMinors := map[string]bool{"jackson chourio": true}
	rankings := map[string]int{"jackson chourio": 12}

	from := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC)

	alerts, err := FetchTransactionAlerts(from, to, myMinors, rankings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Kind != CalledUp {
		t.Errorf("expected CalledUp, got %s", alerts[0].Kind)
	}
	if !alerts[0].OnMyTeam {
		t.Error("expected OnMyTeam=true")
	}
	if alerts[0].Priority != "high" {
		t.Errorf("expected high priority, got %s", alerts[0].Priority)
	}
}

func TestFetchTransactionAlerts_FreeAgentBuzz(t *testing.T) {
	fixture := map[string]interface{}{
		"transactions": []map[string]interface{}{
			{
				"person":          map[string]interface{}{"fullName": "Jasson Dominguez"},
				"toTeam":          map[string]interface{}{"abbreviation": "NYY"},
				"fromTeam":        map[string]interface{}{"abbreviation": "NYY"},
				"typeCode":        "CU",
				"date":            "2026-03-22T00:00:00",
				"description":     "Called up",
				"transactionType": "Call Up",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbTransactionsURL
	mlbTransactionsURL = srv.URL + "?startDate=%s&endDate=%s"
	defer func() { mlbTransactionsURL = origURL }()

	myMinors := map[string]bool{} // not on my team
	rankings := map[string]int{"jasson dominguez": 8}

	alerts, err := FetchTransactionAlerts(
		time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC),
		myMinors, rankings,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Kind != FreeAgentBuzz {
		t.Errorf("expected FreeAgentBuzz, got %s", alerts[0].Kind)
	}
	if alerts[0].Rank != 8 {
		t.Errorf("expected rank 8, got %d", alerts[0].Rank)
	}
}

func TestFetchTransactionAlerts_OptionedLowPriority(t *testing.T) {
	fixture := map[string]interface{}{
		"transactions": []map[string]interface{}{
			{
				"person":          map[string]interface{}{"fullName": "Spencer Torkelson"},
				"toTeam":          map[string]interface{}{"abbreviation": "DET"},
				"fromTeam":        map[string]interface{}{"abbreviation": "DET"},
				"typeCode":        "OPT",
				"date":            "2026-03-22T00:00:00",
				"description":     "Optioned",
				"transactionType": "Optioned",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbTransactionsURL
	mlbTransactionsURL = srv.URL + "?startDate=%s&endDate=%s"
	defer func() { mlbTransactionsURL = origURL }()

	myMinors := map[string]bool{"spencer torkelson": true}

	alerts, err := FetchTransactionAlerts(
		time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC),
		myMinors, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Priority != "low" {
		t.Errorf("expected low priority, got %s", alerts[0].Priority)
	}
}

func TestFetchTransactionAlerts_EmptyResponse(t *testing.T) {
	fixture := map[string]interface{}{"transactions": []interface{}{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(fixture)
	}))
	defer srv.Close()

	origURL := mlbTransactionsURL
	mlbTransactionsURL = srv.URL + "?startDate=%s&endDate=%s"
	defer func() { mlbTransactionsURL = origURL }()

	alerts, err := FetchTransactionAlerts(
		time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC),
		nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(alerts))
	}
}
