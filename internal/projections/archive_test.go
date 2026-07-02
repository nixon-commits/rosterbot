package projections

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestArchiveArtifactsCoversAllRoSSystems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo the type+stats so we can assert each URL was built correctly.
		w.Write([]byte(`{"type":"` + r.URL.Query().Get("type") + `","stats":"` + r.URL.Query().Get("stats") + `"}`))
	}))
	defer srv.Close()
	orig := fgBaseURL
	fgBaseURL = srv.URL + "?type=%s&stats=%s"
	defer func() { fgBaseURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	if len(arts) != 8 {
		t.Fatalf("got %d artifacts, want 8", len(arts))
	}
	var names []string
	for _, a := range arts {
		names = append(names, a.Filename)
		if len(a.Bytes) == 0 || !strings.HasPrefix(string(a.Bytes), `{"type":`) {
			t.Errorf("%s: expected raw JSON body, got %q", a.Filename, a.Bytes)
		}
	}
	sort.Strings(names)
	want := []string{
		"atc-ros-bat.json", "atc-ros-pit.json",
		"depthcharts-ros-bat.json", "depthcharts-ros-pit.json",
		"steamer-ros-bat.json", "steamer-ros-pit.json",
		"thebatx-ros-bat.json", "thebatx-ros-pit.json",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("filenames = %v, want %v", names, want)
	}
}

// A single upstream endpoint failing (as FanGraphs' ratc/bat did with a 500)
// must not discard the systems that fetched fine — the day's archive should keep
// the 7 blobs it got. A 404 stands in for the failure so archive.Get returns
// immediately without retry-backoff sleeps (5xx is retried; the code under test
// only cares that the fetch errored, not the status).
func TestArchiveArtifactsPersistsPartialWhenOneFetchFails(t *testing.T) {
	failType := fgProjectionType[ProjectionATCRoS]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") == failType && r.URL.Query().Get("stats") == "bat" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"type":"` + r.URL.Query().Get("type") + `","stats":"` + r.URL.Query().Get("stats") + `"}`))
	}))
	defer srv.Close()
	orig := fgBaseURL
	fgBaseURL = srv.URL + "?type=%s&stats=%s"
	defer func() { fgBaseURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("partial failure should not error: %v", err)
	}
	if len(arts) != 7 {
		t.Fatalf("got %d artifacts, want 7 (all but atc-ros-bat)", len(arts))
	}
	for _, a := range arts {
		if a.Filename == "atc-ros-bat.json" {
			t.Errorf("atc-ros-bat.json should be absent after its fetch failed")
		}
	}
}

// When every fetch fails there is nothing to persist, so the source must report
// an error (so the archive command counts it as a failed source).
func TestArchiveArtifactsErrorsWhenAllFetchesFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	orig := fgBaseURL
	fgBaseURL = srv.URL + "?type=%s&stats=%s"
	defer func() { fgBaseURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Now())
	if err == nil {
		t.Fatalf("expected an error when every fetch fails, got nil (%d artifacts)", len(arts))
	}
	if len(arts) != 0 {
		t.Errorf("got %d artifacts, want 0 when all fetches fail", len(arts))
	}
}
