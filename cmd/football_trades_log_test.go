package cmd

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// tradeLogStore returns a real file-backed reader/writer pair over a temp dir.
// Real stores rather than fakes: the trap under test is that ndjsonstore.Write
// overwrites the whole object, and only a real round trip exercises that.
func tradeLogStore(t *testing.T) (dynasty.TradeLogReader, dynasty.TradeLogWriter) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "log")
	return dynasty.NewFileTradeLogReader(root), dynasty.NewFileTradeLogWriter(root)
}

func logRow(dt, id string) dynasty.TradeLogRow {
	d, _ := time.Parse("2006-01-02", dt)
	return dynasty.TradeLogRow{Dt: dt, TransactionID: id, GradedAt: d}
}

func loggedIDs(t *testing.T, r dynasty.TradeLogReader) map[string]bool {
	t.Helper()
	rows, err := r.ReadAllTrades()
	if err != nil {
		t.Fatalf("ReadAllTrades: %v", err)
	}
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.TransactionID] = true
	}
	return ids
}

// FootballTrades polls every six hours, so two trades landing on the same UTC
// day arrive in different polls as DISJOINT graded sets. A bare second write
// deletes the first poll's rows with no error and a success summary.
func TestAppendFootballTradeLog_TwoPollsSameDayBothSurvive(t *testing.T) {
	r, w := tradeLogStore(t)
	day := time.Date(2026, 8, 18, 6, 45, 0, 0, time.UTC)

	if _, err := appendFootballTradeLog(r, w, day, []dynasty.TradeLogRow{logRow("2026-08-18", "morning")}); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	evening := day.Add(12 * time.Hour)
	if _, err := appendFootballTradeLog(r, w, evening, []dynasty.TradeLogRow{logRow("2026-08-18", "evening")}); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	ids := loggedIDs(t, r)
	if !ids["morning"] || !ids["evening"] {
		t.Fatalf("logged = %v, want both — the second poll clobbered the first", ids)
	}
	if len(ids) != 2 {
		t.Errorf("logged %d rows, want 2", len(ids))
	}
}

