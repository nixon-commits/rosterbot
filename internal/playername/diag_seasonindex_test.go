//go:build diag

// Live acceptance harness for rosterbot-1x8. Build-tagged so it never runs in
// CI: it needs the network and a name list, so it can only ever be run by hand.
//
//	DIAG_NAMES="$(cat names.txt)" go test -tags diag ./internal/playername/ \
//	    -run TestDiagSeasonIndexParity -v -timeout 30m
//
// It reports resolved / ambiguous / missed for index-only, search-only and the
// combined path, plus every ID on which the two disagree — a disagreement means
// the index returns a DIFFERENT player than today's resolver rather than merely
// a faster one, so each one has to be adjudicated by hand. It also asserts the two live invariants the design rests on: that no dump
// row reports active:false (which is why claimName cannot arbitrate inside the
// index), and that the season dump clears seasonIndexMinPlayers.
package playername

import (
	"context"
	"fmt"
	"sort"
	"testing"
)

func TestDiagSeasonIndexParity(t *testing.T) {
	names := diagNames(t)
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
	var disagreements []string
	for _, n := range names {
		k := Normalize(n)
		a, okA := indexID[k]
		b, okB := searchRP.ByName[k]
		if okA && okB && a != b {
			disagreements = append(disagreements, fmt.Sprintf("%s: index=%d(%s) search=%d(%s)", n, a, ix.names[a], b, searchRP.ByID[b]))
		}
	}
	sort.Strings(disagreements)
	t.Logf("DISAGREEMENTS index-vs-search: %d", len(disagreements))
	for _, d := range disagreements {
		t.Logf("  %s", d)
	}
	// A disagreement is a FINDING TO ADJUDICATE BY HAND, not automatically a
	// defect, so it is reported loudly and does not fail the run. Measured
	// 2026-08-31 over a 1,000-name random sample of the full 10,229-name Fantrax
	// pool there were 5, all on the minor-league tail; checked against Fantrax's
	// own Age column, the index matched exactly on four (Fernando Perez 22,
	// Miguel Rodriguez 20, Luis Sanchez 22, Jose Geraldo 26) where the search
	// returned a namesake 5-11 years older, and on the fifth (Luis Reyes)
	// neither candidate matched Fantrax's age and the index was merely closer.
	// No case was found where the search was right and the index wrong. Over the
	// 517-name ROSTERED pool there were 0.
	//
	// The regression bar is coverage, which is checkable without judgement.
	if combinedResolved < searchResolved {
		t.Errorf("combined resolved %d < search-only %d — the change is a regression", combinedResolved, searchResolved)
	}
}
