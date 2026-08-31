package lineupapi

import (
	"context"
	"slices"
	"strings"
)

// connectCommandName is the argv[0] the connect task is launched with
// (handleConnect launches []string{"connect", "--user", uid}; entrypoint.sh
// joins argv with spaces into the ledger's command string).
const connectCommandName = "connect"

// isConnectRun reports whether this ledger row is a connect run.
//
// Matched on the bare word or the word plus a space so a future `connect-foo`
// command never matches.
func isConnectRun(r Run) bool {
	return r.Command == connectCommandName ||
		strings.HasPrefix(r.Command, connectCommandName+" ")
}

// applyConnectOutcome stamps the connect verdict onto r if and only if lr
// describes THIS run.
//
// EXACT MATCH ONLY: no match means no chip, never a wrong one. The command
// guard is kept even though an ECS task id is unique, because entrypoint.sh
// falls back to `local-<timestamp>` when there is no ECS metadata, and two
// tasks started inside the same second there share an id.
//
// An empty Verdict is treated as no stamp. A record written by a build that
// stamped only the id would otherwise render as an unlabelled chip, which
// states less than saying nothing.
func applyConnectOutcome(r *Run, lr *ConnectRun) {
	if lr == nil || lr.RunID == "" || lr.Verdict == "" {
		return
	}
	if !isConnectRun(*r) || r.ID != lr.RunID {
		return
	}
	r.Connect = &RunConnect{Verdict: lr.Verdict, LastError: lr.LastError}
}

// lastConnectRun reads the caller's connection record, and only when some row
// in this response could use it.
//
// EVERY FAILURE RETURNS NIL. The run rows are the answer; the connect verdict
// is a decoration on one of them, and a store hiccup must not 502 the list.
//
// COST, STATED HONESTLY: web/dashboard/live.js polls GET /v1/runs every 5s for
// the whole session, and a connect row stays inside the newest 25 runs for a
// long time, so the guard below is rarely taken and this is an extra DynamoDB
// GetItem on most polls while a connect row is in the window. Accepted: it is a
// single-item read on the same table the caller's session already touched, and
// the alternative is a wrong chip or none.
//
// KEYED ON THE CALLER, WHICH IS THE SAME KEY THE ROWS ARE (Config.tenantView
// resolves the TenantView from CallerFrom(ctx).UserID too). If a cross-tenant
// runs view ever lands — rosterbot-nejq wants the operator to watch a tester's
// runs — these two keys come apart and the operator silently gets no chip on
// rows that have one. It fails closed rather than showing another tenant's
// outcome, but whoever adds that view must re-key this read with it.
func (cfg Config) lastConnectRun(ctx context.Context, runs []Run) *ConnectRun {
	if cfg.Connections == nil {
		return nil
	}
	uid := CallerFrom(ctx).UserID
	if uid == "" {
		// The bearer token has no UserID by construction — it is break-glass
		// admin access that never reads the user store. TenantStores maps it to
		// the deployment's default tenant for the ROWS, but there is no person
		// whose connection this would be, so there is no verdict to report.
		return nil
	}
	if !slices.ContainsFunc(runs, isConnectRun) {
		return nil
	}
	conn, ok, err := cfg.Connections.GetConnection(ctx, uid)
	if err != nil || !ok || conn == nil {
		return nil
	}
	return conn.LastConnectRun
}

// annotateConnectRuns returns runs with the connect verdict joined on.
//
// It COPIES before writing. RunStore implementations are free to hand back a
// slice they also retain (s3lineup builds a fresh one per call, but a test
// fixture returning its own field is the natural shape), and mutating that in
// place would leak one request's annotation into the next.
func (cfg Config) annotateConnectRuns(ctx context.Context, runs []Run) []Run {
	lr := cfg.lastConnectRun(ctx, runs)
	if lr == nil {
		return runs
	}
	out := slices.Clone(runs)
	for i := range out {
		applyConnectOutcome(&out[i], lr)
	}
	return out
}
