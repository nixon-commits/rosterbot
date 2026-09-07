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
// file mapping playername.Normalize(name) -> a LIST of {"age": N, "club":
// "ABC"} rows — one entry per real Fantrax pool row that normalizes to that
// key, NOT one entry per key — from a Fantrax player pool snapshot (see
// diagPoolSample's doc comment for how) and run:
//
//	DIAG_POOL_JSON=/path/to/pool-ref.json go test -tags diag ./internal/playername/ \
//	    -run TestDiagSeasonIndexParity -v -timeout 30m
//
// (optionally DIAG_POOL_SAMPLE=<n> to cap the sweep to a deterministic subset
// when the full pool is too slow for a hand-run's time budget)
//
// The list shape is load-bearing, not incidental (rosterbot-gcjh): playername.
// Normalize is a many-to-one join key (see the "Deep-pool caveat" bullet in
// CLAUDE.md), so a real Fantrax pool holds keys shared by two or more
// genuinely distinct players — measured on a 10,429-row 2026-08-17 snapshot,
// 169 of 10,222 unique keys hold 2+ rows. A reference file keyed
// {name: {age, club}} (one row, last-write-wins) silently discards every
// collision's other row(s), and a disagreement at one of those keys then gets
// adjudicated against an ARBITRARY one of the real players who share the key
// — which is indistinguishable from a real index defect unless the reference
// itself can say "this key has more than one real answer."
//
// It reports resolved / ambiguous / missed for index-only, search-only and the
// combined path, plus every ID on which the two disagree — a disagreement means
// the index returns a DIFFERENT player than today's resolver rather than merely
// a faster one. When a reference file is supplied, every disagreement is also
// adjudicated against the FULL list of reference ages recorded at its key (see
// adjudicateByAges in adjudicate_test.go): a searchCorrect verdict — the
// search candidate's age matches a reference row and the index candidate's
// doesn't — fails the test, because that is a real index defect rather than a
// coverage regression or a collision the reference key can't disambiguate. A
// key holding more than one reference row where each candidate's age matches
// a DIFFERENT one of those rows adjudicates to ageAmbiguous instead, and an
// ageAmbiguous verdict is NEVER counted as indexCorrect or searchCorrect —
// see adjudicateByAges' doc comment for the full four-way decision, including
// the case where the collision is still decidable. It also asserts the two
// live invariants the design rests on: that no dump row reports active:false
// (which is why claimName cannot arbitrate inside the index), and that the
// season dump clears seasonIndexMinPlayers.
//
// See the dated result recorded at the bottom of this file for the most
// recent sweep and what it actually supports.
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

