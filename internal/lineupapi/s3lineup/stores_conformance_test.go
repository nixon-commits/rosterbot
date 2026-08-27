package s3lineup

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/notificationtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/outputtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/progresstest"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// The S3 adapters, held to the same contracts as their file siblings (see
// lineupapi's stores_conformance_test.go), over s3blobtest's paginating
// in-memory double.

func TestNotificationsStore_Conformance(t *testing.T) {
	notificationtest.Run(t, func(t *testing.T) notificationtest.Store {
		f := s3blobtest.New()
		return &NotificationsStore{blob: f.Blob("b", "notifications/")}
	})
}

func TestOutputStore_Conformance(t *testing.T) {
	outputtest.Run(t, func(t *testing.T) outputtest.Store {
		f := s3blobtest.New()
		return &OutputStore{blob: f.Blob("b", "runs/")}
	})
}

func TestProgressStore_Conformance(t *testing.T) {
	progresstest.Run(t, func(t *testing.T) progresstest.Store {
		f := s3blobtest.New()
		return &ProgressStore{blob: f.Blob("b", "runs/")}
	})
}
