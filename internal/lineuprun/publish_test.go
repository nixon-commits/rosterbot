package lineuprun

import (
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

func TestPublishLineupWritesTodayAndDateKeys(t *testing.T) {
	pub := &memPub{}
	dr := dateResult{
		date:         time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
		benchedToday: map[string]bool{},
	}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}

	if err := publishLineup(dr, cfg, nil, nil, pub); err != nil {
		t.Fatalf("publishLineup: %v", err)
	}

	if _, ok := pub.m[lineupapi.TodayKey]; !ok {
		t.Errorf("missing %q key; got keys %v", lineupapi.TodayKey, keysOf(pub.m))
	}
	if _, ok := pub.m["2026-07-24"]; !ok {
		t.Errorf("missing date key; got keys %v", keysOf(pub.m))
	}
}

func TestPublishLineupNilPublisherIsNoOp(t *testing.T) {
	dr := dateResult{date: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC), benchedToday: map[string]bool{}}
	cfg := &config.Config{LeagueID: "L1", TeamID: "T1"}
	if err := publishLineup(dr, cfg, nil, nil, nil); err != nil {
		t.Fatalf("nil publisher should be a no-op, got: %v", err)
	}
}

func keysOf(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
