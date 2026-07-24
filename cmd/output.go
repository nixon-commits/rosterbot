package cmd

import (
	"context"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

// installOutputRecorder wires lineupapi.RecordOutput so each job persists its
// typed result under the current RUN_ID. Best-effort: a missing RUN_ID or a
// store error never affects the job. STATE_BUCKET -> S3 (runs/<id>/output.json);
// otherwise local .lineup/outputs/<id>.json. Mirrors installNotificationRecorder.
func installOutputRecorder() {
	runID := os.Getenv("RUN_ID")
	if runID == "" {
		return // no id to key on (local non-task run); leave the hook unset (no-op)
	}

	w, err := statestore.FromEnv().Output()
	if err != nil {
		return
	}

	lineupapi.OutputRecorder = func(jobType string, data any) {
		body, err := lineupapi.MarshalOutput(jobType, data)
		if err != nil {
			return
		}
		_ = w.PutOutput(context.Background(), runID, body)
	}
}
