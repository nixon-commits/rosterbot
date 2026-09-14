package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// mlbSeasonURL is the MLB statsapi season endpoint. Var for test override.
var mlbSeasonURL = "https://statsapi.mlb.com/api/v1/seasons/%d?sportId=1"

// seasonTTL is the on-disk lifetime for a cached season record. The dates
// are set before spring training and do not move.
const seasonTTL = 7 * 24 * time.Hour

type seasonPayload struct {
	Seasons []struct {
		SeasonID               string `json:"seasonId"`
		RegularSeasonStartDate string `json:"regularSeasonStartDate"`
		RegularSeasonEndDate   string `json:"regularSeasonEndDate"`
	} `json:"seasons"`
}

// SeasonDates returns MLB's regular-season start and end for the year (UTC
// midnight). It needs no credentials, which is why it is the fallback the
// off-season gate reaches for when the Fantrax season range cannot be read:
// every fantasy season, playoffs included, sits inside MLB's regular season,
// so a day outside these bounds is off-season for any league. A year the
// endpoint does not describe is an error, never zero dates.
func (c *Client) SeasonDates(ctx context.Context, year int) (start, end time.Time, err error) {
	fetch := func() (*seasonPayload, error) { return c.fetchSeasonUncached(ctx, year) }
	var payload *seasonPayload
	if c.CacheDir != "" {
		fc := cache.New[*seasonPayload](c.CacheDir, seasonTTL)
		payload, err = fc.Get(cache.Key("mlb-season", strconv.Itoa(year)), fetch)
	} else {
		payload, err = fetch()
	}
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	for _, s := range payload.Seasons {
		if s.SeasonID != strconv.Itoa(year) {
			continue
		}
		start, err = time.Parse("2006-01-02", s.RegularSeasonStartDate)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("mlb season %d: start %q: %w", year, s.RegularSeasonStartDate, err)
		}
		end, err = time.Parse("2006-01-02", s.RegularSeasonEndDate)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("mlb season %d: end %q: %w", year, s.RegularSeasonEndDate, err)
		}
		return start, end, nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("mlb season %d: not in the response", year)
}

func (c *Client) fetchSeasonUncached(ctx context.Context, year int) (*seasonPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(mlbSeasonURL, year), nil)
	if err != nil {
		return nil, fmt.Errorf("mlb season request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mlb season fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mlb season: status %d", resp.StatusCode)
	}
	var payload seasonPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("mlb season decode: %w", err)
	}
	return &payload, nil
}
