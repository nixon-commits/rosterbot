package projections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

var mlbPeopleURL = "https://statsapi.mlb.com/api/v1/people"

type mlbPeopleResponse struct {
	People []mlbPerson `json:"people"`
}

type mlbPerson struct {
	ID        int         `json:"id"`
	FullName  string      `json:"fullName"`
	BatSide   mlbHandSide `json:"batSide"`
	PitchHand mlbHandSide `json:"pitchHand"`
}

type mlbHandSide struct {
	Code string `json:"code"`
}

// FetchMLBHandedness fetches bat side and pitch hand from the MLB Stats API
// for all provided MLBAM IDs. Returns maps of NormalizeName(name) → "R"/"L"/"S".
// Switch hitters ("S" or "B") are normalized to "S".
func FetchMLBHandedness(mlbamIDs map[string]int) (bats map[string]string, throws map[string]string, err error) {
	bats = make(map[string]string)
	throws = make(map[string]string)

	if len(mlbamIDs) == 0 {
		return
	}

	// Build reverse map: MLBAM ID → normalized name.
	idToName := make(map[int]string, len(mlbamIDs))
	ids := make([]int, 0, len(mlbamIDs))
	for name, id := range mlbamIDs {
		idToName[id] = name
		ids = append(ids, id)
	}

	// Batch by 500 to stay within URL length limits.
	client := &http.Client{Timeout: 15 * time.Second}
	for i := 0; i < len(ids); i += 500 {
		end := i + 500
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]

		idStrs := make([]string, len(batch))
		for j, id := range batch {
			idStrs[j] = strconv.Itoa(id)
		}

		url := mlbPeopleURL + "?personIds=" + strings.Join(idStrs, ",")
		resp, err := client.Get(url)
		if err != nil {
			return bats, throws, fmt.Errorf("mlb people fetch: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return bats, throws, fmt.Errorf("mlb people: status %d", resp.StatusCode)
		}

		var result mlbPeopleResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return bats, throws, fmt.Errorf("mlb people json: %w", err)
		}

		for _, p := range result.People {
			name, ok := idToName[p.ID]
			if !ok {
				continue
			}
			if b := strings.ToUpper(p.BatSide.Code); b == "R" || b == "L" || b == "S" {
				if b == "B" {
					b = "S"
				}
				bats[name] = b
			}
			if t := strings.ToUpper(p.PitchHand.Code); t == "R" || t == "L" {
				throws[name] = t
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
