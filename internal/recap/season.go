package recap

import (
	"fmt"
	"io"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
)

// SeasonPage is everything the year-end page renders. It is built by
// BuildSeasonPage from Fantrax's standings, the playoff bracket and the
// season awards, and rendered by RenderSeason — both pure, so the page renders
// from fixtures without credentials like every other recap render.
type SeasonPage struct {
	Season      string
	ThroughWeek int
	GeneratedAt time.Time
	Standings   []StandingLine
	Bracket     []BracketRound
	// Champion is nil until the final is decided; ChampionLabel then reads TBD.
	Champion      *BracketSlot
	ChampionLabel string
	Awards        *SeasonAwards
	LogoURLs      map[string]string
}

// StandingLine is one row of the final standings, as Fantrax shows it.
type StandingLine struct {
	Rank          int
	TeamID        string
	TeamName      string
	Wins          int
	Losses        int
	Ties          int
	WinPct        float64
	GamesBack     float64
	PointsFor     float64
	PointsAgainst float64
	Streak        string
}

// BracketRound is one round of the playoff bracket.
type BracketRound struct {
	Number   int
	Caption  string
	Period   int
	Start    time.Time
	End      time.Time
	Matchups []BracketMatchup
}

// BracketMatchup is one pairing; WinnerID is set only for a decided game.
type BracketMatchup struct {
	Home, Away           BracketSlot
	HomeScore, AwayScore float64
	Scored               bool
	WinnerID             string
}

// BracketSlot is one side of a pairing: a team, a bye, or an undecided seed.
// Kind is one of "team", "bye", "seed"; Label is what the page prints.
type BracketSlot struct {
	Kind   string
	TeamID string
	Name   string
	Label  string
}

func bracketSlot(s auth_client.PlayoffSlot) BracketSlot {
	switch s.Kind {
	case auth_client.PlayoffSlotBye:
		return BracketSlot{Kind: "bye", Label: "bye"}
	case auth_client.PlayoffSlotSeed:
		return BracketSlot{Kind: "seed", Label: fmt.Sprintf("Seed %d", s.Seed)}
	default:
		return BracketSlot{Kind: "team", TeamID: s.TeamID, Name: s.TeamName, Label: s.TeamName}
	}
}

// BuildSeasonPage assembles the year-end page. Standings are taken as Fantrax
// gives them, never recomputed. Byes and undrawn seeds stay what they are, and
// the champion is a team only once the bracket names one.
func BuildSeasonPage(season string, standings []fantrax.StandingRow, b *auth_client.PlayoffBracket, awards *SeasonAwards, logos map[string]string, generatedAt time.Time) *SeasonPage {
	p := &SeasonPage{Season: season, Awards: awards, LogoURLs: logos, GeneratedAt: generatedAt, ChampionLabel: "TBD"}
	if awards != nil {
		p.ThroughWeek = awards.ThroughWeek
	}
	for _, r := range standings {
		p.Standings = append(p.Standings, StandingLine{Rank: r.Rank, TeamID: r.TeamID, TeamName: r.TeamName, Wins: r.Wins, Losses: r.Losses, Ties: r.Ties,
			WinPct: r.WinPct, GamesBack: r.GamesBack, PointsFor: r.PointsFor, PointsAgainst: r.PointsAgainst, Streak: r.Streak})
	}
	if b == nil {
		return p
	}
	for _, r := range b.Rounds {
		round := BracketRound{Number: r.Number, Caption: r.Caption, Period: r.ScoringPeriod, Start: r.StartDate, End: r.EndDate}
		for _, m := range r.Matchups {
			bm := BracketMatchup{Home: bracketSlot(m.Home), Away: bracketSlot(m.Away), HomeScore: m.HomeScore, AwayScore: m.AwayScore, Scored: m.Scored}
			if m.Scored && bm.Home.Kind == "team" && bm.Away.Kind == "team" {
				switch {
				case m.HomeScore > m.AwayScore:
					bm.WinnerID = bm.Home.TeamID
				case m.AwayScore > m.HomeScore:
					bm.WinnerID = bm.Away.TeamID
				}
			}
			round.Matchups = append(round.Matchups, bm)
		}
		p.Bracket = append(p.Bracket, round)
	}
	if b.Champion != nil && b.Champion.Kind == auth_client.PlayoffSlotTeam {
		c := bracketSlot(*b.Champion)
		p.Champion = &c
		p.ChampionLabel = c.Name
	}
	return p
}

// seasonInput is what the season template receives.
type seasonInput struct {
	*SeasonPage
	Nav []WeekLink
}

// RenderSeason writes the year-end page.
func RenderSeason(w io.Writer, p *SeasonPage, nav []WeekLink) error {
	if p == nil {
		return fmt.Errorf("nil season page")
	}
	return tmpl.ExecuteTemplate(w, "season", seasonInput{SeasonPage: p, Nav: nav})
}

// seasonFilename is where the year-end page lives beside the week pages.
const seasonFilename = "season.html"
