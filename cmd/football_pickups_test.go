package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
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
		name      string
		total     pickupRunResult
		failed    []string
		markersOK bool
		want      bool
	}{
		{"clean run advances", pickupRunResult{New: 4, Sent: 2}, nil, true, true},
		{"a quiet run advances", pickupRunResult{}, nil, true, true},
		{"a failed league holds", pickupRunResult{Sent: 1}, []string{"Bravo"}, true, false},
		{"an unsent tail holds", pickupRunResult{New: 29, Sent: 1, Tail: 11}, nil, true, false},
		{"a failed send holds", pickupRunResult{New: 2, SendFailed: 1, Tail: 2}, nil, true, false},
		{"a failed send alone holds", pickupRunResult{SendFailed: 1}, nil, true, false},
		// No marker store: dedup is off, so holding for a tail could only
		// repeat the same digest forever. The tail is abandoned instead.
		{"an unsent tail without a marker store advances", pickupRunResult{New: 29, Sent: 1, Tail: 11}, nil, false, true},
		{"a failed league without a marker store still holds", pickupRunResult{Sent: 1}, []string{"Bravo"}, false, false},
		{"a failed send without a marker store still holds", pickupRunResult{New: 2, SendFailed: 1, Tail: 2}, nil, false, false},
	}
	for _, c := range cases {
		if got := shouldAdvancePointer(c.total, c.failed, c.markersOK); got != c.want {
			t.Errorf("%s: shouldAdvancePointer(%+v, %v, %v) = %v, want %v", c.name, c.total, c.failed, c.markersOK, got, c.want)
		}
	}
}

// memBlob is an in-memory lineupapi.BlobStore that can be made to fail its
// writes and counts them.
type memBlob struct {
	objs       map[string][]byte
	publishErr error
	publishes  int
}

func newMemBlob() *memBlob { return &memBlob{objs: map[string][]byte{}} }

func (m *memBlob) Get(_ context.Context, key string) ([]byte, bool, error) {
	b, ok := m.objs[key]
	return b, ok, nil
}

func (m *memBlob) Publish(key string, data []byte) error {
	m.publishes++
	if m.publishErr != nil {
		return m.publishErr
	}
	m.objs[key] = data
	return nil
}

// memArchive is an in-memory archive.Store recording the partitions written.
type memArchive struct {
	dirs []string
	err  error
}

func (m *memArchive) WritePartition(dir string, _ []archive.Artifact) error {
	if m.err != nil {
		return m.err
	}
	m.dirs = append(m.dirs, dir)
	return nil
}

func (m *memArchive) provider(calls *int) archiveWriterFunc {
	return func() (archive.Writer, error) {
		if calls != nil {
			*calls++
		}
		return archive.NewWriter(m), nil
	}
}

func finishFixture(t *testing.T) (prev, cur dynasty.PlayerSnapshot, snap *memBlob) {
	t.Helper()
	prev = dynasty.PlayerSnapshot{CapturedAt: time.Date(2026, 9, 30, 15, 15, 0, 0, time.UTC), HasDepthData: true, Players: map[string]dynasty.SnapPlayer{}}
	cur = dynasty.PlayerSnapshot{CapturedAt: time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC), HasDepthData: true, Players: map[string]dynasty.SnapPlayer{}}
	snap = newMemBlob()
	if err := savePickupSnapshot(snap, prev); err != nil {
		t.Fatal(err)
	}
	snap.publishes = 0
	return prev, cur, snap
}

func pointerAt(t *testing.T, snap *memBlob) time.Time {
	t.Helper()
	got, found, err := loadPickupSnapshot(context.Background(), snap)
	if err != nil || !found {
		t.Fatalf("pointer unreadable: found=%v err=%v", found, err)
	}
	return got.CapturedAt
}

