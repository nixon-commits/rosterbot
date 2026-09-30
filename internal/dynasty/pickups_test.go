package dynasty

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// snapOf builds a snapshot the way BuildPlayerSnapshot does, including the
// HasDepthData flag: true when any player carries a depth-chart order.
func snapOf(at time.Time, ps ...SnapPlayer) PlayerSnapshot {
	s := PlayerSnapshot{CapturedAt: at, Players: map[string]SnapPlayer{}}
	for _, p := range ps {
		s.Players[p.ID] = p
		if p.DepthChartOrder > 0 {
			s.HasDepthData = true
		}
	}
	return s
}

var (
	t0 = time.Date(2026, 9, 30, 15, 15, 0, 0, time.UTC)
	t1 = t0.Add(24 * time.Hour)
)

func pickupBundle() *statsguy.Bundle {
	return &statsguy.Bundle{Players: map[string]statsguy.Player{
		"lock":  {ID: "lock", Value: statsguy.FormatValues{SFDynasty: 169, NonSFRedraft: 90}},
		"maye":  {ID: "maye", Value: statsguy.FormatValues{SFDynasty: 6000, NonSFRedraft: 2496}},
		"brown": {ID: "brown", Value: statsguy.FormatValues{SFDynasty: 5000, NonSFRedraft: 3696}},
	}}
}

func TestDetectRoleChanges_ReachingOrderOneFromLowerUnlistedOrNoClub(t *testing.T) {
	prev := snapOf(t0,
		SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 2},
		SnapPlayer{ID: "rush", Name: "Cooper Rush", Team: "ATL", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1}, // already the starter
		SnapPlayer{ID: "bates", Name: "John Bates", Team: "WAS", Position: "TE", DepthChartOrder: 0},                           // unlisted
		SnapPlayer{ID: "star", Name: "Rostered Star", Team: "DET", Position: "RB", DepthChartOrder: 2},
	)
	cur := snapOf(t1,
		SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1},
		SnapPlayer{ID: "rush", Name: "Cooper Rush", Team: "ATL", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1},
		SnapPlayer{ID: "bates", Name: "John Bates", Team: "WAS", Position: "TE", DepthChartPosition: "TE", DepthChartOrder: 1},
		SnapPlayer{ID: "signed", Name: "Just Signed", Team: "CAR", Position: "RB", DepthChartPosition: "RB", DepthChartOrder: 1}, // absent from prev
		SnapPlayer{ID: "star", Name: "Rostered Star", Team: "DET", Position: "RB", DepthChartPosition: "RB", DepthChartOrder: 1},
	)
	got := DetectRoleChanges(prev, cur, map[string]bool{"star": true}, pickupBundle(), "sf_dynasty")
	ids := map[string]RoleChange{}
	for _, r := range got {
		ids[r.PlayerID] = r
	}
	if len(got) != 3 || ids["lock"].PlayerID == "" || ids["bates"].PlayerID == "" || ids["signed"].PlayerID == "" {
		t.Fatalf("got %+v", got)
	}
	if r := ids["lock"]; r.PrevOrder != 2 || r.Value != 169 || !r.Priced {
		t.Errorf("lock = %+v", r)
	}
	if r := ids["bates"]; r.PrevOrder != 0 || r.Priced {
		t.Errorf("bates should be unpriced with prev order 0: %+v", r)
	}
	if r := ids["signed"]; r.PrevTeam != "" {
		t.Errorf("signed should carry no prev team: %+v", r)
	}
	if _, bad := ids["rush"]; bad {
		t.Error("a player already at order 1 on the same club is not a change")
	}
	if _, bad := ids["star"]; bad {
		t.Error("a rostered player is never a pickup")
	}
}

func TestDetectRoleChanges_NewClubStartingJobCounts(t *testing.T) {
	prev := snapOf(t0, SnapPlayer{ID: "x", Name: "X", Team: "NYG", Position: "RB", DepthChartOrder: 1})
	cur := snapOf(t1, SnapPlayer{ID: "x", Name: "X", Team: "CLE", Position: "RB", DepthChartPosition: "RB", DepthChartOrder: 1})
	got := DetectRoleChanges(prev, cur, nil, pickupBundle(), "sf_dynasty")
	if len(got) != 1 || got[0].PrevTeam != "NYG" {
		t.Errorf("got %+v", got)
	}
}

func TestDetectRoleChanges_IsDeterministicallyOrdered(t *testing.T) {
	// The prior capture has depth data (one unrelated order-2 player) but none
	// of the three below, so all three are new starters; the guard needs the
	// flag true on both sides, which an empty snapshot would not carry.
	prev := snapOf(t0, SnapPlayer{ID: "z", Name: "Zed", Team: "B", Position: "WR", DepthChartOrder: 2})
	cur := snapOf(t1,
		SnapPlayer{ID: "b", Name: "Bob", Team: "A", Position: "WR", DepthChartOrder: 1},
		SnapPlayer{ID: "a", Name: "Al", Team: "A", Position: "WR", DepthChartOrder: 1},
		SnapPlayer{ID: "q", Name: "Quin", Team: "A", Position: "QB", DepthChartOrder: 1},
	)
	got := DetectRoleChanges(prev, cur, nil, pickupBundle(), "sf_dynasty")
	if len(got) != 3 || got[0].Name != "Quin" || got[1].Name != "Al" || got[2].Name != "Bob" {
		t.Errorf("want position then name order, got %+v", got)
	}
}

