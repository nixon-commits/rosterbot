package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The two wirings that make a tenant-actionable connect failure reach a phone
// are each a single call whose loss is silent: notify.Deliver is a no-op on a
// nil dispatcher, so dropping either line leaves every behavioural test green
// and only the tenant notices (rosterbot-3has review). These pin them
// structurally, the way TestConnectTaskWritesConnectionsOnlyThroughTheRecorder
// pins the connect writer.

func parseCmdFunc(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s: no top-level func %s", file, name)
	return nil
}

// firstCall returns the position of the first direct call to name inside fn,
// or an invalid position when there is none.
func firstCall(fn *ast.FuncDecl, name string) token.Pos {
	var pos token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if pos.IsValid() {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
			pos = call.Pos()
			return false
		}
		return true
	})
	return pos
}

// initApp must install the dispatcher BEFORE resolving tenant credentials:
// resolveTenantCredentials runs the session ladder, whose failure path calls
// recordTenantConnectFailure, which delivers through notify.Default. In the
// old order Default was still nil there and the push silently never happened.
func TestInitApp_InstallsTheDispatcherBeforeResolvingTenantCredentials(t *testing.T) {
	fn := parseCmdFunc(t, "root.go", "initApp")
	shared := firstCall(fn, "initShared")
	creds := firstCall(fn, "resolveTenantCredentials")
	if !shared.IsValid() || !creds.IsValid() {
		t.Fatalf("initApp must call both initShared (found=%v) and resolveTenantCredentials (found=%v)",
			shared.IsValid(), creds.IsValid())
	}
	if shared >= creds {
		t.Fatalf("initApp calls resolveTenantCredentials before initShared; the session ladder's " +
			"connect-failure push runs inside resolveTenantCredentials and needs notify.Default " +
			"installed first (rosterbot-3has)")
	}
}

// The standalone connect task never calls initShared, so its dispatcher wiring
// is a separate call in runConnect. Without it the user-initiated connect flow
// silently reverts to feed-only.
func TestRunConnect_InstallsTheNotifyDispatcher(t *testing.T) {
	fn := parseCmdFunc(t, "connect.go", "runConnect")
	if !firstCall(fn, "installNotifyDispatcher").IsValid() {
		t.Fatal("runConnect does not call installNotifyDispatcher; the connect task's " +
			"connect-failure push would be a silent no-op (rosterbot-3has)")
	}
}
