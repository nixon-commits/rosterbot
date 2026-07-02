package statcast

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestArchiveArtifactsReturnsFiveCSVs(t *testing.T) {
	var capturedQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQueries = append(capturedQueries, r.URL.RawQuery)
		w.Write([]byte("player_id,x\n1,2\n"))
	}))
	defer srv.Close()
	// All five URL vars point at the fake server; keep their %d/%s verbs.
	save := []*string{&savantHitterExpURL, &savantHitterExp14dURL, &savantHitterSCURL, &savantPitcherExpURL, &savantPitcherExp30URL}
	orig := make([]string, len(save))
	for i, p := range save {
		orig[i] = *p
	}
	savantHitterExpURL = srv.URL + "?year=%d"
	savantHitterExp14dURL = srv.URL + "?year=%d&s=%s&e=%s"
	savantHitterSCURL = srv.URL + "?year=%d"
	savantPitcherExpURL = srv.URL + "?year=%d"
	savantPitcherExp30URL = srv.URL + "?year=%d&s=%s&e=%s"
	defer func() {
		for i, p := range save {
			*p = orig[i]
		}
	}()

	arts, err := ArchiveArtifacts(context.Background(), time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	var names []string
	for _, a := range arts {
		names = append(names, a.Filename)
		if !strings.HasPrefix(string(a.Bytes), "player_id,") {
			t.Errorf("%s: expected raw CSV, got %q", a.Filename, a.Bytes)
		}
	}
	sort.Strings(names)
	want := "hitter-exp-14d.csv,hitter-exp.csv,hitter-statcast.csv,pitcher-exp-30d.csv,pitcher-exp.csv"
	if strings.Join(names, ",") != want {
		t.Errorf("names = %v, want %v", names, want)
	}

	// Verify rolling-window date math: for 2026-06-30, end=06-29, 14d starts at 06-16, 30d starts at 05-31.
	wantSeasonYear := "year=2026"
	want14d := "year=2026&s=2026-06-16&e=2026-06-29"
	want30d := "year=2026&s=2026-05-31&e=2026-06-29"

	found14d := false
	found30d := false
	seasonCount := 0
	for _, q := range capturedQueries {
		if q == want14d {
			found14d = true
		}
		if q == want30d {
			found30d = true
		}
		if q == wantSeasonYear {
			seasonCount++
		}
	}
	if !found14d {
		t.Errorf("14d window query not found. want %s, got queries: %v", want14d, capturedQueries)
	}
	if !found30d {
		t.Errorf("30d window query not found. want %s, got queries: %v", want30d, capturedQueries)
	}
	if seasonCount < 3 {
		t.Errorf("season year query count = %d, want at least 3, got queries: %v", seasonCount, capturedQueries)
	}
}

// One Savant CSV failing (Savant occasionally 500s a single leaderboard) must
// not discard the four that fetched fine — especially since the rolling 14d/30d
// windows roll off upstream and can never be re-fetched. A 404 stands in for the
// failure so archive.Get returns without retry-backoff sleeps.
func TestArchiveArtifactsPersistsPartialWhenOneCSVFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail only the 14d window (unique start date), succeed on the rest.
		if strings.Contains(r.URL.RawQuery, "s=2026-06-16") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("player_id,x\n1,2\n"))
	}))
	defer srv.Close()
	save := []*string{&savantHitterExpURL, &savantHitterExp14dURL, &savantHitterSCURL, &savantPitcherExpURL, &savantPitcherExp30URL}
	orig := make([]string, len(save))
	for i, p := range save {
		orig[i] = *p
	}
	savantHitterExpURL = srv.URL + "?year=%d"
	savantHitterExp14dURL = srv.URL + "?year=%d&s=%s&e=%s"
	savantHitterSCURL = srv.URL + "?year=%d"
	savantPitcherExpURL = srv.URL + "?year=%d"
	savantPitcherExp30URL = srv.URL + "?year=%d&s=%s&e=%s"
	defer func() {
		for i, p := range save {
			*p = orig[i]
		}
	}()

	arts, err := ArchiveArtifacts(context.Background(), time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("partial failure should not error: %v", err)
	}
	if len(arts) != 4 {
		t.Fatalf("got %d artifacts, want 4 (all but hitter-exp-14d)", len(arts))
	}
	for _, a := range arts {
		if a.Filename == "hitter-exp-14d.csv" {
			t.Errorf("hitter-exp-14d.csv should be absent after its fetch failed")
		}
	}
}

// When every CSV fails there is nothing to archive, so the source reports an error.
func TestArchiveArtifactsErrorsWhenAllCSVsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	save := []*string{&savantHitterExpURL, &savantHitterExp14dURL, &savantHitterSCURL, &savantPitcherExpURL, &savantPitcherExp30URL}
	orig := make([]string, len(save))
	for i, p := range save {
		orig[i] = *p
	}
	savantHitterExpURL = srv.URL + "?year=%d"
	savantHitterExp14dURL = srv.URL + "?year=%d&s=%s&e=%s"
	savantHitterSCURL = srv.URL + "?year=%d"
	savantPitcherExpURL = srv.URL + "?year=%d"
	savantPitcherExp30URL = srv.URL + "?year=%d&s=%s&e=%s"
	defer func() {
		for i, p := range save {
			*p = orig[i]
		}
	}()

	arts, err := ArchiveArtifacts(context.Background(), time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatalf("expected an error when every CSV fails, got nil (%d artifacts)", len(arts))
	}
	if len(arts) != 0 {
		t.Errorf("got %d artifacts, want 0 when all fail", len(arts))
	}
}
