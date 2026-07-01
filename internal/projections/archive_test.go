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
