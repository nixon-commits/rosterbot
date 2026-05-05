package fantrax

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	gofantrax "github.com/pmurley/go-fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/pmurley/go-fantrax/models"
)

// PendingTrade represents a single player move within a pending trade.
type PendingTrade struct {
	PlayerName string
	Position   string // e.g. "SP", "3B,INF,OF"
	FromTeam   string // fantasy team name
	ToTeam     string // fantasy team name
	TradeID    string // groups players in the same trade
}

// GetPendingTrades returns all pending trades visible in the league home info.
func (c *Client) GetPendingTrades() ([]PendingTrade, error) {
	raw, err := c.auth.GetLeagueHomeInfoRaw()
	if err != nil {
		return nil, fmt.Errorf("get league home info: %w", err)
	}

	var envelope struct {
		Responses []struct {
			Data struct {
				PendingTransactions struct {
					Sets []struct {
						ID           string `json:"id"`
						Transactions []struct {
							ScorerID     string `json:"scorerId"`
							SourceTeamID string `json:"sourceTeamId"`
							DestTeamID   string `json:"destinationTeamId"`
						} `json:"transactions"`
					} `json:"pendingTransactionSets"`
					ScorerMap map[string]struct {
						Name          string `json:"name"`
						PosShortNames string `json:"posShortNames"`
					} `json:"scorerMap"`
				} `json:"pendingTransactions"`
				FantasyTeams []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"fantasyTeams"`
			} `json:"data"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parse home info: %w", err)
	}
	if len(envelope.Responses) == 0 {
		return nil, nil
	}

	resp := envelope.Responses[0].Data
	teamMap := make(map[string]string, len(resp.FantasyTeams))
	for _, ft := range resp.FantasyTeams {
		teamMap[ft.ID] = ft.Name
	}
	teamName := func(id string) string {
		if name, ok := teamMap[id]; ok {
			return name
		}
		return id
	}

	var pending []PendingTrade
	for _, set := range resp.PendingTransactions.Sets {
		for _, tx := range set.Transactions {
			scorer := resp.PendingTransactions.ScorerMap[tx.ScorerID]
			pending = append(pending, PendingTrade{
				PlayerName: scorer.Name,
				Position:   scorer.PosShortNames,
				FromTeam:   teamName(tx.SourceTeamID),
				ToTeam:     teamName(tx.DestTeamID),
				TradeID:    set.ID,
			})
		}
	}
	return pending, nil
}

// GetRecentTrades fetches all executed trades and returns those processed after since.
func (c *Client) GetRecentTrades(since time.Time) ([]models.Transaction, error) {
	all, err := c.auth.GetAllTrades()
	if err != nil {
		return nil, fmt.Errorf("fetch trades: %w", err)
	}
	var recent []models.Transaction
	for _, tx := range all {
		if tx.ProcessedDate.After(since) {
			recent = append(recent, tx)
		}
	}
	return recent, nil
}

// GetWeekTransactions returns all executed transactions (claims, drops,
// trades) whose ProcessedDate falls on a calendar date in
// [windowStart, windowEnd] (inclusive). Date comparison is YYYY-MM-DD
// lexical to dodge timezone equality pitfalls — same convention used by
// pairsForWeek in the recap pipeline.
func (c *Client) GetWeekTransactions(windowStart, windowEnd time.Time) ([]models.Transaction, error) {
	all, err := c.auth.GetTransactionHistory("250")
	if err != nil {
		return nil, fmt.Errorf("fetch transactions: %w", err)
	}
	startYMD := windowStart.Format("2006-01-02")
	endYMD := windowEnd.Format("2006-01-02")
	var window []models.Transaction
	for _, tx := range all {
		ymd := tx.ProcessedDate.Format("2006-01-02")
		if ymd >= startYMD && ymd <= endYMD {
			window = append(window, tx)
		}
	}
	return window, nil
}

// Player is a simplified view of a rostered hitter.
type Player struct {
	ID             string
	Name           string
	MLBTeam        string   // short team name, e.g. "NYY"
	Positions      []string // Fantrax position ID strings, e.g. ["001", "014"]
	PosShortNames  string   // display positions, e.g. "SP", "RP", "C,1B"
	RosterPosition string   // slot they are currently in (position ID)
	Status         string   // "Active", "Reserve", "Injured Reserve", "Minors"
	NextGameDate   string   // "2026-03-22" or "" if no game found
	InMinors       bool     // true if player is currently in the minor leagues (icon "4")
	IsInjured      bool     // true if player is on IL, day-to-day, or out indefinitely
	Locked         bool     // true if player's game is in progress or final (cannot be moved)
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
	"C":   auth_client.PosC,  // "001"
	"1B":  auth_client.Pos1B, // "002"
	"2B":  "003",
	"3B":  auth_client.Pos3B,   // "004"
	"SS":  auth_client.PosSS,   // "005"
	"INF": "008",               // infield utility
	"OF":  auth_client.PosOF,   // "012"
	"UT":  auth_client.PosUtil, // "014"
}

// pitcherPosNameToID maps league pitcher slot names to auth_client position ID strings.
var pitcherPosNameToID = map[string]string{
	"SP": auth_client.PosSP, // "015"
	"RP": auth_client.PosRP, // "016"
	"P":  auth_client.PosP,  // "017"
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
	public     *gofantrax.Client
	auth       *auth_client.Client
	leagueID   string
	teamID     string
	leagueInfo *gofantrax.LeagueInfo // cached league info
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

// getLeagueInfo returns cached league info, fetching it on first call.
func (c *Client) getLeagueInfo() (*gofantrax.LeagueInfo, error) {
	if c.leagueInfo != nil {
		return c.leagueInfo, nil
	}
	info, err := c.public.GetLeagueInfo(c.leagueID)
	if err != nil {
		return nil, err
	}
	c.leagueInfo = info
	return info, nil
}

// GetHitterRoster returns all hitters on the team (active + reserve; excludes IL/minors).
func (c *Client) GetHitterRoster() ([]Player, error) {
	return c.GetHitterRosterForPeriod(0)
}

// GetHitterRosterForPeriod returns all hitters for the given scoring period.
// Pass 0 to use the current period.
func (c *Client) GetHitterRosterForPeriod(period int) ([]Player, error) {
	var roster *models.TeamRoster
	var err error
	if period == 0 {
		roster, err = c.auth.GetCurrentPeriodTeamRosterInfo(c.teamID)
	} else {
		roster, err = c.auth.GetTeamRosterInfo(fmt.Sprintf("%d", period), c.teamID)
	}
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

// SlotCounts holds slot usage for IL and Minors roster sections (all players, not just hitters).
type SlotCounts struct {
	ILUsed         int
	ILCapacity     int
	MinorsUsed     int
	MinorsCapacity int
}

// GetFullHitterRoster returns all hitters including IL and Minors slots,
// plus slot usage counts (all players, not just hitters).
// Capacity must be set externally via config (FANTRAX_IL_SLOTS, FANTRAX_MINORS_SLOTS).
func (c *Client) GetFullHitterRoster() ([]Player, SlotCounts, error) {
	var counts SlotCounts

	roster, err := c.auth.GetCurrentPeriodTeamRosterInfo(c.teamID)
	if err != nil {
		return nil, SlotCounts{}, fmt.Errorf("get team roster: %w", err)
	}

	// Count used IL/Minors from the parsed roster (all players, not just hitters).
	counts.ILUsed = len(roster.InjuredReserve)
	counts.MinorsUsed = len(roster.MinorsRoster)

	var all []models.RosterPlayer
	all = append(all, roster.ActiveRoster...)
	all = append(all, roster.ReserveRoster...)
	all = append(all, roster.InjuredReserve...)
	all = append(all, roster.MinorsRoster...)

	var players []Player
	for _, rp := range all {
		if !isHitter(rp) {
			continue
		}
		players = append(players, toPlayer(rp))
	}
	return players, counts, nil
}

// GetMinorsRoster returns all players (hitters and pitchers) currently
// in your Minors roster slot. Used by the prospect report.
func (c *Client) GetMinorsRoster() ([]Player, error) {
	roster, err := c.auth.GetCurrentPeriodTeamRosterInfo(c.teamID)
	if err != nil {
		return nil, fmt.Errorf("get minors roster: %w", err)
	}
	var players []Player
	for _, rp := range roster.MinorsRoster {
		players = append(players, toPlayer(rp))
	}
	return players, nil
}

// ProspectPoolPlayer extends Player with fantasy ranking data from the Fantrax player pool.
type ProspectPoolPlayer struct {
	Player
	FantraxRank     int     // Fantrax overall player rank (lower = better)
	PercentRostered float64 // % of leagues rostering this player
	FantasyTeam     string  // fantasy team abbreviation ("FA", "W", or team abbr)
	FantasyPtsPerG  float64
}

// GetAvailableProspects returns minor-league-eligible players not owned
// by any team in the league. Uses the Fantrax player pool API.
func (c *Client) GetAvailableProspects() ([]Player, error) {
	pool, err := c.auth.GetPlayerPool(
		auth_client.WithStatusFilter(auth_client.StatusFilterAvailable),
	)
	if err != nil {
		return nil, fmt.Errorf("get available prospects: %w", err)
	}
	var players []Player
	for _, pp := range pool {
		if !pp.MinorsEligible {
			continue
		}
		players = append(players, Player{
			ID:            pp.PlayerID,
			Name:          pp.Name,
			MLBTeam:       pp.MLBTeamShortName,
			Positions:     pp.Positions,
			PosShortNames: pp.PosShortNames,
			InMinors:      true,
		})
	}
	return players, nil
}

// GetPlayerPoolRaw returns a single raw page of the player pool API response.
func (c *Client) GetPlayerPoolRaw(page int) (*models.PlayerPoolResponse, error) {
	return c.auth.GetPlayerPoolRaw(auth_client.StatusFilterAll, page)
}

// GetFullPlayerPool returns all players from the Fantrax player pool with
// FantasyStatus populated. The library's parser requires 10 cells but this
// league returns 8, so we parse the raw response and patch the status field.
func (c *Client) GetFullPlayerPool() ([]models.PoolPlayer, error) {
	players, err := c.auth.GetPlayerPool(auth_client.WithStatusFilter(auth_client.StatusFilterAll))
	if err != nil {
		return nil, err
	}

	// The library populates FantasyStatus from cells[1] only when len(cells)>=10.
	// This league returns 8 cells so FantasyStatus is empty. Re-parse from raw.
	statusMap, err := c.buildStatusMap()
	if err != nil {
		return nil, err
	}
	for i := range players {
		if s, ok := statusMap[players[i].PlayerID]; ok {
			players[i].FantasyStatus = s.status
			players[i].FantasyTeamID = s.teamID
			players[i].PercentRostered = s.pctRostered
		}
	}
	return players, nil
}

type playerStatus struct {
	status      string
	teamID      string
	pctRostered float64
}

// buildStatusMap fetches raw pool pages and extracts status from cells[1].
func (c *Client) buildStatusMap() (map[string]playerStatus, error) {
	m := make(map[string]playerStatus)
	page := 1
	for {
		raw, err := c.auth.GetPlayerPoolRaw(auth_client.StatusFilterAll, page)
		if err != nil {
			return nil, fmt.Errorf("raw pool page %d: %w", page, err)
		}
		if len(raw.Responses) == 0 {
			break
		}
		data := raw.Responses[0].Data
		for _, entry := range data.StatsTable {
			if len(entry.Cells) < 2 {
				continue
			}
			id := entry.Scorer.ScorerID
			status := entry.Cells[1].Content
			teamID := entry.Cells[1].TeamID
			var pctRost float64
			// %Rostered is the second-to-last cell
			if idx := len(entry.Cells) - 2; idx >= 0 {
				s := entry.Cells[idx].Content
				s = strings.TrimSuffix(s, "%")
				if f, err := strconv.ParseFloat(s, 64); err == nil {
					pctRost = f
				}
			}
			m[id] = playerStatus{status: status, teamID: teamID, pctRostered: pctRost}
		}
		if page >= data.PaginatedResultSet.TotalNumPages {
			break
		}
		page++
	}
	return m, nil
}

// GetMinorsEligiblePool returns all minors-eligible players (rostered and available)
// with fantasy ranking data. Used by the prospect ranking system.
func (c *Client) GetMinorsEligiblePool() ([]ProspectPoolPlayer, error) {
	pool, err := c.auth.GetPlayerPool(
		auth_client.WithStatusFilter(auth_client.StatusFilterAll),
	)
	if err != nil {
		return nil, fmt.Errorf("get minors eligible pool: %w", err)
	}
	var players []ProspectPoolPlayer
	for _, pp := range pool {
		if !pp.MinorsEligible {
			continue
		}
		players = append(players, ProspectPoolPlayer{
			Player: Player{
				ID:            pp.PlayerID,
				Name:          pp.Name,
				MLBTeam:       pp.MLBTeamShortName,
				Positions:     pp.Positions,
				PosShortNames: pp.PosShortNames,
				InMinors:      true,
			},
			FantraxRank:     pp.Rank,
			PercentRostered: pp.PercentRostered,
			FantasyPtsPerG:  pp.FantasyPointsPerG,
			FantasyTeam:     pp.FantasyStatus,
		})
	}
	return players, nil
}

// GetActiveSlots returns the ordered list of active hitter slots for the league.
func (c *Client) GetActiveSlots() ([]Slot, error) {
	info, err := c.getLeagueInfo()
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
	info, err := c.getLeagueInfo()
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

// ApplyLineup sends the updated lineup to Fantrax for the given scoring period.
// Pass 0 to auto-detect the current period.
//
// On a Fantrax "already locked in this period" rejection, the locked players
// are removed from the payload and the request is retried once. Per-player
// lock state diverges from team-game lock state (mid-day announced lineups,
// doubleheaders, timing edges) so the optimizer can stage moves Fantrax
// considers locked even when our pre-flight LockedTeams check passed.
func (c *Client) ApplyLineup(period int, active []PlayerSlot, reserve []string) error {
	if period == 0 {
		p, err := c.auth.GetCurrentPeriod()
		if err != nil {
			return fmt.Errorf("auto-detect period: %w", err)
		}
		period = p
	}

	rawRoster, err := c.auth.GetTeamRosterInfoRaw(fmt.Sprintf("%d", period), c.teamID)
	if err != nil {
		return fmt.Errorf("get roster for period %d: %w", period, err)
	}

	executor := func(fieldMap map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		return c.auth.ConfirmOrExecuteTeamRosterChangesRaw(period, c.teamID, fieldMap, false, true, false)
	}

	return applyLineupWithLockedPlayerRetry(executor, rawRoster, active, reserve)
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
		PosShortNames:  rp.PosShortNames,
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
// INF ("008") accepts 1B, 2B, 3B, SS (not C).
func EligibleForSlot(playerPositions []string, slot Slot) bool {
	if slot.PosID == auth_client.PosUtil { // "014" - UT accepts anyone
		return true
	}
	// INF accepts infield positions (not catcher).
	if slot.PosID == "008" {
		infPosIDs := map[string]bool{"002": true, "003": true, "004": true, "005": true}
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

// EligibleForPitcherSlot returns true if a pitcher is eligible for the given pitcher slot.
// P ("017") accepts any pitcher (SP or RP eligible).
// SP ("015") only accepts SP-eligible pitchers.
// RP ("016", "043", "044") only accepts RP-eligible pitchers.
func EligibleForPitcherSlot(playerPositions []string, slot Slot) bool {
	if slot.PosID == auth_client.PosP { // "017" - P accepts any pitcher
		for _, pos := range playerPositions {
			if pitcherPosIDs[pos] {
				return true
			}
		}
		return false
	}
	// RP slots ("016", "043", "044") accept RP-eligible pitchers.
	if slot.PosID == auth_client.PosRP || slot.PosID == auth_client.PosRP2 || slot.PosID == auth_client.PosRP3 {
		for _, pos := range playerPositions {
			if pos == auth_client.PosRP {
				return true
			}
		}
		return false
	}
	// SP slot ("015") accepts SP-eligible pitchers.
	if slot.PosID == auth_client.PosSP {
		for _, pos := range playerPositions {
			if pos == auth_client.PosSP {
				return true
			}
		}
		return false
	}
	return false
}
