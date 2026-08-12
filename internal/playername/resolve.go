package playername

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// errDegraded marks a resolution that could not complete. Such a result is
// returned to the caller but never persisted — see ResolveMLBAMIDs.
var errDegraded = errors.New("resolution degraded: an upstream search batch failed after retries")

// searchAttempts / searchRetryBackoff bound the per-batch retry. Backoff is a
// var so tests need not sleep.
const searchAttempts = 3

var searchRetryBackoff = 250 * time.Millisecond

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

	// A degraded result is still worth using for this run — the names that DID
	// resolve are correct — but it must not reach the cache. fetchAndResolve
	// signals that by returning errDegraded alongside the partial map; handing
	// the error to cache.Get is what suppresses the write, since Get only saves
	// when fetch returns nil. The partial map is then returned out-of-band.
	//
	// This is the whole fix for rosterbot-i52: the failure itself is transient
	// and unavoidable, but caching it converted a ~150ms blip into a wrong
	// answer served for 7 days, with no signal that anything had happened.
	var degraded *ResolvedPlayers
	rp, err := fc.Get(key, func() (*ResolvedPlayers, error) {
		out, ferr := fetchAndResolve(names)
		if errors.Is(ferr, errDegraded) {
			degraded = out
			return nil, ferr
		}
		return out, ferr
	})
	if degraded != nil {
		return degraded, nil
	}
	return rp, err
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
	// claimed[key] reports whether the player currently holding that name key
	// is active — see claimName.
	claimed := make(map[string]bool)

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

	// Pass 1: the names exactly as the source spelled them.
	ids, failedBatches := searchIDs(ctx, client, searchNames)
	hydrate(ctx, client, ids, rp, claimed)

	// Pass 2: retry whatever pass 1 missed using the normalized spelling.
	//
	// MLB's people-search matches the literal string, and neither spelling is a
	// superset of the other (measured 2026-08-11): "A.J. Blubaugh" and
	// "Zach Cole Jr." return nothing raw but resolve normalized, because
	// Normalize strips periods and generational suffixes; "Ha-Seong Kim" is the
	// reverse, since Normalize turns its hyphen into a space. Searching raw
	// first and normalized only for the leftovers covers both, and costs extra
	// requests only for names that already failed.
	if retry := unresolvedNormalized(names, rp); len(retry) > 0 {
		retryIDs, retryFailed := searchIDs(ctx, client, retry)
		hydrate(ctx, client, retryIDs, rp, claimed)
		failedBatches += retryFailed
	}

	if failedBatches > 0 {
		return rp, fmt.Errorf("%w (%d batch(es))", errDegraded, failedBatches)
	}
	return rp, nil
}

// unresolvedNormalized returns the normalized spellings of input names that are
// still unresolved and whose normalized form actually differs from the raw one
// (re-searching an identical string would just repeat pass 1).
func unresolvedNormalized(names []string, rp *ResolvedPlayers) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		norm := Normalize(n)
		if norm == "" || norm == n || seen[norm] {
			continue
		}
		if _, ok := rp.ByName[norm]; ok {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out
}

