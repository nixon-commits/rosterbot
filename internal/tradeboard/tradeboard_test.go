package tradeboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
)

const (
	me    = "Intentional Balk"
	them  = "Yordan's Schlong"
	other = "BT95"
)

var now = time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)

func hkbPlayers() []hkb.Player {
	return []hkb.Player{
		{Name: "Gage Jump", Value: 1391, Rank: 70, Age: 23.3, PitcherStats: &hkb.PitcherStats{}},
		{Name: "Emmet Sheehan", Value: 1234, Rank: 85, Age: 26.7, PitcherStats: &hkb.PitcherStats{}},
		{Name: "Kyle Harrison", Value: 2292, Rank: 40, Age: 24.9, PitcherStats: &hkb.PitcherStats{}},
		{Name: "Alec Bohm", Value: 900, Rank: 210, Age: 29.1, HitterStats: &hkb.HitterStats{}},
		{Name: "2027 Early 1st", Value: 1419, Rank: 134, AssetType: "PICK"},
		{Name: "2028 Late 3rd", Value: 45, Rank: 774, AssetType: "PICK"},
	}
}

// The live 2-for-1, end to end through the producer path.
func liveOffer() []OfferInput {
	return []OfferInput{
		{TradeID: "lpe0ltl", Player: "Gage Jump", Position: "SP", From: them, To: me},
		{TradeID: "lpe0ltl", Player: "Kyle Harrison", Position: "SP", From: me, To: them},
		{TradeID: "lpe0ltl", Player: "Emmet Sheehan", Position: "SP", From: them, To: me},
	}
}

func TestBuildOffers_PricesBothSidesAndCarriesTheVerdict(t *testing.T) {
	offers := BuildOffers(liveOffer(), hkbPlayers())
	if len(offers) != 1 {
		t.Fatalf("got %d offers, want 1", len(offers))
	}
	o := offers[0]
	if o.Verdict.Status != tradevalue.StatusTooClose {
		t.Errorf("Status = %q, want too-close", o.Verdict.Status)
	}
	if len(o.Sides) != 2 {
		t.Fatalf("got %d sides, want 2", len(o.Sides))
	}
	// Sides are sorted by team name: "Intentional Balk" < "Yordan's Schlong".
	if o.Sides[0].Team != me {
		t.Fatalf("Sides[0] = %q, want %q", o.Sides[0].Team, me)
	}
	if o.Sides[0].Raw != 2625 {
		t.Errorf("my raw = %d, want 2625", o.Sides[0].Raw)
	}
	if o.Sides[0].PricedCount != 2 || o.Sides[0].AssetCount != 2 {
		t.Errorf("my coverage = %d/%d, want 2/2", o.Sides[0].PricedCount, o.Sides[0].AssetCount)
	}
	if o.Sides[1].Raw != 2292 {
		t.Errorf("their raw = %d, want 2292", o.Sides[1].Raw)
	}
}

func TestBuildOffers_IsDeterministic(t *testing.T) {
	first := BuildOffers(liveOffer(), hkbPlayers())
	for i := 0; i < 25; i++ {
		got := BuildOffers(liveOffer(), hkbPlayers())
		if len(got) != len(first) {
			t.Fatalf("offer count moved between runs")
		}
		for j := range got {
			if got[j].TradeID != first[j].TradeID || got[j].Sides[0].Team != first[j].Sides[0].Team {
				t.Fatalf("offer ordering not stable: %+v vs %+v", first[j], got[j])
			}
		}
	}
}

func TestBuildOffers_EmptyInputYieldsNoOffersNotAnError(t *testing.T) {
	if got := BuildOffers(nil, hkbPlayers()); len(got) != 0 {
		t.Errorf("got %d offers from empty input, want 0", len(got))
	}
}

func TestMine_FiltersToMyTrades(t *testing.T) {
	inputs := append(liveOffer(),
		OfferInput{TradeID: "zzz", Player: "Alec Bohm", From: other, To: them},
		OfferInput{TradeID: "zzz", Player: "Gage Jump", From: them, To: other},
	)
	all := BuildOffers(inputs, hkbPlayers())
	if len(all) != 2 {
		t.Fatalf("got %d offers, want 2", len(all))
	}
	mine := Mine(all, me)
	if len(mine) != 1 || mine[0].TradeID != "lpe0ltl" {
		t.Errorf("Mine = %+v, want just lpe0ltl", mine)
	}
}

