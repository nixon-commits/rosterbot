package dynasty

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/pushover"
)

func digestFixture() ([]Chop, []DroppedPlayer, []RoleChange) {
	chops := []Chop{{TransactionID: "c1", RosterID: 9, RosterName: "Ghost Riders", Players: []DroppedPlayer{
		{TransactionID: "c1", PlayerID: "maye", Name: "Drake Maye", Position: "QB", Team: "NE", Kind: "chopped", Value: 2496},
		{TransactionID: "c1", PlayerID: "lemon", Name: "Makai Lemon", Position: "WR", Team: "LAR", Kind: "chopped", Value: 1173},
	}}}
	drops := []DroppedPlayer{{TransactionID: "d1", PlayerID: "lock", Name: "Drew Lock", Position: "QB", Team: "SEA", Kind: "free_agent", DroppedByName: "Flint Tropics", Value: 90}}
	roles := []RoleChange{
		{PlayerID: "bates", Name: "John Bates", Team: "WAS", Position: "TE", DepthChartPosition: "TE", PrevOrder: 0}, // unvalued
		{PlayerID: "rush", Name: "Cooper Rush", Team: "ATL", Position: "QB", DepthChartPosition: "QB", PrevOrder: 2, Value: 91, Priced: true},
	}
	return chops, drops, roles
}

func TestPickupItems_OneItemPerPlayerWithMarkerKeys(t *testing.T) {
	chops, drops, roles := digestFixture()
	items := PickupItems("2026-10-01", chops, drops, roles)
	keys := map[string]bool{}
	for _, it := range items {
		keys[it.Key] = true
	}
	for _, want := range []string{"chop-c1-maye", "chop-c1-lemon", "drop-d1-lock", "role-bates-2026-10-01", "role-rush-2026-10-01"} {
		if !keys[want] {
			t.Errorf("missing key %s in %v", want, keys)
		}
	}
	if len(items) != 5 {
		t.Errorf("items = %d", len(items))
	}
	for _, it := range items {
		switch {
		case it.Kind == "chop" && !strings.Contains(it.Line, "Ghost Riders"):
			t.Errorf("chop line must name the eliminated roster: %q", it.Line)
		case it.Kind == "role" && it.PlayerIDIs("bates") && !strings.Contains(it.Line, "unvalued"):
			t.Errorf("unvalued role change must say so, never be hidden: %q", it.Line)
		case it.Kind == "drop" && !strings.Contains(it.Line, "Flint Tropics"):
			t.Errorf("drop line must name who dropped him: %q", it.Line)
		}
	}
}

func TestOrderPickups_DefaultRanking(t *testing.T) {
	chops, drops, roles := digestFixture()
	got := OrderPickups(PickupItems("2026-10-01", chops, drops, roles), true)
	var order []string
	for _, it := range got {
		order = append(order, it.Key)
	}
	// Chops first (value order within), then priced items by value (Rush 91
	// before Lock 90), then the unvalued role change last. Update this
	// expectation if the operator chooses a different pickupRank.
	want := "chop-c1-maye,chop-c1-lemon,role-rush-2026-10-01,drop-d1-lock,role-bates-2026-10-01"
	if strings.Join(order, ",") != want {
		t.Errorf("order = %s\nwant  = %s", strings.Join(order, ","), want)
	}
}

func TestFormatPickupDigest_TitleCountsAndBodyFitsPushover(t *testing.T) {
	chops, drops, roles := digestFixture()
	profile := LeagueProfile{Name: "Mad Lux Chopped 6.9", Superflex: false, Format: "non_sf_redraft"}
	title, body := FormatPickupDigest(profile, OrderPickups(PickupItems("2026-10-01", chops, drops, roles), false))
	if !strings.HasPrefix(title, "[Mad Lux Chopped 6.9] Pickups: 1 chop, 1 drop, 2 role changes") {
		t.Errorf("title = %q", title)
	}
	if len(body) > pushover.MaxMessageLen {
		t.Errorf("body exceeds Pushover's limit: %d", len(body))
	}
	if !strings.Contains(body, "Drake Maye") || !strings.Contains(body, "unvalued") {
		t.Errorf("body = %q", body)
	}
}

func TestFormatPickupDigest_DropsWholeLinesNotMidLine(t *testing.T) {
	var many []PickupItem
	for i := 0; i < 60; i++ {
		many = append(many, PickupItem{Kind: "drop", Key: "k", Line: strings.Repeat("x", 40), Value: 60 - i, Priced: true})
	}
	_, body := FormatPickupDigest(LeagueProfile{Name: "L"}, many)
	if len(body) > pushover.MaxMessageLen {
		t.Fatalf("len %d", len(body))
	}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line != "" && !strings.HasSuffix(line, "x") && !strings.HasPrefix(line, "…") {
			t.Errorf("line was cut mid-way: %q", line)
		}
	}
}

