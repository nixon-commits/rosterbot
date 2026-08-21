package cmd

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func devs(ids ...string) []lineupapi.PushDevice {
	out := make([]lineupapi.PushDevice, 0, len(ids))
	for _, id := range ids {
		out = append(out, lineupapi.PushDevice{ID: id})
	}
	return out
}

func TestPrunedDeviceIDs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []lineupapi.PushDevice
		want          []string
	}{
		{"none pruned", devs("a", "b"), devs("a", "b"), nil},
		{"one pruned", devs("a", "b"), devs("a"), []string{"b"}},
		{"all pruned", devs("a"), nil, []string{"a"}},
		// The case a count comparison gets wrong: one device dies while another
		// registers, so both lists have length 2 and only identity reveals it.
		{"pruned while another registers", devs("a", "b"), devs("a", "c"), []string{"b"}},
		{"new registration only", devs("a"), devs("a", "c"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := prunedDeviceIDs(tc.before, tc.after)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