// searchIDs resolves names to MLBAM IDs via batched people-search, returning the
// unique IDs found and how many batches failed even after retries.
//
// Batches run in parallel because each call is network-bound and the API
// tolerates concurrency; serial batches were the dominant cost on cold runs
// (~30s for ~100 names). The search spans MLB plus affiliated minors and winter
// ball (sportIds 11/12/13/14 = AAA/AA/A+/A, 16 = winter, 1 = MLB).
func searchIDs(ctx context.Context, client *mlb.Client, searchNames []string) ([]int, int) {
	const searchBatchSize = 25
	idSet := make(map[int]bool)
	var ids []int
	var idMu sync.Mutex
	var failedBatches int

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // courteous parallelism; MLB statsapi is fine with this.
	for i := 0; i < len(searchNames); i += searchBatchSize {
		end := i + searchBatchSize
		if end > len(searchNames) {
			end = len(searchNames)
		}
		batch := searchNames[i:end]
		g.Go(func() error {
			people, err := searchWithRetry(gctx, client, batch)
			idMu.Lock()
			defer idMu.Unlock()
			if err != nil {
				// Recorded rather than swallowed: dropping a batch silently loses
				// up to searchBatchSize names with no trace, and the caller has no
				// way to tell that from "these players don't exist".
				failedBatches++
				return nil
			}
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
	return ids, failedBatches
}

// hydrate bulk-fetches full person details for ids and indexes every name
// variant, so legal and common spellings both resolve.
func hydrate(ctx context.Context, client *mlb.Client, ids []int, rp *ResolvedPlayers, claimed map[string]bool) {
	const peopleBatchSize = 500
	for i := 0; i < len(ids); i += peopleBatchSize {
		end := i + peopleBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		// "active" is load-bearing, not cosmetic: claimName resolves namesake
		// collisions with it, and a fields mask that omits it silently returns
		// nil for every player, collapsing the rule back to lower-ID-wins.
		people, err := client.People.List(ctx, ids[i:end],
			mlb.WithFields("people", "id", "fullName", "firstName", "lastName", "useName", "useLastName", "active"),
		)
		if err != nil {
			continue
		}
		for _, p := range people {
			indexPerson(rp, p, claimed)
		}
	}
}

// searchWithRetry runs one people-search batch, retrying a transient failure
// before giving up on all searchBatchSize of its names.
func searchWithRetry(ctx context.Context, client *mlb.Client, batch []string) ([]models.Person, error) {
	var lastErr error
	for attempt := 0; attempt < searchAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(searchRetryBackoff * time.Duration(attempt)):
			}
		}
		people, err := client.People.Search(ctx,
			mlb.WithNames(batch...),
			mlb.WithQueryParam("sportIds", "11,12,13,14,16,1"),
		)
		if err == nil {
			return people, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// indexPerson adds all name variants for a player to the resolved maps.
//
// claimed tracks, per name key, whether the player currently holding it is
// active; it is what makes a namesake collision resolve deterministically
// instead of last-write-wins. Pass a fresh map alongside a fresh
// ResolvedPlayers.
func indexPerson(rp *ResolvedPlayers, p models.Person, claimed map[string]bool) {
	rp.ByID[p.ID] = p.FullName

	fullNorm := Normalize(p.FullName)
	if fullNorm != "" {
		claimName(rp, claimed, fullNorm, p)
	}
	first := derefStr(p.FirstName)
	last := derefStr(p.LastName)
	use := derefStr(p.UseName)

	if first != "" && last != "" {
		legalNorm := Normalize(first + " " + last)
		if legalNorm != fullNorm && legalNorm != "" {
			claimName(rp, claimed, legalNorm, p)
		}
	}
	if use != "" && last != "" {
		useNorm := Normalize(use + " " + last)
		if useNorm != fullNorm && useNorm != "" {
			claimName(rp, claimed, useNorm, p)
		}
	}
}

// claimName assigns key → p.ID, resolving collisions in favour of the player a
// fantasy roster could plausibly mean (rosterbot-bms).
//
// MLB's people-search deliberately spans historical players and every affiliated
// level, so common names collide with retired namesakes: measured 2026-08-11,
// "Jose Altuve" resolved to a catcher who last played in the 2000s rather than
// the active second baseman, and "Jose Ramirez"/"David Peterson" the same way.
// Unconditional assignment made the winner whichever arrived last, which — with
// the search batches running in parallel — was not even stable between runs.
//
// The rule: an active player always beats an inactive one; between two players
// of equal standing the lower ID wins, purely so the result is deterministic.
// Two ACTIVE namesakes are genuinely ambiguous from a name alone (MLB has had
// two active Will Smiths), and this only guarantees the same answer every run,
// not the right one.
func claimName(rp *ResolvedPlayers, claimed map[string]bool, key string, p models.Person) {
	active := p.Active != nil && *p.Active
	prevID, exists := rp.ByName[key]
	if !exists {
		rp.ByName[key] = p.ID
		claimed[key] = active
		return
	}
	if claimed[key] != active {
		if active {
			rp.ByName[key] = p.ID
			claimed[key] = true
		}
		return
	}
	if p.ID < prevID {
		rp.ByName[key] = p.ID
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
