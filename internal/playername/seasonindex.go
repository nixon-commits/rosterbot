package playername

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	mlb "github.com/pmurley/go-mlb"
	"github.com/pmurley/go-mlb/models"
	"golang.org/x/sync/errgroup"
)

// seasonIndexSports are the sport levels the bulk index is built from: 1 = MLB,
// 11/12/13/14 = AAA/AA/A+/A.
//
// The search path (searchWithRetry) also spans sportId 16 (winter ball); the
// index deliberately does not. An extra level can only ADD candidates to a name
// key, i.e. produce more contested keys and therefore LESS index coverage,
// while adding no correctness — every name the index declines is answered by
// the search anyway. A var rather than a const so the diag harness can widen it
// and re-measure before anyone changes this.
var seasonIndexSports = []int{1, 11, 12, 13, 14}

// seasonIndexTTL matches the once-daily cadence the other bulk upstreams use
// (projections.ProjectionCacheTTL, statcast.CacheTTL): a season roster moves on
// the scale of transactions, not minutes.
const seasonIndexTTL = 24 * time.Hour

// keyMLBSeasonPlayers is the cache-key prefix for one sport's season dump, so
// the prefix has a single owner (the keySavant / keyFanGraphs convention).
const keyMLBSeasonPlayers = "mlb-season-players"

// seasonIndexMinPlayers is the floor below which a cleanly-fetched dump is
// REFUSED rather than indexed.
//
// The guard exists because a partly-populated season answers confidently wrong
// from a fetch that fully succeeded, which is the same defect loadSeasonIndex
// refuses a partial FETCH for, arriving by way of time instead of failure.
// Measured live 2026-08-31, unique players across the five sports:
//
//	2022 5965 · 2023 5875 · 2024 5768 · 2025 5847 · 2026 5890 · 2027 4205 · 2028 0
//
// The 2027 dump — a season that has not begun — fetches cleanly with 4,205
// players and DECONTESTS both known collision keys: `thomas white` reads
// uncontested at 695720 (Tommy White) where the 2026 index correctly declines
// in favour of 806258, and `luis garcia` reads uncontested at 671277 where 2026
// declines against 472610. So the pre-season dump is not merely sparse; it is
// an index that answers, and answers wrongly.
//
// 5000 sits 13% below the smallest completed season (5,768) and 19% above the
// pre-season dump (4,205). The in-season ramp — how fast a new season's dump
// fills through April — is UNMEASURED, because statsapi keeps no history of it.
// The floor is set so that the unmeasured direction is safe: a real season dump
// that has not yet crossed 5,000 is refused and every name falls through to the
// search, which is slower and exactly as correct as today. The floor can never
// turn a refusal into a wrong answer.
//
// A var only so hermetic tests can index a two-row fixture;
// TestSeasonIndexMinPlayers_ProductionFloorIsTheMeasuredOne pins the shipped
// value.
var seasonIndexMinPlayers = 5000

// seasonNow reports the season year the index is built for. A var so tests can
// pin it; deliberately just the current UTC year, with no fallback to last
// season — reaching backwards would reintroduce precisely the retired-namesake
// class (rosterbot-bms) that the season scope removes.
var seasonNow = func() int { return time.Now().UTC().Year() }

// findResult distinguishes the two ways the index can fail to answer, because
// the runtime line has to be able to tell them apart: a contested key means the
// index HAS the name and refuses it, an absent key means it never had it.
type findResult int

const (
	findAbsent findResult = iota
	findContested
	findFound
)

// seasonIndex is a name → MLBAM ID index built from the season player dumps.
//
// The season scope is what makes it worth having: verified live 2026-08-31, the
// retired Jose Altuve (501447) and the inactive Jose Ramirez (691342) are absent
// from all five 2026 dumps while the active 514888 / 608070 are present, so the
// historical namesakes that MLB's people-SEARCH returns cannot appear here at
// all.
//
// It does NOT remove namesake ambiguity, and the bead's claim that it does was
// measured over 133 names rather than over the index. Measured live 2026-08-31
// over all 5,890 unique players: 6,917 keys, of which 55 are contested by two or
// more distinct players.
type seasonIndex struct {
	byKey    map[string][]int
	byPlayer map[int][]string
	names    map[int]string
	players  int
}

// contestedKeys counts the keys held by more than one player. Reported in the
// build line so the property the design rests on is stated at runtime rather
// than only in this comment.
func (ix seasonIndex) contestedKeys() int {
	n := 0
	for _, ids := range ix.byKey {
		if len(ids) > 1 {
			n++
		}
	}
	return n
}

