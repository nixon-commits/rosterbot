package dynasty

import (
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// RoleChange is an unrostered player who became a depth-chart starter since
// the prior capture. This is the signal that caught Lock and Wentz the week
// they drew real FAAB while StatsGuy's value sat still (measured 2026-09-21):
// the value feed lags the news; the depth chart carries it.
type RoleChange struct {
	PlayerID, Name, Team, Position, DepthChartPosition string
	// PrevOrder is the prior capture's order (0 = unlisted); PrevTeam is the
	// prior club when it differs (a new starting job on a new club counts).
	PrevOrder int
	PrevTeam  string
	Value     int
	Priced    bool
}

// DroppedPlayer is a priced player a league member released.
type DroppedPlayer struct {
	TransactionID, PlayerID, Name, Position, Team string
	// Kind is the transaction type: waiver, free_agent or chopped.
	Kind          string
	DroppedBy     int
	DroppedByName string
	Value         int
	// Created is the transaction's SUBMISSION time (epoch millis), kept for
	// the record; detection keys on completion (see DetectDrops).
	Created int64
}

// Chop is one guillotine elimination: the roster and its priced players,
// all of whom hit waivers at once. Only the guillotine league emits the type
// (measured 2026-09-21: 14 players in one chop, four of them worth 2,000+).
type Chop struct {
	TransactionID string
	RosterID      int
	RosterName    string
	Players       []DroppedPlayer
}

// bundlePlayer prices id from the bundle, tolerating a nil bundle (no
// valuations loaded) as "unpriced" rather than a panic.
func bundlePlayer(bundle *statsguy.Bundle, id string) (statsguy.Player, bool) {
	if bundle == nil {
		return statsguy.Player{}, false
	}
	p, ok := bundle.Players[id]
	return p, ok
}

// DetectRoleChanges lists cur's unrostered players at depth-chart order 1
// who were not at order 1 on the same club in prev: lower, unlisted, absent,
// or on another club. Order is (position, name, id) so a digest is stable.
//
// It returns nil unless BOTH captures carry depth data. The player dump is
// cached for 24 h, so a capture built from a cache entry the previous binary
// wrote has order 0 for every player; diffing the next real capture against
// it would report every unrostered starter in the league as a role change.
// Silence is the safe failure here, but it lasts TWO runs after a deploy, not
// one: the first depth-less capture still becomes the baseline (the pointer
// moves on any fully delivered run), so the next run's pair is depth-less on
// its prev side too. The coverage line's depth=<prev>/<cur> shows it.
func DetectRoleChanges(prev, cur PlayerSnapshot, rostered map[string]bool, bundle *statsguy.Bundle, format string) []RoleChange {
	if !prev.HasDepthData || !cur.HasDepthData {
		return nil
	}
	var out []RoleChange
	for id, p := range cur.Players {
		if rostered[id] || p.DepthChartOrder != 1 {
			continue
		}
		before, had := prev.Players[id]
		if had && before.DepthChartOrder == 1 && before.Team == p.Team {
			continue // already the starter there
		}
		rc := RoleChange{PlayerID: id, Name: p.Name, Team: p.Team, Position: p.Position, DepthChartPosition: p.DepthChartPosition}
		if had {
			rc.PrevOrder = before.DepthChartOrder
			if before.Team != p.Team {
				rc.PrevTeam = before.Team
			}
		}
		// Priced only on a POSITIVE value: the bundle lists a player with a
		// zero in any format that does not value him, and "(0)" on a digest
		// line reads as a price rather than as the absence of one.
		if sv, ok := bundlePlayer(bundle, id); ok {
			if v := sv.Value.Get(format); v > 0 {
				rc.Value, rc.Priced = v, true
			}
		}
		out = append(out, rc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].PlayerID < out[j].PlayerID
	})
	return out
}

// pickupOverlap is how far behind the baseline DetectDrops looks. It must be
// at least sleeper.RosterTTL (15 minutes), the lifetime of the transactions
// feed in the S3-backed cache shared with other jobs: a transaction that
// completes after that cache was filled but before the next baseline's capture
// time is invisible to this run, so the next run has to look back past its
// baseline to catch it. Re-detecting an event inside the overlap is harmless,
// because the drop and chop markers key on (transaction, player). One hour
// leaves margin over the cache lifetime.
const pickupOverlap = time.Hour

