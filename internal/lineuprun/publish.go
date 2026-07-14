package lineuprun

import (
	"context"
	"os"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
)

// publishLineup serializes today's optimized lineup into the read-only API's
// wire shape and writes it to object storage — S3 (lineup/ prefix) when running
// on Fargate (STATE_BUCKET set), otherwise the local .lineup/ dir. It publishes
// under both "today" (the alias the endpoint serves) and the date string.
func publishLineup(dr dateResult, cfg *config.Config, hitterSlots, pitcherSlots []fantrax.Slot) error {
	resp := lineupapi.Build(lineupapi.Inputs{
		Date:         dr.date.Format("2006-01-02"),
		LeagueID:     cfg.LeagueID,
		TeamID:       cfg.TeamID,
		HitterSlots:  hitterSlots,
		PitcherSlots: pitcherSlots,
		Hitters:      dr.hitterResult.Scored,
		Pitchers:     dr.pitcherResult.Scored,
		BenchedToday: dr.benchedToday,
		DataWarnings: dr.warnings,
	})
	data, err := lineupapi.Marshal(resp)
	if err != nil {
		return err
	}

	var pub lineupapi.Publisher
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		p, err := s3lineup.New(context.Background(), bucket, "lineup/")
		if err != nil {
			return err
		}
		pub = p
	} else {
		pub = lineupapi.NewFileStore(".lineup")
	}

	if err := pub.Publish(lineupapi.TodayKey, data); err != nil {
		return err
	}
	return pub.Publish(dr.date.Format("2006-01-02"), data)
}
