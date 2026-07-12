package fantrax

import (
	"testing"

	"github.com/pmurley/go-fantrax/models"
)

// TestToPlayer_NormalizesTeamAbbreviation is the regression test for
// rosterbot-i58: before this fix, toPlayer passed rp.TeamShortName through
// raw, while the projection-lookup path separately normalized team
// abbreviations before using them as map keys. The two agreed only because
// upstream happened to return already-canonical abbreviations for all 30
// teams. A synthetic non-canonical abbreviation (mirroring the real OAK->ATH
// rebrand) proves normalization now happens once, at construction, instead
// of depending on upstream never drifting again.
func TestToPlayer_NormalizesTeamAbbreviation(t *testing.T) {
	tests := []struct {
		upstream string
		want     string
	}{
		{"SDP", "SD"},  // real FanGraphs/statsapi vs Fantrax divergence
		{"OAK", "ATH"}, // the exact rebrand that motivated this fix
		{"NYY", "NYY"}, // already-canonical passthrough
	}
	for _, tt := range tests {
		t.Run(tt.upstream, func(t *testing.T) {
			rp := models.RosterPlayer{
				PlayerID:      "p1",
				Name:          "Test Player",
				TeamShortName: tt.upstream,
				PosShortNames: "OF",
				Status:        "Active",
			}
			p := toPlayer(rp)
			if p.MLBTeam != tt.want {
				t.Errorf("toPlayer(TeamShortName=%q).MLBTeam = %q, want %q", tt.upstream, p.MLBTeam, tt.want)
			}
		})
	}
}
