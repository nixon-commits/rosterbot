package lineupapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/projections"
)

// The dynasty enrichment reaches both roles through the same normalized-name
// join, and a player HKB has no row for keeps the fields absent rather than
// zero — a client that read 0 as an age or a value would be reading a miss as a
// measurement.
func TestBuildAttachesHKBEnrichment(t *testing.T) {
	in := fakeInputs()
	in.HKB = map[string]Dynasty{
		projections.NormalizeName("Adley Rutschman"): {Age: 27.4, Value: 5210},
		projections.NormalizeName("Corbin Burnes"):   {Age: 31.1, Value: 3480},
		// "Vlad Guerrero" and "Bench Guy" are deliberately absent.
	}

	resp := Build(in)
	byName := map[string]*Player{}
	for _, s := range resp.Slots {
		if s.Player != nil {
			byName[s.Player.Name] = s.Player
		}
	}

	if got := byName["Adley Rutschman"]; got.Age != 27.4 || got.HKBValue != 5210 {
		t.Errorf("hitter enrichment = age %v value %d, want 27.4 / 5210", got.Age, got.HKBValue)
	}
	if got := byName["Corbin Burnes"]; got.Age != 31.1 || got.HKBValue != 3480 {
		t.Errorf("pitcher enrichment = age %v value %d, want 31.1 / 3480", got.Age, got.HKBValue)
	}
	if got := byName["Vlad Guerrero"]; got.Age != 0 || got.HKBValue != 0 {
		t.Errorf("unmatched player = age %v value %d, want both zero", got.Age, got.HKBValue)
	}

	// omitempty is what makes "unknown" distinguishable from a real reading on
	// the wire: the unmatched player carries no age/hkb_value keys at all.
	data, err := Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Slots []struct {
			Player map[string]json.RawMessage `json:"player"`
		} `json:"slots"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, s := range decoded.Slots {
		name := strings.Trim(string(s.Player["name"]), `"`)
		_, hasAge := s.Player["age"]
		_, hasValue := s.Player["hkb_value"]
		want := name == "Adley Rutschman" || name == "Corbin Burnes"
		if hasAge != want || hasValue != want {
			t.Errorf("%s: age key present=%v, hkb_value present=%v, want both %v", name, hasAge, hasValue, want)
		}
	}
}

// A nil map is the soft-fail path (HKB scrape failed, or the run never loaded
// it) and must produce the same bytes the pre-enrichment contract pinned.
func TestBuildWithoutHKBMatchesBaseContract(t *testing.T) {
	in := fakeInputs()
	in.HKB = nil
	got, err := Marshal(Build(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != wantJSON {
		t.Fatalf("contract mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantJSON)
	}
}
