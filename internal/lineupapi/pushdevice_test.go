package lineupapi_test

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/pushdevicetest"
)

func TestFilePushDeviceStoreConformance(t *testing.T) {
	pushdevicetest.Run(t, func(t *testing.T) lineupapi.PushDeviceStore {
		return lineupapi.NewFileUserStore(t.TempDir())
	})
}
