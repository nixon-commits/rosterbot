package waivers

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// keySavant is the cache-key source prefix for every Baseball Savant CSV. The
// entity/window parts ("hit"/"pit", "exp"/"sc"/"exp14"/…) are appended per call.
const keySavant = "savant"

// SavantCacheTTL is the canonical on-disk lifetime for the Baseball Savant CSV
// slices. Savant recomputes these daily, so 24h matches the upstream cadence.
// Both consumers (waivers, claims) use this one value so they share cache
// entries with a single freshness policy.
const SavantCacheTTL = 24 * time.Hour

// Baseball Savant CSV endpoints. Defined as `var` so tests can replace them
// with httptest server URLs (matches the convention in internal/schedule and
// internal/projections/fangraphs.go).
var (
	savantHitterExpURL    = "https://baseballsavant.mlb.com/leaderboard/expected_statistics?type=batter&year=%d&min=q&csv=true"
	savantHitterExp14dURL = "https://baseballsavant.mlb.com/leaderboard/expected_statistics?type=batter&year=%d&min=20&date_start=%s&date_end=%s&csv=true"
	savantHitterSCURL     = "https://baseballsavant.mlb.com/leaderboard/statcast?type=batter&year=%d&min=q&csv=true"
	savantPitcherExpURL   = "https://baseballsavant.mlb.com/leaderboard/expected_statistics?type=pitcher&year=%d&min=q&csv=true"
	savantPitcherExp30URL = "https://baseballsavant.mlb.com/leaderboard/expected_statistics?type=pitcher&year=%d&min=20&date_start=%s&date_end=%s&csv=true"
)

const savantHTTPTimeout = 30 * time.Second

// LoadSavant fetches all five Savant CSV slices in sequence (cached) and
// returns the combined bundle keyed by MLBAM ID. Any individual fetch failure
// is logged and the corresponding map is left empty — Run continues with the
// signals it has.
func LoadSavant(cacheDir string, year int, today time.Time, ttl time.Duration) (*SavantBundle, error) {
	end := today.AddDate(0, 0, -1)
	start14 := end.AddDate(0, 0, -13)
	start30 := end.AddDate(0, 0, -29)
	dateKey := end.Format("20060102")

	bundle := &SavantBundle{
		HitterExp:     map[int]SavantHitterRow{},
		HitterSC:      map[int]SavantHitterStatcastRow{},
		HitterExp14d:  map[int]SavantHitterRow{},
		PitcherExp:    map[int]SavantPitcherRow{},
		PitcherExp30d: map[int]SavantPitcherRow{},
	}

	hitExpC := cache.New[[]SavantHitterRow](cacheDir, ttl)
	hitSCC := cache.New[[]SavantHitterStatcastRow](cacheDir, ttl)
	hitExp14C := cache.New[[]SavantHitterRow](cacheDir, ttl)
	pitExpC := cache.New[[]SavantPitcherRow](cacheDir, ttl)
	pitExp30C := cache.New[[]SavantPitcherRow](cacheDir, ttl)

	if rows, err := hitExpC.Get(cache.Key(keySavant, "hit", "exp", strconv.Itoa(year)), func() ([]SavantHitterRow, error) {
		return fetchHitterExp(fmt.Sprintf(savantHitterExpURL, year))
	}); err == nil {
		for _, r := range rows {
			bundle.HitterExp[r.MLBAMID] = r
		}
	} else {
		log.Printf("WARNING: savant hit-exp fetch failed: %v", err)
	}

	if rows, err := hitSCC.Get(cache.Key(keySavant, "hit", "sc", strconv.Itoa(year)), func() ([]SavantHitterStatcastRow, error) {
		return fetchHitterSC(fmt.Sprintf(savantHitterSCURL, year))
	}); err == nil {
		for _, r := range rows {
			bundle.HitterSC[r.MLBAMID] = r
		}
	} else {
		log.Printf("WARNING: savant hit-sc fetch failed: %v", err)
	}

	if rows, err := hitExp14C.Get(cache.Key(keySavant, "hit", "exp14", strconv.Itoa(year), dateKey), func() ([]SavantHitterRow, error) {
		return fetchHitterExp(fmt.Sprintf(savantHitterExp14dURL, year, start14.Format("2006-01-02"), end.Format("2006-01-02")))
	}); err == nil {
		for _, r := range rows {
			bundle.HitterExp14d[r.MLBAMID] = r
		}
	} else {
		log.Printf("WARNING: savant hit-exp14 fetch failed: %v", err)
	}

	if rows, err := pitExpC.Get(cache.Key(keySavant, "pit", "exp", strconv.Itoa(year)), func() ([]SavantPitcherRow, error) {
		return fetchPitcherExp(fmt.Sprintf(savantPitcherExpURL, year))
	}); err == nil {
		for _, r := range rows {
			bundle.PitcherExp[r.MLBAMID] = r
		}
	} else {
		log.Printf("WARNING: savant pit-exp fetch failed: %v", err)
	}

	if rows, err := pitExp30C.Get(cache.Key(keySavant, "pit", "exp30", strconv.Itoa(year), dateKey), func() ([]SavantPitcherRow, error) {
		return fetchPitcherExp(fmt.Sprintf(savantPitcherExp30URL, year, start30.Format("2006-01-02"), end.Format("2006-01-02")))
	}); err == nil {
		for _, r := range rows {
			bundle.PitcherExp30d[r.MLBAMID] = r
		}
	} else {
		log.Printf("WARNING: savant pit-exp30 fetch failed: %v", err)
	}

	return bundle, nil
}

