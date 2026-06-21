package claims

import "testing"

func TestToWireResult(t *testing.T) {
	led := Ledger{
		Date: "2026-06-20",
		Entries: []LedgerEntry{
			{Team: "Team A", ClaimType: "FA", NetValue: 3,
				Added:   LedgerPlayer{Name: "New SS", Pos: "SS", Signal: "HOT"},
				Dropped: &LedgerPlayer{Name: "Old SS", Pos: "SS"}},
			{Team: "Team B", ClaimType: "WW", NetValue: -1,
				Added: LedgerPlayer{Name: "Reliever", Pos: "RP"}},
		},
	}
	out := toWireResult(led)
	if len(out.Claims) != 2 {
		t.Fatalf("count: %+v", out)
	}
	if out.Claims[0].Added != "New SS" || out.Claims[0].Dropped != "Old SS" || out.Claims[0].Signal != "HOT" || out.Claims[0].ClaimType != "FA" {
		t.Fatalf("claim0: %+v", out.Claims[0])
	}
	if out.Claims[1].Dropped != "" || out.Claims[1].NetValue != -1 {
		t.Fatalf("claim1: %+v", out.Claims[1])
	}
}
