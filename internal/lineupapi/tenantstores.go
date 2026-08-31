package lineupapi

import (
	"context"
	"net/http"
)

// TenantView is the per-tenant slice of Config: the stores whose contents
// belong to one specific user rather than to the league or the deployment.
//
// It exists because these eight were fields on Config, resolved once at Lambda
// cold start from an environment variable, while the fourteen routes that read
// them never consulted who was asking. Every authenticated caller therefore
// read the operator's lineup, runs, trades, private reports and notification
// feed, whoever they had signed in as — with the fan-out busy writing each
// tenant's real data to prefixes nothing read.
//
// Infra, Users, Enrollments, Connections, Sealer and Jobs are
// deliberately NOT here: they are league-wide, identity-scoped, or a launcher,
// and scoping them per tenant would be wrong rather than merely unnecessary.
type TenantView struct {
	Lineups       ObjectStore
	Runs          RunStore
	Notifications NotificationStore
	Output        OutputStore
	Progress      ProgressStore
	Trades        ObjectStore
	TradeValues   ObjectStore
	AvailablePool ObjectStore
	Reports       ObjectStore
}

// TenantStores resolves the per-tenant stores for one caller.
//
// uid is empty for the bearer-token caller, which has no UserID by
// construction — it is break-glass admin access that deliberately never touches
// the user store. An implementation must give that case a defined answer rather
// than an empty prefix; the deployment's own default tenant is the safe one,
// since the token is the operator's.
type TenantStores interface {
	For(ctx context.Context, uid UserID) (TenantView, error)
}

// tenantView resolves the stores this request may read.
//
// A nil Tenants provider returns the flat Config fields unchanged, which is the
// single-tenant shape `rosterbot serve` and every handler test use. That is a
// deliberate fallback for "tenancy is not configured", NOT for "tenancy failed".
//
// A provider that ERRORS returns an empty view and false. Falling back to the
// flat fields there would hand the caller whichever tenant the deployment
// booted with — precisely the bug this type exists to remove, reappearing on
// exactly the path nobody tests. An empty view makes handlers answer 503, which
// is the honest reading of "we cannot tell whose data this is".
func (cfg Config) tenantView(ctx context.Context) (TenantView, bool) {
	if cfg.Tenants == nil {
		return TenantView{
			Lineups:       cfg.Lineups,
			Runs:          cfg.Runs,
			Notifications: cfg.Notifications,
			Output:        cfg.Output,
			Progress:      cfg.Progress,
			Trades:        cfg.Trades,
			TradeValues:   cfg.TradeValues,
			AvailablePool: cfg.AvailablePool,
			Reports:       cfg.Reports,
		}, true
	}
	return cfg.tenantViewOf(ctx, CallerFrom(ctx).UserID)
}

// tenantViewOf resolves an ARBITRARY tenant's stores. It is the one function in
// this package that can read data belonging to somebody other than the caller.
//
// uid IS NOT TAKEN FROM THE REQUEST, and must never be. isAdminOnlyPath matches
// on r.URL.Path alone (authz.go), so a `?tenant=` query parameter reaches no
// gate at all — the naive cross-tenant read compiles, passes every existing
// authz test, and lets any member read any tenant's ledger. The caller must
// already have established that the requester may read this tenant's data;
// today the only such caller is handleTenants' run summary, gated as a unit by
// the "/v1/tenants" entry in adminOnlyRoutes.
// TestTenantViewOf_CallersAreAnAllowlist and TestNoRequestValueBecomesATenantID
// enforce both halves structurally.
//
// IT HAS NO FLAT-CONFIG FALLBACK, deliberately, and that asymmetry with
// tenantView above is the correctness of this function. A nil Tenants provider
// means tenancy is not configured, so the flat stores belong to whoever the
// process booted as — one ledger, one lineup, one feed. tenantView may serve
// those to the caller, because in that shape the caller IS that tenant. Serving
// them for an arbitrary uid would label one process-wide ledger as N different
// tenants' history, which is exactly the mis-attribution this type was created
// to remove (see the doc on TenantView). `rosterbot serve` is that shape today:
// cmd/serve.go sets a real Runs store and no Tenants provider.
func (cfg Config) tenantViewOf(ctx context.Context, uid UserID) (TenantView, bool) {
	if cfg.Tenants == nil {
		return TenantView{}, false
	}
	v, err := cfg.Tenants.For(ctx, uid)
	if err != nil {
		return TenantView{}, false
	}
	return v, true
}

// requireTenantView resolves the caller's TenantView or writes the standard
// 503 and reports false, so handlers can bail out in one line instead of
// repeating the failure branch.
func (cfg Config) requireTenantView(w http.ResponseWriter, r *http.Request) (TenantView, bool) {
	view, ok := cfg.tenantView(r.Context())
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "could not resolve this account's data")
		return TenantView{}, false
	}
	return view, true
}
