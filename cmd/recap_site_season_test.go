package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/recap"
	"github.com/pmurley/go-fantrax/auth_client"
)

// recap-site is year-round, and every boundary is a day it runs: the final
// Sunday, the Monday after it (the only day the final week can ever render),
// the winter that follows, and the pre-season, where it stops on its own
// through recapSiteStop rather than through the calendar.
func TestSeasonGate_RecapSiteIsNeverGated(t *testing.T) {
	win := seasonWindow{Start: gateStart, End: gateEnd, Source: "fantrax"}
	for _, today := range []time.Time{
		gateEnd,                     // the final Sunday
		gateEnd.AddDate(0, 0, 1),    // the Monday that renders the final week
		gateEnd.AddDate(0, 2, 0),    // deep winter: the retrospective is still live
		gateStart.AddDate(0, 0, -1), // pre-season: runs, then stops cleanly on no weeks
	} {
		if gated, line := seasonGate("recap-site", changedNone, today, win, nil, ""); gated {
			t.Errorf("recap-site gated on %s: %q", today.Format("2006-01-02"), line)
		}
	}
}

// The gate boundary and the champion slot are one fact split across two
// packages: the site can only crown a champion on a day the gate lets it run.
// internal/recap's TestBuildSeasonPage covers the TBD/decided split by itself,
// and it passed all through the 2026 freeze — nothing tied it to the day that
// matters. This does, so re-gating recap-site cannot go green here.
func TestRecapSite_CrownsTheChampionOnTheFirstOffSeasonDay(t *testing.T) {
	firstOffSeasonDay := gateEnd.AddDate(0, 0, 1)
	win := seasonWindow{Start: gateStart, End: gateEnd, Source: "fantrax"}
	if gated, line := seasonGate("recap-site", changedNone, firstOffSeasonDay, win, nil, ""); gated {
		t.Fatalf("recap-site gated on the day the final week completes: %q", line)
	}

	team := func(id, name string) auth_client.PlayoffSlot {
		return auth_client.PlayoffSlot{Kind: auth_client.PlayoffSlotTeam, TeamID: id, TeamName: name}
	}
	champ := team("hsb", "Houston Swang and Bang")
	b := &auth_client.PlayoffBracket{
		Rounds: []auth_client.PlayoffRound{{
			Number: 3, Caption: "Final Round", ScoringPeriod: 25,
			StartDate: gateEnd.AddDate(0, 0, -6), EndDate: gateEnd,
			Matchups: []auth_client.PlayoffMatchup{{
				Home: team("pfaadt", "Pfaadt Wood Kings"), Away: champ,
				HomeScore: 580, AwayScore: 597, Scored: true,
			}},
		}},
		Champion: &champ,
	}

	page := recap.BuildSeasonPage("2026", nil, b, nil, nil, firstOffSeasonDay, recap.SeasonExtras{})
	if page.Champion == nil || page.ChampionLabel != "Houston Swang and Bang" {
		t.Fatalf("champion = %+v / %q, want the decided team", page.Champion, page.ChampionLabel)
	}
	var buf bytes.Buffer
	if err := recap.RenderSeason(&buf, page, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "the final round has not been decided") {
		t.Error("the rendered page still carries the TBD champion span")
	}
	if !strings.Contains(out, "Houston Swang and Bang") {
		t.Error("the rendered page does not name the champion")
	}
}
