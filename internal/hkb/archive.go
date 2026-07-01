package hkb

import (
	"context"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// ArchiveArtifacts fetches the HKB rankings page and returns its raw bytes for
// durable archival. HKB serves only current values, so this is the only way to
// preserve a given day's rankings. The date arg is unused (no date param
// upstream) but present for archive.Source conformance.
func ArchiveArtifacts(ctx context.Context, _ time.Time) ([]archive.Artifact, error) {
	body, err := archive.Get(ctx, fetchURL)
	if err != nil {
		return nil, fmt.Errorf("hkb archive: %w", err)
	}
	return []archive.Artifact{{Filename: "rankings.html", Bytes: body}}, nil
}
