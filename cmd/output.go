package cmd

import (
	"context"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
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

	var w lineupapi.OutputWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewOutput(context.Background(), bucket, "runs/")
		if err != nil {
			return
		}
		w = s
	} else {
		w = lineupapi.NewFileOutputStore(".lineup/outputs")
	}

	lineupapi.OutputRecorder = func(jobType string, data any) {
		body, err := lineupapi.MarshalOutput(jobType, data)
		if err != nil {
			return
		}
		_ = w.PutOutput(context.Background(), runID, body)
	}
}
