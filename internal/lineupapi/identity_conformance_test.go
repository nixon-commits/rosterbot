// This file is package lineupapi_test rather than package lineupapi because
// identitytest imports lineupapi; an in-package test file importing it would
// be an import cycle.
package lineupapi_test

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/identitytest"
)

func TestFileIdentityStore_Conformance(t *testing.T) {
	identitytest.Run(t, func(t *testing.T) lineupapi.IdentityStore {
		return lineupapi.NewFileIdentityStore(t.TempDir())
	})
}
