package playername

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// MLBPerson holds the name variants and ID from the MLB Stats API.
type MLBPerson struct {
	ID        int    `json:"id"`
	FullName  string `json:"fullName"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	UseName   string `json:"useName"`
}

// mlbSearchURL is a var so tests can override it.
var mlbSearchURL = "https://statsapi.mlb.com/api/v1/people/search?names=%s&sportIds=11,12,13,14,16,1"

// mlbPeopleURL is a var so tests can override it.
var mlbPeopleURL = "https://statsapi.mlb.com/api/v1/people?personIds=%s&fields=people,id,fullName,firstName,lastName,useName,useLastName"

// cacheTTL for resolved player IDs (7 days — names don't change often).
const cacheTTL = 7 * 24 * time.Hour

// ResolvedPlayers maps normalized name variants to MLBAM IDs.
// For each player, both the firstName+lastName and useName+lastName
// variants are stored so that different sources (Fantrax uses legal names,
// FanGraphs/HKB use common names) resolve to the same ID.
type ResolvedPlayers struct {
	ByName map[string]int `json:"by_name"` // Normalize(name) → MLBAM ID
	ByID   map[int]string `json:"by_id"`   // MLBAM ID → display name (fullName)
}

// ResolveMLBAMIDs looks up MLBAM IDs for a list of player names via the
// MLB Stats API. Results are cached in .cache/mlb-player-ids.json.
//
// For each resolved player, both name variants (legal firstName and common
// useName combined with lastName) are indexed so cross-source matching works.
func ResolveMLBAMIDs(names []string, cacheDir string) (*ResolvedPlayers, error) {
	fc := cache.New[*ResolvedPlayers](cacheDir, cacheTTL)
	return fc.Get("mlb-player-ids", func() (*ResolvedPlayers, error) {
		return fetchAndResolve(names)
	})
}

// ResolveMLBAMIDsNoCache always fetches fresh (for testing or forced refresh).
func ResolveMLBAMIDsNoCache(names []string) (*ResolvedPlayers, error) {
	return fetchAndResolve(names)
}

func fetchAndResolve(names []string) (*ResolvedPlayers, error) {
	rp := &ResolvedPlayers{
		ByName: make(map[string]int),
		ByID:   make(map[int]string),
	}

	// Deduplicate names for search.
	seen := make(map[string]bool)
	var searchNames []string
	for _, name := range names {
		norm := Normalize(name)
		if !seen[norm] {
			seen[norm] = true
			searchNames = append(searchNames, name)
		}
	}

	// Batch search: the MLB API supports comma-separated names in a single call.
	// Batch into groups to avoid URL length limits.
	const searchBatchSize = 25
	var ids []int
	idSet := make(map[int]bool)
	for i := 0; i < len(searchNames); i += searchBatchSize {
		end := i + searchBatchSize
		if end > len(searchNames) {
			end = len(searchNames)
		}
		people, err := searchMLBBatch(searchNames[i:end])
		if err != nil {
			continue
		}
		for _, p := range people {
			if !idSet[p.ID] {
				idSet[p.ID] = true
				ids = append(ids, p.ID)
			}
		}
	}

	// Bulk-fetch full person details (firstName, useName, lastName).
	const peopleBatchSize = 500
	for i := 0; i < len(ids); i += peopleBatchSize {
		end := i + peopleBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		people, err := fetchPeople(ids[i:end])
		if err != nil {
			continue
		}
		for _, p := range people {
			indexPerson(rp, p)
		}
	}

	return rp, nil
}

// indexPerson adds all name variants for a player to the resolved maps.
func indexPerson(rp *ResolvedPlayers, p MLBPerson) {
	rp.ByID[p.ID] = p.FullName

	fullNorm := Normalize(p.FullName)
	if fullNorm != "" {
		rp.ByName[fullNorm] = p.ID
	}
	if p.FirstName != "" && p.LastName != "" {
		legalNorm := Normalize(p.FirstName + " " + p.LastName)
		if legalNorm != fullNorm && legalNorm != "" {
			rp.ByName[legalNorm] = p.ID
		}
	}
	if p.UseName != "" && p.LastName != "" {
		useNorm := Normalize(p.UseName + " " + p.LastName)
		if useNorm != fullNorm && useNorm != "" {
			rp.ByName[useNorm] = p.ID
		}
	}
}

// searchMLBBatch searches the MLB Stats API for multiple names in one call.
func searchMLBBatch(names []string) ([]MLBPerson, error) {
	escaped := make([]string, len(names))
	for i, n := range names {
		escaped[i] = url.QueryEscape(n)
	}
	u := fmt.Sprintf(mlbSearchURL, strings.Join(escaped, ","))
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		People []MLBPerson `json:"people"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.People, nil
}

// fetchPeople bulk-fetches player details by MLBAM IDs.
func fetchPeople(ids []int) ([]MLBPerson, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	u := fmt.Sprintf(mlbPeopleURL, strings.Join(parts, ","))
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		People []MLBPerson `json:"people"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.People, nil
}
