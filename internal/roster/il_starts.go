package roster

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

// ILStart pairs a player stranded in an IL slot with the date their MLB club
// has announced them as a probable starter.
type ILStart struct {
	Player    fantrax.Player
	StartDate time.Time
}

// CheckILStarters returns players sitting in an IL slot whom MLB has announced
// as probable starters on date.
func CheckILStarters(players []fantrax.Player, probables map[string]string, date time.Time) (starts []ILStart, teamMismatch []string) {
	for _, p := range players {
		if p.Status != "Injured Reserve" {
			continue
		}
		team, ok := probables[playername.Normalize(p.Name)]
		if !ok {
			continue
		}
		if team != p.MLBTeam {
			teamMismatch = append(teamMismatch, p.Name)
			continue
		}
		starts = append(starts, ILStart{Player: p, StartDate: date})
	}
	return starts, teamMismatch
}
