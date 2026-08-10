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

// Baseline re-measured 2026-08-10 over .cache/fantrax-all-trades-epsb8xzlmj203yrx.json
// (25 trade groups) by an independent Python implementation -- its own name
// normalization, its own decay loop, its own comparison -- which reproduced
// these four numbers exactly. The first baseline (2026-08-06: 16 groups, 9/0/7)
// was superseded when the trades cache refreshed and picked up more history;
// re-deriving it independently, rather than pasting in whatever the Go printed,
// is the whole discipline of rosterbot-aei.
//
// The corpus is a live cache file and grows whenever a trade happens, so these
// counts are asserted only when the group count still matches -- same corpus
// must give the same answer. On a changed corpus they are reported and the
// corpus-independent invariants below carry the gate instead, because a check
// that goes red on every league trade is one you learn to bump without reading.
const (
	wantFavors     = 15 // 60%
	wantIncomplete = 9  // 36%, every one of them a draft pick
	wantTooClose   = 1  // 0v0fkidsmskf32gg: raw favors DillonP33, decayed favors Yordan's Schlong
	wantGroups     = 25
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

	// Exact reproduction, but only against the corpus the baseline was taken
	// from. On the same input the shipped code must give the same answer it
	// gave the independent implementation.
	if len(groups) == wantGroups {
		if counts[StatusFavors] != wantFavors {
			t.Errorf("verdict fires = %d, want %d", counts[StatusFavors], wantFavors)
		}
		if counts[StatusIncomplete] != wantIncomplete {
			t.Errorf("incomplete = %d, want %d", counts[StatusIncomplete], wantIncomplete)
		}
		if counts[StatusTooClose] != wantTooClose {
			t.Errorf("too-close = %d, want %d", counts[StatusTooClose], wantTooClose)
		}
	} else {
		t.Logf("corpus has moved (%d groups, baseline %d) — exact counts not asserted. "+
			"If you want them back, re-derive them with an INDEPENDENT implementation over "+
			"the current cache and update the constants; do not paste in what this test just printed.",
			len(groups), wantGroups)
	}

	// Everything below holds on any corpus.

	// The HKB name join is the one input failure that would masquerade as a
	// model result: an unmatched name suppresses the verdict exactly the way a
	// draft pick does, so without this the join could rot to nothing and the
	// distribution would just look pick-heavy. It was 38/38 at first baseline
	// and has never missed since.
	if nameMissBlocked != 0 {
		t.Errorf("HKB name-miss blocked %d trades, want 0 — the cross-source join has regressed", nameMissBlocked)
	}

	// rosterbot-hx5: the statistic must be able to take more than one value on
	// this data. A verdict that always says the same thing is not a
	// measurement, whichever thing it always says.
	if counts[StatusFavors] == 0 || counts[StatusFavors] == len(groups) {
		t.Errorf("verdict is degenerate: %d/%d groups fire, so it conveys no information",
			counts[StatusFavors], len(groups))
	}

	// Pick-blocking is the headline argument for rosterbot-uc3, so it is worth
	// failing on rather than merely logging: if it ever reads zero, either
	// picks became identifiable (finish uc3 and delete this) or the pick
	// detection broke and unpriced assets are silently scoring zero.
	if counts[StatusIncomplete] > 0 && pickBlocked == 0 {
		t.Errorf("%d incomplete verdicts and not one is pick-blocked — pick detection may have broken",
			counts[StatusIncomplete])
	}
}
