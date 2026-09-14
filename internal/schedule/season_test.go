package schedule

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The season endpoint is the credential-free fallback for the off-season
// gate: it answers "when does MLB's regular season run" for a year without
// a Fantrax session, and every fantasy playoff ends on or before that end.
func TestSeasonDates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/seasons/2026" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"seasons":[{"seasonId":"2026","regularSeasonStartDate":"2026-03-26","regularSeasonEndDate":"2026-09-27","postSeasonEndDate":"2026-11-01"}]}`))
	}))
	t.Cleanup(srv.Close)
	orig := mlbSeasonURL
	mlbSeasonURL = srv.URL + "/api/v1/seasons/%d?sportId=1"
	t.Cleanup(func() { mlbSeasonURL = orig })

	c := NewClient()
	start, end, err := c.SeasonDates(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if !start.Equal(time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("season = %s..%s, want 2026-03-26..2026-09-27", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}
	if _, _, err := c.SeasonDates(context.Background(), 2027); err == nil {
		t.Error("a year the endpoint does not answer should be an error, not zero dates")
	}
}
