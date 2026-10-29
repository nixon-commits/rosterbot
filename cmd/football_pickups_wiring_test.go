package cmd

import (
	"go/ast"
	"go/token"
	"testing"
)

// The two run-level rules of football-pickups are each a single expression in
// runFootballPickups whose loss is silent: finishPickupRun and pickupRunError
// are table-tested in isolation, so replacing `markers != nil` with `true`
// (dedup-off tails would hold the pointer forever) or the final
// `return pickupRunError(...)` with `return nil` (a failed send or a lost
// NoBackfill archive day would record SUCCESS) leaves every behavioural test
// green (fix-wave re-review, 2026-09-30). These pin the glue the way
// connect_wiring_test.go pins the dispatcher order.

// finishPickupRunCall returns the one direct call to finishPickupRun in fn.
func finishPickupRunCall(t *testing.T, fn *ast.FuncDecl) *ast.CallExpr {
	t.Helper()
	var found *ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "finishPickupRun" {
			if found != nil {
				t.Fatalf("runFootballPickups calls finishPickupRun more than once")
			}
			found = call
		}
		return true
	})
	if found == nil {
		t.Fatalf("runFootballPickups never calls finishPickupRun")
	}
	return found
}

func TestRunFootballPickups_PassesWhetherAMarkerStoreExistsToFinishPickupRun(t *testing.T) {
	fn := parseCmdFunc(t, "football_pickups.go", "runFootballPickups")
	call := finishPickupRunCall(t, fn)
	if len(call.Args) < 8 {
		t.Fatalf("finishPickupRun called with %d args, want at least 8 (markersOK is the 8th)", len(call.Args))
	}
	bin, ok := call.Args[7].(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		t.Fatalf("markersOK argument = %T, want the expression `markers != nil`", call.Args[7])
	}
	x, xok := bin.X.(*ast.Ident)
	y, yok := bin.Y.(*ast.Ident)
	if !xok || !yok || x.Name != "markers" || y.Name != "nil" {
		t.Errorf("markersOK argument is not `markers != nil`: a constant here would let a dedup-off tail hold the pointer forever")
	}
}

func TestRunFootballPickups_ReturnsTheComposedVerdictLast(t *testing.T) {
	fn := parseCmdFunc(t, "football_pickups.go", "runFootballPickups")
	stmts := fn.Body.List
	ret, ok := stmts[len(stmts)-1].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		t.Fatalf("runFootballPickups must end in a single-value return, got %T", stmts[len(stmts)-1])
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		t.Fatalf("final return is %T, want a call to pickupRunError (a bare nil would record SUCCESS on a failed send or a lost archive day)", ret.Results[0])
	}
	if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "pickupRunError" {
		t.Fatalf("final return calls %v, want pickupRunError", call.Fun)
	}
	if len(call.Args) != 4 {
		t.Fatalf("pickupRunError called with %d args, want 4 (leagues, failed, sendFailed, persistErr)", len(call.Args))
	}
	if sel, ok := call.Args[2].(*ast.SelectorExpr); !ok || sel.Sel.Name != "SendFailed" {
		t.Errorf("3rd argument to pickupRunError is %T, want total.SendFailed so a failed send fails the run", call.Args[2])
	}
	if id, ok := call.Args[3].(*ast.Ident); !ok || id.Name != "persistErr" {
		t.Errorf("4th argument to pickupRunError is %T, want persistErr so a lost archive or pointer write fails the run", call.Args[3])
	}
}
