package recap

import (
	"sort"

	"github.com/pmurley/go-fantrax/models"
)

// BuildRosterActivity transforms a flat list of transactions for the matchup
// week into a per-team activity log. Returns nil if no team made any moves.
//
// Grouping rules (per spec):
//   - Trades: bucket by TradeGroupID, render once per team-side
//   - Swap: same-day exactly 1 CLAIM + 1 DROP for the same team → merged
//   - Otherwise: render claim/drop entries as-is
//
// teamNames maps fantasy TeamID → display name. Unknown TeamIDs use the ID
// as the name fallback.
func BuildRosterActivity(txs []models.Transaction, teamNames map[string]string) *RosterActivity {
	if len(txs) == 0 {
		return nil
	}

	type teamBuilder struct {
		teamID  string
		name    string
		entries []ActivityEntry
	}
	teams := map[string]*teamBuilder{}

	getOrInit := func(teamID string) *teamBuilder {
		if tb, ok := teams[teamID]; ok {
			return tb
		}
		name := teamNames[teamID]
		if name == "" {
			name = teamID
		}
		tb := &teamBuilder{teamID: teamID, name: name}
		teams[teamID] = tb
		return tb
	}

	// 1) Trades — group by TradeGroupID, build per-team entries.
	tradeGroups := map[string][]models.Transaction{}
	for _, tx := range txs {
		if tx.Type == "TRADE" && tx.TradeGroupID != "" {
			tradeGroups[tx.TradeGroupID] = append(tradeGroups[tx.TradeGroupID], tx)
		}
	}
	for _, group := range tradeGroups {
		// Per group, each team's received = players whose ToTeamID == team,
		// sent = players whose FromTeamID == team.
		teamSet := map[string]struct{}{}
		for _, tx := range group {
			teamSet[tx.FromTeamID] = struct{}{}
			teamSet[tx.ToTeamID] = struct{}{}
		}
		for teamID := range teamSet {
			tb := getOrInit(teamID)
			var received, sent []string
			var otherID string
			date := group[0].ProcessedDate
			for _, tx := range group {
				switch teamID {
				case tx.ToTeamID:
					received = append(received, tx.PlayerName)
					if tx.FromTeamID != teamID {
						otherID = tx.FromTeamID
					}
				case tx.FromTeamID:
					sent = append(sent, tx.PlayerName)
					if tx.ToTeamID != teamID {
						otherID = tx.ToTeamID
					}
				}
			}
			otherName := teamNames[otherID]
			if otherName == "" {
				otherName = otherID
			}
			tb.entries = append(tb.entries, ActivityEntry{
				Date:      date,
				Kind:      "trade",
				OtherTeam: otherName,
				Received:  received,
				Sent:      sent,
			})
		}
	}

	// 2) Claims/Drops — bucket per (teamID, YYYY-MM-DD); detect swap = exactly
	// 1 CLAIM + 1 DROP.
	type bucketKey struct {
		teamID string
		date   string
	}
	type bucket struct {
		claims []models.Transaction
		drops  []models.Transaction
	}
	buckets := map[bucketKey]*bucket{}
	for _, tx := range txs {
		if tx.Type != "CLAIM" && tx.Type != "DROP" {
			continue
		}
		key := bucketKey{teamID: tx.TeamID, date: tx.ProcessedDate.Format("2006-01-02")}
		b, ok := buckets[key]
		if !ok {
			b = &bucket{}
			buckets[key] = b
		}
		switch tx.Type {
		case "CLAIM":
			b.claims = append(b.claims, tx)
		case "DROP":
			b.drops = append(b.drops, tx)
		}
	}
	for key, b := range buckets {
		tb := getOrInit(key.teamID)
		if len(b.claims) == 1 && len(b.drops) == 1 {
			tb.entries = append(tb.entries, ActivityEntry{
				Date:    b.claims[0].ProcessedDate,
				Kind:    "swap",
				SwapIn:  b.claims[0].PlayerName,
				SwapOut: b.drops[0].PlayerName,
			})
			continue
		}
		for _, tx := range b.claims {
			tb.entries = append(tb.entries, ActivityEntry{
				Date:      tx.ProcessedDate,
				Kind:      "claim",
				Player:    tx.PlayerName,
				ClaimType: tx.ClaimType,
			})
		}
		for _, tx := range b.drops {
			tb.entries = append(tb.entries, ActivityEntry{
				Date:   tx.ProcessedDate,
				Kind:   "drop",
				Player: tx.PlayerName,
			})
		}
	}

	if len(teams) == 0 {
		return nil
	}

	// Materialize sorted output.
	out := &RosterActivity{Teams: make([]TeamActivity, 0, len(teams))}
	for _, tb := range teams {
		// Stable per-team entry sort: date asc, then a stable secondary on
		// (Kind, Player + SwapIn + OtherTeam) for entries that share a date.
		sort.SliceStable(tb.entries, func(i, j int) bool {
			ei, ej := tb.entries[i], tb.entries[j]
			if !ei.Date.Equal(ej.Date) {
				return ei.Date.Before(ej.Date)
			}
			ki := ei.Kind + ei.Player + ei.SwapIn + ei.OtherTeam
			kj := ej.Kind + ej.Player + ej.SwapIn + ej.OtherTeam
			return ki < kj
		})
		out.Teams = append(out.Teams, TeamActivity{
			TeamID:   tb.teamID,
			TeamName: tb.name,
			Entries:  tb.entries,
		})
	}
	sort.SliceStable(out.Teams, func(i, j int) bool {
		return out.Teams[i].TeamName < out.Teams[j].TeamName
	})

	return out
}
