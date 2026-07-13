package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// shadowNoDataStateFile persists the last known per-system data-availability
// status across shadow runs, so the command can detect a *transition* (a
// system going down, or recovering) instead of re-alerting every single day
// an outage continues. Lives under .cache/ so it rides the same S3 sync as
// the rest of the cache (see cmd/sync.go).
const shadowNoDataStateFile = cacheDir + "/shadow-nodata-state.json"

// systemNoData records whether a projection system's batting/pitching load
// came back empty (LoadResult.NoData) on the most recent shadow capture.
type systemNoData struct {
	Hitters  bool `json:"hitters"`
	Pitchers bool `json:"pitchers"`
}

// loadShadowNoDataState reads the persisted per-system state; any read/parse
// error is treated as "no prior state" so a missing or corrupt file never
// blocks the shadow run.
func loadShadowNoDataState(path string) map[string]systemNoData {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]systemNoData{}
	}
	var state map[string]systemNoData
	if err := json.Unmarshal(data, &state); err != nil {
		return map[string]systemNoData{}
	}
	return state
}

// saveShadowNoDataState persists the per-system state.
func saveShadowNoDataState(path string, state map[string]systemNoData) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// describeNoDataTransition returns a human-readable line for a system whose
// data-availability status changed since the last shadow run, or "" if
// nothing changed.
func describeNoDataTransition(system string, prev, cur systemNoData) string {
	var msg string
	if !prev.Hitters && cur.Hitters {
		msg += system + ": batting projections now unavailable (upstream outage?)\n"
	} else if prev.Hitters && !cur.Hitters {
		msg += system + ": batting projections recovered\n"
	}
	if !prev.Pitchers && cur.Pitchers {
		msg += system + ": pitching projections now unavailable (upstream outage?)\n"
	} else if prev.Pitchers && !cur.Pitchers {
		msg += system + ": pitching projections recovered\n"
	}
	return msg
}