// finishPickupRun owns the dry-run contract and the hold rule's WIRING. Each
// case below fails under one deletion that the pure shouldAdvancePointer test
// cannot see: dropping the advance guard in persistPickupSnapshot, passing a
// constant true instead of the decision, or dropping the dry-run guard.
func TestFinishPickupRun(t *testing.T) {
	now := time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC)
	const wantDir = "sleeper-players/dt=2026-10-01"
	cases := []struct {
		name         string
		dryRun       bool
		total        pickupRunResult
		failed       []string
		markersOK    bool
		archiveErr   error
		publishErr   error
		wantErr      string // substring; "" means no error
		wantArchived bool
		wantPointer  string // "prev" or "cur"
		wantOut      []string
		notOut       []string
	}{
		{name: "held: archive written, pointer unchanged", total: pickupRunResult{New: 29, Sent: 1, Tail: 11}, markersOK: true,
			wantArchived: true, wantPointer: "prev", wantOut: []string{"pointer HELD at 2026-09-30", "11 unsent item(s)"}, notOut: []string{"pointer advanced"}},
		{name: "failed league holds", total: pickupRunResult{Sent: 1}, failed: []string{"Bravo"}, markersOK: true,
			wantArchived: true, wantPointer: "prev", wantOut: []string{"pointer HELD", "1 failed league(s)"}},
		{name: "advance: both written", total: pickupRunResult{New: 4, Sent: 2}, markersOK: true,
			wantArchived: true, wantPointer: "cur", wantOut: []string{"pointer advanced to 2026-10-01"}, notOut: []string{"HELD"}},
		{name: "dry-run: neither written", dryRun: true, total: pickupRunResult{New: 4, Sent: 2}, markersOK: true,
			wantArchived: false, wantPointer: "prev", wantOut: []string{"dry-run", "not archived, pointer not moved"}, notOut: []string{"pointer advanced", "HELD"}},
		{name: "archive failure: pointer unchanged and the error surfaced", total: pickupRunResult{New: 4, Sent: 2}, markersOK: true,
			archiveErr: errors.New("s3 down"), wantErr: "archive sleeper-players: s3 down", wantArchived: false, wantPointer: "prev", notOut: []string{"pointer advanced"}},
		{name: "pointer write failure: archive written and the error surfaced", total: pickupRunResult{New: 4, Sent: 2}, markersOK: true,
			publishErr: errors.New("throttled"), wantErr: "save snapshot pointer: throttled", wantArchived: true, wantPointer: "prev", notOut: []string{"pointer advanced"}},
		{name: "no marker store: tail abandoned loudly, pointer advances", total: pickupRunResult{New: 29, Sent: 1, Tail: 11}, markersOK: false,
			wantArchived: true, wantPointer: "cur",
			wantOut: []string{"pointer advanced", "marker store unavailable: 11 unsent item(s) abandoned, pointer not held for them"}, notOut: []string{"HELD"}},
		{name: "no marker store but a failed send: still held, nothing abandoned", total: pickupRunResult{New: 2, SendFailed: 1, Tail: 2}, markersOK: false,
			wantArchived: true, wantPointer: "prev", wantOut: []string{"pointer HELD"}, notOut: []string{"abandoned"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev, cur, snap := finishFixture(t)
			snap.publishErr = c.publishErr
			arch := &memArchive{err: c.archiveErr}
			var providerCalls int
			var out bytes.Buffer
			err := finishPickupRun(&out, c.dryRun, now, cur, prev, c.total, c.failed, c.markersOK, snap, arch.provider(&providerCalls))
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("error = %v, want one containing %q", err, c.wantErr)
			}
			if archived := len(arch.dirs) == 1 && arch.dirs[0] == wantDir; archived != c.wantArchived {
				t.Errorf("archive partitions = %v, want written=%v (%s)", arch.dirs, c.wantArchived, wantDir)
			}
			if c.dryRun && (providerCalls != 0 || snap.publishes != 0 || len(arch.dirs) != 0) {
				t.Errorf("a dry-run must not even build the archive writer: provider=%d publishes=%d partitions=%v", providerCalls, snap.publishes, arch.dirs)
			}
			wantPointer := prev.CapturedAt
			if c.wantPointer == "cur" {
				wantPointer = cur.CapturedAt
			}
			if got := pointerAt(t, snap); !got.Equal(wantPointer) {
				t.Errorf("pointer = %s, want the %s capture %s", got, c.wantPointer, wantPointer)
			}
			for _, want := range c.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q:\n%s", want, out.String())
				}
			}
			for _, bad := range c.notOut {
				if strings.Contains(out.String(), bad) {
					t.Errorf("output must not contain %q:\n%s", bad, out.String())
				}
			}
		})
	}
}

func TestFinishPickupRun_AnArchiveWriterThatCannotBeBuiltFailsTheRunAndHoldsThePointer(t *testing.T) {
	prev, cur, snap := finishFixture(t)
	err := finishPickupRun(io.Discard, false, cur.CapturedAt, cur, prev, pickupRunResult{}, nil, true, snap,
		func() (archive.Writer, error) { return archive.Writer{}, errors.New("no bucket") })
	if err == nil || !strings.Contains(err.Error(), "init archive writer: no bucket") {
		t.Fatalf("err = %v", err)
	}
	if snap.publishes != 0 {
		t.Error("a pointer that moved without its history landing is worse than the reverse")
	}
}

