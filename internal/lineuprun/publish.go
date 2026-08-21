package lineuprun

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// publishLineup serializes today's optimized lineup into the read-only API's
// wire shape and writes it via pub — the caller supplies the destination
// (S3 or local), selected in cmd through internal/statestore. A nil pub is a
// no-op (the caller chose not to publish).
//
// preview selects WHICH keys are written, and is the whole point of the split:
//
//   - preview=false: the lineup was applied to Fantrax. Publishes under both
//     "today" (the alias the endpoint serves) and the date string.
//   - preview=true: a dry run computed this lineup but applied nothing.
//     Publishes under "preview" ONLY — never "today", which must keep
//     describing what is actually set on the roster, and never the date key,
//     which is the historical record backtesting reads.
//
// now stamps generated_at so a client holding both blobs can order them.
func publishLineup(dr dateResult, cfg *config.Config, hitterSlots, pitcherSlots []fantrax.Slot, hkbMeta map[string]lineupapi.Dynasty, pub lineupapi.Publisher, now time.Time, preview bool) error {
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
		HKB:          hkbMeta,
		GeneratedAt:  now.UTC().Format(time.RFC3339),
	})
	data, err := lineupapi.Marshal(resp)
	if err != nil {
		return err
	}
	if preview {
		return pub.Publish(lineupapi.PreviewKey, data)
	}
	if err := pub.Publish(lineupapi.TodayKey, data); err != nil {
		return err
	}
	return pub.Publish(dr.date.Format("2006-01-02"), data)
}
