package playername

import (
	"strconv"
	"testing"
)

// verdict classifies the outcome of adjudicating one index-vs-search
// disagreement (internal/playername/diag_seasonindex_test.go) against an
// independent reference age, for rosterbot-gcjh.
//
// This lives in a plain _test.go file with no build tag, on purpose: the
// adjudication logic is pure and hermetic (no network, no diag tag) and runs
// under ordinary `go test ./internal/playername/...` / CI, while the
// network-bound harness that actually calls it stays behind //go:build diag
// and never runs there. Splitting it this way is what lets the harness "fail
// honestly" on a real index defect (searchCorrect) without smuggling network
// dependence into CI.
type verdict int

const (
	ageUnknown verdict = iota
	indexCorrect
	searchCorrect
	bothMatch
	neitherMatch
	// ageAmbiguous is the collision-aware verdict (rosterbot-gcjh): the
	// disagreement's normalized key carries more than one real pool row, and
	// the two candidates match two DIFFERENT rows, so there is no signal left
	// to say which candidate is "correct" for the name that was asked. It
	// must never be counted as indexCorrect or searchCorrect — see
	// adjudicateByAges.
	ageAmbiguous
)

func (v verdict) String() string {
	switch v {
	case indexCorrect:
		return "indexCorrect"
	case searchCorrect:
		return "searchCorrect"
	case bothMatch:
		return "bothMatch"
	case neitherMatch:
		return "neitherMatch"
	case ageAmbiguous:
		return "ageAmbiguous"
	default:
		return "ageUnknown"
	}
}

// ageTolerance absorbs birthday-timing slop between when the Fantrax pool
// snapshot was taken and when statsapi is asked for a candidate's current
// age — a player whose birthday falls in between reads a year older on
// whichever side asked later. It is a stated heuristic limitation, not a
// proven invariant: a real 2-year-or-more age gap is what this is meant to
// catch, and a smaller genuine defect could in principle hide inside the
// tolerance as a false bothMatch. See the dated note in
// diag_seasonindex_test.go for how this plays out on real data.
const ageTolerance = 1

// adjudicateByAge decides, for one index-vs-search disagreement at an
// UNCONTESTED reference key (exactly one pool row for that normalized name),
// which candidate's age (if either) matches the independent reference age
// (fantraxAge) within ageTolerance years.
//
// idxAge / searchAge are nil when that candidate's age could not be fetched
// (never a stand-in for age zero, which is a real infant age). Either side
// unknown makes the whole comparison ageUnknown — there is nothing to
// adjudicate without both ages, and guessing from one side alone would be
// exactly the "confidently wrong" failure mode playername's join logic
// elsewhere in this package is built to avoid (see claimName's doc comment).
//
// This is the single-reference-row instance of adjudicateByAges (below), kept
// as its own name because most disagreements land on an ordinary uncontested
// key and a call site reads more clearly as adjudicateByAge(refAge, ...) than
// as a one-element slice literal.
func adjudicateByAge(fantraxAge int, idxAge, searchAge *int) verdict {
	return adjudicateByAges([]int{fantraxAge}, idxAge, searchAge)
}

