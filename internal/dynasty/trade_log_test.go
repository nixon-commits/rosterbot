package dynasty

import (
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

func tradeLogFixture() (sleeper.Transaction, map[string]sleeper.Player, *statsguy.Bundle, map[int]string) {
	txn := sleeper.Transaction{
		TransactionID: "t1",
		Type:          "trade",
		Status:        "complete",
		RosterIDs:     []int{1, 2},
		Adds:          map[string]int{"4984": 1, "9509": 2},
		Created:       time.Date(2026, 1, 23, 18, 30, 0, 0, time.UTC).UnixMilli(),
	}
	players := map[string]sleeper.Player{
		"4984": {PlayerID: "4984", FirstName: "Josh", LastName: "Allen"},
		"9509": {PlayerID: "9509", FirstName: "Bijan", LastName: "Robinson"},
	}
	bundle := &statsguy.Bundle{Players: map[string]statsguy.Player{
		"4984": {ID: "4984", Value: statsguy.FormatValues{SFDynasty: 9000, NonSFDynasty: 5000, SFRedraft: 7000, NonSFRedraft: 4000}},
		"9509": {ID: "9509", Value: statsguy.FormatValues{SFDynasty: 11000, NonSFDynasty: 9000, SFRedraft: 6000, NonSFRedraft: 8000}},
	}}
	// A real league team name, smart quote included.
	names := map[int]string{1: "Zatch's mom Hawk Tua'd", 2: "CeeDee Top"}
	return txn, players, bundle, names
}

func TestTradeLogObjectKey(t *testing.T) {
	d := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	if got, want := TradeLogObjectKey(d), "dt=2026-08-18/trades.ndjson"; got != want {
		t.Errorf("TradeLogObjectKey = %q, want %q", got, want)
	}
}

// THE INVARIANT THE WHOLE FEATURE EXISTS FOR: a stored row's values are what
// StatsGuy said when the trade was graded. StatsGuy publishes no history, so if
// the read path re-derived instead of replaying, a January trade would silently
// be priced against today and presented as what it was worth then.
//
// Adversarial by construction: the bundle is MUTATED between the write and the
// read, so a re-deriving implementation would return the new numbers and fail
// here. Precedent for storing rather than recomputing: internal/tradeboard's
// offer log.
func TestTradeLogRow_ValuesAreFrozenAtGradeTimeNotRepriced(t *testing.T) {
	txn, players, bundle, names := tradeLogFixture()
	graded := time.Date(2026, 1, 23, 19, 0, 0, 0, time.UTC)

	store := ndjsonstore.NewMemStore()
	row := BuildTradeLogRow(graded, txn, players, bundle, names, "sf_dynasty")
	if err := NewTradeLogWriter(store).WriteTradeLog(graded, []TradeLogRow{row}); err != nil {
		t.Fatalf("WriteTradeLog: %v", err)
	}

	// The world moves on: both players are re-valued, one of them past the
	// other, which would also flip the verdict.
	bundle.Players["4984"] = statsguy.Player{ID: "4984", Value: statsguy.FormatValues{SFDynasty: 1, NonSFDynasty: 1, SFRedraft: 1, NonSFRedraft: 1}}
	bundle.Players["9509"] = statsguy.Player{ID: "9509", Value: statsguy.FormatValues{SFDynasty: 99999, NonSFDynasty: 99999, SFRedraft: 99999, NonSFRedraft: 99999}}

	got, err := NewTradeLogReader(store).ReadAllTrades()
	if err != nil {
		t.Fatalf("ReadAllTrades: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}

	byTeam := map[string]TradeLogSide{}
	for _, s := range got[0].Sides {
		byTeam[s.TeamID] = s
	}
	if v := byTeam["1"].Totals.SFDynasty; v != 9000 {
		t.Errorf("side 1 sf_dynasty total = %d, want 9000 (the price at grade time, not today's 1)", v)
	}
	if v := byTeam["2"].Totals.SFDynasty; v != 11000 {
		t.Errorf("side 2 sf_dynasty total = %d, want 11000 (not today's 99999)", v)
	}
	if v := byTeam["1"].Assets[0].Values.NonSFRedraft; v != 4000 {
		t.Errorf("asset non_sf_redraft = %d, want 4000 — every format leaf must be frozen, not just the alert's", v)
	}
	// The verdict is stored too, so it cannot silently invert with the market.
	if s := got[0].Verdicts["sf_dynasty"].Summary; !strings.Contains(s, "CeeDee Top") {
		t.Errorf("stored sf_dynasty summary = %q, want the grade-time winner (CeeDee Top)", s)
	}
}

func TestWriteTradeLogThenReadAllTrades_RoundTrip(t *testing.T) {
	store := ndjsonstore.NewMemStore()
	w := NewTradeLogWriter(store)

	d1 := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	if err := w.WriteTradeLog(d1, []TradeLogRow{{Dt: "2026-08-17", TransactionID: "a", AlertFormat: "sf_dynasty"}}); err != nil {
		t.Fatalf("WriteTradeLog d1: %v", err)
	}
	if err := w.WriteTradeLog(d2, []TradeLogRow{{Dt: "2026-08-18", TransactionID: "b"}}); err != nil {
		t.Fatalf("WriteTradeLog d2: %v", err)
	}

	got, err := NewTradeLogReader(store).ReadAllTrades()
	if err != nil {
		t.Fatalf("ReadAllTrades: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	// dt= partitions sort lexically, which is chronological.
	if got[0].Dt != "2026-08-17" || got[1].Dt != "2026-08-18" {
		t.Errorf("order = %q, %q; want chronological", got[0].Dt, got[1].Dt)
	}
	if got[0].AlertFormat != "sf_dynasty" {
		t.Errorf("AlertFormat = %q, want sf_dynasty", got[0].AlertFormat)
	}
}

func TestReadAllTrades_EmptyStoreIsNotAnError(t *testing.T) {
	got, err := NewTradeLogReader(ndjsonstore.NewMemStore()).ReadAllTrades()
	if err != nil {
		t.Fatalf("ReadAllTrades on empty store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// ndjsonstore.Write is a whole-object overwrite and the producer polls every
// six hours, so two trades on the same UTC day arrive as DISJOINT graded sets.
// Writing the second set bare deletes the first with no error at all.
func TestMergeTradeLog_SecondPollDoesNotClobberTheFirst(t *testing.T) {
	store := ndjsonstore.NewMemStore()
	w, r := NewTradeLogWriter(store), NewTradeLogReader(store)
	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)

	first := []TradeLogRow{{Dt: "2026-08-18", TransactionID: "morning"}}
	if err := w.WriteTradeLog(day, MergeTradeLog(nil, first)); err != nil {
		t.Fatalf("first write: %v", err)
	}

	prior, err := r.ReadAllTrades()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	second := []TradeLogRow{{Dt: "2026-08-18", TransactionID: "evening"}}
	if err := w.WriteTradeLog(day, MergeTradeLog(prior, second)); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := r.ReadAllTrades()
	if err != nil {
		t.Fatalf("ReadAllTrades: %v", err)
	}
	ids := map[string]bool{}
	for _, row := range got {
		ids[row.TransactionID] = true
	}
	if !ids["morning"] || !ids["evening"] {
		t.Fatalf("rows = %v, want both polls' trades — a bare second Write silently deletes the first", ids)
	}
}

// A marker READ error falls through to grade-and-send, so the same transaction
// can be graded twice in one day. The first row is the one whose values match
// the alert that actually went out.
func TestMergeTradeLog_PriorWinsOnDuplicateTransaction(t *testing.T) {
	prior := []TradeLogRow{{TransactionID: "t1", AlertFormat: "first"}}
	fresh := []TradeLogRow{{TransactionID: "t1", AlertFormat: "second"}}
	got := MergeTradeLog(prior, fresh)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].AlertFormat != "first" {
		t.Errorf("AlertFormat = %q, want %q — the earlier row is the grade the alert reported", got[0].AlertFormat, "first")
	}
}

func TestDedupeTradeLog_KeepsTheEarliestGrade(t *testing.T) {
	early := time.Date(2026, 1, 23, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	rows := []TradeLogRow{
		{TransactionID: "t1", GradedAt: late, Regraded: true},
		{TransactionID: "t1", GradedAt: early},
		{TransactionID: "t2", GradedAt: late},
	}
	got := DedupeTradeLog(rows)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.TransactionID == "t1" && !r.GradedAt.Equal(early) {
			t.Errorf("t1 kept GradedAt %v, want the earliest (%v) — a later re-price must never displace the captured grade", r.GradedAt, early)
		}
	}
}

func TestBuildTradeLogRow_PricesAllFourFormatsIndependently(t *testing.T) {
	txn, players, bundle, names := tradeLogFixture()
	row := BuildTradeLogRow(time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), txn, players, bundle, names, "sf_dynasty")

	if len(row.Verdicts) != len(TradeLogFormats) {
		t.Fatalf("len(Verdicts) = %d, want %d", len(row.Verdicts), len(TradeLogFormats))
	}
	// sf_dynasty: 9000 vs 11000 -> side 2 leads. non_sf_redraft: 4000 vs 8000
	// -> side 2 leads too, but sf_redraft is 7000 vs 6000 -> side 1 leads. A
	// row carrying one format would relabel the other three under the toggle.
	if got := row.Verdicts["sf_dynasty"].FavoredTeamID; got != "2" {
		t.Errorf("sf_dynasty favored = %q, want 2", got)
	}
	if got := row.Verdicts["sf_redraft"].FavoredTeamID; got != "1" {
		t.Errorf("sf_redraft favored = %q, want 1 — the leader genuinely differs by format", got)
	}

	byTeam := map[string]TradeLogSide{}
	for _, s := range row.Sides {
		byTeam[s.TeamID] = s
	}
	if byTeam["1"].Totals.SFRedraft != 7000 || byTeam["2"].Totals.SFRedraft != 6000 {
		t.Errorf("sf_redraft totals = %d / %d, want 7000 / 6000", byTeam["1"].Totals.SFRedraft, byTeam["2"].Totals.SFRedraft)
	}
}

// The dashboard renders the stored Summary rather than formatting the float, so
// it can never disagree with the %.0f the Pushover alert used.
func TestBuildTradeLogRow_SummaryMatchesTheAlertRendering(t *testing.T) {
	txn, players, bundle, names := tradeLogFixture()
	row := BuildTradeLogRow(time.Now().UTC(), txn, players, bundle, names, "sf_dynasty")

	v := row.Verdicts["sf_dynasty"]
	want := TradeVerdictSummary(TradeVerdict{
		Status: v.Status, FavoredTeamID: v.FavoredTeamID, FavoredTeamName: v.FavoredTeamName, Pct: v.Pct,
	})
	if v.Summary != want {
		t.Errorf("Summary = %q, want %q", v.Summary, want)
	}
	if !strings.HasPrefix(v.Summary, "favors CeeDee Top (+") || !strings.HasSuffix(v.Summary, "%)") {
		t.Errorf("Summary = %q, want the alert's `favors <team> (+N%%)` shape", v.Summary)
	}
	if strings.Contains(v.Summary, ".") {
		t.Errorf("Summary = %q, want %%.0f rounding with no decimal point", v.Summary)
	}
}

func TestBuildTradeLogRow_UnpricedAssetSuppressesEveryFormatsVerdict(t *testing.T) {
	txn, players, bundle, names := tradeLogFixture()
	// The real live-league case: a FAAB leg StatsGuy prices in no format.
	txn.WaiverBudget = []sleeper.WaiverBudgetTransfer{{Sender: 2, Receiver: 1, Amount: 18}}

	row := BuildTradeLogRow(time.Now().UTC(), txn, players, bundle, names, "sf_dynasty")
	for _, f := range TradeLogFormats {
		v := row.Verdicts[f]
		if v.Status != TradeIncomplete {
			t.Errorf("%s: Status = %q, want %q — Priced is a property of the asset, not the format", f, v.Status, TradeIncomplete)
		}
		if v.UnpricedAssets != 1 {
			t.Errorf("%s: UnpricedAssets = %d, want 1", f, v.UnpricedAssets)
		}
	}
	// The unpriced asset is still present, so the view can say WHY.
	var faab *TradeLogAsset
	for i := range row.Sides {
		for j := range row.Sides[i].Assets {
			if strings.Contains(row.Sides[i].Assets[j].Name, "FAAB") {
				faab = &row.Sides[i].Assets[j]
			}
		}
	}
	if faab == nil {
		t.Fatal("FAAB asset dropped from the row; the view could not explain the incomplete verdict")
	}
	if faab.Priced {
		t.Error("FAAB reported as priced")
	}
}

// txn.Adds is a map and Go randomizes map iteration, so an unsorted builder
// yields a different asset order on every call — churning the stored row (and
// the alert body) between runs for no reason.
func TestBuildTradeLogRow_AssetOrderIsDeterministic(t *testing.T) {
	txn, players, bundle, names := tradeLogFixture()
	txn.Adds = map[string]int{"4984": 1, "9509": 1, "a": 1, "b": 1, "c": 1}
	for id := range txn.Adds {
		if _, ok := players[id]; !ok {
			players[id] = sleeper.Player{PlayerID: id, FirstName: strings.ToUpper(id), LastName: "Player"}
			bundle.Players[id] = statsguy.Player{ID: id, Value: statsguy.FormatValues{SFDynasty: 100}}
		}
	}

	first := BuildTradeLogRow(time.Now().UTC(), txn, players, bundle, names, "sf_dynasty")
	firstNames := assetNames(first)
	for i := 0; i < 200; i++ {
		got := assetNames(BuildTradeLogRow(time.Now().UTC(), txn, players, bundle, names, "sf_dynasty"))
		if got != firstNames {
			t.Fatalf("asset order changed between calls:\n  %s\n  %s", firstNames, got)
		}
	}
}

func assetNames(r TradeLogRow) string {
	var b strings.Builder
	for _, s := range r.Sides {
		b.WriteString(s.TeamID)
		b.WriteString(":")
		for _, a := range s.Assets {
			b.WriteString(a.Name)
			b.WriteString(",")
		}
		b.WriteString("|")
	}
	return b.String()
}

// A transaction with no usable Created must read as UNKNOWN, never as 1970.
func TestBuildTradeLogRow_MissingCreatedIsUnknownNotTheEpoch(t *testing.T) {
	txn, players, bundle, names := tradeLogFixture()
	txn.Created = 0
	row := BuildTradeLogRow(time.Now().UTC(), txn, players, bundle, names, "sf_dynasty")
	if !row.TradeDate.IsZero() {
		t.Errorf("TradeDate = %v, want the zero time", row.TradeDate)
	}
	if e := entryFor(row); e.TradeDate != "" {
		t.Errorf("model TradeDate = %q, want empty (unknown), not an epoch date", e.TradeDate)
	}
}

func TestBuildTradeLogRow_TradeDateComesFromCreatedMillis(t *testing.T) {
	txn, players, bundle, names := tradeLogFixture()
	row := BuildTradeLogRow(time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), txn, players, bundle, names, "sf_dynasty")
	if got, want := row.TradeDate.Format("2006-01-02"), "2026-01-23"; got != want {
		t.Errorf("TradeDate = %q, want %q (Sleeper's Created is epoch MILLIS)", got, want)
	}
	if got, want := row.Dt, "2026-08-18"; got != want {
		t.Errorf("Dt = %q, want %q — the partition is the GRADE date, not the trade date", got, want)
	}
}

func TestBuildTradeLogModel_EmptyLog(t *testing.T) {
	m := BuildTradeLogModel(nil, time.Now())
	if !m.Empty {
		t.Error("Empty = false on an empty log")
	}
	if len(m.Trades) != 0 {
		t.Errorf("len(Trades) = %d, want 0", len(m.Trades))
	}
}

func TestBuildTradeLogModel_NewestTradeFirstAndUnknownDatesLast(t *testing.T) {
	mk := func(id, tradeDate string) TradeLogRow {
		r := TradeLogRow{Dt: "2026-08-18", TransactionID: id, GradedAt: time.Now().UTC()}
		if tradeDate != "" {
			d, _ := time.Parse("2006-01-02", tradeDate)
			r.TradeDate = d
		}
		return r
	}
	m := BuildTradeLogModel([]TradeLogRow{
		mk("old", "2026-01-23"),
		mk("unknown", ""),
		mk("new", "2026-08-01"),
	}, time.Now())

	var order []string
	for _, e := range m.Trades {
		order = append(order, e.TransactionID)
	}
	want := []string{"new", "old", "unknown"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v (newest first; an unknown date must not sort as year zero)", order, want)
		}
	}
}

func TestBuildTradeLogModel_DedupesAcrossPartitionsAndReportsCoverage(t *testing.T) {
	early := time.Date(2026, 1, 23, 0, 0, 0, 0, time.UTC)
	rows := []TradeLogRow{
		{Dt: "2026-01-23", TransactionID: "t1", GradedAt: early, AlertFormat: "captured"},
		{Dt: "2026-08-18", TransactionID: "t1", GradedAt: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), AlertFormat: "regraded", Regraded: true},
	}
	m := BuildTradeLogModel(rows, time.Now())
	if len(m.Trades) != 1 {
		t.Fatalf("len(Trades) = %d, want 1", len(m.Trades))
	}
	if m.Trades[0].AlertFormat != "captured" {
		t.Errorf("kept the %q row, want the captured one", m.Trades[0].AlertFormat)
	}
	if m.Regraded != 0 {
		t.Errorf("Regraded = %d, want 0 — the captured row won, so nothing in the list is a re-price", m.Regraded)
	}
}

// CoversFrom must be the earliest TRADE date, never the earliest partition.
// --relog writes every rebuilt row into TODAY's partition, so a grade-date
// reading would have the view print "logged from 2026-08-18 onward" directly
// above a card headed 2026-01-23 — a claim the rows beneath it contradict.
func TestBuildTradeLogModel_CoversFromIsTheTradeDateNotThePartition(t *testing.T) {
	jan, _ := time.Parse("2006-01-02", "2026-01-23")
	may, _ := time.Parse("2006-01-02", "2026-05-07")
	relogDay := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)

	// Exactly what --relog produces: old trades, all graded today.
	rows := []TradeLogRow{
		{Dt: "2026-08-18", TransactionID: "a", TradeDate: jan, GradedAt: relogDay, Regraded: true},
		{Dt: "2026-08-18", TransactionID: "b", TradeDate: may, GradedAt: relogDay, Regraded: true},
	}
	m := BuildTradeLogModel(rows, relogDay)
	if m.CoversFrom != "2026-01-23" {
		t.Errorf("CoversFrom = %q, want 2026-01-23 (the earliest trade), not the partition date", m.CoversFrom)
	}
	if m.Regraded != 2 {
		t.Errorf("Regraded = %d, want 2 — the view must be able to say part of this list is re-priced", m.Regraded)
	}
}

// A log holding only undated trades has nothing honest to say about coverage,
// and must say nothing rather than guess.
func TestBuildTradeLogModel_NoTradeDatesMeansNoCoverageClaim(t *testing.T) {
	m := BuildTradeLogModel([]TradeLogRow{{Dt: "2026-08-18", TransactionID: "a", GradedAt: time.Now().UTC()}}, time.Now())
	if m.CoversFrom != "" {
		t.Errorf("CoversFrom = %q, want empty", m.CoversFrom)
	}
}

// The inversion that earliest-wins alone would cause: --relog writes a
// re-priced row TODAY for a trade not yet alerted, and the genuine grade-time
// capture then lands on a LATER day. Keeping the earlier row would keep the
// re-price and discard the real one — this store's invariant, broken from the
// opposite direction.
func TestDedupeTradeLog_CapturedRowBeatsARegradedOneEvenWhenLater(t *testing.T) {
	relogDay := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	captureDay := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC) // a week LATER
	rows := []TradeLogRow{
		{TransactionID: "t1", GradedAt: relogDay, Regraded: true, AlertFormat: "repriced"},
		{TransactionID: "t1", GradedAt: captureDay, AlertFormat: "captured"},
	}
	got := DedupeTradeLog(rows)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].AlertFormat != "captured" || got[0].Regraded {
		t.Errorf("kept %+v, want the captured row — a re-price must never outrank a real grade-time capture, whatever the dates", got[0])
	}
}

// Among rows of the same kind the earliest still wins, so a duplicate capture
// caused by a marker read failure cannot displace the original.
func TestDedupeTradeLog_AmongCapturedRowsTheEarliestStillWins(t *testing.T) {
	early := time.Date(2026, 1, 23, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	got := DedupeTradeLog([]TradeLogRow{
		{TransactionID: "t1", GradedAt: late, AlertFormat: "second"},
		{TransactionID: "t1", GradedAt: early, AlertFormat: "first"},
	})
	if got[0].AlertFormat != "first" {
		t.Errorf("kept %q, want the first capture", got[0].AlertFormat)
	}
	// And among two regraded rows, likewise.
	got = DedupeTradeLog([]TradeLogRow{
		{TransactionID: "t2", GradedAt: late, Regraded: true, AlertFormat: "second"},
		{TransactionID: "t2", GradedAt: early, Regraded: true, AlertFormat: "first"},
	})
	if got[0].AlertFormat != "first" {
		t.Errorf("kept %q among regraded rows, want the first", got[0].AlertFormat)
	}
}
