package waivers

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}
