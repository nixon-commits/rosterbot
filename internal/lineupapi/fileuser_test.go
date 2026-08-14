package lineupapi_test

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/enrollmenttest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/usertest"
)

func TestFileUserStore_Conformance(t *testing.T) {
	usertest.Run(t, func(t *testing.T) lineupapi.UserStore {
		return lineupapi.NewFileUserStore(t.TempDir())
	})
}

func TestFileUserStore_EnrollmentConformance(t *testing.T) {
	enrollmenttest.Run(t, func(t *testing.T) lineupapi.EnrollmentStore {
		return lineupapi.NewFileUserStore(t.TempDir())
	})
}
