package lineuprun

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// memPub is an in-memory lineupapi.Publisher capturing published payloads.
type memPub struct{ m map[string][]byte }

func (p *memPub) Publish(key string, data []byte) error {
	if p.m == nil {
		p.m = map[string][]byte{}
	}
	p.m[key] = data
	return nil
}

// stamp is a fixed publish time, so generated_at assertions do not race a clock.
var stamp = time.Date(2026, 7, 24, 18, 30, 0, 0, time.UTC)

func todayResult() dateResult {
	return dateResult{
		date:         time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
		benchedToday: map[string]bool{},
		isToday:      true,
	}
}

func TestPublishLineupWritesTodayAndDateKeys(t *testing.T) {
	pub := &memPub{}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}

	if err := publishLineup(todayResult(), cfg, nil, nil, nil, pub, stamp, false); err != nil {
		t.Fatalf("publishLineup: %v", err)
	}

	if _, ok := pub.m[lineupapi.TodayKey]; !ok {
		t.Errorf("missing %q key; got keys %v", lineupapi.TodayKey, keysOf(pub.m))
	}
	if _, ok := pub.m["2026-07-24"]; !ok {
		t.Errorf("missing date key; got keys %v", keysOf(pub.m))
	}
	if _, ok := pub.m[lineupapi.PreviewKey]; ok {
		t.Errorf("an applied lineup must not write %q; got keys %v", lineupapi.PreviewKey, keysOf(pub.m))
	}
}

// The safety invariant the preview split exists for: a dry run applied nothing
// to Fantrax, so it must not touch the key that says what IS applied — nor the
// date key, which is the historical record backtesting reads. A regression here
// is silent: the app would render a hypothetical lineup as the live one.
func TestPublishLineupPreviewWritesOnlyPreviewKey(t *testing.T) {
	pub := &memPub{}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1", DryRun: true}

	if err := publishLineup(todayResult(), cfg, nil, nil, nil, pub, stamp, true); err != nil {
		t.Fatalf("publishLineup: %v", err)
	}

	if _, ok := pub.m[lineupapi.PreviewKey]; !ok {
		t.Errorf("missing %q key; got keys %v", lineupapi.PreviewKey, keysOf(pub.m))
	}
	if _, ok := pub.m[lineupapi.TodayKey]; ok {
		t.Errorf("a dry run overwrote the applied-lineup key %q", lineupapi.TodayKey)
	}
	if _, ok := pub.m["2026-07-24"]; ok {
		t.Errorf("a dry run overwrote the historical date key")
	}
	if len(pub.m) != 1 {
		t.Errorf("preview published %d keys, want exactly 1: %v", len(pub.m), keysOf(pub.m))
	}
}

// generated_at is what lets a client order the applied lineup against a
// preview. If it stops being emitted the two blobs become unorderable and the
// client silently falls back to showing the applied one.
func TestPublishLineupStampsGeneratedAt(t *testing.T) {
	pub := &memPub{}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}

	if err := publishLineup(todayResult(), cfg, nil, nil, nil, pub, stamp, false); err != nil {
		t.Fatalf("publishLineup: %v", err)
	}

	var resp lineupapi.LineupResponse
	if err := json.Unmarshal(pub.m[lineupapi.TodayKey], &resp); err != nil {
		t.Fatalf("unmarshal published payload: %v", err)
	}
	got, err := time.Parse(time.RFC3339, resp.GeneratedAt)
	if err != nil {
		t.Fatalf("generated_at = %q, not RFC3339: %v", resp.GeneratedAt, err)
	}
	if !got.Equal(stamp) {
		t.Errorf("generated_at = %s, want %s", got, stamp)
	}
}

func TestPublishLineupNilPublisherIsNoOp(t *testing.T) {
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}
	if err := publishLineup(todayResult(), cfg, nil, nil, nil, nil, stamp, false); err != nil {
		t.Fatalf("nil publisher should be a no-op, got: %v", err)
	}
}

// A nil HKB map is the soft-fail path (the scrape failed, or the run is one
// that never loads it), so publishing must still produce a valid payload
// rather than depending on the enrichment being present.
func TestPublishLineupWithoutHKBMetaStillPublishes(t *testing.T) {
	pub := &memPub{}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}

	if err := publishLineup(todayResult(), cfg, nil, nil, nil, pub, stamp, false); err != nil {
		t.Fatalf("publishLineup: %v", err)
	}
	var resp lineupapi.LineupResponse
	if err := json.Unmarshal(pub.m[lineupapi.TodayKey], &resp); err != nil {
		t.Fatalf("unmarshal published payload: %v", err)
	}
	if resp.Date != "2026-07-24" {
		t.Errorf("date = %q, want 2026-07-24", resp.Date)
	}
}

// publishToday's routing table. The dry-run-without-the-flag row is the one
// that changed: it used to publish nothing at all, which is what made the
// app's Optimize button a no-op in every Debug build (rbapp-c56).
func TestPublishTodayRouting(t *testing.T) {
	cases := []struct {
		name            string
		dryRun          bool
		publishFlag     bool
		wantKey         string
		wantOtherAbsent string
	}{
		{"applied run writes today", false, false, lineupapi.TodayKey, lineupapi.PreviewKey},
		{"dry run writes preview", true, false, lineupapi.PreviewKey, lineupapi.TodayKey},
		{"dry run with --publish-lineup still writes today", true, true, lineupapi.TodayKey, lineupapi.PreviewKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub := &memPub{}
			var out testWriter
			publishToday(EmitInputs{
				Results:       []dateResult{todayResult()},
				Cfg:           &config.Config{LeagueID: "L1", TeamID: "T1", DryRun: tc.dryRun},
				PublishLineup: tc.publishFlag,
				Publisher:     pub,
				Out:           &out,
			})
			if _, ok := pub.m[tc.wantKey]; !ok {
				t.Errorf("missing %q; got keys %v", tc.wantKey, keysOf(pub.m))
			}
			if _, ok := pub.m[tc.wantOtherAbsent]; ok {
				t.Errorf("unexpectedly wrote %q; got keys %v", tc.wantOtherAbsent, keysOf(pub.m))
			}
		})
	}
}

// A run whose results contain no today date publishes nothing at all — a
// backfill for past dates must not overwrite either live key.
func TestPublishTodaySkipsWhenNoTodayResult(t *testing.T) {
	pub := &memPub{}
	var out testWriter
	past := todayResult()
	past.isToday = false
	publishToday(EmitInputs{
		Results:   []dateResult{past},
		Cfg:       &config.Config{LeagueID: "L1", TeamID: "T1"},
		Publisher: pub,
		Out:       &out,
	})
	if len(pub.m) != 0 {
		t.Errorf("published %v, want nothing", keysOf(pub.m))
	}
}

// testWriter swallows publishToday's warning output.
type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

func keysOf(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
