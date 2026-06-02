package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/nixon-commits/rosterbot/internal/pipeline"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// --- JSON response types ---

type healthResponse struct {
	Status string `json:"status"`
}

type projectionsResponse struct {
	Date             string           `json:"date"`
	ProjectionSystem string           `json:"projectionSystem"`
	Hitters          []hitterJSON     `json:"hitters"`
	Pitchers         []pitcherJSON    `json:"pitchers"`
	Warnings         []string         `json:"warnings"`
}

type hitterJSON struct {
	Name        string  `json:"name"`
	Team        string  `json:"team"`
	Positions   string  `json:"positions"`
	Slot        string  `json:"slot"`
	Status      string  `json:"status"`
	HasGame     bool    `json:"hasGame"`
	Locked      bool    `json:"locked"`
	SteamerPts  float64 `json:"steamerPts"`
	RecentFPG   float64 `json:"recentFPG"`
	SteamerWt   float64 `json:"steamerWt"`
	RecentWt    float64 `json:"recentWt"`
	BlendedPts  float64 `json:"blendedPts"`
	ParkFactor  float64 `json:"parkFactor"`
	MatchupMult float64 `json:"matchupMult"`
	FinalPts    float64 `json:"finalPts"`
	GamesPlayed int     `json:"gamesPlayed"`
	HasRecent   bool    `json:"hasRecent"`
}

type pitcherJSON struct {
	Name        string  `json:"name"`
	Team        string  `json:"team"`
	Positions   string  `json:"positions"`
	Slot        string  `json:"slot"`
	Status      string  `json:"status"`
	HasGame     bool    `json:"hasGame"`
	IsStarter   bool    `json:"isStarter"`
	SteamerPts  float64 `json:"steamerPts"`
	RecentFPG   float64 `json:"recentFPG"`
	SteamerWt   float64 `json:"steamerWt"`
	RecentWt    float64 `json:"recentWt"`
	ExpectedPts float64 `json:"expectedPts"`
	GamesPlayed int     `json:"gamesPlayed"`
	IsSP        bool    `json:"isSP"`
}

type blendCurveResponse struct {
	HitterCurve    []curvePoint    `json:"hitterCurve"`
	PitcherSPCurve []curvePoint    `json:"pitcherSPCurve"`
	PitcherRPCurve []curvePoint    `json:"pitcherRPCurve"`
	RosterPlayers  []rosterDot     `json:"rosterPlayers"`
}

type curvePoint struct {
	GP       int     `json:"gp"`
	SteamerWt float64 `json:"steamerWt"`
	RecentWt  float64 `json:"recentWt"`
}

type rosterDot struct {
	Name      string  `json:"name"`
	GP        int     `json:"gp"`
	SteamerWt float64 `json:"steamerWt"`
	RecentWt  float64 `json:"recentWt"`
	Type      string  `json:"type"` // "hitter", "pitcherSP", "pitcherRP"
}

type lineupDiffResponse struct {
	Date               string        `json:"date"`
	Current            lineupSide    `json:"current"`
	Optimized          lineupSide    `json:"optimized"`
	Changes            []changeJSON  `json:"changes"`
	ChangedPlayerNames []string      `json:"changedPlayerNames"`
	TotalDelta         float64       `json:"totalDelta"`
	Warnings           []string      `json:"warnings"`
}

type lineupSide struct {
	Hitters  []slotPlayer `json:"hitters"`
	Pitchers []slotPlayer `json:"pitchers"`
}

type slotPlayer struct {
	Name    string  `json:"name"`
	Team    string  `json:"team"`
	Slot    string  `json:"slot"`
	Pts     float64 `json:"pts"`
	HasGame bool    `json:"hasGame"`
}

type changeJSON struct {
	PlayerName string  `json:"playerName"`
	Direction  string  `json:"direction"`
	ToSlot     string  `json:"toSlot"`
	PtsDelta   float64 `json:"ptsDelta"`
}