// buildSeasonIndex indexes exactly the three name variants indexPerson already
// uses, so a name that resolves through the search resolves through the index.
//
// useLastName / fullFMLName variants are deliberately absent. Measured live
// 2026-08-31 over the masked dump, adding useName·useLastName and
// firstName·useLastName changes NOTHING — still 6,917 keys and 55 contested —
// because useLastName equals lastName on every row that matters. They would be
// pure surface area.
//
// p.Active is deliberately NOT filtered on: season membership is the filter,
// and measured live all 5,890 dump rows report active:true, so a filter is a
// no-op that could only ever silently drop a row if MLB changed the semantics.
func buildSeasonIndex(people []models.Person) seasonIndex {
	ix := seasonIndex{
		byKey:    make(map[string][]int, len(people)),
		byPlayer: make(map[int][]string, len(people)),
		names:    make(map[int]string, len(people)),
	}
	for _, p := range people {
		if _, seen := ix.names[p.ID]; !seen {
			ix.players++
		}
		ix.names[p.ID] = p.FullName

		full := Normalize(p.FullName)
		ix.add(full, p.ID)
		first, last, use := derefStr(p.FirstName), derefStr(p.LastName), derefStr(p.UseName)
		if first != "" && last != "" {
			ix.add(Normalize(first+" "+last), p.ID)
		}
		if use != "" && last != "" {
			ix.add(Normalize(use+" "+last), p.ID)
		}
	}
	return ix
}

// add records id under key unless it is already there. The already-present check
// is load-bearing: a player appears in several sport dumps at once (Sam
// Antonacci 803011 is in sports 1 and 11 live), and appending unconditionally
// would let him contest his own key and stop resolving.
func (ix seasonIndex) add(key string, id int) {
	if key == "" {
		return
	}
	for _, have := range ix.byKey[key] {
		if have == id {
			return
		}
	}
	ix.byKey[key] = append(ix.byKey[key], id)
	ix.byPlayer[id] = append(ix.byPlayer[id], key)
}

// find answers only an UNCONTESTED key, on the reasoning of hkb.Lookup.Find
// (internal/hkb/lookup.go:67-73): a wrong match is worse than a miss, because a
// miss falls through to the search and costs one batch while a wrong match is
// returned as a confident answer.
//
// The index cannot reuse claimName to arbitrate. Measured live 2026-08-31, all
// 5,890 dump rows report active:true, so claimName's active tier is inert here
// and the rule collapses to lower-ID-wins — which is wrong on real data: key
// `thomas white` holds 695720 (Tommy White, firstName Thomas) and 806258
// (Thomas White), and lower-ID picks 695720 where the production search returns
// 806258. `luis garcia` holds 472610 and 671277 the same way.
func (ix seasonIndex) find(name string) (int, string, findResult) {
	ids := ix.byKey[Normalize(name)]
	switch len(ids) {
	case 1:
		return ids[0], ix.names[ids[0]], findFound
	case 0:
		return 0, "", findAbsent
	default:
		return 0, "", findContested
	}
}

// variantKeys returns the index keys held SOLELY by id, so a matched player can
// be projected into ResolvedPlayers under every spelling the search would have
// given him. Bounded by construction at the three variants buildSeasonIndex
// writes.
//
// The uncontested filter is not redundant with find: a player matched on his
// uncontested fullName key can still share his firstName+lastName variant with
// somebody else, and writing that key would assert an answer the index has no
// right to give.
func (ix seasonIndex) variantKeys(id int) []string {
	var out []string
	for _, k := range ix.byPlayer[id] {
		if ids := ix.byKey[k]; len(ids) == 1 {
			out = append(out, k)
		}
	}
	return out
}

// seasonIndexMemo memoizes one built index per (base URL, season) for the life
// of the process, INCLUDING a failed build.
//
// Both halves matter. recap-site performs ~186 resolutions per build and
// internal/recap fans out per team, so without the memo a cold run pays 186 × 5
// fetches of a 1.2 MB payload; and without memoizing the FAILURE, a statsapi
// outage costs 186 doomed builds and 186 identical "unavailable" lines, which is
// not degrading to noise but to a flood. The negative result is process-scoped
// and never reaches the cache — cache.Get only saves when fetch returns nil
// (internal/cache/cache.go:112).
var (
	seasonIndexMu   sync.Mutex
	seasonIndexMemo = map[string]seasonIndexEntry{}
)

