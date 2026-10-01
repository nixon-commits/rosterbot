package dynasty

import (
	"encoding/json"
	"fmt"
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
		dropTxn("old", "free_agent", t0.Add(-2*pickupOverlap), map[string]int{"maye": 7}), // before the capture and its overlap
		dropTxn("fa", "free_agent", t0.Add(time.Hour), map[string]int{"lock": 7}),         // valued, kept
		dropTxn("wv", "waiver", t0.Add(2*time.Hour), map[string]int{"nobody": 7}),         // unvalued, dropped
		dropTxn("claimed", "waiver", t0.Add(3*time.Hour), map[string]int{"brown": 7}),     // rostered again since
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

// StatsGuy keeps a player in the bundle with a zero in a format that does not
// value him (measured live 2026-09-30: "DROP Drew Lock QB SEA (0)" in the
// redraft leagues). A zero is not a price: it must neither surface as a valued
// drop nor render as "(0)" on a role change.
func zeroValueBundle() *statsguy.Bundle {
	return &statsguy.Bundle{Players: map[string]statsguy.Player{
		"zero": {ID: "zero", Value: statsguy.FormatValues{SFDynasty: 50, NonSFRedraft: 0}},
	}}
}

func TestDetectDrops_ZeroValueInTheLeaguesFormatIsNotAValuedDrop(t *testing.T) {
	players := map[string]sleeper.Player{"zero": {PlayerID: "zero", FirstName: "Zed", LastName: "Zero", Position: "QB", Team: "SEA"}}
	txns := []sleeper.Transaction{
		dropTxn("fa", "free_agent", t0.Add(time.Hour), map[string]int{"zero": 7}),
		dropTxn("chop", "chopped", t0.Add(2*time.Hour), map[string]int{"zero": 9}),
	}
	drops, chops := DetectDrops(txns, t0, nil, players, zeroValueBundle(), "non_sf_redraft", nil)
	if len(drops) != 0 || len(chops) != 0 {
		t.Errorf("a player valued 0 in non_sf_redraft must not be reported: drops=%+v chops=%+v", drops, chops)
	}
	// The same player IS valued in a format that prices him, so the skip is
	// about the league's column and not about the player.
	drops, _ = DetectDrops(txns, t0, nil, players, zeroValueBundle(), "sf_dynasty", nil)
	if len(drops) != 1 || drops[0].Value != 50 {
		t.Errorf("sf_dynasty drops = %+v, want the player at 50", drops)
	}
}

func TestDetectRoleChanges_ZeroValueInTheLeaguesFormatIsUnpriced(t *testing.T) {
	prev := snapOf(t0, SnapPlayer{ID: "zero", Name: "Zed Zero", Team: "SEA", Position: "QB", DepthChartOrder: 2})
	cur := snapOf(t1, SnapPlayer{ID: "zero", Name: "Zed Zero", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1})
	got := DetectRoleChanges(prev, cur, nil, zeroValueBundle(), "non_sf_redraft")
	if len(got) != 1 || got[0].Priced || got[0].Value != 0 {
		t.Fatalf("got %+v, want one unpriced role change", got)
	}
	got = DetectRoleChanges(prev, cur, nil, zeroValueBundle(), "sf_dynasty")
	if len(got) != 1 || !got[0].Priced || got[0].Value != 50 {
		t.Errorf("sf_dynasty: got %+v, want priced at 50", got)
	}
}

// waiverDone is a completed waiver claim submitted at created and processed
// at done: Sleeper's `created` is the SUBMISSION time and `status_updated` is
// when waivers ran (measured 2026-09-30).
func waiverDone(id string, created, done time.Time, drops map[string]int) sleeper.Transaction {
	tx := dropTxn(id, "waiver", created, drops)
	tx.StatusUpdated = done.UnixMilli()
	return tx
}

func pickupDropPlayers() map[string]sleeper.Player {
	return map[string]sleeper.Player{
		"lock":  {PlayerID: "lock", FirstName: "Drew", LastName: "Lock", Position: "QB", Team: "SEA"},
		"brown": {PlayerID: "brown", FirstName: "A.J.", LastName: "Brown", Position: "WR", Team: "PHI"},
	}
}

func dropIDs(ds []DroppedPlayer) map[string]bool {
	out := map[string]bool{}
	for _, d := range ds {
		out[d.PlayerID] = true
	}
	return out
}

// A waiver claim submitted BEFORE the baseline and processed after it must be
// reported on the day it completes. Filtering on `created` lost 10 of 24
// completed waiver drops (42%) in one league (measured 2026-09-30).
func TestDetectDrops_WaiverSubmittedBeforeTheBaselineButCompletedAfterIsReported(t *testing.T) {
	txns := []sleeper.Transaction{
		waiverDone("w1", t0.Add(-20*time.Hour), t0.Add(17*time.Hour), map[string]int{"lock": 7}),
	}
	drops, _ := DetectDrops(txns, t0, nil, pickupDropPlayers(), pickupBundle(), "non_sf_redraft", nil)
	if len(drops) != 1 || drops[0].PlayerID != "lock" {
		t.Fatalf("a claim completed after the baseline must be reported even though it was created before it: %+v", drops)
	}
}

// Completion decides, not submission: a waiver that completed before the
// baseline (outside the overlap) was already visible to the previous run.
func TestDetectDrops_WaiverCompletedBeforeTheBaselineAndOverlapIsNotReported(t *testing.T) {
	txns := []sleeper.Transaction{
		waiverDone("w1", t0.Add(-30*time.Hour), t0.Add(-pickupOverlap-time.Minute), map[string]int{"lock": 7}),
	}
	drops, _ := DetectDrops(txns, t0, nil, pickupDropPlayers(), pickupBundle(), "non_sf_redraft", nil)
	if len(drops) != 0 {
		t.Fatalf("a claim that completed before since-overlap was seen by the previous run: %+v", drops)
	}
	// And a claim created AFTER the baseline but whose recorded completion is
	// before it (clock skew or a replayed row) is judged on completion too.
	txns = []sleeper.Transaction{
		waiverDone("w2", t0.Add(time.Hour), t0.Add(-5*time.Hour), map[string]int{"lock": 7}),
	}
	if drops, _ := DetectDrops(txns, t0, nil, pickupDropPlayers(), pickupBundle(), "non_sf_redraft", nil); len(drops) != 0 {
		t.Errorf("completion, not creation, is the filter: %+v", drops)
	}
}

// A transaction that completed between the shared transactions cache's fill
// and the next baseline's capture time is invisible to that run; the next run
// must look back past its baseline by pickupOverlap to catch it.
func TestDetectDrops_OverlapCatchesACompletionJustBeforeTheBaseline(t *testing.T) {
	txns := []sleeper.Transaction{
		waiverDone("inside", t0.Add(-5*time.Hour), t0.Add(-30*time.Minute), map[string]int{"lock": 7}),
		waiverDone("outside", t0.Add(-5*time.Hour), t0.Add(-pickupOverlap-time.Minute), map[string]int{"brown": 7}),
	}
	got := dropIDs(first(DetectDrops(txns, t0, nil, pickupDropPlayers(), pickupBundle(), "non_sf_redraft", nil)))
	if !got["lock"] {
		t.Errorf("a completion 30m before the baseline sits inside the %s overlap and must be reported: %v", pickupOverlap, got)
	}
	if got["brown"] {
		t.Errorf("a completion past the overlap must not be reported: %v", got)
	}
}

func first(drops []DroppedPlayer, _ []Chop) []DroppedPlayer { return drops }

// A row without status_updated (an older feed shape, or a free-agent move
// whose two stamps coincide) falls back to created, in both directions.
func TestDetectDrops_ZeroStatusUpdatedFallsBackToCreated(t *testing.T) {
	txns := []sleeper.Transaction{
		dropTxn("after", "free_agent", t0.Add(time.Hour), map[string]int{"lock": 7}),
		dropTxn("before", "free_agent", t0.Add(-pickupOverlap-time.Minute), map[string]int{"brown": 7}),
	}
	got := dropIDs(first(DetectDrops(txns, t0, nil, pickupDropPlayers(), pickupBundle(), "non_sf_redraft", nil)))
	if !got["lock"] || got["brown"] {
		t.Errorf("with StatusUpdated == 0 the filter must use Created: want lock only, got %v", got)
	}
}

// The completion-time rule reaches production only through the
// `status_updated` struct tag on sleeper.Transaction; every other test here
// sets StatusUpdated directly, so a tag typo would pass them all while every
// real row fell back to Created. This decodes a row shaped like the public
// feed (status_updated after the baseline, created before it) and asks the
// detector itself.
func TestDetectDrops_RealFeedRowReportsOnStatusUpdated(t *testing.T) {
	raw := fmt.Sprintf(`[{"transaction_id":"w1","type":"waiver","status":"complete","roster_ids":[7],
	  "created":%d,"status_updated":%d,"drops":{"lock":7}}]`,
		t0.Add(-20*time.Hour).UnixMilli(), t0.Add(17*time.Hour).UnixMilli())
	var txns []sleeper.Transaction
	if err := json.Unmarshal([]byte(raw), &txns); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := dropIDs(first(DetectDrops(txns, t0, nil, pickupDropPlayers(), pickupBundle(), "non_sf_redraft", nil)))
	if !got["lock"] {
		t.Fatalf("a feed row whose status_updated is after the baseline must be reported; got %v (is the json tag still \"status_updated\"?)", got)
	}
}
