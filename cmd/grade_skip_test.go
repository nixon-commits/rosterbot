package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func gradeDay(date string, players ...fantrax.DayPlayerFP) fantrax.DayRoster {
	d, _ := time.Parse("2006-01-02", date)
	return fantrax.DayRoster{Date: d, Players: players}
}

// The discriminator has to separate three cases that all produce zero graded
// rows: a day with no baseball (mark it), a day whose grading input never
// arrived (leave the gap visible), and a day that graded fine (never mark).
func TestUngradeableDays(t *testing.T) {
	played := fantrax.DayPlayerFP{PlayerID: "1", HadGame: true, FPts: 12.5}
	idle := fantrax.DayPlayerFP{PlayerID: "2"}
	// Scored without HadGame set — the marker must not fire on a day that
	// clearly produced fantasy points.
	scoredOnly := fantrax.DayPlayerFP{PlayerID: "3", FPts: 3.5}

	tests := []struct {
		name   string
		days   []fantrax.DayRoster
		graded map[string]int
		want   []string
	}{
		{
			name: "all-star break: roster present, nobody appeared",
			days: []fantrax.DayRoster{
				gradeDay("2026-07-13", idle, idle),
				gradeDay("2026-07-14", idle, idle),
			},
			want: []string{"2026-07-13", "2026-07-14"},
		},
		{
			name: "a normal day is never marked",
			days: []fantrax.DayRoster{gradeDay("2026-07-17", played, idle)},
			want: nil,
		},
		{
			name: "points without HadGame still count as baseball",
			days: []fantrax.DayRoster{gradeDay("2026-07-17", scoredOnly, idle)},
			want: nil,
		},
		// The 2026-07-01 case. An empty snapshot is a fetch that returned
		// nothing, not evidence that no games were played — marking it would
		// suppress the one real gap this must keep reporting.
		{
			name: "empty snapshot is not evidence of no baseball",
			days: []fantrax.DayRoster{gradeDay("2026-07-01")},
			want: nil,
		},
		{
			name:   "a day that graded rows is never marked",
			days:   []fantrax.DayRoster{gradeDay("2026-07-13", idle)},
			graded: map[string]int{"2026-07-13": 20},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ungradeableDays(tt.days, tt.graded)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d markers %v, want %v", len(got), dts(got), tt.want)
			}
			for i, w := range tt.want {
				if got[i].Dt != w {
					t.Errorf("marker[%d].Dt = %q, want %q", i, got[i].Dt, w)
				}
			}
		})
	}
}

// The marker's body is the evidence for its own claim, so a reader who finds
// the object in S3 can tell "the roster was there and nobody played" from "the
// snapshot was empty" without re-running anything.
func TestUngradeableDays_MarkerCarriesItsEvidence(t *testing.T) {
	idle := fantrax.DayPlayerFP{PlayerID: "2"}
	got := ungradeableDays([]fantrax.DayRoster{gradeDay("2026-07-13", idle, idle, idle)}, nil)
	if len(got) != 1 {
		t.Fatalf("got %d markers, want 1", len(got))
	}
	if got[0].RosterPlayers != 3 {
		t.Errorf("RosterPlayers = %d, want 3", got[0].RosterPlayers)
	}
	if got[0].PlayersWithGames != 0 {
		t.Errorf("PlayersWithGames = %d, want 0", got[0].PlayersWithGames)
	}
	if got[0].Reason == "" {
		t.Error("Reason is empty — the marker has to say why it exists")
	}
	if got[0].WrittenAt.IsZero() {
		t.Error("WrittenAt is zero — the marker has to date itself")
	}
}

func dts(ms []analysis.SkipMarker) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Dt
	}
	return out
}
