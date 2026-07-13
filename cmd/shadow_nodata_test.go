package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDescribeNoDataTransition(t *testing.T) {
	tests := []struct {
		name string
		prev systemNoData
		cur  systemNoData
		want string
	}{
		{
			name: "no change, healthy",
			prev: systemNoData{},
			cur:  systemNoData{},
			want: "",
		},
		{
			name: "no change, still down",
			prev: systemNoData{Hitters: true, Pitchers: true},
			cur:  systemNoData{Hitters: true, Pitchers: true},
			want: "",
		},
		{
			name: "hitters newly down",
			prev: systemNoData{},
			cur:  systemNoData{Hitters: true},
			want: "atc-ros: batting projections now unavailable (upstream outage?)\n",
		},
		{
			name: "pitchers recovered",
			prev: systemNoData{Pitchers: true},
			cur:  systemNoData{},
			want: "atc-ros: pitching projections recovered\n",
		},
		{
			name: "both flip in opposite directions",
			prev: systemNoData{Hitters: true},
			cur:  systemNoData{Pitchers: true},
			want: "atc-ros: batting projections recovered\natc-ros: pitching projections now unavailable (upstream outage?)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeNoDataTransition("atc-ros", tt.prev, tt.cur)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShadowNoDataState_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow-nodata-state.json")

	// Missing file behaves like empty state.
	state := loadShadowNoDataState(path)
	if len(state) != 0 {
		t.Fatalf("expected empty state for missing file, got %v", state)
	}

	state["atc-ros"] = systemNoData{Hitters: true, Pitchers: true}
	state["steamer-ros"] = systemNoData{}
	if err := saveShadowNoDataState(path, state); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	reloaded := loadShadowNoDataState(path)
	if reloaded["atc-ros"] != (systemNoData{Hitters: true, Pitchers: true}) {
		t.Errorf("expected atc-ros state to round-trip, got %+v", reloaded["atc-ros"])
	}
	if reloaded["steamer-ros"] != (systemNoData{}) {
		t.Errorf("expected steamer-ros state to round-trip as healthy, got %+v", reloaded["steamer-ros"])
	}
}

func TestShadowNoDataState_CorruptFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shadow-nodata-state.json")
	if err := saveShadowNoDataState(path, map[string]systemNoData{}); err != nil {
		t.Fatalf("setup save failed: %v", err)
	}
	// Overwrite with garbage.
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("corrupt write failed: %v", err)
	}
	state := loadShadowNoDataState(path)
	if len(state) != 0 {
		t.Errorf("expected empty state for corrupt file, got %v", state)
	}
}
