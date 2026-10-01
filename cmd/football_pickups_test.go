package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

func pickupItemsFixture() []dynasty.PickupItem {
	return []dynasty.PickupItem{
		{Kind: "drop", Key: "drop-d1-lock", PlayerID: "lock", Line: "DROP Drew Lock QB SEA (90) by Flint Tropics", Value: 90, Priced: true},
		{Kind: "role", Key: "role-bates-2026-10-01", PlayerID: "bates", Line: "ROLE John Bates TE WAS now #1 TE (was unlisted) (unvalued)"},
	}
}

func pickupAlertFixture(t *testing.T) (pickupAlertInputs, *[]string) {
	t.Helper()
	sent := &[]string{}
	return pickupAlertInputs{
		markers: lineupapi.NewFileBlobStore(t.TempDir(), ""),
		league:  dynasty.LeagueProfile{LeagueID: "L1", Name: "Palm Trees", Superflex: true, Format: "sf_dynasty"},
		items:   pickupItemsFixture(),
		send:    func(title, body string) error { *sent = append(*sent, title+"\n"+body); return nil },
		out:     io.Discard,
	}, sent
}

func TestAlertPickups_SendsOneDigestAndMarksEveryItem(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	res := alertPickups(context.Background(), in)
	if res.New != 2 || res.Sent != 1 || len(*sent) != 1 {
		t.Fatalf("res=%+v sent=%d", res, len(*sent))
	}
	if res.Tail != 0 || res.SendFailed != 0 {
		t.Errorf("a digest that carried everything leaves no tail: res=%+v", res)
	}
	if !strings.HasPrefix((*sent)[0], "[Palm Trees] Pickups:") || !strings.Contains((*sent)[0], "Drew Lock") || !strings.Contains((*sent)[0], "John Bates") {
		t.Errorf("digest = %q", (*sent)[0])
	}
	for _, key := range []string{"L1-drop-d1-lock", "L1-role-bates-2026-10-01"} {
		if _, found, err := in.markers.Get(context.Background(), key); err != nil || !found {
			t.Errorf("marker %s not written (found=%v err=%v)", key, found, err)
		}
	}
}

func TestAlertPickups_SecondRunSendsNothing(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	alertPickups(context.Background(), in)
	res := alertPickups(context.Background(), in)
	if res.New != 0 || res.Sent != 0 || len(*sent) != 1 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
}

func TestAlertPickups_PartiallyMarkedDigestCarriesOnlyNewItems(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	if err := in.markers.Publish("L1-drop-d1-lock", []byte("x")); err != nil {
		t.Fatal(err)
	}
	res := alertPickups(context.Background(), in)
	if res.New != 1 || len(*sent) != 1 || strings.Contains((*sent)[0], "Drew Lock") || !strings.Contains((*sent)[0], "John Bates") {
		t.Errorf("res=%+v sent=%v", res, *sent)
	}
}

func TestAlertPickups_DryRunSendsAndMarksNothing(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.dryRun = true
	res := alertPickups(context.Background(), in)
	if res.New != 2 || res.Sent != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-d1-lock"); found {
		t.Error("dry-run wrote a marker")
	}
}

func TestAlertPickups_FailedSendMarksNothing(t *testing.T) {
	in, _ := pickupAlertFixture(t)
	in.send = func(string, string) error { return errors.New("apns down") }
	res := alertPickups(context.Background(), in)
	if res.Sent != 0 {
		t.Errorf("res=%+v", res)
	}
	if res.SendFailed != 1 || res.Tail != 2 {
		t.Errorf("a failed send leaves every fresh item outstanding: res=%+v, want SendFailed 1 Tail 2", res)
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-d1-lock"); found {
		t.Error("a failed send must not mark: the next run has to retry")
	}
}

func TestAlertPickups_NilMarkersStillSends(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.markers = nil
	alertPickups(context.Background(), in)
	if len(*sent) != 1 {
		t.Errorf("sent = %d", len(*sent))
	}
}

