package lineupapi

import "net/http"

// Keys for the three private dashboard reports, one object each under the
// state bucket's reports/ prefix. They share a prefix — unlike the two Trades
// artifacts, which are split precisely because they have different producers
// and different cadences — because all three are written by the same
// projection-site run on the same daily schedule, so one age reading judges
// them honestly.
const (
	// ReportModelKey is the projection-accuracy model (internal/report).
	ReportModelKey = "model"
	// ReportGapKey is the realized-vs-hindsight lineup gap (internal/lineupgap).
	ReportGapKey = "gap"
	// ReportViewsKey is recap-site readership (internal/recaplog).
	ReportViewsKey = "views"
)

// reportNames is the allowlist of servable reports and the phrase each one uses
// in its error messages. It is a map rather than a bare key echo so {name} can
// never name an object the caller was not offered: Go's ServeMux already
// refuses a "/" inside a path segment, and this is the second lock on the same
// door.
//
// value.json and football.json are deliberately absent. Both are league-wide
// standings — the same numbers every manager in the league can already read off
// Fantrax and Sleeper — so they stay on the public report/ prefix. The three
// here are the operator's own performance: how many points their lineup left on
// the bench, how well their projections graded, and who reads their site.
var reportNames = map[string]string{
	ReportModelKey: "projection accuracy report",
	ReportGapKey:   "lineup gap report",
	ReportViewsKey: "recap readership report",
}

// handleReport serves one private report's stored bytes.
//
// These three moved here from the dashboard bucket's report/ prefix, which
// CloudFront's default behavior serves straight from S3 with no auth
// (rosterbot-crq.3). That was the operator's own performance data on an open
// URL at N=1, and per-manager data on an open URL the moment the league is
// multi-tenant — the tenants being competitors in one fantasy league. Only
// /v1/* is passkey-gated, so the bytes live in STATE_BUCKET (which no
// distribution serves) and are read server-side by this handler.
func (cfg Config) handleReport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	what, ok := reportNames[name]
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown report: "+name)
		return
	}
	// serveBlob (trades.go) is the passthrough contract: stored bytes out
	// untouched, never decoded, so the producer owns the schema.
	serveBlob(w, r, cfg.Reports, name, what)
}