// adjudicateByAges is the collision-aware generalization of adjudicateByAge
// (rosterbot-gcjh). The reference file the diag harness builds keys on
// playername.Normalize(name), and that key is not unique: measured over a
// real 10,429-row Fantrax pool snapshot, 169 keys hold two or more distinct
// players (e.g. two different real "antonio jimenez"es). A disagreement at
// one of those keys cannot be adjudicated against a single "the" reference
// age the way a one-row key can — refAges is the WHOLE list of ages recorded
// at that key, and either candidate matching ANY entry in it is a legitimate
// answer, not evidence the OTHER candidate is wrong.
//
// idxAge / searchAge nil (age unfetched) makes the whole comparison
// ageUnknown, same as adjudicateByAge; so does an empty refAges (no
// reference row at all for this key — the harness reports that case
// separately before ever reaching here, but the pure function stays total).
//
// Given both ages, there are exactly four outcomes:
//
//   - Neither candidate's age is within ageTolerance of any entry in refAges:
//     neitherMatch (as before — real regardless of key ambiguity).
//   - Exactly one candidate's age matches an entry in refAges and the other
//     matches none: DECIDABLE even at an ambiguous key. The matching
//     candidate corresponds to at least one of the confirmed real people
//     recorded at this key; the other candidate's age corresponds to NONE of
//     them — it is not simply "the other real namesake", it is an age nobody
//     at this key actually has. That is still meaningful evidence, in the
//     matching candidate's favor: indexCorrect or searchCorrect accordingly.
//   - Both candidates match entries in refAges, and the entries they matched
//     share a value: bothMatch, the single-row case restated (they could
//     both be describing the one real person at this key, or two rows that
//     happen to share an age — either way there is no signal left to prefer
//     one over the other).
//   - Both candidates match entries in refAges, and the entries they matched
//     do NOT share a value: each candidate corresponds to a DIFFERENT real
//     player recorded at this key. There is no way to know which of the
//     several real people the name being adjudicated actually refers to, so
//     this is ageAmbiguous — never indexCorrect or searchCorrect, because
//     "correct" here would claim knowledge the reference data doesn't
//     support.
func adjudicateByAges(refAges []int, idxAge, searchAge *int) verdict {
	if idxAge == nil || searchAge == nil {
		return ageUnknown
	}
	if len(refAges) == 0 {
		return ageUnknown
	}

	idxMatches := matchingAges(refAges, *idxAge)
	searchMatches := matchingAges(refAges, *searchAge)

	switch {
	case len(idxMatches) == 0 && len(searchMatches) == 0:
		return neitherMatch
	case len(idxMatches) > 0 && len(searchMatches) == 0:
		return indexCorrect
	case len(searchMatches) > 0 && len(idxMatches) == 0:
		return searchCorrect
	}

	for age := range idxMatches {
		if searchMatches[age] {
			return bothMatch
		}
	}
	return ageAmbiguous
}

// matchingAges returns the set of distinct values in refAges that fall
// within ageTolerance of age.
func matchingAges(refAges []int, age int) map[int]bool {
	out := map[int]bool{}
	for _, r := range refAges {
		if absDiff(age, r) <= ageTolerance {
			out[r] = true
		}
	}
	return out
}

func absDiff(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

// fmtAgePtr renders an age pointer for log/error messages: "?" for a nil
// (unfetched) age rather than a raw pointer address.
func fmtAgePtr(p *int) string {
	if p == nil {
		return "?"
	}
	return strconv.Itoa(*p)
}

func intPtr(n int) *int { return &n }

func TestAdjudicateByAge(t *testing.T) {
	tests := []struct {
		name       string
		fantraxAge int
		idxAge     *int
		searchAge  *int
		want       verdict
	}{
		{"index matches exactly, search way off", 22, intPtr(22), intPtr(31), indexCorrect},
		{"search matches exactly, index way off", 22, intPtr(31), intPtr(22), searchCorrect},
		{"index off by exactly the tolerance still matches", 22, intPtr(23), intPtr(35), indexCorrect},
		{"search off by exactly the tolerance still matches", 22, intPtr(35), intPtr(21), searchCorrect},
		{"index off by tolerance+1 does not match", 22, intPtr(24), intPtr(24), neitherMatch},
		{"both within tolerance", 22, intPtr(22), intPtr(23), bothMatch},
		{"neither within tolerance", 22, intPtr(30), intPtr(35), neitherMatch},
		{"index age unknown", 22, nil, intPtr(22), ageUnknown},
		{"search age unknown", 22, intPtr(22), nil, ageUnknown},
		{"both ages unknown", 22, nil, nil, ageUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adjudicateByAge(tc.fantraxAge, tc.idxAge, tc.searchAge)
			if got != tc.want {
				t.Errorf("adjudicateByAge(%d, idx=%s, search=%s) = %s, want %s",
					tc.fantraxAge, fmtAgePtr(tc.idxAge), fmtAgePtr(tc.searchAge), got, tc.want)
			}
		})
	}
}

