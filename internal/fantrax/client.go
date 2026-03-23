package fantrax

import (
	"fmt"
	"strings"
	"time"

	gofantrax "github.com/pmurley/go-fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
)

// Player is a simplified view of a rostered hitter.
type Player struct {
	ID             string
	Name           string
	MLBTeam        string // short team name, e.g. "NYY"
	Positions      []string
	RosterPosition string // slot they are currently in
	Status         string // "Active", "Reserve", "Injured Reserve", "Minors"
	NextGameDate   string // "2026-03-22" or "" if no game found
}

// Slot describes one active roster slot and the position ID required to fill it.
type Slot struct {
	PosID   string // auth_client constant, e.g. auth_client.PosC
	PosName string // human-readable, e.g. "C"
}

// ScoringWeights maps stat short-names (as returned by Fantrax) to point values.
type ScoringWeights map[string]float64

// Client wraps the go-fantrax libraries and exposes only what the optimizer needs.
type Client struct {
	public   *gofantrax.Client
	auth     *auth_client.Client
	leagueID string
	teamID   string
}

// NewClient creates both the public (read) and auth (read+write) Fantrax clients.
// auth_client.NewClient will use chromedp to log in if no cookie cache exists.
func NewClient(leagueID, teamID string) (*Client, error) {
	pub, err := gofantrax.NewClient(leagueID, false)
	if err != nil {
		return nil, fmt.Errorf("fantrax public client: %w", err)
	}

	auth, err := auth_client.NewClient(leagueID, false)
	if err != nil {
		return nil, fmt.Errorf("fantrax auth client: %w", err)
	}

	return &Client{
		public:   pub,
		auth:     auth,
		leagueID: leagueID,
		teamID:   teamID,
	}, nil
}

// GetHitterRoster returns all hitters on the team (active + reserve; excludes IL/minors).
func (c *Client) GetHitterRoster() ([]Player, error) {
	roster, err := c.auth.GetCurrentPeriodTeamRosterInfo(c.teamID)
	if err != nil {
		return nil, fmt.Errorf("get team roster: %w", err)
	}

	var players []Player
	for _, rp := range append(roster.ActiveRoster, roster.ReserveRoster...) {
		if !isHitter(rp) {
			continue
		}
		players = append(players, toPlayer(rp))
	}
	return players, nil
}

// GetActiveSlots returns the ordered list of active hitter slots for the league.
// Slot order determines filling priority (positional slots before UTIL).
func (c *Client) GetActiveSlots() ([]Slot, error) {
	info, err := c.public.GetLeagueInfo(c.leagueID)
	if err != nil {
		return nil, fmt.Errorf("get league info: %w", err)
	}

	// Fixed ordering: positional slots first, UTIL last.
	order := []struct {
		code  string
		posID string
		name  string
	}{
		{"C", auth_client.PosC, "C"},
		{"1B", auth_client.Pos1B, "1B"},
		{"2B", "003", "2B"},
		{"3B", auth_client.Pos3B, "3B"},
		{"SS", auth_client.PosSS, "SS"},
		{"MI", auth_client.PosMI, "MI"},
		{"CF", auth_client.PosCF, "CF"},
		{"OF", auth_client.PosOF, "OF"},
		{"UTIL", auth_client.PosUtil, "UTIL"},
	}

	var slots []Slot
	for _, o := range order {
		constraint, ok := info.RosterInfo.PositionConstraints[o.code]
		if !ok {
			continue
		}
		for i := 0; i < constraint.MaxActive; i++ {
			slots = append(slots, Slot{PosID: o.posID, PosName: o.name})
		}
	}
	return slots, nil
}

// GetScoringWeights returns hitting stat → point value from league settings.
func (c *Client) GetScoringWeights() (ScoringWeights, error) {
	info, err := c.public.GetLeagueInfo(c.leagueID)
	if err != nil {
		return nil, fmt.Errorf("get league info: %w", err)
	}

	weights := make(ScoringWeights)
	for _, setting := range info.ScoringSystem.ScoringCategorySettings {
		if setting.Group.Code != "HITTING" {
			continue
		}
		for _, cfg := range setting.Configs {
			if cfg.Points != 0 {
				weights[cfg.ScoringCategory.ShortName] = cfg.Points
			}
		}
	}
	return weights, nil
}

// ApplyLineup sends the full updated fieldMap to Fantrax.
// It fetches the current roster, applies the desired active/reserve assignments,
// and calls ConfirmOrExecuteTeamRosterChanges.
func (c *Client) ApplyLineup(active []PlayerSlot, reserve []string) error {
	editor, err := c.auth.NewRosterEditor(0, c.teamID, false, true)
	if err != nil {
		return fmt.Errorf("create roster editor: %w", err)
	}

	for _, ps := range active {
		if err := editor.MoveToActive(ps.PlayerID, ps.PosID); err != nil {
			return fmt.Errorf("move %s to active: %w", ps.PlayerID, err)
		}
	}
	for _, id := range reserve {
		if err := editor.MoveToReserve(id); err != nil {
			return fmt.Errorf("move %s to reserve: %w", id, err)
		}
	}

	result, err := editor.Apply(false)
	if err != nil {
		return fmt.Errorf("apply roster changes: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("roster change rejected: %s", result.ErrorMessage)
	}
	return nil
}

// PlayerSlot pairs a player ID with the active slot's position ID.
type PlayerSlot struct {
	PlayerID string
	PosID    string
}

// isHitter returns true if the player is a position player (not a pure pitcher).
func isHitter(rp models.RosterPlayer) bool {
	for _, pos := range rp.Positions {
		pos = strings.ToUpper(pos)
		if pos == "SP" || pos == "RP" || pos == "P" {
			continue
		}
		return true
	}
	return false
}

func toPlayer(rp models.RosterPlayer) Player {
	nextDate := ""
	if rp.NextGame != nil && rp.NextGame.DateTime != "" {
		nextDate = extractDate(rp.NextGame.DateTime)
	}
	return Player{
		ID:             rp.PlayerID,
		Name:           rp.Name,
		MLBTeam:        rp.TeamShortName,
		Positions:      rp.Positions,
		RosterPosition: rp.RosterPosition,
		Status:         rp.Status,
		NextGameDate:   nextDate,
	}
}

// extractDate returns YYYY-MM-DD from a datetime string.
func extractDate(dt string) string {
	// DateTime may be "2026-03-22T13:05:00" or "March 22, 2026 1:05 PM" — normalize.
	t, err := time.Parse("2006-01-02T15:04:05", dt)
	if err != nil {
		// Try to parse other common formats.
		for _, layout := range []string{"January 2, 2006 3:04 PM", "Jan 2, 2006 3:04 PM"} {
			if t2, e2 := time.Parse(layout, dt); e2 == nil {
				t = t2
				err = nil
				break
			}
		}
	}
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}