func TestAlertPickups_RefusedTailIsNotMarked(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	var many []dynasty.PickupItem
	for i := 0; i < 40; i++ {
		many = append(many, dynasty.PickupItem{Kind: "drop", Key: "drop-t-" + strings.Repeat("p", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26)), PlayerID: "p", Line: strings.Repeat("x", 60), Value: 100 - i, Priced: true})
	}
	in.items = many
	res := alertPickups(context.Background(), in)
	if res.Sent != 1 || len(*sent) != 1 {
		t.Fatalf("res=%+v", res)
	}
	marked := 0
	for _, it := range many {
		if _, found, _ := in.markers.Get(context.Background(), "L1-"+it.Key); found {
			marked++
		}
	}
	if marked == 0 || marked == len(many) {
		t.Errorf("marked %d of %d: an overflowing digest must mark only the items it carried", marked, len(many))
	}
	// The tail reaches a later digest only because the run REPORTS it
	// (res.Tail) and runFootballPickups then holds the snapshot pointer, so the
	// next run re-detects the same events against the same baseline and finds
	// these still unmarked. Marking alone would not do it: an advanced pointer
	// would skip them for good.
	if res.Tail != len(many)-marked || res.SendFailed != 0 {
		t.Errorf("Tail = %d SendFailed = %d, want Tail %d (the items the digest did not carry)", res.Tail, res.SendFailed, len(many)-marked)
	}
	// The first (highest-ranked) items are the marked ones.
	if _, found, _ := in.markers.Get(context.Background(), "L1-"+many[0].Key); !found {
		t.Error("the top-ranked item was shown and must be marked")
	}
}

// An item whose line alone exceeds the Pushover budget can never appear in a
// digest, so leaving it unmarked would wedge the league: every run would
// lead with it, show nothing, and never reach the items behind it. The head
// item is marked (and named on stderr) so the queue drains.
func TestAlertPickups_UnshowableHeadItemIsMarkedNotSent(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.items = []dynasty.PickupItem{
		{Kind: "drop", Key: "drop-huge", PlayerID: "huge", Line: strings.Repeat("x", 1100), Value: 100, Priced: true},
	}
	res := alertPickups(context.Background(), in)
	if res.Sent != 0 || len(*sent) != 0 {
		t.Errorf("an unshowable item must not send: res=%+v sent=%d", res, len(*sent))
	}
	if res.New != 1 {
		t.Errorf("res.New = %d, want 1", res.New)
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-huge"); !found {
		t.Error("the unshowable head item must be marked so it stops wedging the league")
	}
}

// The unshowable head is marked, but the items behind it are still unsent:
// they are the Tail, so the pointer is held and the next run leads with them.
func TestAlertPickups_UnshowableHeadItemLeavesTheRestAsTail(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.items = []dynasty.PickupItem{
		{Kind: "drop", Key: "drop-huge", PlayerID: "huge", Line: strings.Repeat("x", 1100), Value: 100, Priced: true},
		{Kind: "drop", Key: "drop-small", PlayerID: "small", Line: "DROP Small Fry WR JAX (10) by Flint Tropics", Value: 10, Priced: true},
	}
	res := alertPickups(context.Background(), in)
	if res.Sent != 0 || len(*sent) != 0 || res.New != 2 || res.Tail != 1 {
		t.Errorf("res=%+v sent=%d, want nothing sent and the one trailing item as Tail", res, len(*sent))
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-huge"); !found {
		t.Error("the head item must be marked")
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-small"); found {
		t.Error("the item behind the head was never sent and must stay unmarked")
	}
}

func TestAlertPickups_UnshowableHeadItemDryRunMarksNothing(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.dryRun = true
	in.items = []dynasty.PickupItem{
		{Kind: "drop", Key: "drop-huge", PlayerID: "huge", Line: strings.Repeat("x", 1100), Value: 100, Priced: true},
	}
	res := alertPickups(context.Background(), in)
	if res.Sent != 0 || len(*sent) != 0 || res.New != 1 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
	if _, found, _ := in.markers.Get(context.Background(), "L1-drop-huge"); found {
		t.Error("dry-run wrote a marker")
	}
}

func TestAlertPickups_EmptyItemsSendsNothing(t *testing.T) {
	in, sent := pickupAlertFixture(t)
	in.items = nil
	res := alertPickups(context.Background(), in)
	if res.Sent != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
}

