//go:build diag

// Diagnostic harness for the paired system comparison (rosterbot-kb4). Never
// runs in CI — it needs a real Analysis Store on disk.
//
//	aws s3 sync s3://<state-bucket>/analysis/grades/user=<uid>/ /tmp/grades/
//	DIAG_GRADES=/tmp/grades go test -tags diag -run TestDiagCompare -v ./internal/report/
//
// Point DIAG_GRADES at the TENANT-scoped prefix. layout.Analysis is PerTenant,
// so the deployed reader is rooted at analysis/grades/user=<uid>/; syncing the
// whole analysis/grades/ prefix also pulls the pre-migration copies the
// rosterbot-crq.11 backfill left behind and double-counts every row.
package report

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func TestDiagCompare(t *testing.T) {
	root := os.Getenv("DIAG_GRADES")
	if root == "" {
		t.Skip("set DIAG_GRADES to a local copy of the tenant's analysis/grades/ prefix")
	}
	rows, err := analysis.NewFileReader(root).ReadAll()
	if err != nil {
		t.Fatalf("read grades: %v", err)
	}

	var legacy int
	latest := ""
	for _, r := range rows {
		if r.Legacy {
			legacy++
		}
		if r.Dt > latest {
			latest = r.Dt
		}
	}
	t.Logf("rows=%d legacy=%d latest=%s", len(rows), legacy, latest)

	end, err := time.Parse("2006-01-02", latest)
	if err != nil {
		t.Fatalf("parse latest: %v", err)
	}
	m := Aggregate(rows, end, time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC))

	// DIAG_MODEL_OUT writes the real payload the dashboard fetches, so the JS
	// can be rendered against production shape rather than a hand-built stub.
	if out := os.Getenv("DIAG_MODEL_OUT"); out != "" {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal model: %v", err)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatalf("write model: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", out, len(b))
	}

	for _, role := range []string{"hitters", "pitchers"} {
		t.Logf("--- %s (season) ---", role)
		for _, s := range m.Compare[viewKey(0, role)] {
			rho := "        n/a"
			days := 0
			if s.Rho != nil {
				rho = formatRho(s.Rho)
				days = s.Rho.Days
			}
			t.Logf("  %-18s %s  %2dd  paired n=%-5d own n=%-5d MAE %.4f  best=%v",
				s.System, rho, days, s.N, s.TotalN, s.MAE, s.Best)
		}
	}
}

func formatRho(r *RhoStat) string {
	sign := "+"
	if r.Rho < 0 {
		sign = "-"
	}
	v := r.Rho
	if v < 0 {
		v = -v
	}
	return sign + fmtFixed(v) + " se " + fmtFixed(r.SE)
}

func fmtFixed(f float64) string {
	const digits = 4
	scale := 1.0
	for i := 0; i < digits; i++ {
		scale *= 10
	}
	n := int64(f*scale + 0.5)
	out := make([]byte, 0, 8)
	whole := n / int64(scale)
	frac := n % int64(scale)
	out = append(out, byte('0'+whole))
	out = append(out, '.')
	div := int64(scale) / 10
	for div > 0 {
		out = append(out, byte('0'+(frac/div)%10))
		div /= 10
	}
	return string(out)
}
