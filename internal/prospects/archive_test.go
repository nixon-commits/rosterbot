package prospects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestArchiveArtifactsReturnsBoardJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"prospect":true}]`))
	}))
	defer srv.Close()
	orig := fgProspectURL
	fgProspectURL = srv.URL + "?draft=%dprospect&season=%d"
	defer func() { fgProspectURL = orig }()

	arts, err := ArchiveArtifacts(context.Background(), time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ArchiveArtifacts: %v", err)
	}
	if len(arts) != 1 || arts[0].Filename != "fangraphs-board.json" {
		t.Fatalf("got %+v, want one fangraphs-board.json", arts)
	}
	if !strings.HasPrefix(string(arts[0].Bytes), `[{"prospect"`) {
		t.Errorf("bytes = %q, want raw board JSON", arts[0].Bytes)
	}
}