// TestAdjudicateByAges is the collision-aware table test (rosterbot-gcjh).
// Every case here is drawn from the shape a real collision key takes in the
// Fantrax pool (see the doc comment on adjudicateByAges): a key can hold one
// row (the ordinary case, and it must behave exactly like adjudicateByAge) or
// several, and among the multi-row cases some are genuinely undecidable
// (ageAmbiguous) while others remain decidable despite the collision.
func TestAdjudicateByAges(t *testing.T) {
	tests := []struct {
		name      string
		refAges   []int
		idxAge    *int
		searchAge *int
		want      verdict
	}{
		{
			name:      "one reference row behaves as today: index correct",
			refAges:   []int{22},
			idxAge:    intPtr(22),
			searchAge: intPtr(31),
			want:      indexCorrect,
		},
		{
			name:      "one reference row behaves as today: both within tolerance",
			refAges:   []int{22},
			idxAge:    intPtr(22),
			searchAge: intPtr(23),
			want:      bothMatch,
		},
		{
			name:      "one reference row behaves as today: neither matches",
			refAges:   []int{22},
			idxAge:    intPtr(30),
			searchAge: intPtr(35),
			want:      neitherMatch,
		},
		{
			// The rosterbot-gcjh smoking gun: two real rows at this key, the
			// index candidate's age matches row 1 and the search candidate's
			// age matches row 2. Both candidates correspond to a real person
			// recorded at this key — there is no signal left to say which of
			// the two the disagreement is actually about, so this must NOT
			// resolve to indexCorrect or searchCorrect.
			name:      "two reference rows, each candidate matches a different row: ambiguous",
			refAges:   []int{22, 25},
			idxAge:    intPtr(22),
			searchAge: intPtr(25),
			want:      ageAmbiguous,
		},
		{
			// Still decidable: the index candidate matches row 1 (age 22),
			// but the search candidate (age 99) matches NEITHER row — no
			// confirmed real person at this key is 99, regardless of which
			// of the two real people the name is actually about. That is
			// still evidence in the index candidate's favor.
			name:      "two reference rows, only index candidate matches any row: index correct",
			refAges:   []int{22, 25},
			idxAge:    intPtr(22),
			searchAge: intPtr(99),
			want:      indexCorrect,
		},
		{
			// Mirror image of the previous case, justified the same way: the
			// search candidate matches a real row and the index candidate's
			// age (99) matches no one recorded at this key.
			name:      "two reference rows, only search candidate matches any row: search correct",
			refAges:   []int{22, 25},
			idxAge:    intPtr(99),
			searchAge: intPtr(25),
			want:      searchCorrect,
		},
		{
			name:      "two reference rows, neither candidate matches any row",
			refAges:   []int{22, 25},
			idxAge:    intPtr(60),
			searchAge: intPtr(70),
			want:      neitherMatch,
		},
		{
			name:      "two reference rows, both candidates match the same row: bothMatch not ambiguous",
			refAges:   []int{22, 40},
			idxAge:    intPtr(22),
			searchAge: intPtr(23),
			want:      bothMatch,
		},
		{
			name:      "no reference rows at all",
			refAges:   nil,
			idxAge:    intPtr(22),
			searchAge: intPtr(22),
			want:      ageUnknown,
		},
		{
			name:      "index age unknown, multi-row key",
			refAges:   []int{22, 25},
			idxAge:    nil,
			searchAge: intPtr(25),
			want:      ageUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adjudicateByAges(tc.refAges, tc.idxAge, tc.searchAge)
			if got != tc.want {
				t.Errorf("adjudicateByAges(%v, idx=%s, search=%s) = %s, want %s",
					tc.refAges, fmtAgePtr(tc.idxAge), fmtAgePtr(tc.searchAge), got, tc.want)
			}
		})
	}
}
