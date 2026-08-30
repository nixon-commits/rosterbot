package projections

import "testing"

// TestValidBaseSystem pins the stored-preference contract shared by the
// settings API (write-time validation) and the optimize resolver (run-time
// fallback): base family names only. The -ros variants are deliberately
// excluded — an explicit RoS system skips the preseason fallback tier, so a
// stored one would degrade worse than the default when a RoS feed breaks.
func TestValidBaseSystem(t *testing.T) {
	for _, ok := range []string{ProjectionSteamer, ProjectionDepthCharts, ProjectionBatX, ProjectionATC} {
		if !ValidBaseSystem(ok) {
			t.Errorf("ValidBaseSystem(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "zips", ProjectionDepthChartsRoS, "DEPTHCHARTS"} {
		if ValidBaseSystem(bad) {
			t.Errorf("ValidBaseSystem(%q) = true, want false", bad)
		}
	}
}
