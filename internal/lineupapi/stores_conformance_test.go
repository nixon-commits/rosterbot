package lineupapi_test

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/notificationtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/outputtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/progresstest"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// The local file adapters, held to the same contracts as their S3 siblings
// (see the twin conformance tests in s3lineup). One file for the three
// simple stores; the run ledger's suite lives in
// runstore_conformance_test.go because it carries the lookback-window story.

func TestFileNotificationStore_Conformance(t *testing.T) {
	notificationtest.Run(t, func(t *testing.T) notificationtest.Store {
		return lineupapi.NewFileNotificationStore(t.TempDir())
	})
}

func TestFileOutputStore_Conformance(t *testing.T) {
	outputtest.Run(t, func(t *testing.T) outputtest.Store {
		return lineupapi.NewFileOutputStore(t.TempDir())
	})
}

func TestFileProgressStore_Conformance(t *testing.T) {
	progresstest.Run(t, func(t *testing.T) progresstest.Store {
		return lineupapi.NewFileProgressStore(t.TempDir())
	})
}
