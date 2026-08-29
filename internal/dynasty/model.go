package dynasty

import (
	"sort"
	"time"
)

// Model is the football.json payload consumed by the dashboard SPA: the most
// recent day's per-team standings only (no historical series -- the store is
// new, and the gate pre-commitment this renders is a same-day snapshot
// question, not a trend). All four StatsGuy formats are shipped per team so
// the page derives the format toggle client-side, matching every other
// dashboard model in this repo (teamvalue/valuereport, Row itself).
type Model struct {
	Empty       bool           `json:"empty"`
	GeneratedAt string         `json:"generatedAt"`
	LatestDate  string         `json:"latestDate"`
	Teams       []TeamStanding `json:"teams"` // sorted by StartersSFDynasty, descending
}

// TeamStanding carries both the Starters Value (the headline) and the
// full-roster total (shown demoted beside it) for one team, per StatsGuy
// format, plus the join-coverage counts that qualify both numbers.
type TeamStanding struct {
	TeamID   string `json:"teamId"`
	TeamName string `json:"teamName"`

	StartersSFDynasty    int `json:"startersSfDynasty"`
	StartersNonSFDynasty int `json:"startersNonSfDynasty"`
	StartersSFRedraft    int `json:"startersSfRedraft"`
	StartersNonSFRedraft int `json:"startersNonSfRedraft"`

	FullRosterSFDynasty    int `json:"fullRosterSfDynasty"`
	FullRosterNonSFDynasty int `json:"fullRosterNonSfDynasty"`
	FullRosterSFRedraft    int `json:"fullRosterSfRedraft"`
	FullRosterNonSFRedraft int `json:"fullRosterNonSfRedraft"`

	RosteredCount int `json:"rosteredCount"`
	MatchedCount  int `json:"matchedCount"`

	// Unmatched names the rostered players behind a MatchedCount shortfall.
	// Empty on a fully-matched team AND on a partition written before
	// Coverage.Unmatched was persisted -- the two are told apart by the counts,
	// never by this field alone.
	Unmatched []string `json:"unmatched,omitempty"`
}

// BuildModel rolls the stored rows + coverage into the standings snapshot for
// the most recent date present. Pure: no I/O.
func BuildModel(rows []Row, coverage []Coverage, generatedAt time.Time) *Model {
	if len(rows) == 0 {
		return &Model{Empty: true}
	}

	latest := ""
	for _, r := range rows {
		if r.Dt > latest {
			latest = r.Dt
		}
	}

	byTeam := map[string]*TeamStanding{}
	order := []string{}
	for _, r := range rows {
		if r.Dt != latest {
			continue
		}
		t := byTeam[r.TeamID]
		if t == nil {
			t = &TeamStanding{TeamID: r.TeamID, TeamName: r.TeamName}
			byTeam[r.TeamID] = t
			order = append(order, r.TeamID)
		}
		t.FullRosterSFDynasty += r.ValueSFDynasty
		t.FullRosterNonSFDynasty += r.ValueNonSFDynasty
		t.FullRosterSFRedraft += r.ValueSFRedraft
		t.FullRosterNonSFRedraft += r.ValueNonSFRedraft
		if r.AssetType == "player" && r.IsStarter {
			t.StartersSFDynasty += r.ValueSFDynasty
			t.StartersNonSFDynasty += r.ValueNonSFDynasty
			t.StartersSFRedraft += r.ValueSFRedraft
			t.StartersNonSFRedraft += r.ValueNonSFRedraft
		}
	}

	for _, c := range coverage {
		if c.Dt != latest {
			continue
		}
		if t, ok := byTeam[c.TeamID]; ok {
			t.RosteredCount = c.RosteredCount
			t.MatchedCount = c.MatchedCount
			t.Unmatched = c.Unmatched
		}
	}

	teams := make([]TeamStanding, 0, len(order))
	for _, id := range order {
		teams = append(teams, *byTeam[id])
	}
	sort.Slice(teams, func(i, j int) bool {
		if teams[i].StartersSFDynasty != teams[j].StartersSFDynasty {
			return teams[i].StartersSFDynasty > teams[j].StartersSFDynasty
		}
		return teams[i].TeamID < teams[j].TeamID
	})

	return &Model{
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		LatestDate:  latest,
		Teams:       teams,
	}
}
