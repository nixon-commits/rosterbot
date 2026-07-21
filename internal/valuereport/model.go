// Package valuereport builds the view model for value.json — the multi-team
// time series of aggregate HKB dynasty value — from the Team Value Store
// (internal/teamvalue). It mirrors internal/report (which aggregates the
// Analysis Store into the projection-accuracy model): pure aggregation, no I/O.
package valuereport

import (
	"sort"

	"github.com/nixon-commits/rosterbot/internal/teamvalue"
)

// Model is the value.json payload consumed by the dashboard SPA. The four
// value leaves are shipped per point so the page derives every metric
// (Total / MLB / Minors / Hitter / Pitcher) client-side without a server
// round-trip.
type Model struct {
	Empty     bool        `json:"empty"`
	Dates     []string    `json:"dates"`      // sorted unique dt, chronological
	FirstDate string      `json:"first_date"` // earliest captured day
	LastDate  string      `json:"last_date"`  // latest captured day
	Teams     []TeamMeta  `json:"teams"`
	Series    []SeriesRow `json:"series"`
	Latest    []LatestRow `json:"latest"` // most recent day, total-descending
}

// TeamMeta identifies a team and its stable chart color.
type TeamMeta struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Logo  string `json:"logo"`
	Color string `json:"color"`
}

// SeriesRow is one (team, day) point carrying the four value leaves; the page
// derives the selected metric client-side.
type SeriesRow struct {
	TeamID        string `json:"team"`
	Dt            string `json:"dt"`
	HitterMLB     int    `json:"h_mlb"`
	HitterMinors  int    `json:"h_min"`
	PitcherMLB    int    `json:"p_mlb"`
	PitcherMinors int    `json:"p_min"`
}

// LatestRow is a team's most-recent-day snapshot for the standings table.
type LatestRow struct {
	TeamID        string `json:"team"`
	Name          string `json:"name"`
	Logo          string `json:"logo"`
	Color         string `json:"color"`
	Total         int    `json:"total"`
	MLB           int    `json:"mlb"`
	Minors        int    `json:"minors"`
	Hitter        int    `json:"hitter"`
	Pitcher       int    `json:"pitcher"`
	MatchedCount  int    `json:"matched"`
	RosteredCount int    `json:"rostered"`
}

// palette is a categorical color set (stable, colorblind-conscious ordering).
var palette = []string{
	"#4e79a7", "#f28e2b", "#59a14f", "#e15759", "#76b7b2", "#edc948",
	"#b07aa1", "#ff9da7", "#9c755f", "#bab0ac", "#86bcb6", "#d37295",
	"#a0cbe8", "#ffbe7d",
}

// BuildModel transforms the durable rows into the page view model. Teams are
// colored deterministically by sorted TeamID so a team keeps its color across
// renders. An empty store yields Empty=true (the page shows a collecting-data note).
func BuildModel(rows []teamvalue.Row) *Model {
	if len(rows) == 0 {
		return &Model{Empty: true}
	}

	dateSet := map[string]bool{}
	teamName := map[string]string{}
	teamLogo := map[string]string{}
	for _, r := range rows {
		dateSet[r.Dt] = true
		// Last write for a team wins its name/logo (most recent partition).
		teamName[r.TeamID] = r.TeamName
		teamLogo[r.TeamID] = r.LogoURL
	}

	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	teamIDs := make([]string, 0, len(teamName))
	for id := range teamName {
		teamIDs = append(teamIDs, id)
	}
	sort.Strings(teamIDs)

	teams := make([]TeamMeta, len(teamIDs))
	colorOf := map[string]string{}
	for i, id := range teamIDs {
		c := palette[i%len(palette)]
		colorOf[id] = c
		teams[i] = TeamMeta{ID: id, Name: teamName[id], Logo: teamLogo[id], Color: c}
	}

	series := make([]SeriesRow, len(rows))
	for i, r := range rows {
		series[i] = SeriesRow{
			TeamID: r.TeamID, Dt: r.Dt,
			HitterMLB: r.HitterMLBValue, HitterMinors: r.HitterMinorsValue,
			PitcherMLB: r.PitcherMLBValue, PitcherMinors: r.PitcherMinorsValue,
		}
	}

	last := dates[len(dates)-1]
	var latest []LatestRow
	for _, r := range rows {
		if r.Dt != last {
			continue
		}
		latest = append(latest, LatestRow{
			TeamID: r.TeamID, Name: r.TeamName, Logo: r.LogoURL, Color: colorOf[r.TeamID],
			Total: r.TotalValue(), MLB: r.MLBValue(), Minors: r.MinorsValue(),
			Hitter: r.HitterValue(), Pitcher: r.PitcherValue(),
			MatchedCount: r.MatchedCount, RosteredCount: r.RosteredCount,
		})
	}
	sort.Slice(latest, func(i, j int) bool {
		if latest[i].Total != latest[j].Total {
			return latest[i].Total > latest[j].Total
		}
		return latest[i].TeamID < latest[j].TeamID
	})

	return &Model{
		Dates: dates, FirstDate: dates[0], LastDate: last,
		Teams: teams, Series: series, Latest: latest,
	}
}