// fetchCSV does a GET, parses the first row as a header, and returns
// (header columnIndex map, all subsequent rows, error). Strips a UTF-8 BOM
// if present — Savant ships CSVs prefixed with EF BB BF.
func fetchCSV(url string) (map[string]int, [][]string, error) {
	client := &http.Client{Timeout: savantHTTPTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, nil, fmt.Errorf("savant fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("savant %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("savant body read: %w", err)
	}
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})

	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1 // tolerate trailing empty cells
	all, err := r.ReadAll()
	if err != nil {
		preview := body
		if len(preview) > 80 {
			preview = preview[:80]
		}
		return nil, nil, fmt.Errorf("savant csv parse: %w (body len=%d, first=%q)", err, len(body), string(preview))
	}
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("savant csv empty")
	}

	col := make(map[string]int, len(all[0]))
	for i, h := range all[0] {
		col[strings.TrimSpace(strings.ToLower(h))] = i
	}
	return col, all[1:], nil
}

// firstCol returns the index of the first matching column name, or -1 if none.
// Names are matched case-insensitively against the lowercased header.
func firstCol(col map[string]int, names ...string) int {
	for _, n := range names {
		if i, ok := col[strings.ToLower(n)]; ok {
			return i
		}
	}
	return -1
}

func cellFloat(record []string, idx int) float64 {
	if idx < 0 || idx >= len(record) {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(record[idx]), 64)
	return v
}

func cellInt(record []string, idx int) int {
	if idx < 0 || idx >= len(record) {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(record[idx]))
	return v
}

// fetchHitterExp parses the hitter expected_statistics CSV.
// Required columns (looked up by lowercase name with reasonable aliases):
//
//	player_id, pa, woba, est_woba (xwOBA).
func fetchHitterExp(url string) ([]SavantHitterRow, error) {
	col, rows, err := fetchCSV(url)
	if err != nil {
		return nil, err
	}
	idID := firstCol(col, "player_id", "playerid", "mlbam_id")
	idPA := firstCol(col, "pa", "plate_appearances")
	idWOBA := firstCol(col, "woba")
	idXWOBA := firstCol(col, "est_woba", "xwoba", "estimated_woba_using_speedangle")
	if idID < 0 || idWOBA < 0 || idXWOBA < 0 {
		return nil, fmt.Errorf("savant hitter exp: missing required columns")
	}
	out := make([]SavantHitterRow, 0, len(rows))
	for _, rec := range rows {
		mlbam := cellInt(rec, idID)
		if mlbam == 0 {
			continue
		}
		out = append(out, SavantHitterRow{
			MLBAMID: mlbam,
			PA:      cellInt(rec, idPA),
			WOBA:    cellFloat(rec, idWOBA),
			XwOBA:   cellFloat(rec, idXWOBA),
		})
	}
	return out, nil
}

// fetchHitterSC parses the hitter Statcast quality-of-contact CSV.
func fetchHitterSC(url string) ([]SavantHitterStatcastRow, error) {
	col, rows, err := fetchCSV(url)
	if err != nil {
		return nil, err
	}
	idID := firstCol(col, "player_id", "playerid", "mlbam_id")
	idBarrel := firstCol(col, "barrel_batted_rate", "brl_percent", "brl_batted_rate", "barrels_per_bbe_percent")
	idHard := firstCol(col, "hard_hit_percent", "hardhit_percent", "ev95percent")
	idSweet := firstCol(col, "sweet_spot_percent", "anglesweetspotpercent")
	if idID < 0 {
		return nil, fmt.Errorf("savant hitter statcast: missing player_id")
	}
	out := make([]SavantHitterStatcastRow, 0, len(rows))
	for _, rec := range rows {
		mlbam := cellInt(rec, idID)
		if mlbam == 0 {
			continue
		}
		out = append(out, SavantHitterStatcastRow{
			MLBAMID:   mlbam,
			Barrel:    cellFloat(rec, idBarrel),
			HardHit:   cellFloat(rec, idHard),
			SweetSpot: cellFloat(rec, idSweet),
		})
	}
	return out, nil
}

// fetchPitcherExp parses the pitcher expected_statistics CSV.
func fetchPitcherExp(url string) ([]SavantPitcherRow, error) {
	col, rows, err := fetchCSV(url)
	if err != nil {
		return nil, err
	}
	idID := firstCol(col, "player_id", "playerid", "mlbam_id")
	idPA := firstCol(col, "pa", "tbf", "batters_faced")
	idERA := firstCol(col, "era")
	idXERA := firstCol(col, "xera", "est_era")
	idWOBA := firstCol(col, "woba")
	idXWOBA := firstCol(col, "est_woba", "xwoba")
	if idID < 0 || idXERA < 0 {
		return nil, fmt.Errorf("savant pitcher exp: missing required columns")
	}
	out := make([]SavantPitcherRow, 0, len(rows))
	for _, rec := range rows {
		mlbam := cellInt(rec, idID)
		if mlbam == 0 {
			continue
		}
		out = append(out, SavantPitcherRow{
			MLBAMID: mlbam,
			PA:      cellInt(rec, idPA),
			ERA:     cellFloat(rec, idERA),
			XERA:    cellFloat(rec, idXERA),
			WOBA:    cellFloat(rec, idWOBA),
			XwOBA:   cellFloat(rec, idXWOBA),
		})
	}
	return out, nil
}
