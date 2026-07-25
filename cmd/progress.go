package cmd

import (
	"context"
	"encoding/json"
	"os"

	"github.com/nixon-commits/rosterbot/internal/progress"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

// installProgressRecorder wires progress.Recorder so a run's phase transitions
// persist under the current RUN_ID (runs/<id>/progress.json). Best-effort:
// missing RUN_ID or a store error never affects the job. STATE_BUCKET -> S3;
// otherwise local .lineup/progress/<id>.json. Mirrors installOutputRecorder.
func installProgressRecorder() {
	runID := os.Getenv("RUN_ID")
	if runID == "" {
		return
	}

	w, err := statestore.FromEnv().Progress()
	if err != nil {
		return
	}

	progress.Recorder = func(s progress.Snapshot) {
		body, err := json.Marshal(s)
		if err != nil {
			return
		}
		_ = w.PutProgress(context.Background(), runID, body)
	}
}