// Re-running a poll that grades nothing new must leave the partition exactly as
// it was, not duplicate it.
func TestAppendFootballTradeLog_IsIdempotentOnARepeatedRow(t *testing.T) {
	r, w := tradeLogStore(t)
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	row := logRow("2026-08-18", "t1")

	for i := 0; i < 3; i++ {
		if _, err := appendFootballTradeLog(r, w, day, []dynasty.TradeLogRow{row}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	rows, err := r.ReadAllTrades()
	if err != nil {
		t.Fatalf("ReadAllTrades: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1 — a repeated grade must merge, not append", len(rows))
	}
}

// ReadAllTrades walks EVERY partition. Merging all of it into today would copy
// the whole history forward on each write, growing every partition without
// bound and making each one a false snapshot of the entire log.
func TestAppendFootballTradeLog_DoesNotCopyOlderPartitionsForward(t *testing.T) {
	r, w := tradeLogStore(t)

	yesterday := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	if _, err := appendFootballTradeLog(r, w, yesterday, []dynasty.TradeLogRow{logRow("2026-08-17", "old")}); err != nil {
		t.Fatalf("yesterday: %v", err)
	}
	today := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	if _, err := appendFootballTradeLog(r, w, today, []dynasty.TradeLogRow{logRow("2026-08-18", "new")}); err != nil {
		t.Fatalf("today: %v", err)
	}

	rows, err := r.ReadAllTrades()
	if err != nil {
		t.Fatalf("ReadAllTrades: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want exactly 2 — yesterday's row was copied into today's partition", len(rows))
	}
	byDt := map[string]string{}
	for _, row := range rows {
		byDt[row.Dt] = row.TransactionID
	}
	if byDt["2026-08-17"] != "old" || byDt["2026-08-18"] != "new" {
		t.Errorf("partitions = %v, want each day holding only its own grades", byDt)
	}
}

func TestAppendFootballTradeLog_ReadFailureIsReportedNotSwallowed(t *testing.T) {
	_, w := tradeLogStore(t)
	_, err := appendFootballTradeLog(failingTradeLogReader{}, w, time.Now().UTC(), []dynasty.TradeLogRow{logRow("2026-08-18", "t1")})
	if err == nil {
		t.Fatal("want an error: writing without the prior partition would silently clobber it")
	}
}

type failingTradeLogReader struct{}

func (failingTradeLogReader) ReadAllTrades() ([]dynasty.TradeLogRow, error) {
	return nil, errors.New("boom")
}

type failingTradeLogWriter struct{}

func (failingTradeLogWriter) WriteTradeLog(time.Time, []dynasty.TradeLogRow) error {
	return errors.New("boom")
}

// ---------------------------------------------------------- alerting loop

func tradeAlertFixture(t *testing.T) (tradeRunInputs, *[]string) {
	t.Helper()
	sent := &[]string{}
	return tradeRunInputs{
		markers: lineupapi.NewFileBlobStore(t.TempDir(), ""),
		now:     time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
		trades: []sleeper.Transaction{{
			TransactionID: "t1",
			Type:          "trade",
			Status:        "complete",
			RosterIDs:     []int{1, 2},
			Adds:          map[string]int{"4984": 1, "9509": 2},
			Created:       time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli(),
		}},
		players: map[string]sleeper.Player{
			"4984": {PlayerID: "4984", FirstName: "Josh", LastName: "Allen"},
			"9509": {PlayerID: "9509", FirstName: "Bijan", LastName: "Robinson"},
		},
		bundle: &statsguy.Bundle{Players: map[string]statsguy.Player{
			"4984": {ID: "4984", Value: statsguy.FormatValues{SFDynasty: 9000}},
			"9509": {ID: "9509", Value: statsguy.FormatValues{SFDynasty: 11000}},
		}},
		names:  map[int]string{1: "Zatch's mom Hawk Tua'd", 2: "CeeDee Top"},
		format: "sf_dynasty",
		send:   func(title, body string) error { *sent = append(*sent, title); return nil },
		out:    io.Discard,
	}, sent
}

// THE ACCEPTANCE CRITERION: a log-write failure must not prevent the alert.
//
// Demonstrated by execution rather than by reading the code: the send happens
// inside gradeAndAlertTrades, which returns the rows; the log write is a
// separate call on those rows. A writer that always fails cannot reach back and
// unsend anything.
func TestTradeLogWriteFailure_DoesNotPreventTheAlert(t *testing.T) {
	in, sent := tradeAlertFixture(t)
	res := gradeAndAlertTrades(context.Background(), in)

	if len(*sent) != 1 {
		t.Fatalf("sent %d alerts, want 1", len(*sent))
	}
	if res.Alerted != 1 || res.Graded != 1 {
		t.Fatalf("graded=%d alerted=%d, want 1/1", res.Graded, res.Alerted)
	}

	r, _ := tradeLogStore(t)
	if _, err := appendFootballTradeLog(r, failingTradeLogWriter{}, in.now, res.LogRows); err == nil {
		t.Fatal("want the write to fail in this test")
	}
	// The alert is still exactly where it was: sent, and counted.
	if len(*sent) != 1 || res.Alerted != 1 {
		t.Errorf("after the failed log write: sent=%d alerted=%d, want 1/1", len(*sent), res.Alerted)
	}
}

// A dry run must not send, must not mark, and must produce no rows to log — so
// the caller has nothing to write and no partition appears.
func TestGradeAndAlertTrades_DryRunSendsNothingAndLogsNothing(t *testing.T) {
	in, sent := tradeAlertFixture(t)
	in.dryRun = true
	res := gradeAndAlertTrades(context.Background(), in)

	if len(*sent) != 0 {
		t.Errorf("dry run sent %d alerts, want 0", len(*sent))
	}
	if len(res.LogRows) != 0 {
		t.Errorf("dry run produced %d log rows, want 0", len(res.LogRows))
	}
	if res.Graded != 1 {
		t.Errorf("Graded = %d, want 1 (a dry run still grades and prints)", res.Graded)
	}
	if _, found, err := in.markers.Get(context.Background(), "t1"); err != nil || found {
		t.Errorf("dry run wrote a dedup marker (found=%v, err=%v) — a later real run would then skip the alert", found, err)
	}
}

// A send that fails must not be marked and must not be logged: it retries next
// poll, and the log's claim is "what was reported".
func TestGradeAndAlertTrades_FailedSendIsNeitherMarkedNorLogged(t *testing.T) {
	in, _ := tradeAlertFixture(t)
	in.send = func(string, string) error { return errors.New("pushover down") }

	res := gradeAndAlertTrades(context.Background(), in)
	if res.Alerted != 0 {
		t.Errorf("Alerted = %d, want 0", res.Alerted)
	}
	if len(res.LogRows) != 0 {
		t.Errorf("logged %d rows for an alert that never went out, want 0", len(res.LogRows))
	}
	if _, found, _ := in.markers.Get(context.Background(), "t1"); found {
		t.Error("a failed send was marked; the retry would never happen")
	}
}

// An already-marked trade is skipped before grading — which is exactly why the
// backfill needs --relog, and why an unmarked one produces a row.
func TestGradeAndAlertTrades_MarkedTradeIsSkippedBeforeGrading(t *testing.T) {
	in, sent := tradeAlertFixture(t)
	if err := in.markers.Publish("t1", []byte("favors CeeDee Top (+20%)")); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	res := gradeAndAlertTrades(context.Background(), in)
	if res.Skipped != 1 || res.Graded != 0 {
		t.Errorf("skipped=%d graded=%d, want 1/0", res.Skipped, res.Graded)
	}
	if len(*sent) != 0 || len(res.LogRows) != 0 {
		t.Errorf("sent=%d rows=%d, want 0/0", len(*sent), len(res.LogRows))
	}
}

// The successful path writes the marker AND returns a row whose stored summary
// is byte-identical to the marker body — one renderer, two consumers.
func TestGradeAndAlertTrades_MarkerBodyAndLoggedSummaryAgree(t *testing.T) {
	in, _ := tradeAlertFixture(t)
	res := gradeAndAlertTrades(context.Background(), in)

	body, found, err := in.markers.Get(context.Background(), "t1")
	if err != nil || !found {
		t.Fatalf("marker not written (found=%v, err=%v)", found, err)
	}
	if len(res.LogRows) != 1 {
		t.Fatalf("len(LogRows) = %d, want 1", len(res.LogRows))
	}
	if got, want := res.LogRows[0].Verdicts["sf_dynasty"].Summary, string(body); got != want {
		t.Errorf("logged summary %q != marker body %q", got, want)
	}
}

// The count reported must be what LANDED, not what was offered. A --relog over
// a day whose grades were genuinely captured adds nothing, and printing
// "wrote 14" there is a success summary describing a write that did not happen.
func TestAppendFootballTradeLog_ReportsRowsActuallyAdded(t *testing.T) {
	r, w := tradeLogStore(t)
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)

	captured := []dynasty.TradeLogRow{logRow("2026-08-18", "a"), logRow("2026-08-18", "b")}
	added, err := appendFootballTradeLog(r, w, day, captured)
	if err != nil || added != 2 {
		t.Fatalf("first write: added=%d err=%v, want 2/nil", added, err)
	}

	// A relog offering the same two plus one genuinely new row.
	relog := []dynasty.TradeLogRow{logRow("2026-08-18", "a"), logRow("2026-08-18", "b"), logRow("2026-08-18", "c")}
	added, err = appendFootballTradeLog(r, w, day, relog)
	if err != nil {
		t.Fatalf("relog: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 — two of the three were already captured and the prior rows won", added)
	}
}

// A nil markers store is the soft-degrade path when statestore fails to
// construct one (runFootballTrades warns and passes nil rather than
// hard-failing the whole run). alertmarker treats nil as "no dedup" -- every
// trade must still send, on every run, since there is no marker store left to
// even check against.
func TestGradeAndAlertTrades_NilMarkersStoreStillSendsEveryAlert(t *testing.T) {
	in, sent := tradeAlertFixture(t)
	in.markers = nil

	res := gradeAndAlertTrades(context.Background(), in)
	if len(*sent) != 1 || res.Alerted != 1 || res.Skipped != 0 {
		t.Fatalf("first run: sent=%d alerted=%d skipped=%d, want 1/1/0", len(*sent), res.Alerted, res.Skipped)
	}

	// A second run against the same nil store must send again -- nothing was
	// ever recorded to dedup against, so "no store" must never read as
	// "already alerted".
	res2 := gradeAndAlertTrades(context.Background(), in)
	if len(*sent) != 2 || res2.Alerted != 1 || res2.Skipped != 0 {
		t.Fatalf("second run: sent=%d alerted=%d skipped=%d, want 2/1/0", len(*sent), res2.Alerted, res2.Skipped)
	}
}

// The alert title must name the actual reason. The zero-total Incomplete branch
// added for four-format grading carries NO unpriced assets, so the old blanket
// "too many unpriced assets" would send the operator hunting for one that does
// not exist.
func TestFormatTradeAlert_TitleNamesTheRealReasonForNoVerdict(t *testing.T) {
	txn := sleeper.Transaction{RosterIDs: []int{1, 2}}
	sides := []dynasty.TradeSide{{TeamID: "1", TeamName: "A"}, {TeamID: "2", TeamName: "B"}}

	cases := []struct {
		name    string
		verdict dynasty.TradeVerdict
		want    string
	}{
		{"favors", dynasty.TradeVerdict{Status: dynasty.TradeFavors, FavoredTeamName: "A", Pct: 45}, "Trade: favors A (+45%)"},
		{"unpriced", dynasty.TradeVerdict{Status: dynasty.TradeIncomplete, UnpricedAssets: 3}, "Trade: too many unpriced assets to grade"},
		{"all-zero", dynasty.TradeVerdict{Status: dynasty.TradeIncomplete}, "Trade: nothing to compare, so no verdict"},
	}
	for _, c := range cases {
		title, _ := formatTradeAlert(txn, sides, c.verdict)
		if title != c.want {
			t.Errorf("%s: title = %q, want %q", c.name, title, c.want)
		}
	}
}
