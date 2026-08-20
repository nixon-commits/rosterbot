package lineupapi

import "net/http"

// Keys for the two Trades-tab artifacts, one object each. They live in
// separate S3 prefixes (trades/ and tradevalues/) rather than sharing one, so
// the Infra page can judge each against its own producer's cadence — a dead
// daily values job must not hide behind a healthy hourly offers job.
const (
	// TradesCurrentKey is the pending-offer snapshot, rewritten hourly.
	TradesCurrentKey = "current"
	// TradeValuesKey is the league values table, rewritten daily.
	TradeValuesKey = "values"
)

// handleTrades serves the pending-offer snapshot.
//
// These payloads go through /v1/* rather than being published to the
// dashboard bucket's report/ prefix like model.json and value.json. That is
// not a stylistic choice: CloudFront's default behavior serves the dashboard
// bucket straight from S3 with no auth, so anything under report/ is readable
// by anyone who knows the distribution domain. Only /v1/* is passkey-gated,
// and open trade negotiations in a live league belong behind it.
func (cfg Config) handleTrades(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	serveBlob(w, r, view.Trades, TradesCurrentKey, "pending offers")
}

// handleTradeValues serves the league values table.
func (cfg Config) handleTradeValues(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	serveBlob(w, r, view.TradeValues, TradeValuesKey, "trade values table")
}

// serveBlob passes stored JSON bytes through untouched, the same contract as
// GET /v1/runs/{id}/progress. The API deliberately does not parse these: the
// producer owns the schema, so a shape change ships without a Lambda deploy,
// and lineupapi stays free of any dependency that would drag chromedp in
// behind it.
func serveBlob(w http.ResponseWriter, r *http.Request, store ObjectStore, key, what string) {
	if store == nil {
		writeErr(w, http.StatusNotImplemented, what+" store not configured")
		return
	}
	data, ok, err := store.Get(r.Context(), key)
	if err != nil {
		writeErr(w, http.StatusBadGateway, what+" store unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no "+what+" available yet")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
