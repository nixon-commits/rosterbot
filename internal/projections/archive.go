package projections

import (
	"context"
	"fmt"
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
	for _, sys := range archivedSystems {
		apiType, ok := fgProjectionType[sys]
		if !ok {
			return nil, fmt.Errorf("archive: unknown system %q", sys)
		}
		for _, stats := range []string{"bat", "pit"} {
			body, err := archive.Get(ctx, fmt.Sprintf(fgBaseURL, apiType, stats))
			if err != nil {
				return nil, fmt.Errorf("archive %s %s: %w", sys, stats, err)
			}
			arts = append(arts, archive.Artifact{
				Filename: fmt.Sprintf("%s-%s.json", sys, stats),
				Bytes:    body,
			})
		}
	}
	return arts, nil
}