// The player dump is cached 24 h. A capture built from a cache entry that the
// previous binary wrote has order 0 for everyone; diffing the next fresh
// capture against it would report every unrostered starter as a role change.
func TestDetectRoleChanges_SilentWithoutDepthDataOnBothSides(t *testing.T) {
	noDepth := snapOf(t0, SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartOrder: 0})
	if noDepth.HasDepthData {
		t.Fatal("fixture must have no depth data")
	}
	withDepth := snapOf(t1, SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1})
	if !withDepth.HasDepthData {
		t.Fatal("fixture must have depth data")
	}
	if got := DetectRoleChanges(noDepth, withDepth, nil, pickupBundle(), "sf_dynasty"); got != nil {
		t.Errorf("prev without depth data must yield nil, got %+v", got)
	}

	// Reverse: a current capture flagged as having no depth data. The order-1
	// player is present so only the flag can explain the silence.
	prev := snapOf(t0, SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartOrder: 2})
	cur := snapOf(t1, SnapPlayer{ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1})
	cur.HasDepthData = false
	if got := DetectRoleChanges(prev, cur, nil, pickupBundle(), "sf_dynasty"); got != nil {
		t.Errorf("cur without depth data must yield nil, got %+v", got)
	}
}

func dropTxn(id, kind string, created time.Time, drops map[string]int) sleeper.Transaction {
	return sleeper.Transaction{TransactionID: id, Type: kind, Status: "complete", Created: created.UnixMilli(), Drops: drops, RosterIDs: []int{7}}
}

func TestDetectDrops_ValuedCompletedDropsSinceTheCapture(t *testing.T) {
	players := map[string]sleeper.Player{
		"maye":   {PlayerID: "maye", FirstName: "Drake", LastName: "Maye", Position: "QB", Team: "NE"},
		"brown":  {PlayerID: "brown", FirstName: "A.J.", LastName: "Brown", Position: "WR", Team: "PHI"},
		"lock":   {PlayerID: "lock", FirstName: "Drew", LastName: "Lock", Position: "QB", Team: "SEA"},
		"nobody": {PlayerID: "nobody", FirstName: "Un", LastName: "Valued", Position: "WR", Team: "JAX"},
	}
	names := map[int]string{7: "Flint Tropics", 9: "Ghost Riders"}
	txns := []sleeper.Transaction{
		dropTxn("old", "free_agent", t0.Add(-time.Hour), map[string]int{"maye": 7}),   // before the capture
		dropTxn("fa", "free_agent", t0.Add(time.Hour), map[string]int{"lock": 7}),     // valued, kept
		dropTxn("wv", "waiver", t0.Add(2*time.Hour), map[string]int{"nobody": 7}),     // unvalued, dropped
		dropTxn("claimed", "waiver", t0.Add(3*time.Hour), map[string]int{"brown": 7}), // rostered again since
		{TransactionID: "trade", Type: "trade", Status: "complete", Created: t0.Add(4 * time.Hour).UnixMilli(), Drops: map[string]int{"maye": 9}, RosterIDs: []int{7, 9}},
		{TransactionID: "pending", Type: "waiver", Status: "pending", Created: t0.Add(5 * time.Hour).UnixMilli(), Drops: map[string]int{"maye": 7}},
		dropTxn("chop", "chopped", t0.Add(6*time.Hour), map[string]int{"maye": 9, "brown": 9, "nobody": 9}),
	}
	drops, chops := DetectDrops(txns, t0, map[string]bool{"brown": true}, players, pickupBundle(), "non_sf_redraft", names)
	if len(drops) != 1 || drops[0].PlayerID != "lock" || drops[0].Value != 90 || drops[0].DroppedByName != "Flint Tropics" || drops[0].Kind != "free_agent" {
		t.Errorf("drops = %+v", drops)
	}
	if len(chops) != 1 || chops[0].RosterID != 9 || chops[0].RosterName != "Ghost Riders" {
		t.Fatalf("chops = %+v", chops)
	}
	// brown is rostered again (claimed) so he is not available; nobody is
	// unpriced; only maye remains, and he is priced in the redraft column.
	if len(chops[0].Players) != 1 || chops[0].Players[0].PlayerID != "maye" || chops[0].Players[0].Value != 2496 {
		t.Errorf("chop players = %+v", chops[0].Players)
	}
}

func TestDetectDrops_OrdersByValueThenName(t *testing.T) {
	players := map[string]sleeper.Player{
		"maye":  {PlayerID: "maye", FirstName: "Drake", LastName: "Maye", Position: "QB", Team: "NE"},
		"brown": {PlayerID: "brown", FirstName: "A.J.", LastName: "Brown", Position: "WR", Team: "PHI"},
	}
	txns := []sleeper.Transaction{dropTxn("x", "waiver", t0.Add(time.Hour), map[string]int{"maye": 7, "brown": 7})}
	drops, _ := DetectDrops(txns, t0, nil, players, pickupBundle(), "non_sf_redraft", nil)
	if len(drops) != 2 || drops[0].PlayerID != "brown" || drops[1].PlayerID != "maye" {
		t.Errorf("want brown (3696) before maye (2496): %+v", drops)
	}
}
