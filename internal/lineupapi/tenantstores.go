package lineupapi

import "context"

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
// Infra, Identities, Users, Enrollments, Connections, Sealer and Jobs are
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
			Reports:       cfg.Reports,
		}, true
	}
	v, err := cfg.Tenants.For(ctx, CallerFrom(ctx).UserID)
	if err != nil {
		return TenantView{}, false
	}
	return v, true
}
