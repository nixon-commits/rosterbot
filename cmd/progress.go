package cmd

import (
	"context"
	"encoding/json"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/progress"
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

	var w lineupapi.ProgressWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewProgress(context.Background(), bucket, "runs/")
		if err != nil {
			return
		}
		w = s
	} else {
		w = lineupapi.NewFileProgressStore(".lineup/progress")
	}

	progress.Recorder = func(s progress.Snapshot) {
		body, err := json.Marshal(s)
		if err != nil {
			return
		}
		_ = w.PutProgress(context.Background(), runID, body)
	}
}
