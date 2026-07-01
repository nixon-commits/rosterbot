package prospects

import (
	"context"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// ArchiveArtifacts fetches the raw FanGraphs prospect board JSON for durable
// archival (the source actually wired in run.go). season = date.Year(), matching
// FanGraphsRankingSource.GetTopProspects.
func ArchiveArtifacts(ctx context.Context, date time.Time) ([]archive.Artifact, error) {
	season := date.Year()
	body, err := archive.Get(ctx, fmt.Sprintf(fgProspectURL, season, season))
	if err != nil {
		return nil, fmt.Errorf("prospects archive: %w", err)
	}
	return []archive.Artifact{{Filename: "fangraphs-board.json", Bytes: body}}, nil
}
