package lineupapi

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
)

// Role exists so a client can scale Proj per role: on the 2026-08-15 lineup
// hitters spanned 2.92-5.48 and pitchers 2.85-19.89, so one shared scale puts
// every hitter in the bottom sixth of it.
func TestBuildSetsRoleOnBothSidesIncludingBench(t *testing.T) {
	resp := Build(fakeInputs())

	want := map[string]string{
		"Adley Rutschman": RoleHitter,
		"Vlad Guerrero":   RoleHitter,
		"Bench Guy":       RoleHitter, // reserve: still a hitter
		"Corbin Burnes":   RolePitcher,
	}
	seen := 0
	for _, s := range resp.Slots {
		if s.Player == nil {
			continue
		}
		w, ok := want[s.Player.Name]
		if !ok {
			t.Fatalf("unexpected player %q", s.Player.Name)
		}
		if s.Player.Role != w {
			t.Errorf("%s: role = %q, want %q", s.Player.Name, s.Player.Role, w)
		}
		seen++
	}
	if seen != len(want) {
		t.Errorf("saw %d players, want %d", seen, len(want))
	}
}

// The whole reason Role is stated rather than inferred: a two-way player is in
// BOTH optimizer lists, and both rows carry his full eligibility. Pos therefore
// cannot tell his hitter row from his pitcher row, and a client inferring the
// role from Pos would shade a ~4-point hitter row on a ~20-point pitcher scale.
//
// Asserting on Pos as well is deliberate — it pins that the two rows really are
// indistinguishable by eligibility, so this test fails if someone "simplifies"
// Role away on the grounds that Pos already carries it.
func TestBuildRoleComesFromTheListNotFromPositions(t *testing.T) {
	in := fakeInputs()
	// Reserve on both sides: fakeInputs defines no UT slot and its one SP slot is
	// already filled, and Build drops an active player with nowhere to sit. The
	// bench path reaches the same two constructors, which is what this asserts.
	twoWay := fantrax.Player{
		ID: "tw1", Name: "Shohei Ohtani", MLBTeam: "LAD",
		Positions:      []string{"014", "015"}, // UT + SP
		RosterPosition: "014", Status: "Reserve",
	}
	in.Hitters = append(in.Hitters, optimizer.ScoredPlayer{
		Player: twoWay, ExpectedPts: 4.2, HasGame: true,
	})
	twoWayP := twoWay
	twoWayP.RosterPosition = "015"
	twoWayP.PosShortNames = "SP"
	in.Pitchers = append(in.Pitchers, optimizer.ScoredPitcher{
		Player: twoWayP, ExpectedPts: 19.8, HasGame: true, IsStarter: true,
	})

	var roles []string
	var rows []*Player
	for _, s := range Build(in).Slots {
		if s.Player != nil && s.Player.Name == "Shohei Ohtani" {
			roles = append(roles, s.Player.Role)
			rows = append(rows, s.Player)
		}
	}

	if len(rows) != 2 {
		t.Fatalf("two-way player produced %d rows, want 2 (one per optimizer list)", len(rows))
	}
	if rows[0].Role == rows[1].Role {
		t.Errorf("both rows have role %q; the two lists must produce different roles", rows[0].Role)
	}
	gotHitter, gotPitcher := false, false
	for i, r := range rows {
		switch r.Role {
		case RoleHitter:
			gotHitter = true
			if r.Proj != 4.2 {
				t.Errorf("hitter row proj = %v, want 4.2", r.Proj)
			}
		case RolePitcher:
			gotPitcher = true
			if r.Proj != 19.8 {
				t.Errorf("pitcher row proj = %v, want 19.8", r.Proj)
			}
		default:
			t.Errorf("row %d: unknown role %q", i, r.Role)
		}
	}
	if !gotHitter || !gotPitcher {
		t.Errorf("roles = %v, want one hitter and one pitcher", roles)
	}

	// Pos is identical on both rows, which is exactly why Role cannot be derived
	// from it.
	if len(rows[0].Pos) != len(rows[1].Pos) {
		t.Fatalf("pos differ in length: %v vs %v", rows[0].Pos, rows[1].Pos)
	}
	for i := range rows[0].Pos {
		if rows[0].Pos[i] != rows[1].Pos[i] {
			t.Fatalf("pos differ: %v vs %v — this test's premise no longer holds", rows[0].Pos, rows[1].Pos)
		}
	}
}
