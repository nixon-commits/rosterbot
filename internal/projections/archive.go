package projections

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// archivedSystems is the set of rest-of-season projection systems captured daily
// for durable archival — the full projection landscape, not just the bot's
// configured system, since FanGraphs serves only the current projection.
var archivedSystems = []string{
	ProjectionSteamerRoS,
	ProjectionDepthChartsRoS,
	ProjectionBatXRoS,
	ProjectionATCRoS,
}

// ArchiveArtifacts fetches raw FanGraphs batting+pitching JSON for every archived
// RoS system and returns them as <system>-bat.json / <system>-pit.json. It builds
// URLs directly (not via SetProjectionSystem, which mutates package globals).
func ArchiveArtifacts(ctx context.Context, _ time.Time) ([]archive.Artifact, error) {
	var arts []archive.Artifact
	var errs []error
	for _, sys := range archivedSystems {
		apiType, ok := fgProjectionType[sys]
		if !ok {
			return nil, fmt.Errorf("archive: unknown system %q", sys)
		}
		for _, stats := range []string{"bat", "pit"} {
			body, err := archive.Get(ctx, fmt.Sprintf(fgBaseURL, apiType, stats))
			if err != nil {
				// A single flaky endpoint (e.g. FanGraphs returning 500 for one
				// system's batting feed) must not discard the systems that
				// fetched fine — skip it and keep whatever we could collect, so
				// one bad shard costs one system-day rather than all of FG.
				errs = append(errs, fmt.Errorf("%s %s: %w", sys, stats, err))
				continue
			}
			arts = append(arts, archive.Artifact{
				Filename: fmt.Sprintf("%s-%s.json", sys, stats),
				Bytes:    body,
			})
		}
	}
	if len(arts) == 0 {
		return nil, fmt.Errorf("archive projections: all fetches failed: %w", errors.Join(errs...))
	}
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "warn: archive projections: %d of %d fetches failed, archived the rest: %v\n",
			len(errs), len(archivedSystems)*2, errors.Join(errs...))
	}
	return arts, nil
}