// diagPoolSample loads DIAG_POOL_JSON (a {playername.Normalize(name): [{"age":
// N, "club": "ABC"}, ...]} reference file, one list entry per real Fantrax
// pool row sharing that normalized key) and returns a deterministic name
// sample plus the reference map, or (nil, nil) if the env var is unset — the
// caller falls back to diagNames(t) in that case.
//
// The list shape (rosterbot-gcjh) is not cosmetic: playername.Normalize
// collapses distinct real players onto the same key (169 of 10,222 keys on a
// real 10,429-row snapshot measured 2026-09-07), and a {name: {age, club}}
// shape can hold only the LAST row parsed for a collided key — silently
// picking an arbitrary one of the real players as "the" reference and making
// every disagreement at that key look like a defect against whichever
// namesake didn't survive the overwrite. Keeping every row is what lets the
// adjudicator (adjudicateByAges) recognize a collision and refuse to call it
// either the index or the search.
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
//	type poolRefRow struct {
//		Age  int    `json:"age"`
//		Club string `json:"club"`
//	}
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
//		// Keep EVERY row per key — do not overwrite. A key with more than one
//		// row means more than one real player shares it; last-write-wins would
//		// silently discard the collision (rosterbot-gcjh).
//		out := map[string][]poolRefRow{}
//		for _, p := range pool.Data {
//			k := playername.Normalize(p.Name)
//			if k == "" {
//				continue
//			}
//			out[k] = append(out[k], poolRefRow{Age: p.Age, Club: p.MLBTeamShortName})
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
func diagPoolSample(t *testing.T) ([]string, map[string][]poolRef) {
	path := os.Getenv("DIAG_POOL_JSON")
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("DIAG_POOL_JSON: reading %s: %v", path, err)
	}
	var ref map[string][]poolRef
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
//
// A whole batch failing after searchAttempts retries used to fold silently
// into "age unknown" for every ID in that batch — indistinguishable, in the
// adjudication summary, from an ID statsapi genuinely has no currentAge for.
// That matters: a transient statsapi hiccup mid-sweep should read as "retry
// the sweep", not as "N names have an unexplainable missing age". fetchAges
// now t.Logf's each failed batch by name and returns the set of IDs it asked
// for in a failed batch, so the caller can count them apart from ordinary
// ageUnknown verdicts.
func fetchAges(ctx context.Context, t *testing.T, client *mlb.Client, ids []int) (ages map[int]int, failedBatchIDs map[int]bool) {
	out := map[int]int{}
	failed := map[int]bool{}
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
		batch := uniq[i:end]
		people, err := ageLookupRetry(ctx, client, batch)
		if err != nil {
			t.Logf("age lookup: batch %d-%d (%d id(s): %v) failed after %d attempt(s): %v",
				i, end-1, len(batch), batch, searchAttempts, err)
			for _, id := range batch {
				failed[id] = true
			}
			continue
		}
		for _, p := range people {
			if p.CurrentAge != nil {
				out[p.ID] = *p.CurrentAge
			}
		}
	}
	return out, failed
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
		ages, failedBatchIDs := fetchAges(ctx, t, client, ids)
		counts := map[verdict]int{}
		var searchCorrectCases []string
		var transientUnknown int // ageUnknown caused by a failed age-lookup batch
		for _, d := range disagreements {
			refs, ok := poolRefs[Normalize(d.name)]
			if !ok {
				counts[ageUnknown]++
				t.Logf("  %s: no reference-file entry — ageUnknown", d.name)
				continue
			}
			refAges := make([]int, len(refs))
			for i, r := range refs {
				refAges[i] = r.Age
			}
			var idxAge, searchAge *int
			if a, ok2 := ages[d.idxID]; ok2 {
				idxAge = &a
			}
			if a, ok2 := ages[d.srchID]; ok2 {
				searchAge = &a
			}
			v := adjudicateByAges(refAges, idxAge, searchAge)
			counts[v]++
			if v == ageUnknown && (failedBatchIDs[d.idxID] || failedBatchIDs[d.srchID]) {
				transientUnknown++
			}
			t.Logf("  %s: referenceAges=%v idxAge=%s searchAge=%s -> %s",
				d.name, refAges, fmtAgePtr(idxAge), fmtAgePtr(searchAge), v)
			if v == searchCorrect {
				searchCorrectCases = append(searchCorrectCases, fmt.Sprintf(
					"%s: reference age(s) %v match search candidate %d (age %s) but not index candidate %d (age %s)",
					d.name, refAges, d.srchID, fmtAgePtr(searchAge), d.idxID, fmtAgePtr(idxAge)))
			}
		}
		t.Logf("ADJUDICATION verdicts: indexCorrect=%d searchCorrect=%d bothMatch=%d neitherMatch=%d ageAmbiguous=%d ageUnknown=%d (of which %d transient: caused by a failed age-lookup batch, not a genuine missing age)",
			counts[indexCorrect], counts[searchCorrect], counts[bothMatch], counts[neitherMatch], counts[ageAmbiguous], counts[ageUnknown], transientUnknown)
		// searchCorrect is the gap rosterbot-gcjh closes: unlike a plain
		// disagreement, it means the age cross-check found the SEARCH path
		// right and the INDEX path wrong at an UNAMBIGUOUS key — a real index
		// defect, not merely an unresolved finding for a human to adjudicate
		// by hand, and not a collision the reference key can't disambiguate
		// (that case adjudicates to ageAmbiguous instead and is never counted
		// here — see adjudicateByAges' doc comment).
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

