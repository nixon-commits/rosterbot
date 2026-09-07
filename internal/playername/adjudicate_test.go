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

// adjudicateByAge decides, for one index-vs-search disagreement, which
// candidate's age (if either) matches an independent reference age
// (fantraxAge) within ageTolerance years.
//
// idxAge / searchAge are nil when that candidate's age could not be fetched
// (never a stand-in for age zero, which is a real infant age). Either side
// unknown makes the whole comparison ageUnknown — there is nothing to
// adjudicate without both ages, and guessing from one side alone would be
// exactly the "confidently wrong" failure mode playername's join logic
// elsewhere in this package is built to avoid (see claimName's doc comment).
func adjudicateByAge(fantraxAge int, idxAge, searchAge *int) verdict {
	if idxAge == nil || searchAge == nil {
		return ageUnknown
	}
	idxMatch := absDiff(*idxAge, fantraxAge) <= ageTolerance
	searchMatch := absDiff(*searchAge, fantraxAge) <= ageTolerance
	switch {
	case idxMatch && searchMatch:
		return bothMatch
	case idxMatch:
		return indexCorrect
	case searchMatch:
		return searchCorrect
	default:
		return neitherMatch
	}
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
