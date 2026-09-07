//go:build diag

// Live acceptance harness for rosterbot-1x8, extended for rosterbot-gcjh with
// an age cross-check that can fail honestly on a real index defect. Build-tagged
// so it never runs in CI: it needs the network and a name list, so it can only
// ever be run by hand.
//
//	DIAG_NAMES="$(cat names.txt)" go test -tags diag ./internal/playername/ \
//	    -run TestDiagSeasonIndexParity -v -timeout 30m
//
// Or, for the larger age-adjudicated sweep (rosterbot-gcjh): build a reference
// file mapping playername.Normalize(name) -> {"age": N, "club": "ABC"} from a
// Fantrax player pool snapshot (see diagPoolSample's doc comment for how) and
// run:
//
//	DIAG_POOL_JSON=/path/to/pool-ref.json go test -tags diag ./internal/playername/ \
//	    -run TestDiagSeasonIndexParity -v -timeout 30m
//
// (optionally DIAG_POOL_SAMPLE=<n> to cap the sweep to a deterministic subset
// when the full pool is too slow for a hand-run's time budget)
//
// It reports resolved / ambiguous / missed for index-only, search-only and the
// combined path, plus every ID on which the two disagree — a disagreement means
// the index returns a DIFFERENT player than today's resolver rather than merely
// a faster one. When a reference file is supplied, every disagreement is also
// adjudicated against it by age (see adjudicateByAge in adjudicate_test.go): a
// searchCorrect verdict — the search candidate's age matches the reference and
// the index candidate's doesn't — fails the test, because that is a real index
// defect rather than a coverage regression. It also asserts the two live
// invariants the design rests on: that no dump row reports active:false (which
// is why claimName cannot arbitrate inside the index), and that the season dump
// clears seasonIndexMinPlayers.
//
// 2026-09-07 (rosterbot-gcjh): ran with a 5,000-name deterministic sample drawn
// from a 10,429-row Fantrax pool snapshot (2026-08-17) and FOUND 4 real index
// defects the 2026-08-31 hand-check's smaller sample missed. See the dated
// result recorded at the bottom of this file.
package playername

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	mlb "github.com/pmurley/go-mlb"
	"github.com/pmurley/go-mlb/models"
)

// poolRef is one reference row built from a Fantrax player pool snapshot: the
// independent ground truth the age cross-check adjudicates disagreements
// against. See diagPoolSample for the file shape and how to build one.
type poolRef struct {
	Age  int    `json:"age"`
	Club string `json:"club"`
}

// diagPoolSample loads DIAG_POOL_JSON (a {playername.Normalize(name): {"age":
// N, "club": "ABC"}} reference file) and returns a deterministic name sample
// plus the reference map, or (nil, nil) if the env var is unset — the caller
// falls back to diagNames(t) in that case.
//
// The reference file is deliberately NOT committed (it is derived from a
// point-in-time Fantrax pool export and would go stale immediately) and there
// is no committed generator script either, since this harness never runs in
// CI. To rebuild one from a Fantrax player-pool cache file (rows carrying
// Name / Age / MLBTeamShortName, as `internal/fantrax`'s pool fetch caches
// them), drop this into a scratch main.go and `go run` it against a copy of
// the cache file:
//
//	package main
//
//	import (
//		"encoding/json"
//		"os"
//
//		"github.com/nixon-commits/rosterbot/internal/playername"
//	)
//
//	func main() {
//		var pool struct {
//			Data []struct {
//				Name            string `json:"Name"`
//				Age             int    `json:"Age"`
//				MLBTeamShortName string `json:"MLBTeamShortName"`
//			} `json:"data"`
//		}
//		raw, _ := os.ReadFile(os.Args[1])
//		_ = json.Unmarshal(raw, &pool)
//		out := map[string]struct {
//			Age  int    `json:"age"`
//			Club string `json:"club"`
//		}{}
//		for _, p := range pool.Data {
//			k := playername.Normalize(p.Name)
//			if k == "" {
//				continue
//			}
//			out[k] = struct {
//				Age  int    `json:"age"`
//				Club string `json:"club"`
//			}{Age: p.Age, Club: p.MLBTeamShortName}
//		}
//		enc, _ := json.Marshal(out)
//		os.Stdout.Write(enc)
//	}
//
//	go run ./scratch/poolref pool.json > pool-ref.json
//
// DIAG_POOL_SAMPLE caps the sweep to a deterministic subset (seeded shuffle,
// fixed seed, then re-sorted) when the full pool is too slow for the
// ~15-30 minute budget a hand-run affords; omit it to sweep every name in the
// file. Seeded rather than time-based so a repeated run samples the identical
// subset and is directly comparable to an earlier one.
func diagPoolSample(t *testing.T) ([]string, map[string]poolRef) {
	path := os.Getenv("DIAG_POOL_JSON")
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("DIAG_POOL_JSON: reading %s: %v", path, err)
	}
	var ref map[string]poolRef
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatalf("DIAG_POOL_JSON: parsing %s: %v", path, err)
	}
	names := make([]string, 0, len(ref))
	for n := range ref {
		names = append(names, n)
	}
	sort.Strings(names)

	sampleSize := len(names)
	if raw := os.Getenv("DIAG_POOL_SAMPLE"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil {
			t.Fatalf("DIAG_POOL_SAMPLE: %v", convErr)
		}
		sampleSize = n
	}
	if sampleSize < len(names) {
		r := rand.New(rand.NewSource(20260907))
		r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		names = names[:sampleSize]
		sort.Strings(names)
	}
	t.Logf("pool sample: %d name(s) from %s (of %d total)", len(names), path, len(ref))
	return names, ref
}

