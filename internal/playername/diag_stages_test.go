//go:build diag

package playername

import (
	"context"
	"sync"
	"testing"

	mlb "github.com/pmurley/go-mlb"
	"golang.org/x/sync/errgroup"
)

// TestDiagStages replicates fetchAndResolve stage-for-stage but reports the
// errors it swallows, to show WHICH stage loses players.
func TestDiagStages(t *testing.T) {
	names := diagNames(t)
	client := newDiagClient()
	ctx := context.Background()

	seen := map[string]bool{}
	var searchNames []string
	for _, n := range names {
		if k := Normalize(n); !seen[k] {
			seen[k] = true
			searchNames = append(searchNames, n)
		}
	}
	t.Logf("STAGE 0: %d unique names", len(searchNames))

	// --- STAGE 1: parallel batched search (production shape: 25/batch, limit 8)
	const searchBatchSize = 25
	idSet := map[int]bool{}
	var ids []int
	var mu sync.Mutex
	var batchErrs, batchOK int

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
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
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				batchErrs++
				t.Logf("   STAGE1 batch[%d:%d] ERROR: %v", i, end, err)
				return nil
			}
			batchOK++
			t.Logf("   STAGE1 batch[%d:%d] ok: %d people", i, end, len(people))
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
	t.Logf("STAGE 1 (parallel search): %d batches ok, %d errored -> %d unique ids", batchOK, batchErrs, len(ids))

	// --- STAGE 2: bulk People.List with WithFields
	const peopleBatchSize = 500
	rp := &ResolvedPlayers{ByName: map[string]int{}, ByID: map[int]string{}}
	var listErrs int
	for i := 0; i < len(ids); i += peopleBatchSize {
		end := i + peopleBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		people, err := client.People.List(ctx, ids[i:end],
			mlb.WithFields("people", "id", "fullName", "firstName", "lastName", "useName", "useLastName"),
		)
		if err != nil {
			listErrs++
			t.Logf("   STAGE2 List[%d:%d] ERROR: %v", i, end, err)
			continue
		}
		t.Logf("   STAGE2 List[%d:%d] requested %d ids -> returned %d people", i, end, end-i, len(people))
		for _, p := range people {
			indexPerson(rp, p, map[string]bool{})
		}
	}
	t.Logf("STAGE 2 (People.List): %d errors -> %d indexed players, %d name variants",
		listErrs, len(rp.ByID), len(rp.ByName))

	// --- STAGE 2b: same List call WITHOUT WithFields, to test the fields mask
	if len(ids) > 0 {
		end := len(ids)
		if end > peopleBatchSize {
			end = peopleBatchSize
		}
		people, err := client.People.List(ctx, ids[:end])
		if err != nil {
			t.Logf("   STAGE2b List (no WithFields) ERROR: %v", err)
		} else {
			rp2 := &ResolvedPlayers{ByName: map[string]int{}, ByID: map[int]string{}}
			for _, p := range people {
				indexPerson(rp2, p, map[string]bool{})
			}
			t.Logf("   STAGE2b List (NO WithFields) requested %d -> returned %d people, indexed %d, variants %d",
				end, len(people), len(rp2.ByID), len(rp2.ByName))
		}
	}

	var missing int
	for _, n := range names {
		if id, ok := rp.ByName[Normalize(n)]; !ok || id == 0 {
			missing++
		}
	}
	t.Logf("=== FINAL: %d/%d input names unresolved ===", missing, len(names))
}
