package playername

import (
	"context"
	"net/http"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	mlb "github.com/pmurley/go-mlb"
	"github.com/pmurley/go-mlb/models"
)

// mlbBaseURL is the MLB Stats API host (without /api/ — go-mlb appends that).
// Var for test override.
var mlbBaseURL = "https://statsapi.mlb.com"

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

	client := mlb.NewClient(
		mlb.WithBaseURL(mlbBaseURL),
		mlb.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
		mlb.WithCache(nil),
	)
	ctx := context.Background()

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

	// Batch search across MLB + affiliated minors + winter ball
	// (sportIds 11/12/13/14 = AAA/AA/A+/A, 16 = winter, 1 = MLB).
	const searchBatchSize = 25
	idSet := make(map[int]bool)
	var ids []int
	for i := 0; i < len(searchNames); i += searchBatchSize {
		end := i + searchBatchSize
		if end > len(searchNames) {
			end = len(searchNames)
		}
		people, err := client.People.Search(ctx,
			mlb.WithNames(searchNames[i:end]...),
			mlb.WithQueryParam("sportIds", "11,12,13,14,16,1"),
		)
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

	// Bulk-fetch full person details (firstName, useName, lastName) so we can
	// index both legal and use-name variants.
	const peopleBatchSize = 500
	for i := 0; i < len(ids); i += peopleBatchSize {
		end := i + peopleBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		people, err := client.People.List(ctx, ids[i:end],
			mlb.WithFields("people", "id", "fullName", "firstName", "lastName", "useName", "useLastName"),
		)
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
func indexPerson(rp *ResolvedPlayers, p models.Person) {
	rp.ByID[p.ID] = p.FullName

	fullNorm := Normalize(p.FullName)
	if fullNorm != "" {
		rp.ByName[fullNorm] = p.ID
	}
	first := derefStr(p.FirstName)
	last := derefStr(p.LastName)
	use := derefStr(p.UseName)

	if first != "" && last != "" {
		legalNorm := Normalize(first + " " + last)
		if legalNorm != fullNorm && legalNorm != "" {
			rp.ByName[legalNorm] = p.ID
		}
	}
	if use != "" && last != "" {
		useNorm := Normalize(use + " " + last)
		if useNorm != fullNorm && useNorm != "" {
			rp.ByName[useNorm] = p.ID
		}
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
