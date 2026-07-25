package lineuprun

import (
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// publishLineup serializes today's optimized lineup into the read-only API's
// wire shape and writes it via pub — the caller supplies the destination
// (S3 or local), selected in cmd through internal/statestore. A nil pub is a
// no-op (the caller chose not to publish). It publishes under both "today"
// (the alias the endpoint serves) and the date string.
func publishLineup(dr dateResult, cfg *config.Config, hitterSlots, pitcherSlots []fantrax.Slot, pub lineupapi.Publisher) error {
	if pub == nil {
		return nil
	}
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
	if err := pub.Publish(lineupapi.TodayKey, data); err != nil {
		return err
	}
	return pub.Publish(dr.date.Format("2006-01-02"), data)
}
