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

// Baseline re-measured 2026-08-11 over the same 25-group corpus
// (.cache/fantrax-all-trades-epsb8xzlmj203yrx.json) after rosterbot-uc3
// (draft-pick identity recovery) shipped, by an independent Python
// implementation -- its own name normalization, its own pick-tier
// averaging, its own decay loop, its own comparison -- which reproduced
// these four numbers exactly. The prior baseline (2026-08-10: same 25
// groups, 15/1/9) predates uc3: 5 of the 9 incomplete groups carried a
// still-upcoming (2027) pick that NewDraftPickAsset can now price, so they
// moved into favors (+3) or too-close (+2); the other 4 carried a pick from
// a draft that has already happened, which HKB no longer lists as an
// upcoming asset, so they stay incomplete. Re-deriving this independently,
// rather than pasting in whatever the Go printed, is the whole discipline
// of rosterbot-aei.
//
// The corpus is a live cache file and grows whenever a trade happens, so these
// counts are asserted only when the group count still matches -- same corpus
// must give the same answer. On a changed corpus they are reported and the
// corpus-independent invariants below carry the gate instead, because a check
// that goes red on every league trade is one you learn to bump without reading.
const (
	wantFavors     = 18 // 72%
	wantIncomplete = 4  // 16%, every one of them a draft pick from an already-resolved draft
	wantTooClose   = 3  // 0v0fkidsmskf32gg (pre-existing) plus two newly priced by uc3's pick
	// averaging, both flipping leader between raw and decayed once the pick
	// entered the sum: 2bm0369cmsmd4m4u (raw favors Houston Swang and Bang
	// 4033-3799, decayed favors Intentional Balk 3799-3778) and
	// hdh6we5pmsi3229b (raw favors Yordan's Schlong 4222-4205, decayed
	// favors Intentional Balk 3829.4-3492.52).
	wantGroups = 25
)

type cachedTrades struct {
	Data []struct {
		PlayerName            string `json:"playerName"`
		PlayerPosition        string `json:"playerPosition"`
		ToTeamName            string `json:"toTeamName"`
		TradeGroupID          string `json:"tradeGroupId"`
		IsDraftPick           bool   `json:"isDraftPick"`
		DraftPickYear         int    `json:"draftPickYear"`
		DraftPickRound        int    `json:"draftPickRound"`
		DraftPickNumber       int    `json:"draftPickNumber"`
		DraftPickOriginalTeam string `json:"draftPickOriginalTeam"`
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
		var asset Asset
		if r.IsDraftPick {
			asset = NewDraftPickAsset(r.DraftPickYear, r.DraftPickRound, r.DraftPickNumber, r.DraftPickOriginalTeam, players.Data)
		} else {
			asset = NewAsset(r.PlayerName, r.PlayerPosition, lookup)
		}
		g[r.ToTeamName] = append(g[r.ToTeamName], asset)
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

	// rosterbot-uc3 recovers identity and, for a still-upcoming draft, a
	// price for a traded pick -- but a pick from an already-resolved draft
	// stays permanently unpriced, since HKB stops listing it once that
	// draft is no longer upcoming. So every remaining incomplete verdict on
	// this corpus should still be pick-blocked; if it ever reads zero while
	// incomplete verdicts exist, pick detection itself has broken and
	// unpriced assets are silently scoring zero.
	if counts[StatusIncomplete] > 0 && pickBlocked == 0 {
		t.Errorf("%d incomplete verdicts and not one is pick-blocked — pick detection may have broken",
			counts[StatusIncomplete])
	}
}
