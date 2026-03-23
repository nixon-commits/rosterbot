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
