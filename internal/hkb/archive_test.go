package hkb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestArchiveArtifactsReturnsRawPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>RAW HKB PAGE</html>"))
	}))
	defer srv.Close()
	orig := fetchURL
	fetchURL = srv.URL
	defer func() { fetchURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	if len(arts) != 1 || arts[0].Filename != "rankings.html" {
		t.Fatalf("got %+v, want one rankings.html", arts)
	}
	if string(arts[0].Bytes) != "<html>RAW HKB PAGE</html>" {
		t.Errorf("bytes = %q, want raw page verbatim", arts[0].Bytes)
	}
}