func teams() []teamvalue.Row {
	return []teamvalue.Row{
		{TeamID: "1", TeamName: me, PitcherMLBValue: 5000, HitterMLBValue: 3000},
		{TeamID: "2", TeamName: them, PitcherMLBValue: 4000, HitterMLBValue: 6000},
		{TeamID: "3", TeamName: other, PitcherMLBValue: 4500, HitterMLBValue: 1000},
	}
}

func pvs() []PlayerValue {
	return []PlayerValue{
		{Name: "Gage Jump", Value: 1391, IsPitcher: true, Matched: true},
		{Name: "Emmet Sheehan", Value: 1234, IsPitcher: true, Matched: true},
		{Name: "Kyle Harrison", Value: 2292, IsPitcher: true, Matched: true},
		{Name: "Alec Bohm", Value: 900, Matched: true},
	}
}

func metric(t *testing.T, imp *Impact, name string) ImpactMetric {
	t.Helper()
	for _, m := range imp.Metrics {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no %q metric in %+v", name, imp.Metrics)
	return ImpactMetric{}
}

func TestBuildImpact_MovesValueBetweenTheRightLeavesAndReRanks(t *testing.T) {
	imp, note := BuildImpact(me, liveOffer(), teams(), pvs())
	if imp == nil {
		t.Fatalf("BuildImpact = nil (%s), want an impact", note)
	}
	if note != "" {
		t.Errorf("note = %q, want empty alongside a non-nil impact", note)
	}

	// I receive 1391+1234 and give up 2292: pitcher value +333.
	p := metric(t, imp, "Pitchers")
	if p.Before != 5000 || p.After != 5333 || p.Delta != 333 {
		t.Errorf("Pitchers = %d -> %d (delta %d), want 5000 -> 5333 (+333)", p.Before, p.After, p.Delta)
	}
	// I was already 1st in pitcher value and stay 1st.
	if p.RankBefore != 1 || p.RankAfter != 1 {
		t.Errorf("Pitchers rank = %d -> %d, want 1 -> 1", p.RankBefore, p.RankAfter)
	}
	if p.Teams != 3 {
		t.Errorf("Teams = %d, want 3", p.Teams)
	}

	// No hitters changed hands.
	if h := metric(t, imp, "Hitters"); h.Delta != 0 {
		t.Errorf("Hitters delta = %d, want 0", h.Delta)
	}
	if tot := metric(t, imp, "Total"); tot.Delta != 333 {
		t.Errorf("Total delta = %d, want 333", tot.Delta)
	}
}

// The rank is the reason this exists: a value-neutral trade can still move
// you in the standings for a category.
func TestBuildImpact_RankChangesWhenValueCrossesARival(t *testing.T) {
	// Give up Harrison (2292) for Bohm (900): pitcher value 5000 -> 2708,
	// dropping me from 1st to 3rd behind both rivals.
	inputs := []OfferInput{
		{TradeID: "x", Player: "Kyle Harrison", From: me, To: them},
		{TradeID: "x", Player: "Alec Bohm", From: them, To: me},
	}
	imp, note := BuildImpact(me, inputs, teams(), pvs())
	if imp == nil {
		t.Fatalf("BuildImpact = nil (%s)", note)
	}
	p := metric(t, imp, "Pitchers")
	if p.After != 2708 {
		t.Errorf("Pitchers after = %d, want 2708", p.After)
	}
	if p.RankBefore != 1 || p.RankAfter != 3 {
		t.Errorf("Pitchers rank = %d -> %d, want 1 -> 3", p.RankBefore, p.RankAfter)
	}
	h := metric(t, imp, "Hitters")
	if h.Delta != 900 {
		t.Errorf("Hitters delta = %d, want +900", h.Delta)
	}
}

func TestBuildImpact_SuppressedWhenAnythingCannotBeResolved(t *testing.T) {
	cases := map[string][]OfferInput{
		"unidentified draft pick": {
			{TradeID: "x", Player: "", From: them, To: me},
			{TradeID: "x", Player: "Kyle Harrison", From: me, To: them},
		},
		"player missing from the values table": {
			{TradeID: "x", Player: "Nobody At All", From: them, To: me},
			{TradeID: "x", Player: "Kyle Harrison", From: me, To: them},
		},
		"team the values table does not know": {
			{TradeID: "x", Player: "Gage Jump", From: "Ghost Team", To: me},
			{TradeID: "x", Player: "Kyle Harrison", From: me, To: "Ghost Team"},
		},
	}
	for name, inputs := range cases {
		t.Run(name, func(t *testing.T) {
			imp, note := BuildImpact(me, inputs, teams(), pvs())
			if imp != nil {
				t.Errorf("BuildImpact = %+v, want nil", imp)
			}
			// Suppression must always be legible: the tab shows this
			// instead of silently dropping the section.
			if note == "" {
				t.Error("suppressed impact carried no reason")
			}
			t.Logf("note: %s", note)
		})
	}
}

func TestBuildImpact_NilWhenIAmNotAParticipant(t *testing.T) {
	inputs := []OfferInput{
		{TradeID: "x", Player: "Gage Jump", From: them, To: other},
		{TradeID: "x", Player: "Alec Bohm", From: other, To: them},
	}
	imp, note := BuildImpact("Not A Team", inputs, teams(), pvs())
	if imp != nil {
		t.Errorf("BuildImpact = %+v, want nil", imp)
	}
	if note == "" {
		t.Error("suppressed impact carried no reason")
	}
}

func TestBuildValuesTable(t *testing.T) {
	pool := []PoolPlayer{
		{Name: "Kyle Harrison", Position: "SP", FantasyTeamID: "1", IsPitcher: true},
		{Name: "Alec Bohm", Position: "3B,INF", FantasyTeamID: "1"},
		{Name: "Unranked Nobody", Position: "OF", FantasyTeamID: "2"},
		{Name: "Some Free Agent", Position: "OF"}, // no team: skipped
	}
	names := map[string]string{"1": me, "2": them}

	vt := BuildValuesTable(now, pool, hkbPlayers(), teams(), names)

	if vt.Rostered != 3 || vt.Matched != 2 {
		t.Errorf("coverage = %d/%d, want 2/3", vt.Matched, vt.Rostered)
	}
	if len(vt.Players) != 3 {
		t.Fatalf("got %d players, want 3 (free agent excluded)", len(vt.Players))
	}
	// Sorted by value descending.
	if vt.Players[0].Name != "Kyle Harrison" || vt.Players[0].Value != 2292 {
		t.Errorf("Players[0] = %+v, want Kyle Harrison at 2292", vt.Players[0])
	}
	if vt.Players[0].OwnerName != me {
		t.Errorf("OwnerName = %q, want %q", vt.Players[0].OwnerName, me)
	}
	// The unmatched player is present but flagged, not silently dropped.
	last := vt.Players[len(vt.Players)-1]
	if last.Name != "Unranked Nobody" || last.Matched || last.Value != 0 {
		t.Errorf("unmatched row = %+v, want present with Matched=false and Value=0", last)
	}
	// Picks come through as a reference list, value-sorted.
	if len(vt.Picks) != 2 || vt.Picks[0].Name != "2027 Early 1st" {
		t.Errorf("Picks = %+v, want 2 with the 1419 first", vt.Picks)
	}
}

// assertWireTimestamp checks the four things a generated_at must satisfy: the
// exact expected string, no fractional component, that same string surviving
// the encoder that actually ships the payload, and acceptance by the strict
// RFC3339 parser the iOS client uses.
//
// It takes the whole payload rather than just the field because marshalling
// the real struct is the half that matters — the field could be a correct
// string while a MarshalJSON somewhere above it re-encoded the value.
func assertWireTimestamp(t *testing.T, got, want string, payload any) {
	t.Helper()
	if got != want {
		t.Errorf("generated_at = %q, want %q", got, want)
	}
	if strings.Contains(got, ".") {
		t.Errorf("generated_at = %q carries a fractional component", got)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"generated_at":"`+want+`"`) {
		t.Errorf("serialized form wrong: %s", string(b[:min(len(b), 160)]))
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("strict RFC3339 parse failed: %v", err)
	}
}

// TestValuesTableGeneratedAtHasNoFractionalSeconds pins the wire format of
// generated_at on GET /v1/trades/values against a strict RFC3339 parser.
//
// The input carries a NONZERO nanosecond count on purpose. That is the whole
// test: a whole-second input passes against a time.Time field too, because
// RFC3339Nano only renders a fraction when there is one to render. So the
// obvious fixture -- today's real caller, which passes a UTC-midnight date --
// cannot fail, and neither can a client tested against the live endpoint. Only
// a fractional input separates the fixed field from the broken one.
//
// The iOS client's ISO8601DateFormatter([.withInternetDateTime]) returns nil
// on a fractional timestamp, and a nil date there reads as "not stale" rather
// than as an error -- a staleness notice that silently never fires.
func TestValuesTableGeneratedAtHasNoFractionalSeconds(t *testing.T) {
	// A nanosecond count RFC3339Nano would render as ".902729184".
	stamp := time.Date(2026, 8, 24, 21, 47, 27, 902729184, time.UTC)

	// The encoder that actually ships it: cmd/team-values.go json.Marshals the
	// whole table and publishes the bytes, and lineupapi serves them without
	// reparsing.
	vt := BuildValuesTable(stamp, nil, nil, nil, nil)
	assertWireTimestamp(t, vt.GeneratedAt, "2026-08-24T21:47:27Z", vt)
}

// TestValuesTableGeneratedAtIsUTC pins the .UTC() call, which is easy to drop
// as redundant -- today's caller already passes UTC, so nothing else in the
// tree would notice.
//
// A non-UTC time formats as a numeric offset ("-04:00") instead of "Z". That
// still parses as RFC3339, so this is a convention pin rather than a parse
// guard: the field's doc comment promises UTC, and generated_at on
// GET /v1/lineup/today and GET /v1/pool/available both keep it, so a client
// comparing two artifacts' stamps as strings would silently start disagreeing.
func TestValuesTableGeneratedAtIsUTC(t *testing.T) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	stamp := time.Date(2026, 8, 24, 17, 47, 27, 0, eastern)

	vt := BuildValuesTable(stamp, nil, nil, nil, nil)
	if want := "2026-08-24T21:47:27Z"; vt.GeneratedAt != want {
		t.Errorf("generated_at = %q, want %q (UTC, not a numeric offset)", vt.GeneratedAt, want)
	}
}

// TestNewSnapshotGeneratedAtHasNoFractionalSeconds is the GET /v1/trades twin
// of TestValuesTableGeneratedAtHasNoFractionalSeconds, and it is the half that
// was actually broken in production.
//
// ValuesTable is built from a UTC-midnight date, so its nanosecond count is
// always zero and the endpoint never emitted a fractional stamp. Snapshot is
// built from time.Now() on every hourly optimize run, so its nanosecond count
// is never zero -- measured 1000/1000 -- and GET /v1/trades served
// "2026-08-26T19:37:34.523083Z" every single time, which is precisely the
// shape the iOS client's ISO8601DateFormatter returns nil on.
func TestNewSnapshotGeneratedAtHasNoFractionalSeconds(t *testing.T) {
	// A nanosecond count RFC3339Nano would render as ".523083".
	stamp := time.Date(2026, 8, 26, 19, 37, 34, 523083000, time.UTC)

	// The encoder that ships it: cmd/trades.go json.Marshals the snapshot and
	// publishes the bytes; lineupapi's serveBlob returns them byte-for-byte
	// without reparsing.
	snap := NewSnapshot(stamp, "Team A", nil)
	assertWireTimestamp(t, snap.GeneratedAt, "2026-08-26T19:37:34Z", snap)
}

// TestNewSnapshotGeneratedAtIsUTC pins the .UTC() call. cmd/optimize.go passes
// time.Now(), which carries the machine's local zone -- so unlike the
// ValuesTable twin, this one is not merely a convention pin: drop the .UTC()
// and a Fargate task in a non-UTC zone would emit a numeric offset.
func TestNewSnapshotGeneratedAtIsUTC(t *testing.T) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	stamp := time.Date(2026, 8, 26, 15, 37, 34, 0, eastern)

	snap := NewSnapshot(stamp, "Team A", nil)
	if want := "2026-08-26T19:37:34Z"; snap.GeneratedAt != want {
		t.Errorf("generated_at = %q, want %q (UTC, not a numeric offset)", snap.GeneratedAt, want)
	}
}

func TestInferOutcome(t *testing.T) {
	offer := BuildOffers(liveOffer(), hkbPlayers())[0]

	exact := []ExecutedAsset{
		{TradeGroupID: "g1", Player: "Gage Jump", To: me},
		{TradeGroupID: "g1", Player: "Emmet Sheehan", To: me},
		{TradeGroupID: "g1", Player: "Kyle Harrison", To: them},
	}
	if got := InferOutcome(offer, exact); got != OutcomeAccepted {
		t.Errorf("exact match = %q, want %q", got, OutcomeAccepted)
	}

	// A different trade between the same two teams must not claim it.
	partial := []ExecutedAsset{
		{TradeGroupID: "g2", Player: "Gage Jump", To: me},
		{TradeGroupID: "g2", Player: "Kyle Harrison", To: them},
	}
	if got := InferOutcome(offer, partial); got != OutcomeNotExecuted {
		t.Errorf("partial match = %q, want %q", got, OutcomeNotExecuted)
	}
	if got := InferOutcome(offer, nil); got != OutcomeNotExecuted {
		t.Errorf("no executed trades = %q, want %q", got, OutcomeNotExecuted)
	}
}

func TestMergeLog_PreservesFirstSeenAndResolvesVanishedOffers(t *testing.T) {
	offers := BuildOffers(liveOffer(), hkbPlayers())
	earlier := now.Add(-48 * time.Hour)

	// First observation.
	day1 := MergeLog(earlier, me, nil, offers, nil)
	if len(day1) != 1 || day1[0].Outcome != OutcomeOpen {
		t.Fatalf("day1 = %+v, want one open row", day1)
	}
	if !day1[0].FirstSeen.Equal(earlier) {
		t.Errorf("FirstSeen = %v, want %v", day1[0].FirstSeen, earlier)
	}

	// Still open a day later: FirstSeen holds, LastSeen advances.
	day2 := MergeLog(now, me, day1, offers, nil)
	if !day2[0].FirstSeen.Equal(earlier) {
		t.Errorf("FirstSeen moved to %v, want %v", day2[0].FirstSeen, earlier)
	}
	if !day2[0].LastSeen.Equal(now) {
		t.Errorf("LastSeen = %v, want %v", day2[0].LastSeen, now)
	}

	// Gone, with no matching executed trade.
	day3 := MergeLog(now, me, day2, nil, nil)
	if day3[0].Outcome != OutcomeNotExecuted {
		t.Errorf("Outcome = %q, want %q", day3[0].Outcome, OutcomeNotExecuted)
	}
	// The as-of valuation survives the offer disappearing.
	if len(day3[0].Sides) != 2 || day3[0].Sides[0].Raw != 2625 {
		t.Errorf("valuation lost on resolve: %+v", day3[0].Sides)
	}

	// A resolved row does not get re-judged on later runs.
	day4 := MergeLog(now, me, day3, nil, []ExecutedAsset{
		{TradeGroupID: "g", Player: "Gage Jump", To: me},
		{TradeGroupID: "g", Player: "Emmet Sheehan", To: me},
		{TradeGroupID: "g", Player: "Kyle Harrison", To: them},
	})
	if day4[0].Outcome != OutcomeNotExecuted {
		t.Errorf("resolved outcome changed to %q; it must be decided once", day4[0].Outcome)
	}
}

func TestLogRoundTripsThroughTheStore(t *testing.T) {
	rows := MergeLog(now, me, nil, BuildOffers(liveOffer(), hkbPlayers()), nil)

	b, err := MarshalNDJSON(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := UnmarshalNDJSON(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].TradeID != "lpe0ltl" {
		t.Fatalf("round trip = %+v", got)
	}
	if got[0].Sides[0].Raw != 2625 || got[0].Verdict.Status != tradevalue.StatusTooClose {
		t.Errorf("valuation did not survive the round trip: %+v", got[0])
	}
	if want := "dt=2026-08-06/offers.ndjson"; ObjectKey(now) != want {
		t.Errorf("ObjectKey = %q, want %q", ObjectKey(now), want)
	}
}