// 2026-09-07 result, RE-RUN (rosterbot-gcjh, collision-aware reference): a
// same-day earlier run of this harness (see git history for the superseded
// text this replaces) used a reference file shaped {name: {age, club}} —
// exactly one row per normalized key, last-write-wins on a duplicate key —
// and reported 4 "real index defects." That reference shape was itself the
// defect: playername.Normalize collapses distinct real players onto one key
// (measured on the same 10,429-row Fantrax pool snapshot,
// fantrax-player-pool-epsb8xzlmj203yrx.json, fetched_at 2026-08-17: 10,222
// unique keys, 169 of which hold 2+ real rows — the doc comment's earlier
// "207 duplicate-key collisions" was counting excess ROWS [10,429−10,222],
// not collision KEYS), so an index-vs-search disagreement at one of those 169
// keys was being adjudicated against an ARBITRARY one of the several real
// players who share it. Rebuilt the reference to keep every row per key (see
// diagPoolSample's doc comment) and re-ran with the SAME DIAG_POOL_SAMPLE=5000
// seed (20260907) against the SAME pool snapshot, wall time ~36s:
//
//	names asked: 5000
//	INDEX-ONLY   resolved=2770 ambiguous=23  missed=2207
//	SEARCH-ONLY  resolved=4650 missed=350
//	COMBINED     resolved=4713 missed=287        (>= search-only: no regression)
//	DISAGREEMENTS index-vs-search: 23
//	ADJUDICATION verdicts: indexCorrect=11 searchCorrect=0 bothMatch=3 neitherMatch=0 ageAmbiguous=9 ageUnknown=0 (0 transient)
//
// Index-only, search-only and combined resolution counts are byte-identical
// to the earlier same-day run (neither the index nor the search changed —
// only the adjudication logic did), which isolates the entire swing in the
// verdict histogram to the collision fix. All 4 of the earlier run's
// "searchCorrect" cases are among the 9 now classified ageAmbiguous:
//
//	antonio jimenez:  refAges=[22 25] idx=806128(age 22) search=682607(age 25) -> ageAmbiguous
//	isaiah jackson:   refAges=[22 24] idx=805365(age 22) search=694722(age 24) -> ageAmbiguous
//	jose devers:      refAges=[23 26] idx=691410(age 23) search=672701(age 26) -> ageAmbiguous
//	michael martinez: refAges=[19 27] idx=824765(age 19) search=681597(age 27) -> ageAmbiguous
//
// In each case BOTH candidates' ages matched a real row at that key (one row
// each) — exactly the "each candidate matches a different row" shape
// adjudicateByAges refuses to call either way. These were never real index
// defects; they were the reference file's own last-write-wins bug expressing
// itself as a false positive against whichever candidate happened to match
// the row that survived the overwrite.
//
// With the collision fixed, there are ZERO remaining unambiguous
// searchCorrect cases in this sweep — the test PASSES. This restores, rather
// than overturns, the 2026-08-31 hand-check's "no case where the search was
// right and the index wrong" — now on a 5x larger, automated, collision-aware
// sample instead of a 1,000-name hand-check, and finding the same answer.
//
// Two things this result does NOT support, stated plainly because the
// earlier text overclaimed on both:
//
//   - This is a rate over the 23 DISAGREEMENTS, not over the 5,000 names
//     asked, and not even over the 2,770 the index answered. The method can
//     only ever see a defect on a name where the index and the search
//     disagree — the other 2,747 index answers that AGREE with the search are
//     never cross-checked against the reference at all, so "0 defects found"
//     is a FLOOR on the index's real error rate, not a measurement of it: a
//     defect where index and search happen to agree (both wrong the same way,
//     or the search itself resolves the wrong namesake) is invisible to this
//     harness by construction.
//   - The collision caveat is not a rounding error: 9 of the 23 disagreements
//     (39%) were ageAmbiguous — undecidable by this method — and all 4 of the
//     previously-reported "defects" were drawn from exactly that ambiguous
//     set. A rate computed by DIVIDING BY the index-answered population
//     (2770) and IGNORING the ambiguous bucket would still be wrong for the
//     same reason: 0/2770 (or 0/23) reads as a precise measurement where the
//     honest statement is "found none, out of a method that can't see most of
//     the space and refuses to guess on the part that's structurally
//     ambiguous."
//
// Limitation, stated plainly: Fantrax Age is a snapshot as of the pool file's
// fetch time, not live, and the ±1yr tolerance (adjudicateByAges,
// adjudicate_test.go) exists to absorb ordinary birthday drift between that
// snapshot and whenever statsapi is asked — it can in principle swallow a
// smaller real defect as a false bothMatch, and the 3 bothMatch cases above
// are exactly the disagreements this limitation leaves unresolved. This is a
// heuristic cross-check, not a proof. Regenerating the reference file from a
// fresher pool snapshot and re-running (same DIAG_POOL_SAMPLE seed) is the way
// to check whether this floor holds up over time.
