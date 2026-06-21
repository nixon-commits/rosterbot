package gscheck

import "testing"

func TestToWireResult(t *testing.T) {
	vs := []Violation{
		{TeamName: "Over Team", GSUsed: 7, Kind: ViolationMax},
		{TeamName: "Under Team", GSUsed: 2, Kind: ViolationMin},
	}
	out := toWireResult(vs, "Week 11", 5, 3)
	if out.Period != "Week 11" || len(out.Violations) != 2 {
		t.Fatalf("out: %+v", out)
	}
	if out.Violations[0].Kind != "over" || out.Violations[0].Limit != 5 || out.Violations[0].OverBy != 2 {
		t.Fatalf("v0: %+v", out.Violations[0])
	}
	if out.Violations[1].Kind != "under" || out.Violations[1].Limit != 3 || out.Violations[1].OverBy != 0 {
		t.Fatalf("v1: %+v", out.Violations[1])
	}
}
