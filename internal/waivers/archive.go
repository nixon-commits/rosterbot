package waivers

import (
	"context"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// ArchiveArtifacts fetches all five Baseball Savant CSVs (raw bytes) for durable
// archival. The 14d/30d windows are rolling and roll off permanently upstream, so
// this is the only way to preserve them. Window math mirrors LoadSavant so the
// archived windows match what the waivers/claims path actually consumes.
func ArchiveArtifacts(ctx context.Context, date time.Time) ([]archive.Artifact, error) {
	year := date.Year()
	end := date.AddDate(0, 0, -1)
	start14 := end.AddDate(0, 0, -13)
	start30 := end.AddDate(0, 0, -29)
	df := func(t time.Time) string { return t.Format("2006-01-02") }

	specs := []struct {
		filename string
		url      string
	}{
		{"hitter-exp.csv", fmt.Sprintf(savantHitterExpURL, year)},
		{"hitter-statcast.csv", fmt.Sprintf(savantHitterSCURL, year)},
		{"hitter-exp-14d.csv", fmt.Sprintf(savantHitterExp14dURL, year, df(start14), df(end))},
		{"pitcher-exp.csv", fmt.Sprintf(savantPitcherExpURL, year)},
		{"pitcher-exp-30d.csv", fmt.Sprintf(savantPitcherExp30URL, year, df(start30), df(end))},
	}

	var arts []archive.Artifact
	for _, s := range specs {
		body, err := archive.Get(ctx, s.url)
		if err != nil {
			return nil, fmt.Errorf("savant archive %s: %w", s.filename, err)
		}
		arts = append(arts, archive.Artifact{Filename: s.filename, Bytes: body})
	}
	return arts, nil
}
