package roster

import "github.com/nixon-commits/fantrax-optimizer/internal/fantrax"

// AlertType classifies the roster mismatch.
type AlertType string

const (
	HealthyInIL      AlertType = "healthy-in-il"
	CalledUpInMinors AlertType = "called-up-in-minors"
	InjuredInActive  AlertType = "injured-in-active"
	MinorInActive    AlertType = "minor-in-active"
)

// Alert describes a single roster mismatch.
type Alert struct {
	Player     fantrax.Player
	Type       AlertType
	Suggestion string
}

// CheckRoster scans the full roster for players in the wrong slot.
func CheckRoster(players []fantrax.Player) []Alert {
	var alerts []Alert
	for _, p := range players {
		switch p.Status {
		case "Injured Reserve":
			if !p.IsInjured {
				alerts = append(alerts, Alert{
					Player:     p,
					Type:       HealthyInIL,
					Suggestion: "move to Active/Reserve",
				})
			}
		case "Minors":
			if !p.InMinors {
				alerts = append(alerts, Alert{
					Player:     p,
					Type:       CalledUpInMinors,
					Suggestion: "move to Active/Reserve",
				})
			}
		case "Active", "Reserve":
			if p.IsInjured {
				alerts = append(alerts, Alert{
					Player:     p,
					Type:       InjuredInActive,
					Suggestion: "move to IL",
				})
			}
			if p.InMinors {
				alerts = append(alerts, Alert{
					Player:     p,
					Type:       MinorInActive,
					Suggestion: "move to Minors",
				})
			}
		}
	}
	return alerts
}
