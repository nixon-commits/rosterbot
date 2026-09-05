package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/tradeboard"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
)

// executedLookback bounds how far back the executed-trade feed is consulted
// when deciding whether a vanished offer was accepted. An offer resolves
// within days, so a month is generous; the feed is a single cached call
// regardless, so the window costs nothing but is bounded on principle.
const executedLookback = 30 * 24 * time.Hour

// captureTradeOffers writes the Trades tab's pending-offer snapshot and
// appends to the durable offer log.
//
// It runs on the hourly Lineup job because that is the only schedule fast
// enough to matter: a trade offer is acted on in hours, and the daily jobs
// would show it up to a day late. It costs one small league-home call --
// notably NOT the 10.7 MB player pool, which is why the league values table
// is produced by the daily team-values job instead and merely read here.
//
// Every failure below is the caller's to swallow. This is bolted onto the
// lineup hot path and must never be able to take it down; the precedent is
// cmd/grade.go writing lineup gaps.
func captureTradeOffers(ctx context.Context, ft *fantrax.Client, cfg *config.Config, now time.Time) error {
	pending, err := ft.GetPendingTrades()
	if err != nil {
		return fmt.Errorf("get pending trades: %w", err)
	}

	// Resolve my team ID to the name the trade feed uses. Everything
	// downstream keys on names because that is all PendingTrade carries.
	_, teamNames, _, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return fmt.Errorf("get teams: %w", err)
	}
	myTeam := teamNames[cfg.TeamID]
	if myTeam == "" {
		return fmt.Errorf("team %s not found in standings", cfg.TeamID)
	}

	hkbPlayers, err := hkb.GetPlayers(ctx, cacheDir)
	if err != nil {
		return fmt.Errorf("get HKB players: %w", err)
	}

	inputs := make([]tradeboard.OfferInput, 0, len(pending))
	for _, p := range pending {
		inputs = append(inputs, tradeboard.OfferInput{
			TradeID:  p.TradeID,
			Player:   p.PlayerName,
			Position: p.Position,
			From:     p.FromTeam,
			To:       p.ToTeam,
		})
	}
	mine := tradeboard.Mine(tradeboard.BuildOffers(inputs, hkbPlayers), myTeam)

	// Impact needs the league's team totals and each player's bucket, both of
	// which live in the daily values table. Its absence degrades the snapshot
	// to offers-without-impact rather than failing it: on a fresh deploy the
	// table does not exist until the first team-values run.
	table := readTradeValues()
	if table != nil {
		byTrade := map[string][]tradeboard.OfferInput{}
		for _, in := range inputs {
			byTrade[in.TradeID] = append(byTrade[in.TradeID], in)
		}
		for i := range mine {
			if mine[i].Verdict.Status == tradevalue.StatusIncomplete {
				mine[i].ImpactNote = "the offer could not be fully priced"
				continue // an offer we cannot price is one we cannot project
			}
			mine[i].Impact, mine[i].ImpactNote = tradeboard.BuildImpact(
				myTeam, byTrade[mine[i].TradeID], table.Teams, table.Players)
		}
	} else {
		for i := range mine {
			mine[i].ImpactNote = "no league value data yet — run team-values"
		}
	}

	snap := tradeboard.NewSnapshot(now, myTeam, mine)
	printTradeSnapshot(snap, table)

	if dryRun {
		fmt.Printf("trades (dry-run): %d offer(s) for %s; not written\n", len(mine), myTeam)
		return nil
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	store, err := statestore.FromEnv().TradesStore()
	if err != nil {
		return fmt.Errorf("open trades store: %w", err)
	}
	if err := store.Publish(lineupapi.TradesCurrentKey, data); err != nil {
		return fmt.Errorf("publish snapshot: %w", err)
	}

	if err := appendOfferLog(ft, now, myTeam, mine); err != nil {
		return fmt.Errorf("append offer log: %w", err)
	}
	fmt.Printf("Wrote %d pending offer(s) for %s\n", len(mine), myTeam)
	return nil
}

