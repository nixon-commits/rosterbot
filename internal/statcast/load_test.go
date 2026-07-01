package statcast

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// fixtureServer returns a Server that responds to any GET with the given file's contents.
func fixtureServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	body := mustReadFile(t, path)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(body)
	}))
}

func TestFetchHitterExp(t *testing.T) {
	srv := fixtureServer(t, "testdata/savant_hitter_exp.csv")
	defer srv.Close()

	rows, err := fetchHitterExp(srv.URL)
	if err != nil {
		t.Fatalf("fetchHitterExp: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d", len(rows))
	}

	// Find Soto.
	var soto HitterRow
	for _, r := range rows {
		if r.MLBAMID == 665742 {
			soto = r
		}
	}
	if soto.MLBAMID == 0 {
		t.Fatal("missing Soto row")
	}
	if soto.PA != 400 {
		t.Errorf("Soto PA: want 400, got %d", soto.PA)
	}
	if soto.WOBA < 0.354 || soto.WOBA > 0.356 {
		t.Errorf("Soto wOBA: want ~.355, got %v", soto.WOBA)
	}
	if soto.XwOBA < 0.394 || soto.XwOBA > 0.396 {
		t.Errorf("Soto xwOBA: want ~.395, got %v", soto.XwOBA)
	}
}

func TestFetchHitterSC(t *testing.T) {
	srv := fixtureServer(t, "testdata/savant_hitter_sc.csv")
	defer srv.Close()

	rows, err := fetchHitterSC(srv.URL)
	if err != nil {
		t.Fatalf("fetchHitterSC: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.MLBAMID == 665742 {
			if r.Barrel < 14 || r.Barrel > 15 {
				t.Errorf("Soto barrel: want ~14.2, got %v", r.Barrel)
			}
			if r.HardHit < 48 || r.HardHit > 49 {
				t.Errorf("Soto hard-hit: want ~48.5, got %v", r.HardHit)
			}
		}
	}
}

func TestFetchPitcherExp(t *testing.T) {
	srv := fixtureServer(t, "testdata/savant_pitcher_exp.csv")
	defer srv.Close()

	rows, err := fetchPitcherExp(srv.URL)
	if err != nil {
		t.Fatalf("fetchPitcherExp: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.MLBAMID == 888001 { // Buylow
			if r.ERA-r.XERA < 1.0 {
				t.Errorf("Buylow expected ERA-xERA >= 1.0, got %.2f", r.ERA-r.XERA)
			}
		}
	}
}

func TestFetchCSV_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(""))
	}))
	defer srv.Close()
	if _, err := fetchHitterExp(srv.URL); err == nil {
		t.Fatal("expected error on empty body, got nil")
	}
}

func TestFetchCSV_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := fetchHitterExp(srv.URL); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}
