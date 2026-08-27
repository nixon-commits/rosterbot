package s3lineup

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/infralistertest"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// TestInfraStore_Conformance holds the S3 lister to the same key-grammar
// contract as the local one, over the paginating in-memory double. The seed
// writes raw objects at prefix+remainder — the lister holds ListBucket only,
// so the layout IS the interface.
func TestInfraStore_Conformance(t *testing.T) {
	infralistertest.Run(t, func(t *testing.T) (lineupapi.InfraLister, string, infralistertest.Seed) {
		f := s3blobtest.New()
		prefix := layout.Analysis.S3Prefix
		seed := func(remainder string, data []byte) {
			f.Objects[prefix+remainder] = data
		}
		return &InfraStore{blob: f.Blob("b", "")}, prefix, seed
	})
}