type seasonIndexEntry struct {
	ix  seasonIndex
	err error
}

// resetSeasonIndexMemo clears the process memo. Test support: it is what lets a
// test prove the DISK cache independently of the memo, and what keeps tests
// isolated from one another — httptest ports are recycled by the OS after
// Close, so a later server can otherwise inherit an earlier server's index.
func resetSeasonIndexMemo() {
	seasonIndexMu.Lock()
	defer seasonIndexMu.Unlock()
	seasonIndexMemo = map[string]seasonIndexEntry{}
}

// loadSeasonIndex returns the season index, building it at most once per
// process per (base URL, season).
//
// The lock is held ACROSS the fetches, not merely around the map access. That
// is single-flight, and it is what internal/recap needs: collectTeam fans out at
// opts.Concurrency and every worker reaches ResolveMLBAMIDs, so a lock narrowed
// to map access would have N teams each perform the five fetches on a cold memo
// and hold N copies of the ~6,900-key map at once. The cost of the wider lock is
// that a second caller blocks for one index build.
func loadSeasonIndex(ctx context.Context, client *mlb.Client, year int, cacheDir string) (seasonIndex, error) {
	memoKey := mlbBaseURL + "|" + strconv.Itoa(year)

	seasonIndexMu.Lock()
	defer seasonIndexMu.Unlock()
	if e, ok := seasonIndexMemo[memoKey]; ok {
		return e.ix, e.err
	}

	ix, err := buildSeasonIndexFrom(ctx, client, year, cacheDir)
	seasonIndexMemo[memoKey] = seasonIndexEntry{ix: ix, err: err}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mlb id index: unavailable (%v) — every name falls through to search\n", err)
		return seasonIndex{}, err
	}
	fmt.Fprintf(os.Stderr, "mlb id index: built for season %d — %d players, %d keys, %d contested key(s)\n",
		year, ix.players, len(ix.byKey), ix.contestedKeys())
	return ix, nil
}

// buildSeasonIndexFrom fetches all five sport dumps and builds the index, or
// returns an error having built nothing.
//
// All-or-nothing is load-bearing and measured: with sport 1 dropped, key
// `luis garcia` loses 472610 and reads UNCONTESTED at 671277 — a partial index
// answers confidently wrong exactly where the full one correctly declines. The
// same defect arrives through time rather than failure, which is what
// seasonIndexMinPlayers guards.
func buildSeasonIndexFrom(ctx context.Context, client *mlb.Client, year int, cacheDir string) (seasonIndex, error) {
	people := make([][]models.Person, len(seasonIndexSports))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(len(seasonIndexSports))
	for i, sport := range seasonIndexSports {
		g.Go(func() error {
			got, err := fetchSeasonSport(gctx, client, sport, year, cacheDir)
			if err != nil {
				return fmt.Errorf("sport %d season %d: %w", sport, year, err)
			}
			people[i] = got
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return seasonIndex{}, err
	}

	var all []models.Person
	for _, p := range people {
		all = append(all, p...)
	}
	ix := buildSeasonIndex(all)
	if ix.players < seasonIndexMinPlayers {
		return seasonIndex{}, fmt.Errorf("season %d dump holds only %d players, below the %d floor (a partly-populated season decontests real namesake collisions)", year, ix.players, seasonIndexMinPlayers)
	}
	return ix, nil
}

// fetchSeasonSport reads one sport's season dump, through the cache when the
// caller supplied a directory.
//
// The fields mask is mandatory rather than an optimisation: measured live
// 2026-08-31 the five dumps are 9,076,523 bytes unmasked against 1,270,786
// masked, a 7.1x reduction with no loss of any variant the index uses. On disk
// the five cached artifacts total 2,026,168 bytes — a re-marshal of
// []models.Person, whose Link field is not omitempty — and on Fargate that is
// five S3 objects rewritten once a day.
func fetchSeasonSport(ctx context.Context, client *mlb.Client, sportID, year int, cacheDir string) ([]models.Person, error) {
	key := cache.Key(keyMLBSeasonPlayers, strconv.Itoa(year), strconv.Itoa(sportID))
	return cache.GetOrFetch(cacheDir, key, seasonIndexTTL, func() ([]models.Person, error) {
		return client.Sports.Players(ctx, sportID,
			mlb.WithSeason(year),
			mlb.WithFields("people", "id", "fullName", "firstName", "lastName", "useName", "useLastName", "active"),
		)
	})
}
