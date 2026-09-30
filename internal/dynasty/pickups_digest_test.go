package dynasty

import (
	"fmt"
	"reflect"
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
	title, body, shown := FormatPickupDigest(profile, OrderPickups(PickupItems("2026-10-01", chops, drops, roles), false))
	if !strings.HasPrefix(title, "[Mad Lux Chopped 6.9] Pickups: 1 chop, 1 drop, 2 role changes") {
		t.Errorf("title = %q", title)
	}
	if len(body) > pushover.MaxMessageLen {
		t.Errorf("body exceeds Pushover's limit: %d", len(body))
	}
	if shown != 5 || strings.Contains(body, "more") {
		t.Errorf("everything fits, so shown must be 5 with no trailer: shown=%d body=%q", shown, body)
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
	_, body, _ := FormatPickupDigest(LeagueProfile{Name: "L"}, many)
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
// naive append would overflow. shown and the trailer must account for every
// item: shown + N == len(items).
func TestFormatPickupDigest_TrailerFitsAndAccountsForEveryItem(t *testing.T) {
	for lineLen := 1; lineLen <= 120; lineLen++ {
		line := strings.Repeat("x", lineLen)
		for _, n := range []int{1, 5, 23, 24, 25, 26, 60, 200} {
			items := make([]PickupItem, n)
			for i := range items {
				items[i] = PickupItem{Kind: "drop", Key: "k", Line: line, Priced: true}
			}
			_, body, shown := FormatPickupDigest(LeagueProfile{Name: "L"}, items)
			if len(body) > pushover.MaxMessageLen {
				t.Fatalf("lineLen=%d n=%d: body is %d bytes", lineLen, n, len(body))
			}
			if carried := strings.Count(body, line+"\n"); carried != shown {
				t.Fatalf("lineLen=%d n=%d: body carries %d lines but shown=%d", lineLen, n, carried, shown)
			}
			if shown == n {
				if strings.Contains(body, "more") {
					t.Fatalf("lineLen=%d n=%d: everything shown but body has a trailer: %q", lineLen, n, body)
				}
				continue
			}
			var left int
			tail := body[strings.LastIndex(body, "…"):]
			if _, err := fmt.Sscanf(tail, "…and %d more", &left); err != nil || tail != fmt.Sprintf("…and %d more", left) {
				t.Fatalf("lineLen=%d n=%d shown=%d: body must end with the trailer, tail %q", lineLen, n, shown, tail)
			}
			if shown+left != n {
				t.Fatalf("lineLen=%d n=%d: shown %d + trailer %d != %d", lineLen, n, shown, left, n)
			}
		}
	}
}

// The digest carries exactly items[:shown] in the order given: the caller
// marks those and only those, so a line refused for space is never marked
// and never lost. Lines of varying length would expose a fit that skips a
// long line to place a shorter one later.
func TestFormatPickupDigest_ShownIsAnExactRankOrderPrefix(t *testing.T) {
	var items []PickupItem
	for i := 0; i < 80; i++ {
		items = append(items, PickupItem{Kind: "drop", Key: fmt.Sprintf("k%d", i),
			Line: fmt.Sprintf("%02d", i) + strings.Repeat("y", 10+(i*37)%90), Priced: true})
	}
	_, body, shown := FormatPickupDigest(LeagueProfile{Name: "L"}, items)
	if shown == 0 || shown == len(items) {
		t.Fatalf("fixture must force a partial digest, shown=%d", shown)
	}
	lines := strings.Split(strings.TrimSuffix(body, fmt.Sprintf("…and %d more", len(items)-shown)), "\n")
	lines = lines[:len(lines)-1] // the piece after the final newline is empty
	if len(lines) != shown {
		t.Fatalf("body has %d lines, shown=%d", len(lines), shown)
	}
	for i, l := range lines {
		if l != items[i].Line {
			t.Fatalf("line %d is %q, want items[%d] %q: shown set is not the prefix", i, l, i, items[i].Line)
		}
	}
	if !strings.HasSuffix(body, fmt.Sprintf("…and %d more", len(items)-shown)) {
		t.Errorf("trailer must count the N lowest-ranked: %q", body[len(body)-20:])
	}
}

// A refused line ends the digest: the short third line would fit in the
// leftover space, but showing it would put a lower-ranked item in the digest
// while a higher-ranked one is left out, and the caller marks items[:shown].
func TestFormatPickupDigest_StopsAtTheFirstRefusal(t *testing.T) {
	items := []PickupItem{
		{Kind: "drop", Key: "a", Line: strings.Repeat("a", 600), Priced: true},
		{Kind: "drop", Key: "b", Line: strings.Repeat("b", 600), Priced: true},
		{Kind: "drop", Key: "c", Line: strings.Repeat("c", 50), Priced: true},
	}
	_, body, shown := FormatPickupDigest(LeagueProfile{Name: "L"}, items)
	if shown != 1 {
		t.Fatalf("shown = %d, want 1 (the 50-byte third line must not be shown when the second does not fit)", shown)
	}
	if strings.Contains(body, "ccc") {
		t.Errorf("the third line was skipped ahead to: %q", body)
	}
	if !strings.HasSuffix(body, "…and 2 more") {
		t.Errorf("trailer = %q, want …and 2 more", body[len(body)-16:])
	}
	if len(body) > pushover.MaxMessageLen {
		t.Errorf("body is %d bytes", len(body))
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
	title, _, _ := FormatPickupDigest(LeagueProfile{Name: "G"}, PickupItems("2026-10-01", chops, nil, nil))
	if !strings.HasPrefix(title, "[G] Pickups: 2 chops, 0 drops, 0 role changes") {
		t.Errorf("title = %q", title)
	}
}

func TestFormatPickupDigest_MalformedChopKeyDoesNotPanic(t *testing.T) {
	title, _, _ := FormatPickupDigest(LeagueProfile{Name: "G"}, []PickupItem{{Kind: "chop", Key: "nohyphen", Line: "x"}})
	if !strings.Contains(title, "1 chop") {
		t.Errorf("title = %q", title)
	}
}

// Equal-ranked items keep their input order, so a re-run over the same
// detector output produces the same digest, and sorting works on a copy so
// the caller's slice (which the marker pass indexes) is never reordered.
// The input is deliberately out of rank order: an in-place sort would change
// it, and equal-rank items listed in reverse key order make a tie-break on
// anything but input position show.
func TestOrderPickups_IsStableAndDoesNotMutateInput(t *testing.T) {
	in := []PickupItem{
		{Kind: "role", Key: "unvalued", Position: "WR"},
		{Kind: "drop", Key: "d10", Value: 10, Priced: true},
		{Kind: "drop", Key: "d50-c", Value: 50, Priced: true},
		{Kind: "drop", Key: "d50-b", Value: 50, Priced: true},
		{Kind: "drop", Key: "d50-a", Value: 50, Priced: true},
	}
	orig := append([]PickupItem(nil), in...)

	got := OrderPickups(in, false)

	if !reflect.DeepEqual(in, orig) {
		t.Errorf("input was mutated:\n got  %v\n want %v", keysOf(in), keysOf(orig))
	}
	want := "d50-c,d50-b,d50-a,d10,unvalued"
	if strings.Join(keysOf(got), ",") != want {
		t.Errorf("order = %v\nwant  = %s", keysOf(got), want)
	}
}

// Five elements sort by insertion inside the standard library, which is
// stable whichever entry point is used; 40 equal-rank items reach the
// non-insertion path, where an unstable sort reorders ties.
func TestOrderPickups_StableAcrossALargeTiedRun(t *testing.T) {
	var in []PickupItem
	for i := 39; i >= 0; i-- { // reverse key order: a key tie-break would flip it
		in = append(in, PickupItem{Kind: "drop", Key: fmt.Sprintf("t%02d", i), Value: 50, Priced: true})
		if i%4 == 0 {
			in = append(in, PickupItem{Kind: "drop", Key: fmt.Sprintf("hi%02d", i), Value: 99, Priced: true})
		}
	}
	got := OrderPickups(in, false)
	var wantKeys []string
	for _, it := range in {
		if it.Value == 99 {
			wantKeys = append(wantKeys, it.Key)
		}
	}
	for _, it := range in {
		if it.Value == 50 {
			wantKeys = append(wantKeys, it.Key)
		}
	}
	if !reflect.DeepEqual(keysOf(got), wantKeys) {
		t.Errorf("tied items were reordered:\n got  %v\n want %v", keysOf(got), wantKeys)
	}
}

func TestOrderPickups_SuperflexLiftsAnUnvaluedQBWithinItsTierOnly(t *testing.T) {
	in := []PickupItem{{Kind: "role", Key: "te", Position: "TE"}, {Kind: "role", Key: "qb", Position: "QB"}}
	if got := OrderPickups(in, true); got[0].Key != "qb" {
		t.Errorf("superflex QB should lead the unvalued tier: %v", keysOf(got))
	}
	if got := OrderPickups(in, false); got[0].Key != "te" {
		t.Errorf("non-superflex keeps input order: %v", keysOf(got))
	}
}

func keysOf(items []PickupItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}
