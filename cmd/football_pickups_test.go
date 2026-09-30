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
		t.Errorf("marked %d of %d: an overflowing digest must mark only the items it carried, so the tail alerts next run", marked, len(many))
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
			return pickupRunResult{New: 1, Sent: 1}
		})
	if strings.Join(ran, ",") != "Alpha,Charlie" || len(failed) != 1 || failed[0] != "Bravo" || total.Sent != 2 {
		t.Errorf("ran=%v failed=%v total=%+v", ran, failed, total)
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