func TestPollPickups_OneFailingLeagueDoesNotStopTheOthers(t *testing.T) {
	leagues := []leagueContext{
		{League: sleeper.League{LeagueID: "1", Name: "Alpha"}}, {League: sleeper.League{LeagueID: "2", Name: "Bravo"}}, {League: sleeper.League{LeagueID: "3", Name: "Charlie"}},
	}
	var ran []string
	total, failed := pollPickups(context.Background(), leagues,
		func(ctx context.Context, lc leagueContext) (pickupLeagueInputs, error) {
			if lc.League.Name == "Bravo" {
				return pickupLeagueInputs{}, errors.New("boom")
			}
			return pickupLeagueInputs{}, nil
		},
		func(lc leagueContext, in pickupLeagueInputs) pickupRunResult {
			ran = append(ran, lc.League.Name)
			return pickupRunResult{New: 1, Sent: 1, Tail: 3, SendFailed: 1}
		})
	if strings.Join(ran, ",") != "Alpha,Charlie" || len(failed) != 1 || failed[0] != "Bravo" || total.Sent != 2 {
		t.Errorf("ran=%v failed=%v total=%+v", ran, failed, total)
	}
	// Tail and SendFailed are what runFootballPickups decides the pointer on,
	// so the totals must carry them.
	if total.Tail != 6 || total.SendFailed != 2 {
		t.Errorf("total = %+v, want Tail 6 SendFailed 2", total)
	}
}

func TestPickupSnapshot_RoundTripsThroughTheBlobStore(t *testing.T) {
	store := lineupapi.NewFileBlobStore(t.TempDir(), "")
	if _, found, err := loadPickupSnapshot(context.Background(), store); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}
	snap := dynasty.PlayerSnapshot{CapturedAt: time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC), Players: map[string]dynasty.SnapPlayer{"lock": {ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartOrder: 1}}}
	if err := savePickupSnapshot(store, snap); err != nil {
		t.Fatal(err)
	}
	got, found, err := loadPickupSnapshot(context.Background(), store)
	if err != nil || !found || !got.CapturedAt.Equal(snap.CapturedAt) || got.Players["lock"].DepthChartOrder != 1 {
		t.Errorf("got=%+v found=%v err=%v", got, found, err)
	}
}

func TestCoverageLine_PrintsTheZeroCase(t *testing.T) {
	var out bytes.Buffer
	printPickupCoverage(&out, "Alpha", time.Date(2026, 9, 30, 15, 15, 0, 0, time.UTC), false, true, 2500, 2100, pickupRunResult{})
	if !strings.Contains(out.String(), "football-pickups: Alpha: prev=2026-09-30 depth=no/yes players=2500 unrostered=2100 role=0 drops=0 chops=0 new=0") {
		t.Errorf("line = %q", out.String())
	}
}

func TestShouldAdvancePointer(t *testing.T) {
	cases := []struct {
		name   string
		total  pickupRunResult
		failed []string
		want   bool
	}{
		{"clean run advances", pickupRunResult{New: 4, Sent: 2}, nil, true},
		{"a quiet run advances", pickupRunResult{}, nil, true},
		{"a failed league holds", pickupRunResult{Sent: 1}, []string{"Bravo"}, false},
		{"an unsent tail holds", pickupRunResult{New: 29, Sent: 1, Tail: 11}, nil, false},
		{"a failed send holds", pickupRunResult{New: 2, SendFailed: 1, Tail: 2}, nil, false},
		{"a failed send alone holds", pickupRunResult{SendFailed: 1}, nil, false},
	}
	for _, c := range cases {
		if got := shouldAdvancePointer(c.total, c.failed); got != c.want {
			t.Errorf("%s: shouldAdvancePointer(%+v, %v) = %v, want %v", c.name, c.total, c.failed, got, c.want)
		}
	}
}

func TestRosteredSet_CountsPlayersTaxiAndReserve(t *testing.T) {
	got := rosteredSet([]sleeper.Roster{
		{RosterID: 1, Players: []string{"p1", "p2"}, Taxi: []string{"t1"}, Reserve: []string{"r1"}},
		{RosterID: 2, Players: []string{"p3"}},
	})
	for _, id := range []string{"p1", "p2", "p3", "t1", "r1"} {
		if !got[id] {
			t.Errorf("%s must read as rostered (taxi and IR players are not available to add)", id)
		}
	}
	if got["nobody"] || len(got) != 5 {
		t.Errorf("got %v", got)
	}
}

func TestFilterRolesByPosition(t *testing.T) {
	roles := []dynasty.RoleChange{{PlayerID: "k", Position: "K"}, {PlayerID: "qb", Position: "QB"}, {PlayerID: "te", Position: "TE"}}
	got := filterRolesByPosition(roles, map[string]bool{"QB": true, "TE": true})
	if len(got) != 2 || got[0].PlayerID != "qb" || got[1].PlayerID != "te" {
		t.Errorf("got %+v, want qb and te in order", got)
	}
	if got := filterRolesByPosition(roles, nil); len(got) != 0 {
		t.Errorf("no rosterable positions must keep nothing: %+v", got)
	}
}