func TestWriteFirstBaseline(t *testing.T) {
	_, cur, _ := finishFixture(t)
	cur.Players["lock"] = dynasty.SnapPlayer{ID: "lock"}
	t.Run("dry-run prints its own line INSTEAD of claiming a write", func(t *testing.T) {
		snap, arch := newMemBlob(), &memArchive{}
		var out bytes.Buffer
		if err := writeFirstBaseline(&out, true, cur.CapturedAt, cur, snap, arch.provider(nil)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "wrote baseline") || !strings.Contains(out.String(), "(dry-run)") || !strings.Contains(out.String(), "not written") {
			t.Errorf("output = %q", out.String())
		}
		if snap.publishes != 0 || len(arch.dirs) != 0 {
			t.Error("a dry-run baseline writes nothing")
		}
	})
	t.Run("a real run writes both and then says so", func(t *testing.T) {
		snap, arch := newMemBlob(), &memArchive{}
		var out bytes.Buffer
		if err := writeFirstBaseline(&out, false, cur.CapturedAt, cur, snap, arch.provider(nil)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "wrote baseline of 1 players") || len(arch.dirs) != 1 || snap.publishes != 1 {
			t.Errorf("out=%q archived=%v publishes=%d", out.String(), arch.dirs, snap.publishes)
		}
	})
	t.Run("a failed write never claims success", func(t *testing.T) {
		snap, arch := newMemBlob(), &memArchive{err: errors.New("s3 down")}
		var out bytes.Buffer
		if err := writeFirstBaseline(&out, false, cur.CapturedAt, cur, snap, arch.provider(nil)); err == nil {
			t.Fatal("want the archive error")
		}
		if strings.Contains(out.String(), "wrote baseline") || snap.publishes != 0 {
			t.Errorf("out=%q publishes=%d", out.String(), snap.publishes)
		}
	})
}

// The run's verdict names every cause, and is nil only when nothing went
// wrong. A failed send or a failed persist used to exit 0, so the ledger
// recorded SUCCESS and opsalert's Streak could never fire.
func TestPickupRunError(t *testing.T) {
	if err := pickupRunError(3, nil, 0, nil); err != nil {
		t.Errorf("a clean run is nil, got %v", err)
	}
	for _, c := range []struct {
		name       string
		failed     []string
		sendFailed int
		persistErr error
		want       []string
	}{
		{"a failed league", []string{"Bravo"}, 0, nil, []string{"1 of 3 league(s) failed: Bravo"}},
		{"a failed send", nil, 2, nil, []string{"2 digest send(s) failed"}},
		{"a failed persist", nil, 0, errors.New("archive sleeper-players: s3 down"), []string{"archive sleeper-players: s3 down"}},
		{"all three", []string{"Bravo"}, 1, errors.New("save snapshot pointer: throttled"), []string{"1 of 3 league(s) failed: Bravo", "1 digest send(s) failed", "save snapshot pointer: throttled"}},
	} {
		err := pickupRunError(3, c.failed, c.sendFailed, c.persistErr)
		if err == nil {
			t.Errorf("%s: want an error", c.name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q missing %q", c.name, err.Error(), w)
			}
		}
		if !strings.HasPrefix(err.Error(), "football-pickups: ") {
			t.Errorf("%s: %q", c.name, err.Error())
		}
	}
}

// Under --dry-run nothing is marked, so the unshowable-head warning must not
// say it was.
func TestUnshowableHeadWarning(t *testing.T) {
	live := unshowableHeadWarning("Palm Trees", "drop-huge", false)
	if !strings.Contains(live, "marked without alert") || strings.Contains(live, "would be") {
		t.Errorf("live = %q", live)
	}
	dry := unshowableHeadWarning("Palm Trees", "drop-huge", true)
	if !strings.Contains(dry, "would be marked without alert") || !strings.Contains(dry, "nothing marked") {
		t.Errorf("dry-run = %q", dry)
	}
	if !strings.Contains(live, "Palm Trees") || !strings.Contains(live, "drop-huge") {
		t.Errorf("the league and item must be named: %q", live)
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
	if first.items[0].Key != "role-lock-20260930T151500Z" {
		t.Errorf("key = %q, want the baseline's full UTC timestamp (20260930T151500Z)", first.items[0].Key)
	}

	// Two baselines on the SAME UTC date (a manual rerun that advanced the
	// pointer, then the next scheduled run) must not share role keys, or a
	// player who regained #1 in between would be silenced by the earlier
	// baseline's marker.
	prevLater := prev
	prevLater.CapturedAt = prev.CapturedAt.Add(3 * time.Hour)
	sameDay := detectLeaguePickups(prevLater, cur, lc, pickupLeagueInputs{rostered: map[string]bool{}}, nil, bundle)
	if len(sameDay.items) != 1 {
		t.Fatalf("sameDay = %+v", sameDay.items)
	}
	if sameDay.items[0].Key == first.items[0].Key {
		t.Errorf("two baselines on one UTC date must yield different role keys, both gave %q", first.items[0].Key)
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