// disagreement is one name where the index and search paths answer with
// different MLBAM IDs.
type disagreement struct {
	name          string
	idxID, srchID int
}

// ageLookupRetry mirrors searchWithRetry's shape (internal/playername/resolve.go)
// for the People.List endpoint: a batched personIds= lookup can transiently
// fail too, and the existing retry constants (searchAttempts,
// searchRetryBackoff) are the established precedent rather than a bare
// single-shot call.
func ageLookupRetry(ctx context.Context, client *mlb.Client, ids []int) ([]models.Person, error) {
	var lastErr error
	for attempt := 0; attempt < searchAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(searchRetryBackoff * time.Duration(attempt)):
			}
		}
		people, err := client.People.List(ctx, ids, mlb.WithFields("people", "id", "currentAge"))
		if err == nil {
			return people, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// fetchAges batches a personIds= lookup for CurrentAge only, for exactly the
// IDs a disagreement involves.
//
// This deliberately does NOT read CurrentAge off the season-dump rows
// (fetchSeasonSport, seasonindex.go) — that field mask is a measured,
// load-bearing production constant (a 7.1x payload reduction) that excludes
// currentAge on purpose, and widening it to serve this diag harness would be
// a production behavior change smuggled in as a test-only one. A scoped
// personIds= lookup bypasses the mask entirely and stays cheap: disagreements
// are rare (~5 per 1,000 names, measured 2026-08-31), so even a multi-
// thousand-name sweep asks for only a couple hundred IDs at most.
func fetchAges(ctx context.Context, client *mlb.Client, ids []int) map[int]int {
	out := map[int]int{}
	seen := map[int]bool{}
	var uniq []int
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	const batchSize = 500
	for i := 0; i < len(uniq); i += batchSize {
		end := i + batchSize
		if end > len(uniq) {
			end = len(uniq)
		}
		people, err := ageLookupRetry(ctx, client, uniq[i:end])
		if err != nil {
			continue
		}
		for _, p := range people {
			if p.CurrentAge != nil {
				out[p.ID] = *p.CurrentAge
			}
		}
	}
	return out
}

func TestDiagSeasonIndexParity(t *testing.T) {
	names, poolRefs := diagPoolSample(t)
	if names == nil {
		names = diagNames(t)
	}
	ctx := context.Background()
	client := newDiagClient()
	year := seasonNow()

	// Build straight from the dumps rather than through loadSeasonIndex, so the
	// active-row and population invariants can be checked on the raw rows.
	ix := seasonIndex{
		byKey:    map[string][]int{},
		byPlayer: map[int][]string{},
		names:    map[int]string{},
	}
	for _, sport := range seasonIndexSports {
		people, err := fetchSeasonSport(ctx, client, sport, year, "")
		if err != nil {
			t.Fatalf("sport %d: %v", sport, err)
		}
		t.Logf("sport %2d: %d rows", sport, len(people))
		for _, p := range people {
			if p.Active == nil || !*p.Active {
				t.Errorf("dump row %d (%s) reports active:false — the index's active tier is assumed inert, so claimName cannot arbitrate here", p.ID, p.FullName)
			}
		}
		sub := buildSeasonIndex(people)
		for k, ids := range sub.byKey {
			for _, id := range ids {
				ix.add(k, id)
			}
		}
		for id, n := range sub.names {
			if _, had := ix.names[id]; !had {
				ix.players++
			}
			ix.names[id] = n
		}
	}
	t.Logf("index: %d players, %d keys, %d contested key(s), season %d", ix.players, len(ix.byKey), ix.contestedKeys(), year)
	if ix.players < seasonIndexMinPlayers {
		t.Fatalf("season %d dump holds %d players, below the %d floor — the index would be refused", year, ix.players, seasonIndexMinPlayers)
	}

	// INDEX-ONLY.
	var idxResolved, idxContested, idxMissed int
	indexID := map[string]int{}
	var contestedNames, missedNames []string
	for _, n := range names {
		id, _, res := ix.find(n)
		switch res {
		case findFound:
			idxResolved++
			indexID[Normalize(n)] = id
		case findContested:
			idxContested++
			contestedNames = append(contestedNames, n)
		default:
			idxMissed++
			missedNames = append(missedNames, n)
		}
	}
	sort.Strings(contestedNames)
	sort.Strings(missedNames)
	t.Logf("INDEX-ONLY   resolved=%d ambiguous=%d missed=%d", idxResolved, idxContested, idxMissed)
	t.Logf("  contested: %v", contestedNames)
	t.Logf("  missed:    %v", missedNames)

	// SEARCH-ONLY: the unmodified resolver, with the index disabled by pinning
	// the season to a year statsapi has no dump for.
	oldSeason := seasonNow
	seasonNow = func() int { return year + 2 }
	resetSeasonIndexMemo()
	searchRP, err := ResolveMLBAMIDsNoCache(names)
	seasonNow = oldSeason
	resetSeasonIndexMemo()
	if err != nil {
		t.Fatalf("search-only: %v", err)
	}
	searchResolved := 0
	for _, n := range names {
		if _, ok := searchRP.ByName[Normalize(n)]; ok {
			searchResolved++
		}
	}
	t.Logf("SEARCH-ONLY  resolved=%d missed=%d", searchResolved, len(names)-searchResolved)

	// COMBINED: the shipped path.
	combinedRP, err := ResolveMLBAMIDsNoCache(names)
	if err != nil {
		t.Fatalf("combined: %v", err)
	}
	combinedResolved := 0
	var stillMissed []string
	for _, n := range names {
		if _, ok := combinedRP.ByName[Normalize(n)]; ok {
			combinedResolved++
		} else {
			stillMissed = append(stillMissed, n)
		}
	}
	sort.Strings(stillMissed)
	t.Logf("COMBINED     resolved=%d missed=%d", combinedResolved, len(names)-combinedResolved)
	t.Logf("  missed:    %v", stillMissed)

	// The load-bearing figure: where the two paths both answer, do they answer
	// the same player?
	var disagreements []disagreement
	for _, n := range names {
		k := Normalize(n)
		a, okA := indexID[k]
		b, okB := searchRP.ByName[k]
		if okA && okB && a != b {
			disagreements = append(disagreements, disagreement{name: n, idxID: a, srchID: b})
		}
	}
	sort.Slice(disagreements, func(i, j int) bool { return disagreements[i].name < disagreements[j].name })
	t.Logf("DISAGREEMENTS index-vs-search: %d", len(disagreements))
	for _, d := range disagreements {
		t.Logf("  %s: index=%d(%s) search=%d(%s)", d.name, d.idxID, ix.names[d.idxID], d.srchID, searchRP.ByID[d.srchID])
	}

	// AGE CROSS-CHECK (rosterbot-gcjh): only runs when a reference file was
	// supplied, since without independent ground truth a disagreement can only
	// be reported, never adjudicated. A disagreement with no reference-file
	// entry (a name outside the sampled pool) is logged as unresolved rather
	// than silently skipped.
	switch {
	case len(disagreements) == 0:
		// nothing to adjudicate
	case poolRefs == nil:
		t.Logf("age cross-check: skipped (DIAG_POOL_JSON not set) — %d disagreement(s) reported above but not adjudicated", len(disagreements))
	default:
		var ids []int
		for _, d := range disagreements {
			ids = append(ids, d.idxID, d.srchID)
		}
		ages := fetchAges(ctx, client, ids)
		counts := map[verdict]int{}
		var searchCorrectCases []string
		for _, d := range disagreements {
			ref, ok := poolRefs[Normalize(d.name)]
			if !ok {
				counts[ageUnknown]++
				t.Logf("  %s: no reference-file entry — ageUnknown", d.name)
				continue
			}
			var idxAge, searchAge *int
			if a, ok2 := ages[d.idxID]; ok2 {
				idxAge = &a
			}
			if a, ok2 := ages[d.srchID]; ok2 {
				searchAge = &a
			}
			v := adjudicateByAge(ref.Age, idxAge, searchAge)
			counts[v]++
			t.Logf("  %s: referenceAge=%d idxAge=%s searchAge=%s -> %s",
				d.name, ref.Age, fmtAgePtr(idxAge), fmtAgePtr(searchAge), v)
			if v == searchCorrect {
				searchCorrectCases = append(searchCorrectCases, fmt.Sprintf(
					"%s: reference age %d matches search candidate %d (age %s) but not index candidate %d (age %s)",
					d.name, ref.Age, d.srchID, fmtAgePtr(searchAge), d.idxID, fmtAgePtr(idxAge)))
			}
		}
		t.Logf("ADJUDICATION verdicts: indexCorrect=%d searchCorrect=%d bothMatch=%d neitherMatch=%d ageUnknown=%d",
			counts[indexCorrect], counts[searchCorrect], counts[bothMatch], counts[neitherMatch], counts[ageUnknown])
		// searchCorrect is the gap rosterbot-gcjh closes: unlike a plain
		// disagreement, it means the age cross-check found the SEARCH path
		// right and the INDEX path wrong — a real index defect, not merely an
		// unresolved finding for a human to adjudicate by hand.
		for _, c := range searchCorrectCases {
			t.Errorf("age cross-check found a real index defect: %s", c)
		}
	}

	// The load-bearing regression bar is coverage, which is checkable without
	// judgement.
	if combinedResolved < searchResolved {
		t.Errorf("combined resolved %d < search-only %d — the change is a regression", combinedResolved, searchResolved)
	}
}

// 2026-09-07 result (rosterbot-gcjh): ran the extended harness with
// DIAG_POOL_JSON pointing at a reference file built from a 10,429-row Fantrax
// pool snapshot (fantrax-player-pool-epsb8xzlmj203yrx.json, fetched_at
// 2026-08-17; 10,222 unique normalized keys after 207 duplicate-key
// collisions), DIAG_POOL_SAMPLE=5000 (seeded deterministic subset, seed
// 20260907), wall time ~8.5 minutes:
//
//	INDEX-ONLY   resolved=2770 ambiguous=23  missed=2207
//	SEARCH-ONLY  resolved=4650 missed=350
//	COMBINED     resolved=4713 missed=287        (>= search-only: no regression)
//	DISAGREEMENTS index-vs-search: 23
//	ADJUDICATION indexCorrect=15 searchCorrect=4 bothMatch=3 neitherMatch=1 ageUnknown=0
//
// This REPLACES the earlier 2026-08-31 hand-check's conclusion. That check —
// 5 disagreements over a 1,000-name sample, adjudicated by hand — found "no
// case where the search was right and the index wrong." This 5,000-name sweep,
// adjudicated the same way (Fantrax's Age column) but automatically and at 5x
// the sample, found 4:
//
//	antonio jimenez:  reference age 25, index candidate 806128 age 22, search candidate 682607 age 25
//	isaiah jackson:   reference age 24, index candidate 805365 age 22, search candidate 694722 age 24
//	jose devers:      reference age 26, index candidate 691410 age 23, search candidate 672701 age 26
//	michael martinez: reference age 27, index candidate 824765 age 19, search candidate 681597 age 27
//
// This is a FINDING TO REPORT, not a defect fixed by this bead (rosterbot-gcjh
// scoped the age cross-check itself, not a repair to seasonIndex.find's
// uncontested-key selection). It shows the 2026-08-31 hand-check's "no case
// found" was a sample-size artifact, not a property of the design: the season
// index's uncontested-key answer is sometimes the WRONG namesake, at a rate of
// roughly 4-in-5000 (~0.08%) names on this sweep — small, but no longer zero.
// A follow-up bead should decide whether that rate is acceptable or whether
// seasonIndex.find needs an arbitration signal beyond "uncontested" (the
// existing doc comment on claimName explains why active-status can't be that
// signal here).
//
// Limitation, stated plainly: Fantrax Age is a snapshot as of the pool file's
// fetch time, not live, and the ±1yr tolerance (adjudicateByAge,
// adjudicate_test.go) exists to absorb ordinary birthday drift between that
// snapshot and whenever statsapi is asked — it can in principle swallow a
// smaller real defect as a false bothMatch, and the 3 bothMatch cases above are
// exactly the disagreements this limitation leaves unresolved. This is a
// heuristic cross-check, not a proof. Regenerating the reference file from a
// fresher pool snapshot and re-running (same DIAG_POOL_SAMPLE seed) is the way
// to check whether the rate holds up over time.