// pickupDetectFixture is two captures one day apart: a QB and a K both reach
// depth-chart order 1 on a club, neither on any roster.
func pickupDetectFixture() (prev, cur dynasty.PlayerSnapshot) {
	prev = dynasty.PlayerSnapshot{CapturedAt: time.Date(2026, 9, 30, 15, 15, 0, 0, time.UTC), HasDepthData: true, Players: map[string]dynasty.SnapPlayer{
		"lock": {ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 2},
		"kick": {ID: "kick", Name: "Kicker One", Team: "SEA", Position: "K", DepthChartPosition: "K", DepthChartOrder: 2},
	}}
	cur = dynasty.PlayerSnapshot{CapturedAt: prev.CapturedAt.Add(24 * time.Hour), HasDepthData: true, Players: map[string]dynasty.SnapPlayer{
		"lock": {ID: "lock", Name: "Drew Lock", Team: "SEA", Position: "QB", DepthChartPosition: "QB", DepthChartOrder: 1},
		"kick": {ID: "kick", Name: "Kicker One", Team: "SEA", Position: "K", DepthChartPosition: "K", DepthChartOrder: 1},
	}}
	return prev, cur
}

func TestDetectLeaguePickups_RoleKeysAreScopedToTheBaselineNotToday(t *testing.T) {
	prev, cur := pickupDetectFixture()
	lc := leagueContext{
		League:  sleeper.League{LeagueID: "L1", Name: "Palm Trees", RosterPositions: []string{"QB", "SUPER_FLEX"}},
		Profile: dynasty.LeagueProfile{LeagueID: "L1", Name: "Palm Trees", Format: "sf_dynasty", Superflex: true},
	}
	bundle := &statsguy.Bundle{Players: map[string]statsguy.Player{"lock": {ID: "lock", Value: statsguy.FormatValues{SFDynasty: 169}}}}

	first := detectLeaguePickups(prev, cur, lc, pickupLeagueInputs{rostered: map[string]bool{}}, nil, bundle)
	// The pointer was held, so the next run diffs the SAME prev against a
	// capture one day later: the same event must carry the same key, or its
	// dedup marker would never match and a sent role change would repeat.
	cur2 := cur
	cur2.CapturedAt = cur.CapturedAt.Add(24 * time.Hour)
	second := detectLeaguePickups(prev, cur2, lc, pickupLeagueInputs{rostered: map[string]bool{}}, nil, bundle)

	if len(first.items) != 1 || len(second.items) != 1 {
		t.Fatalf("first=%+v second=%+v", first.items, second.items)
	}
	if first.items[0].Key != second.items[0].Key {
		t.Errorf("keys differ across a held pointer: %q vs %q", first.items[0].Key, second.items[0].Key)
	}
	if first.items[0].Key != "role-lock-2026-09-30" {
		t.Errorf("key = %q, want the baseline's date (2026-09-30)", first.items[0].Key)
	}
}

func TestDetectLeaguePickups_RoleChangesAreFilteredToTheLeaguesOwnPositions(t *testing.T) {
	prev, cur := pickupDetectFixture()
	noKicker := leagueContext{
		League:  sleeper.League{LeagueID: "L1", Name: "No K", RosterPositions: []string{"QB", "RB", "WR", "TE"}},
		Profile: dynasty.LeagueProfile{LeagueID: "L1", Name: "No K", Format: "sf_dynasty"},
	}
	got := detectLeaguePickups(prev, cur, noKicker, pickupLeagueInputs{rostered: map[string]bool{}}, nil, nil)
	if got.roles != 1 || len(got.items) != 1 || got.items[0].PlayerID != "lock" {
		t.Errorf("got %+v, want only the QB (cur is built from the union of every league's positions, so another league's K slot must not leak in)", got)
	}
	withKicker := leagueContext{
		League:  sleeper.League{LeagueID: "L2", Name: "Has K", RosterPositions: []string{"QB", "K"}},
		Profile: dynasty.LeagueProfile{LeagueID: "L2", Name: "Has K", Format: "sf_dynasty"},
	}
	if got := detectLeaguePickups(prev, cur, withKicker, pickupLeagueInputs{rostered: map[string]bool{}}, nil, nil); got.roles != 2 {
		t.Errorf("a league that rosters kickers keeps the K role change: %+v", got)
	}
}
