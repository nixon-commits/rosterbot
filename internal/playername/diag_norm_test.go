//go:build diag

package playername

import (
	"context"
	"testing"
	"time"

	mlb "github.com/pmurley/go-mlb"
)

// TestDiagNormalizedSearch checks whether the already-computed Normalize()
// form is a usable search string (lowercase, no periods, no suffix).
func TestDiagNormalizedSearch(t *testing.T) {
	client := newDiagClient()
	ctx := context.Background()
	for _, n := range []string{
		"A.J. Blubaugh", "T.J. Rumfield", "Zach Cole Jr.", "Jake Latz",
		"Jose Berrios", "Ha-Seong Kim", "Vladimir Guerrero Jr.", "Nico Hoerner",
	} {
		norm := Normalize(n)
		people, err := client.People.Search(ctx, mlb.WithNames(norm),
			mlb.WithQueryParam("sportIds", "11,12,13,14,16,1"))
		if err != nil {
			t.Logf("%-24q norm=%-24q ERROR %v", n, norm, err)
			continue
		}
		var got []string
		for i, p := range people {
			if i >= 3 {
				break
			}
			got = append(got, p.FullName)
		}
		t.Logf("%-24q norm=%-24q -> %d %v", n, norm, len(people), got)
		time.Sleep(200 * time.Millisecond)
	}
}
