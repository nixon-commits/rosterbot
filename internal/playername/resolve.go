package playername

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	mlb "github.com/pmurley/go-mlb"
	"github.com/pmurley/go-mlb/models"
	"golang.org/x/sync/errgroup"
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
// MLB Stats API. Results are cached at
// `.cache/mlb-player-ids-<sha8>.json` where sha8 is a short hash of the
// (sorted, deduped) input names. Keying on the input set means a roster
// addition produces a new cache file and the new player is resolved
// promptly — the previous shared `mlb-player-ids` key returned the LAST
// caller's resolution regardless of who asked, leaving newly-rostered
// prospects unresolved until the 7-day TTL expired.
//
// For each resolved player, both name variants (legal firstName and common
// useName combined with lastName) are indexed so cross-source matching works.
func ResolveMLBAMIDs(names []string, cacheDir string) (*ResolvedPlayers, error) {
	fc := cache.New[*ResolvedPlayers](cacheDir, cacheTTL)
	key := cache.Key("mlb-player-ids", namesHash(names))
	return fc.Get(key, func() (*ResolvedPlayers, error) {
		return fetchAndResolve(names)
	})
}

// namesHash returns a short hex hash of the deduped, sorted, normalized
// name set so that identical inputs produce the same cache key while
// different inputs miss properly.
func namesHash(names []string) string {
	seen := map[string]bool{}
	uniq := make([]string, 0, len(names))
	for _, n := range names {
		k := Normalize(n)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, k)
	}
	sort.Strings(uniq)
	h := sha256.New()
	for _, n := range uniq {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
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
	// Batches run in parallel because each MLB People.Search call is
	// network-bound and the API tolerates concurrent requests; serial
	// batches were the dominant cost on cold runs (~30s for ~100 names).
	const searchBatchSize = 25
	idSet := make(map[int]bool)
	var ids []int
	var idMu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // courteous parallelism; MLB statsapi is fine with this.
	for i := 0; i < len(searchNames); i += searchBatchSize {
		end := i + searchBatchSize
		if end > len(searchNames) {
			end = len(searchNames)
		}
		batch := searchNames[i:end]
		g.Go(func() error {
			people, err := client.People.Search(gctx,
				mlb.WithNames(batch...),
				mlb.WithQueryParam("sportIds", "11,12,13,14,16,1"),
			)
			if err != nil {
				return nil // soft-fail mirrors the prior `continue` semantics
			}
			idMu.Lock()
			defer idMu.Unlock()
			for _, p := range people {
				if !idSet[p.ID] {
					idSet[p.ID] = true
					ids = append(ids, p.ID)
				}
			}
			return nil
		})
	}
	_ = g.Wait()

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
