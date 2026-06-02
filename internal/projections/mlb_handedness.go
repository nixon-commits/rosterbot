package projections

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	mlb "github.com/pmurley/go-mlb"
)

// mlbBaseURL is the MLB Stats API host (without /api/ — go-mlb appends that
// itself). Exposed as a var for test override.
var mlbBaseURL = "https://statsapi.mlb.com"

// FetchMLBHandedness fetches bat side and pitch hand from the MLB Stats API
// for all provided MLBAM IDs. Returns maps of NormalizeName(name) → "R"/"L"/"S".
// Switch hitters ("S" or "B") are normalized to "S".
func FetchMLBHandedness(mlbamIDs map[string]int) (bats map[string]string, throws map[string]string, err error) {
	bats = make(map[string]string)
	throws = make(map[string]string)

	if len(mlbamIDs) == 0 {
		return
	}

	idToName := make(map[int]string, len(mlbamIDs))
	ids := make([]int, 0, len(mlbamIDs))
	for name, id := range mlbamIDs {
		idToName[id] = name
		ids = append(ids, id)
	}

	client := mlb.NewClient(
		mlb.WithBaseURL(mlbBaseURL),
		mlb.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
		mlb.WithCache(nil),
	)
	ctx := context.Background()

	// Batch by 500 to stay within URL length limits.
	for i := 0; i < len(ids); i += 500 {
		end := i + 500
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]

		people, perr := client.People.List(ctx, batch)
		if perr != nil {
			return bats, throws, fmt.Errorf("mlb people fetch: %w", perr)
		}

		for _, p := range people {
			name, ok := idToName[p.ID]
			if !ok {
				continue
			}
			if p.BatSide != nil {
				if b := strings.ToUpper(p.BatSide.Code); b == "R" || b == "L" || b == "S" || b == "B" {
					if b == "B" {
						b = "S"
					}
					bats[name] = b
				}
			}
			if p.PitchHand != nil {
				if t := strings.ToUpper(p.PitchHand.Code); t == "R" || t == "L" {
					throws[name] = t
				}
			}
		}
	}

	return bats, throws, nil
}

// HandednessData holds cached bat-side and pitch-hand maps.
type HandednessData struct {
	Bats   map[string]string `json:"bats"`
	Throws map[string]string `json:"throws"`
}

// FetchMLBHandednessCached is like FetchMLBHandedness but uses a file cache.
// Uses a single stable cache key since handedness data is player-intrinsic and
// doesn't vary by projection system.
func FetchMLBHandednessCached(mlbamIDs map[string]int, cacheDir string, ttl time.Duration) (map[string]string, map[string]string, error) {
	c := cache.New[HandednessData](cacheDir, ttl)
	key := cache.Key("mlb-handedness")
	data, err := c.Get(key, func() (HandednessData, error) {
		bats, throws, err := FetchMLBHandedness(mlbamIDs)
		return HandednessData{Bats: bats, Throws: throws}, err
	})
	if err != nil {
		return nil, nil, err
	}
	return data.Bats, data.Throws, nil
}