type compareResponse struct {
	Date    string               `json:"date"`
	Systems []compareSystemEntry `json:"systems"`
}

type compareSystemEntry struct {
	ProjectionSystem string            `json:"projectionSystem"`
	Hitters          []compareHitter   `json:"hitters"`
	Pitchers         []comparePitcher  `json:"pitchers"`
	Error            string            `json:"error,omitempty"`
}

type compareHitter struct {
	Name       string  `json:"name"`
	Team       string  `json:"team"`
	Positions  string  `json:"positions"`
	SteamerPts float64 `json:"steamerPts"`
	BlendedPts float64 `json:"blendedPts"`
	FinalPts   float64 `json:"finalPts"`
}

type comparePitcher struct {
	Name        string  `json:"name"`
	Team        string  `json:"team"`
	Positions   string  `json:"positions"`
	SteamerPts  float64 `json:"steamerPts"`
	ExpectedPts float64 `json:"expectedPts"`
	IsSP        bool    `json:"isSP"`
}

// --- Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (s *Server) handleProjections(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	proj := r.URL.Query().Get("projections")
	result, err := s.getResult(date, proj)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	slotName := buildSlotNames(result)

	resp := projectionsResponse{
		Date:             result.Date.Format("2006-01-02"),
		ProjectionSystem: result.ProjectionSystem,
		Warnings:         result.Warnings,
	}

	for _, h := range result.Hitters {
		hj := hitterJSON{
			Name:        h.Player.Name,
			Team:        h.Player.MLBTeam,
			Positions:   h.Player.PosShortNames,
			Slot:        slotName[h.Player.RosterPosition],
			Status:      h.Player.Status,
			HasGame:     h.HasGame,
			Locked:      h.Player.Locked,
			ParkFactor:  h.ParkFactor,
			MatchupMult: h.MatchupMult,
			FinalPts:    h.ExpectedPts,
		}
		if h.Breakdown != nil {
			hj.SteamerPts = h.Breakdown.BasePts
			hj.RecentFPG = h.Breakdown.RecentFPG
			hj.SteamerWt = h.Breakdown.BaseWt
			hj.RecentWt = h.Breakdown.RecentWt
			hj.BlendedPts = h.Breakdown.BlendedPts
			hj.GamesPlayed = h.Breakdown.GamesPlayed
			hj.HasRecent = h.Breakdown.HasRecent
		}
		resp.Hitters = append(resp.Hitters, hj)
	}

	for _, p := range result.Pitchers {
		resp.Pitchers = append(resp.Pitchers, pitcherJSON{
			Name:        p.Player.Name,
			Team:        p.Player.MLBTeam,
			Positions:   p.Player.PosShortNames,
			Slot:        slotName[p.Player.RosterPosition],
			Status:      p.Player.Status,
			HasGame:     p.HasGame,
			IsStarter:   p.IsStarter,
			SteamerPts:  p.SteamerPts,
			RecentFPG:   p.RecentFPG,
			SteamerWt:   p.SteamerWt,
			RecentWt:    p.RecentWt,
			ExpectedPts: p.ExpectedPts,
			GamesPlayed: p.GamesPlayed,
			IsSP:        p.IsSP,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBlendCurve(w http.ResponseWriter, r *http.Request) {
	resp := blendCurveResponse{}

	// Generate hitter curve (GP 0 to 162).
	for gp := 0; gp <= 162; gp++ {
		sw, rw := projections.HitterBlendWeightsForDisplay(gp)
		resp.HitterCurve = append(resp.HitterCurve, curvePoint{GP: gp, SteamerWt: sw, RecentWt: rw})
	}

	// Generate pitcher SP curve (GP 0 to 60).
	for gp := 0; gp <= 60; gp++ {
		sw, rw := projections.PitcherBlendWeightsForDisplay(gp, true)
		resp.PitcherSPCurve = append(resp.PitcherSPCurve, curvePoint{GP: gp, SteamerWt: sw, RecentWt: rw})
	}

	// Generate pitcher RP curve (GP 0 to 60).
	for gp := 0; gp <= 60; gp++ {
		sw, rw := projections.PitcherBlendWeightsForDisplay(gp, false)
		resp.PitcherRPCurve = append(resp.PitcherRPCurve, curvePoint{GP: gp, SteamerWt: sw, RecentWt: rw})
	}

	// Plot roster players from cached pipeline result (if available).
	date := r.URL.Query().Get("date")
	proj := r.URL.Query().Get("projections")
	result, err := s.getResult(date, proj)
	if err == nil {
		for _, h := range result.Hitters {
			if h.Breakdown != nil && h.Breakdown.HasRecent {
				resp.RosterPlayers = append(resp.RosterPlayers, rosterDot{
					Name:      h.Player.Name,
					GP:        h.Breakdown.GamesPlayed,
					SteamerWt: h.Breakdown.BaseWt,
					RecentWt:  h.Breakdown.RecentWt,
					Type:      "hitter",
				})
			}
		}
		for _, p := range result.Pitchers {
			if p.GamesPlayed > 0 {
				pType := "pitcherRP"
				if p.IsSP {
					pType = "pitcherSP"
				}
				resp.RosterPlayers = append(resp.RosterPlayers, rosterDot{
					Name:      p.Player.Name,
					GP:        p.GamesPlayed,
					SteamerWt: p.SteamerWt,
					RecentWt:  p.RecentWt,
					Type:      pType,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLineupDiff(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	proj := r.URL.Query().Get("projections")
	result, err := s.getResult(date, proj)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	slotName := buildSlotNames(result)

	// Build current lineup (before optimization).
	// Current = players in their current RosterPosition.
	current := lineupSide{}
	for _, h := range result.Hitters {
		current.Hitters = append(current.Hitters, slotPlayer{
			Name:    h.Player.Name,
			Team:    h.Player.MLBTeam,
			Slot:    slotName[h.Player.RosterPosition],
			Pts:     h.ExpectedPts,
			HasGame: h.HasGame,
		})
	}
	for _, p := range result.Pitchers {
		current.Pitchers = append(current.Pitchers, slotPlayer{
			Name:    p.Player.Name,
			Team:    p.Player.MLBTeam,
			Slot:    slotName[p.Player.RosterPosition],
			Pts:     p.ExpectedPts,
			HasGame: p.HasGame,
		})
	}

	// Build optimized lineup by applying changes.
	optimized := lineupSide{}
	activateSlots := make(map[string]string) // playerID → slotName
	benchIDs := make(map[string]bool)
	for _, ps := range result.HitterResult.ToActivate {
		activateSlots[ps.PlayerID] = slotName[ps.PosID]
	}
	for _, ps := range result.PitcherResult.ToActivate {
		activateSlots[ps.PlayerID] = slotName[ps.PosID]
	}
	for _, id := range result.HitterResult.ToBench {
		benchIDs[id] = true
	}
	for _, id := range result.PitcherResult.ToBench {
		benchIDs[id] = true
	}

	var changedNames []string
	for _, h := range result.Hitters {
		slot := slotName[h.Player.RosterPosition]
		if newSlot, ok := activateSlots[h.Player.ID]; ok {
			slot = newSlot
			changedNames = append(changedNames, h.Player.Name)
		} else if benchIDs[h.Player.ID] {
			slot = "BN"
			changedNames = append(changedNames, h.Player.Name)
		}
		optimized.Hitters = append(optimized.Hitters, slotPlayer{
			Name:    h.Player.Name,
			Team:    h.Player.MLBTeam,
			Slot:    slot,
			Pts:     h.ExpectedPts,
			HasGame: h.HasGame,
		})
	}
	for _, p := range result.Pitchers {
		slot := slotName[p.Player.RosterPosition]
		if newSlot, ok := activateSlots[p.Player.ID]; ok {
			slot = newSlot
			changedNames = append(changedNames, p.Player.Name)
		} else if benchIDs[p.Player.ID] {
			slot = "BN"
			changedNames = append(changedNames, p.Player.Name)
		}
		optimized.Pitchers = append(optimized.Pitchers, slotPlayer{
			Name:    p.Player.Name,
			Team:    p.Player.MLBTeam,
			Slot:    slot,
			Pts:     p.ExpectedPts,
			HasGame: p.HasGame,
		})
	}

	var changes []changeJSON
	for _, c := range result.Changes {
		changes = append(changes, changeJSON{
			PlayerName: c.PlayerName,
			Direction:  c.Direction,
			ToSlot:     c.ToSlot,
			PtsDelta:   c.PtsDelta,
		})
	}

	// Deduplicate changed names.
	seen := make(map[string]bool)
	var uniqueNames []string
	for _, n := range changedNames {
		if !seen[n] {
			seen[n] = true
			uniqueNames = append(uniqueNames, n)
		}
	}

	resp := lineupDiffResponse{
		Date:               result.Date.Format("2006-01-02"),
		Current:            current,
		Optimized:          optimized,
		Changes:            changes,
		ChangedPlayerNames: uniqueNames,
		TotalDelta:         result.TotalDelta,
		Warnings:           result.Warnings,
	}

	writeJSON(w, http.StatusOK, resp)
}

var compareSystems = []string{
	projections.ProjectionSteamer,
	projections.ProjectionDepthCharts,
	projections.ProjectionBatX,
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")

	// Fetch all 3 projection systems concurrently.
	entries := make([]compareSystemEntry, len(compareSystems))
	var wg sync.WaitGroup
	for i, sys := range compareSystems {
		wg.Add(1)
		go func(idx int, projSys string) {
			defer wg.Done()
			result, err := s.getResult(date, projSys)
			if err != nil {
				log.Printf("compare: %s failed: %v", projSys, err)
				entries[idx] = compareSystemEntry{
					ProjectionSystem: projSys,
					Error:            err.Error(),
				}
				return
			}
			entry := compareSystemEntry{
				ProjectionSystem: result.ProjectionSystem,
			}
			for _, h := range result.Hitters {
				ch := compareHitter{
					Name:      h.Player.Name,
					Team:      h.Player.MLBTeam,
					Positions: h.Player.PosShortNames,
					FinalPts:  h.ExpectedPts,
				}
				if h.Breakdown != nil {
					ch.SteamerPts = h.Breakdown.BasePts
					ch.BlendedPts = h.Breakdown.BlendedPts
				}
				entry.Hitters = append(entry.Hitters, ch)
			}
			for _, p := range result.Pitchers {
				entry.Pitchers = append(entry.Pitchers, comparePitcher{
					Name:        p.Player.Name,
					Team:        p.Player.MLBTeam,
					Positions:   p.Player.PosShortNames,
					SteamerPts:  p.SteamerPts,
					ExpectedPts: p.ExpectedPts,
					IsSP:        p.IsSP,
				})
			}
			entries[idx] = entry
		}(i, sys)
	}
	wg.Wait()

	// Determine date from first successful result.
	respDate := date
	if respDate == "" {
		for _, e := range entries {
			if e.Error == "" && len(e.Hitters) > 0 {
				// Use date from the pipeline run — fetched from getResult.
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, compareResponse{
		Date:    respDate,
		Systems: entries,
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func buildSlotNames(result *pipeline.Result) map[string]string {
	m := make(map[string]string)
	for _, s := range result.HitterSlots {
		m[s.PosID] = s.PosName
	}
	for _, s := range result.PitcherSlots {
		m[s.PosID] = s.PosName
	}

	// Map reserve/bench status positions.
	m[""] = "BN"
	if _, ok := m["200"]; !ok {
		m["200"] = "BN"
	}

	// Common reserve/IL/Minors position IDs.
	reserveSlots := map[string]string{
		"050": "IL",
		"051": "IL+",
		"060": "MiLB",
	}
	for id, name := range reserveSlots {
		if _, ok := m[id]; !ok {
			m[id] = name
		}
	}

	return m
}

