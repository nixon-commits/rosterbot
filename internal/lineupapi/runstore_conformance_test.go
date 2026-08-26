package lineupapi_test

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/runstoretest"
)

// TestFileRunStore_Conformance holds the local adapter to the same RunStore
// contract the S3 adapter is held to — most pointedly the Get lookback
// window, which the file store used to ignore (it scanned every record), so a
// run resolvable in local `serve` 404'd in production.
func TestFileRunStore_Conformance(t *testing.T) {
	runstoretest.Run(t, func(t *testing.T) (lineupapi.RunStore, runstoretest.RunWriter) {
		s := lineupapi.NewFileRunStore(t.TempDir())
		return s, s
	})
}