// DetectDrops lists the priced players released by completed waiver or
// free-agent transactions that COMPLETED after since (less pickupOverlap) and
// still unrostered now, and groups guillotine chops by transaction.
//
// Completion is Transaction.StatusUpdated, falling back to Created when the
// feed carries none. Created is when a waiver claim was SUBMITTED; the claim
// completes when waivers process, hours or days later, and a pending claim is
// invisible in the public feed. Filtering on Created therefore skipped every
// claim submitted before the previous baseline and processed after it, on the
// day it became visible, and the advancing pointer never re-examined it
// (measured 2026-09-30: 10 of 24 completed waiver drops, 42%, in one league;
// free-agent moves complete instantly and were unaffected).
//
// A trade lists its swapped players under drops too and is excluded by type:
// those are not releases (measured 2026-09-21, the dynasty league's top three
// "drops" were all trade halves). An unpriced player — absent from the bundle,
// or valued 0 in the league's column — is not reported: the spec prices drops
// in the league's column, and a drop with no value is not the signal this job
// carries.
//
// Each Drops map is iterated in random order, so every output slice is sorted
// on a total key (value desc, name, player id; chops by transaction id).
func DetectDrops(txns []sleeper.Transaction, since time.Time, rostered map[string]bool, players map[string]sleeper.Player, bundle *statsguy.Bundle, format string, names map[int]string) (drops []DroppedPlayer, chops []Chop) {
	sinceMillis := since.Add(-pickupOverlap).UnixMilli()
	chopByTxn := map[string]*Chop{}
	for _, t := range txns {
		at := t.StatusUpdated
		if at == 0 {
			at = t.Created
		}
		if t.Status != "complete" || at <= sinceMillis {
			continue
		}
		if t.Type != "waiver" && t.Type != "free_agent" && t.Type != "chopped" {
			continue
		}
		for pid, byRoster := range t.Drops {
			if rostered[pid] {
				continue // claimed since; not available
			}
			sv, inBundle := bundlePlayer(bundle, pid)
			// A zero in the league's own column is not a price either: the
			// bundle keeps a player listed with 0 in a format that does not
			// value him (measured live 2026-09-30: "DROP Drew Lock (0)" in the
			// redraft leagues), and a valued drop must have a value.
			if !inBundle || sv.Value.Get(format) <= 0 {
				continue
			}
			p := players[pid]
			d := DroppedPlayer{
				TransactionID: t.TransactionID, PlayerID: pid, Name: PlayerDisplayName(p),
				Position: p.Position, Team: p.Team, Kind: t.Type,
				DroppedBy: byRoster, DroppedByName: names[byRoster],
				Value: sv.Value.Get(format), Created: t.Created,
			}
			if d.Name == "" {
				d.Name = pid
			}
			if t.Type == "chopped" {
				c, ok := chopByTxn[t.TransactionID]
				if !ok {
					c = &Chop{TransactionID: t.TransactionID, RosterID: byRoster, RosterName: names[byRoster]}
					chopByTxn[t.TransactionID] = c
				}
				c.Players = append(c.Players, d)
				continue
			}
			drops = append(drops, d)
		}
	}
	byValue := func(ds []DroppedPlayer) {
		sort.Slice(ds, func(i, j int) bool {
			if ds[i].Value != ds[j].Value {
				return ds[i].Value > ds[j].Value
			}
			if ds[i].Name != ds[j].Name {
				return ds[i].Name < ds[j].Name
			}
			if ds[i].PlayerID != ds[j].PlayerID {
				return ds[i].PlayerID < ds[j].PlayerID
			}
			return ds[i].TransactionID < ds[j].TransactionID
		})
	}
	byValue(drops)
	for _, c := range chopByTxn {
		byValue(c.Players)
		chops = append(chops, *c)
	}
	sort.Slice(chops, func(i, j int) bool { return chops[i].TransactionID < chops[j].TransactionID })
	return drops, chops
}
