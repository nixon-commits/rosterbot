package lineupapi_test

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/usertest"
)

func TestFileUserStore_Conformance(t *testing.T) {
	usertest.Run(t, func(t *testing.T) lineupapi.UserStore {
		return lineupapi.NewFileUserStore(t.TempDir())
	})
}
