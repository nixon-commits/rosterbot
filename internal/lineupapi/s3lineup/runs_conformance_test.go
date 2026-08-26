package s3lineup

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/runstoretest"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// TestRunsStore_Conformance runs the shared RunStore contract against the S3
// adapter over s3blobtest's paginating in-memory double. PageSize is forced
// down to 50 so the lookback subtest's RunLookback+1 records span several
// list pages — at the fake's default (S3's 1000) they fit one page and the
// walk's continuation handling would go unexercised, which is exactly the
// bug class (rosterbot-432) the paginating fake exists to catch.
func TestRunsStore_Conformance(t *testing.T) {
	runstoretest.Run(t, func(t *testing.T) (lineupapi.RunStore, runstoretest.RunWriter) {
		f := s3blobtest.New()
		f.PageSize = 50
		s := &RunsStore{blob: f.Blob("b", "runs/")}
		return s, s
	})
}
