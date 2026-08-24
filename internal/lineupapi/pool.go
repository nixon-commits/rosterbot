package lineupapi

import "net/http"

// AvailablePoolKey is the Pickups screen's payload, rewritten daily.
const AvailablePoolKey = "available"

// handleAvailablePool serves the unowned-player table: every free agent joined
// onto HKB dynasty value and 30-day momentum, pre-split into an mlb and a
// prospects segment.
//
// Pre-split by the producer rather than by the client, deliberately. A flat
// top-N would return an empty prospects section on any day MLB free agents
// outvalued every available prospect, and nothing in the payload would
// distinguish that from "there are no available prospects". Two arrays make the
// crowding-out case unrepresentable rather than merely unlikely — the same
// instinct as lineuprun/emit.go's applyAuthorization.
func (cfg Config) handleAvailablePool(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	serveBlob(w, r, view.AvailablePool, AvailablePoolKey, "available player pool")
}