// appendOfferLog folds the current offers into the durable log. The log is the
// only record that a rejected offer ever existed -- Fantrax keeps none -- so
// it is written even when there are no open offers, both to advance the
// outcome of anything that just vanished and to keep the daily partition
// present rather than leaving a hole the Infra gap scan would flag.
func appendOfferLog(ft *fantrax.Client, now time.Time, myTeam string, current []tradeboard.Offer) error {
	reader, err := statestore.FromEnv().TradeOfferReader()
	if err != nil {
		return err
	}
	prior, err := reader.ReadAll()
	if err != nil {
		return err
	}

	var executed []tradeboard.ExecutedAsset
	if txs, err := ft.GetRecentTrades(now.Add(-executedLookback)); err == nil {
		for _, tx := range txs {
			executed = append(executed, tradeboard.ExecutedAsset{
				TradeGroupID: tx.TradeGroupID,
				Player:       tx.PlayerName,
				To:           tx.ToTeamName,
			})
		}
	}
	// A failed executed-trade fetch is deliberately not fatal: without it every
	// vanished offer infers "not-executed", which is the same answer the strict
	// matcher gives when it finds nothing, and losing the log entry entirely
	// would be worse than one possibly-understated outcome.

	rows := tradeboard.MergeLog(now, myTeam, tradeboard.Latest(prior), current, executed)
	w, err := statestore.FromEnv().TradeOfferWriter()
	if err != nil {
		return err
	}
	return w.WriteOffers(now, rows)
}

// readTradeValues loads the daily league values table, or nil if it is not
// available yet.
func readTradeValues() *tradeboard.ValuesTable {
	store, err := statestore.FromEnv().TradeValuesStore()
	if err != nil {
		return nil
	}
	data, ok, err := store.Get(context.Background(), lineupapi.TradeValuesKey)
	if err != nil || !ok {
		return nil
	}
	var t tradeboard.ValuesTable
	if err := json.Unmarshal(data, &t); err != nil {
		return nil
	}
	return &t
}

// printTradeSnapshot renders the offers to stdout so a dry-run and the job log
// show the same thing the tab will.
func printTradeSnapshot(s tradeboard.Snapshot, table *tradeboard.ValuesTable) {
	if len(s.Offers) == 0 {
		fmt.Printf("Pending offers for %s: none\n", s.MyTeam)
		return
	}
	fmt.Printf("Pending offers for %s — %s\n", s.MyTeam, s.GeneratedAt)
	if table != nil {
		// Printed verbatim, not reformatted: GeneratedAt is already the
		// RFC3339 string that ships on GET /v1/trades/values, and the job
		// log is where an operator checks what the endpoint is serving.
		fmt.Printf("  (values as of %s, HKB coverage %d/%d)\n",
			table.GeneratedAt, table.Matched, table.Rostered)
	}

	for _, o := range s.Offers {
		fmt.Printf("\n  trade %s\n", o.TradeID)
		for _, side := range o.Sides {
			fmt.Printf("    %s receives  raw %d  adj %.0f  (%d/%d priced)\n",
				side.Team, side.Raw, side.Adjusted, side.PricedCount, side.AssetCount)
			for _, a := range side.Assets {
				val := fmt.Sprintf("%5d", a.Value)
				if !a.Priced {
					val = "    ?"
				}
				fmt.Printf("      %s  %-26s %s\n", val, a.Name, a.Position)
			}
		}
		fmt.Printf("    verdict: %s\n", verdictLine(o.Verdict))
		if o.Impact == nil {
			if o.ImpactNote != "" {
				fmt.Printf("    impact: unavailable — %s\n", o.ImpactNote)
			}
		} else {
			for _, m := range o.Impact.Metrics {
				if m.Delta == 0 && m.RankBefore == m.RankAfter {
					continue
				}
				fmt.Printf("      %-9s %6d -> %-6d (%+d)   rank %d -> %d of %d\n",
					m.Name, m.Before, m.After, m.Delta, m.RankBefore, m.RankAfter, m.Teams)
			}
		}
	}
}

func verdictLine(v tradevalue.Verdict) string {
	switch v.Status {
	case tradevalue.StatusFavors:
		return fmt.Sprintf("favors %s (raw %.1f%%, adjusted %.1f%%)", v.FavoredTeam, v.RawPct, v.AdjPct)
	case tradevalue.StatusDeadEven:
		return "no verdict — the leading sides price exactly level"
	case tradevalue.StatusTooClose:
		return fmt.Sprintf("too close to call — raw %s, adjusted %s",
			tradevalue.MethodReading(v.RawLeader, v.RawPct), tradevalue.MethodReading(v.AdjLeader, v.AdjPct))
	default:
		return fmt.Sprintf("incomplete — %d asset(s) could not be priced", v.UnpricedAssets)
	}
}
