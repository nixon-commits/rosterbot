package claims

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Ledger is the persisted daily audit record of processed claims.
type Ledger struct {
	Date        string        `json:"date"`
	GeneratedAt time.Time     `json:"generated_at"`
	Entries     []LedgerEntry `json:"entries"`
}

type LedgerEntry struct {
	Team      string        `json:"team"`
	TeamID    string        `json:"team_id"`
	ClaimType string        `json:"claim_type"`
	Added     LedgerPlayer  `json:"added"`
	Dropped   *LedgerPlayer `json:"dropped,omitempty"`
	NetValue  int           `json:"net_value"`
	BidAmount string        `json:"bid_amount,omitempty"`
	Priority  string        `json:"priority,omitempty"`
}

type LedgerPlayer struct {
	Name         string  `json:"name"`
	Pos          string  `json:"pos"`
	MLBAMID      int     `json:"mlbam_id,omitempty"`
	HKBValue     int     `json:"hkb_value"`
	HKBRank      int     `json:"hkb_rank,omitempty"`
	Signal       string  `json:"signal,omitempty"`
	ProjectedFPG float64 `json:"projected_pts_per_game,omitempty"`
}

// BuildLedger flattens moves into ledger entries (one per added player). The
// move's net value and the (first) dropped player are attributed to each entry.
func BuildLedger(day time.Time, moves []Move) Ledger {
	led := Ledger{Date: day.Format("2006-01-02"), GeneratedAt: time.Now().UTC()}
	for _, m := range moves {
		var dropped *LedgerPlayer
		if len(m.Dropped) > 0 {
			d := ledgerPlayer(m.Dropped[0])
			dropped = &d
		}
		for _, a := range m.Added {
			led.Entries = append(led.Entries, LedgerEntry{
				Team:      m.TeamName,
				TeamID:    m.TeamID,
				ClaimType: m.ClaimType,
				Added:     ledgerPlayer(a),
				Dropped:   dropped,
				NetValue:  m.NetValue(),
				BidAmount: m.BidAmount,
				Priority:  m.Priority,
			})
		}
	}
	return led
}

func ledgerPlayer(p SidePlayer) LedgerPlayer {
	return LedgerPlayer{
		Name: p.Name, Pos: p.Position, MLBAMID: p.MLBAMID,
		HKBValue: p.Value, HKBRank: p.Rank,
		Signal: p.Signal.String(), ProjectedFPG: p.ProjectedFPG,
	}
}

// WriteLedger writes the ledger to <dir>/<date>.json, creating dir as needed.
func WriteLedger(dir string, led Ledger) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(led, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, led.Date+".json"), data, 0o644)
}
