package fantrax

import "testing"

// TestGetPendingTrades_CacheIsScopedToTheCredentialTeam pins the tenant
// boundary on the one cached response that depends on WHO is logged in rather
// than on the key's parameters: pendingTransactions is the offer list visible
// to the authenticated team. Under per-tenant fan-out every tenant's hourly
// task shares the cache/ prefix (layout.Cache is deliberately not PerTenant —
// league data is identical for everyone), so a league-scoped key here serves
// tenant A's offer view to tenant B for the whole todayTTL window: B's own
// offers vanish from their Trades tab, and B's NoBackfill offer log records a
// wrong world it can never re-derive.
func TestGetPendingTrades_CacheIsScopedToTheCredentialTeam(t *testing.T) {
	dir := t.TempDir()
	a := &Client{teamID: "teamA", leagueID: "lg1", cacheDir: dir}
	b := &Client{teamID: "teamB", leagueID: "lg1", cacheDir: dir}

	orig := fetchPendingTradesFn
	t.Cleanup(func() { fetchPendingTradesFn = orig })
	fetchPendingTradesFn = func(c *Client) ([]PendingTrade, error) {
		// The marker is the credential the fetch ran under — exactly the thing
		// a shared cache entry would misattribute.
		return []PendingTrade{{TradeID: "seen-by-" + c.teamID}}, nil
	}

	got, err := a.GetPendingTrades()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TradeID != "seen-by-teamA" {
		t.Fatalf("tenant A's read = %+v, want its own fetch", got)
	}

	got, err = b.GetPendingTrades()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TradeID != "seen-by-teamB" {
		t.Fatalf("tenant B read %+v through the shared cache — a credential-scoped "+
			"response must never be served across teams", got)
	}
}
