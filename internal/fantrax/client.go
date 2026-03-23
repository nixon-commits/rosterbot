package fantrax

import (
	"fmt"
	"os"
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
	MLBTeam        string   // short team name, e.g. "NYY"
	Positions      []string // Fantrax position ID strings, e.g. ["001", "014"]
	RosterPosition string   // slot they are currently in (position ID)
	Status         string   // "Active", "Reserve", "Injured Reserve", "Minors"
	NextGameDate   string   // "2026-03-22" or "" if no game found
	InMinors       bool     // true if player is currently in the minor leagues (icon "4")
	IsInjured      bool     // true if player is on IL, day-to-day, or out indefinitely
}

// Slot describes one active roster slot.
// PosID is the auth_client constant (e.g. "001"), PosName is the league key (e.g. "C").
type Slot struct {
	PosID   string
	PosName string
}

// ScoringWeights maps stat short-names to point values.
type ScoringWeights map[string]float64

// posNameToID maps league position constraint keys to auth_client position ID strings.
var posNameToID = map[string]string{
	"C":   auth_client.PosC,    // "001"
	"1B":  auth_client.Pos1B,   // "002"
	"2B":  "003",
	"3B":  auth_client.Pos3B,   // "004"
	"SS":  auth_client.PosSS,   // "005"
	"INF": "008",               // infield utility
	"OF":  auth_client.PosOF,   // "012"
	"UT":  auth_client.PosUtil, // "014"
}

// pitcherPosIDs are the Fantrax position IDs that indicate a pitcher slot.
var pitcherPosIDs = map[string]bool{
	auth_client.PosSP:  true, // "015"
	auth_client.PosRP:  true, // "016"
	auth_client.PosP:   true, // "017"
	auth_client.PosRP2: true, // "043"
	auth_client.PosRP3: true, // "044"
}

// Client wraps the go-fantrax libraries.
type Client struct {
	public   *gofantrax.Client
	auth     *auth_client.Client
	leagueID string
	teamID   string
}

// NewClient creates both the public (read) and auth (read+write) Fantrax clients.
func NewClient(leagueID, teamID string) (*Client, error) {
	if err := os.MkdirAll(auth_client.CacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	pub, err := gofantrax.NewClient(leagueID, false)
	if err != nil {
		return nil, fmt.Errorf("fantrax public client: %w", err)
	}

	authc, err := auth_client.NewClient(leagueID, false)
	if err != nil {
		return nil, fmt.Errorf("fantrax auth client: %w", err)
	}

	return &Client{
		public:   pub,
		auth:     authc,
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
func (c *Client) GetActiveSlots() ([]Slot, error) {
	info, err := c.public.GetLeagueInfo(c.leagueID)
	if err != nil {
		return nil, fmt.Errorf("get league info: %w", err)
	}

	// Ordered: positional slots first, utility last.
	order := []string{"C", "1B", "2B", "3B", "SS", "INF", "OF", "UT"}

	var slots []Slot
	for _, name := range order {
		posID, ok := posNameToID[name]
		if !ok {
			continue
		}
		constraint, ok := info.RosterInfo.PositionConstraints[name]
		if !ok {
			continue
		}
		for i := 0; i < constraint.MaxActive; i++ {
			slots = append(slots, Slot{PosID: posID, PosName: name})
		}
	}
	return slots, nil
}

// GetScoringWeights returns hitting stat short-names → point values.
func (c *Client) GetScoringWeights() (ScoringWeights, error) {
	info, err := c.public.GetLeagueInfo(c.leagueID)
	if err != nil {
		return nil, fmt.Errorf("get league info: %w", err)
	}

	weights := make(ScoringWeights)
	for _, setting := range info.ScoringSystem.ScoringCategorySettings {
		if setting.Group.Code != "BASEBALL_HITTING" {
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

// ApplyLineup sends the updated lineup to Fantrax.
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
		if strings.Contains(result.ErrorMessage, "no changes detected") ||
			strings.Contains(strings.ToLower(result.ErrorMessage), "same lineup") {
			return nil // already optimal
		}
		return fmt.Errorf("roster change rejected: %s", result.ErrorMessage)
	}
	return nil
}

// PlayerSlot pairs a player ID with the active slot's position ID.
type PlayerSlot struct {
	PlayerID string
	PosID    string
}

// isHitter returns true if the player has at least one non-pitcher eligible position.
func isHitter(rp models.RosterPlayer) bool {
	for _, pos := range rp.Positions {
		if !pitcherPosIDs[pos] {
			return true
		}
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
		InMinors:       models.HasIcon(rp.Icons, models.IconMinorLeagues),
		IsInjured:      models.HasInjury(rp.Icons),
	}
}

// extractDate returns YYYY-MM-DD from a datetime string.
func extractDate(dt string) string {
	t, err := time.Parse("2006-01-02T15:04:05", dt)
	if err != nil {
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

// EligibleForSlot returns true if the player's position IDs include the slot's position ID.
// UT ("014") accepts all hitters.
// INF ("008") accepts C, 1B, 2B, 3B, SS.
func EligibleForSlot(playerPositions []string, slot Slot) bool {
	if slot.PosID == auth_client.PosUtil { // "014" - UT accepts anyone
		return true
	}
	// INF accepts infield positions.
	if slot.PosID == "008" {
		infPosIDs := map[string]bool{"001": true, "002": true, "003": true, "004": true, "005": true}
		for _, pos := range playerPositions {
			if infPosIDs[pos] {
				return true
			}
		}
		return false
	}
	for _, pos := range playerPositions {
		if pos == slot.PosID {
			return true
		}
	}
	return false
}