// The trailer needs room of its own: a body filled to the brim by lines must
// still fit once "…and N more" is appended, and N must be exactly the number
// of lines left out. Sweeping line lengths lands on the boundary where a
// naive append would overflow.
func TestFormatPickupDigest_TrailerFitsAndCountsExactlyTheRefusedLines(t *testing.T) {
	for lineLen := 1; lineLen <= 120; lineLen++ {
		for _, n := range []int{1, 5, 23, 24, 25, 26, 60, 200} {
			items := make([]PickupItem, n)
			for i := range items {
				items[i] = PickupItem{Kind: "drop", Key: "k", Line: strings.Repeat("x", lineLen), Priced: true}
			}
			_, body := FormatPickupDigest(LeagueProfile{Name: "L"}, items)
			if len(body) > pushover.MaxMessageLen {
				t.Fatalf("lineLen=%d n=%d: body is %d bytes", lineLen, n, len(body))
			}
			shown := strings.Count(body, strings.Repeat("x", lineLen)+"\n")
			refused := n - shown
			switch {
			case refused == 0 && strings.Contains(body, "more"):
				t.Fatalf("lineLen=%d n=%d: nothing refused but body has a trailer: %q", lineLen, n, body)
			case refused > 0 && !strings.HasSuffix(body, fmt.Sprintf("…and %d more", refused)):
				t.Fatalf("lineLen=%d n=%d: want trailer for %d refused, body tail %q", lineLen, n, refused, body[max(0, len(body)-24):])
			}
		}
	}
}

func TestFormatPickupDigest_TitleCountsChopTransactionsNotPlayers(t *testing.T) {
	chops := []Chop{
		{TransactionID: "c1", RosterName: "A", Players: []DroppedPlayer{{PlayerID: "p1"}, {PlayerID: "p2"}, {PlayerID: "p3"}}},
		{TransactionID: "c2", RosterName: "B", Players: []DroppedPlayer{{PlayerID: "p4"}}},
	}
	for i := range chops {
		for j := range chops[i].Players {
			chops[i].Players[j].TransactionID = chops[i].TransactionID
		}
	}
	title, _ := FormatPickupDigest(LeagueProfile{Name: "G"}, PickupItems("2026-10-01", chops, nil, nil))
	if !strings.HasPrefix(title, "[G] Pickups: 2 chops, 0 drops, 0 role changes") {
		t.Errorf("title = %q", title)
	}
}

func TestFormatPickupDigest_MalformedChopKeyDoesNotPanic(t *testing.T) {
	title, _ := FormatPickupDigest(LeagueProfile{Name: "G"}, []PickupItem{{Kind: "chop", Key: "nohyphen", Line: "x"}})
	if !strings.Contains(title, "1 chop") {
		t.Errorf("title = %q", title)
	}
}

// Equal-ranked items keep their input order, so a re-run over the same
// detector output produces the same digest.
func TestOrderPickups_IsStableAndDoesNotMutateInput(t *testing.T) {
	in := []PickupItem{
		{Kind: "drop", Key: "a", Value: 50, Priced: true},
		{Kind: "drop", Key: "b", Value: 50, Priced: true},
		{Kind: "drop", Key: "c", Value: 50, Priced: true},
		{Kind: "role", Key: "d", Position: "WR"},
		{Kind: "role", Key: "e", Position: "TE"},
	}
	got := OrderPickups(in, false)
	var keys []string
	for _, it := range got {
		keys = append(keys, it.Key)
	}
	if strings.Join(keys, ",") != "a,b,c,d,e" {
		t.Errorf("order = %v", keys)
	}
	if in[0].Key != "a" || in[4].Key != "e" {
		t.Errorf("input was mutated: %v", in)
	}
	// Superflex lifts an unvalued QB role change above other unvalued ones only.
	sf := OrderPickups([]PickupItem{{Kind: "role", Key: "te", Position: "TE"}, {Kind: "role", Key: "qb", Position: "QB"}}, true)
	if sf[0].Key != "qb" {
		t.Errorf("superflex QB should lead the unvalued tier: %v", sf)
	}
	nsf := OrderPickups([]PickupItem{{Kind: "role", Key: "te", Position: "TE"}, {Kind: "role", Key: "qb", Position: "QB"}}, false)
	if nsf[0].Key != "te" {
		t.Errorf("non-superflex keeps input order: %v", nsf)
	}
}
