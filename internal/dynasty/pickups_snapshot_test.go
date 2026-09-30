package dynasty

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func intp(i int) *int { return &i }

func TestRosterablePositions_ExpandsFlexSlotsAndIgnoresBench(t *testing.T) {
	got := RosterablePositions([]sleeper.League{
		{RosterPositions: []string{"QB", "RB", "WR", "TE", "FLEX", "SUPER_FLEX", "BN", "IR", "TAXI"}},
		{RosterPositions: []string{"K", "DEF", "REC_FLEX", "IDP_FLEX"}},
	})
	for _, want := range []string{"QB", "RB", "WR", "TE", "K", "DEF", "DL", "LB", "DB"} {
		if !got[want] {
			t.Errorf("%s missing from %v", want, got)
		}
	}
	for _, no := range []string{"BN", "IR", "TAXI", "FLEX", "SUPER_FLEX", "REC_FLEX", "IDP_FLEX"} {
		if got[no] {
			t.Errorf("%s is a slot, not a position: %v", no, got)
		}
	}
}

func TestBuildPlayerSnapshot_FiltersToOnClubRosterablePlayers(t *testing.T) {
	now := time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC)
	players := map[string]sleeper.Player{
		"1": {PlayerID: "1", FirstName: "Drew", LastName: "Lock", Position: "QB", Team: "SEA", DepthChartPosition: "QB", DepthChartOrder: intp(1), Status: "Active"},
		"2": {PlayerID: "2", FirstName: "Free", LastName: "Agent", Position: "RB", Team: ""},                              // no club
		"3": {PlayerID: "3", FirstName: "Some", LastName: "Kicker", Position: "K", Team: "CHI", DepthChartOrder: intp(1)}, // not rosterable here
		"4": {PlayerID: "4", FirstName: "Deep", LastName: "Bench", Position: "WR", Team: "DET", DepthChartOrder: nil, InjuryStatus: "Out"},
	}
	snap := BuildPlayerSnapshot(now, players, map[string]bool{"QB": true, "RB": true, "WR": true, "TE": true})
	if !snap.CapturedAt.Equal(now) {
		t.Errorf("CapturedAt = %v", snap.CapturedAt)
	}
	if len(snap.Players) != 2 {
		t.Fatalf("players = %v", snap.Players)
	}
	if p := snap.Players["1"]; p.Name != "Drew Lock" || p.DepthChartOrder != 1 || p.DepthChartPosition != "QB" || p.Team != "SEA" {
		t.Errorf("lock = %+v", p)
	}
	if p := snap.Players["4"]; p.DepthChartOrder != 0 || p.InjuryStatus != "Out" {
		t.Errorf("nil order must become 0: %+v", p)
	}
}

func TestBuildPlayerSnapshot_RecordsWhetherDepthDataExists(t *testing.T) {
	now := time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC)

	// Dump with no depth data
	players := map[string]sleeper.Player{
		"1": {PlayerID: "1", FirstName: "No", LastName: "Data", Position: "QB", Team: "SEA", DepthChartOrder: nil},
		"2": {PlayerID: "2", FirstName: "Also", LastName: "None", Position: "RB", Team: "BUF", DepthChartOrder: nil},
	}
	snap := BuildPlayerSnapshot(now, players, map[string]bool{"QB": true, "RB": true})
	if snap.HasDepthData {
		t.Errorf("HasDepthData should be false when all orders are nil")
	}

	// Dump with at least one depth data point
	players2 := map[string]sleeper.Player{
		"1": {PlayerID: "1", FirstName: "Lock", LastName: "In", Position: "QB", Team: "SEA", DepthChartOrder: intp(1)},
		"2": {PlayerID: "2", FirstName: "No", LastName: "Data", Position: "RB", Team: "BUF", DepthChartOrder: nil},
	}
	snap2 := BuildPlayerSnapshot(now, players2, map[string]bool{"QB": true, "RB": true})
	if !snap2.HasDepthData {
		t.Errorf("HasDepthData should be true when at least one order > 0")
	}
}

// HasDepthData describes the snapshot's contents, so a player the filter
// dropped must not set it: here the only order-1 player is a kicker the
// league cannot roster.
func TestBuildPlayerSnapshot_DepthFlagIgnoresFilteredPlayers(t *testing.T) {
	now := time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC)
	players := map[string]sleeper.Player{
		"1": {PlayerID: "1", FirstName: "Some", LastName: "Kicker", Position: "K", Team: "CHI", DepthChartOrder: intp(1)},
		"2": {PlayerID: "2", FirstName: "No", LastName: "Order", Position: "WR", Team: "DET", DepthChartOrder: nil},
	}
	snap := BuildPlayerSnapshot(now, players, map[string]bool{"QB": true, "RB": true, "WR": true, "TE": true})
	if len(snap.Players) != 1 {
		t.Fatalf("players = %v", snap.Players)
	}
	if snap.HasDepthData {
		t.Error("an excluded player's depth order must not set HasDepthData")
	}
}

// The snapshot is archived forever (~2,500 rows a day), so the wire format is
// compact — zero-valued optional fields are omitted — and always UTC, however
// the caller's clock is zoned.
func TestPlayerSnapshot_WireFormatIsCompactAndUTC(t *testing.T) {
	edt := time.FixedZone("EDT", -4*3600)
	now := time.Date(2026, 10, 1, 11, 15, 0, 0, edt)
	players := map[string]sleeper.Player{
		"1": {PlayerID: "1", FirstName: "Deep", LastName: "Bench", Position: "WR", Team: "DET", DepthChartOrder: nil},
	}
	snap := BuildPlayerSnapshot(now, players, map[string]bool{"WR": true})
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		CapturedAt string                     `json:"captured_at"`
		Players    map[string]json.RawMessage `json:"players"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(wire.CapturedAt, "Z") {
		t.Errorf("captured_at = %q, want a UTC timestamp ending in Z", wire.CapturedAt)
	}
	var row map[string]any
	if err := json.Unmarshal(wire.Players["1"], &row); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"depth_chart_order", "injury_status"} {
		if _, present := row[key]; present {
			t.Errorf("%s should be omitted for a player with none: %s", key, wire.Players["1"])
		}
	}
}
