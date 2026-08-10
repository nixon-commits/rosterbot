//go:build corpus

// Run with: go test -tags corpus ./internal/tradevalue/ -v -run Corpus
//
// This is the reproduction gate for rosterbot-gsy, kept behind a build tag
// for the same reason internal/s3blob's live test is: it reads real cached
// data from .cache/ that CI does not have.
//
// It exists because of the rosterbot-hx5 lesson -- a metric that can only
// emit one value is not a measurement. Evaluate has three branches, and the
// question this answers is not "is the code correct" (the unit tests cover
// that) but "on real trades, does this statistic ever actually vary, and how
// often does each branch fire".
//
// The baseline below was produced during planning by a hand-written Python
// reimplementation, deliberately NOT by calling this package: per
// rosterbot-aei, verifying a gate by running the code under test agrees with
// itself and teaches nothing.
package tradevalue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

// Baseline measured 2026-08-06 over .cache/fantrax-all-trades-epsb8xzlmj203yrx.json
// (48 rows / 16 trade groups) by an independent Python implementation.
const (
	wantFavors     = 9 // 56%
	wantIncomplete = 7 // 44%, every one of them a draft pick
	wantTooClose   = 0
	wantGroups     = 16
)

type cachedTrades struct {
	Data []struct {
		PlayerName     string `json:"playerName"`
		PlayerPosition string `json:"playerPosition"`
		ToTeamName     string `json:"toTeamName"`
		TradeGroupID   string `json:"tradeGroupId"`
	} `json:"data"`
}

type cachedHKB struct {
	Data []hkb.Player `json:"data"`
}

func loadJSON[T any](t *testing.T, pattern string) T {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		t.Skipf("no cache file matching %s; run the bot once to populate .cache/", pattern)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("parse %s: %v", matches[0], err)
	}
	return v
}

func TestCorpus_VerdictDistributionOverRealTrades(t *testing.T) {
	trades := loadJSON[cachedTrades](t, "../../.cache/fantrax-all-trades-*.json")
	players := loadJSON[cachedHKB](t, "../../.cache/hkb-players.json")
	lookup := hkb.BuildLookup(players.Data)

	// Group rows into trades, then into sides by receiving team.
	groups := map[string]map[string][]Asset{}
	for _, r := range trades.Data {
		g, ok := groups[r.TradeGroupID]
		if !ok {
			g = map[string][]Asset{}
			groups[r.TradeGroupID] = g
		}
		g[r.ToTeamName] = append(g[r.ToTeamName], NewAsset(r.PlayerName, r.PlayerPosition, lookup))
	}

	counts := map[Status]int{}
	pickBlocked, nameMissBlocked := 0, 0
	for _, g := range groups {
		sides := make([]Side, 0, len(g))
		for team, assets := range g {
			sides = append(sides, Side{Team: team, Assets: assets})
		}
		sort.Slice(sides, func(i, j int) bool { return sides[i].Team < sides[j].Team })

		v := Evaluate(sides)
		counts[v.Status]++
		if v.Status == StatusIncomplete {
			hasPick := false
			for _, s := range sides {
				for _, a := range s.Assets {
					if a.IsPick {
						hasPick = true
					}
				}
			}
			if hasPick {
				pickBlocked++
			} else {
				nameMissBlocked++
			}
		}
	}

	t.Logf("trade groups:        %d", len(groups))
	t.Logf("  verdict fires:     %d", counts[StatusFavors])
	t.Logf("  too close to call: %d", counts[StatusTooClose])
	t.Logf("  incomplete:        %d  (%d pick-blocked, %d HKB name-miss)",
		counts[StatusIncomplete], pickBlocked, nameMissBlocked)

	if len(groups) != wantGroups {
		t.Errorf("trade groups = %d, want %d (corpus changed; re-derive the baseline independently before editing it)", len(groups), wantGroups)
	}
	if counts[StatusFavors] != wantFavors {
		t.Errorf("verdict fires = %d, want %d", counts[StatusFavors], wantFavors)
	}
	if counts[StatusIncomplete] != wantIncomplete {
		t.Errorf("incomplete = %d, want %d", counts[StatusIncomplete], wantIncomplete)
	}
	if counts[StatusTooClose] != wantTooClose {
		t.Errorf("too-close = %d, want %d", counts[StatusTooClose], wantTooClose)
	}
	if nameMissBlocked != 0 {
		t.Errorf("HKB name-miss blocked %d trades, want 0 (the join was 38/38 at baseline)", nameMissBlocked)
	}

	// The point of the gate: the statistic must be able to take more than
	// one value on this data. A verdict that always says the same thing is
	// not a measurement.
	if counts[StatusFavors] == 0 || counts[StatusFavors] == len(groups) {
		t.Errorf("verdict is degenerate: %d/%d groups fire, so it conveys no information",
			counts[StatusFavors], len(groups))
	}
}
