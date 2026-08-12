package lineupapi

import (
	"strings"
	"testing"
)

// The date/daterange types exist because the previous shape regex accepted
// values the CLI then rejected — several minutes into an already-launched ECS
// task, where the only evidence is a FAILED run in the ledger. These pin the
// cases a regex cannot catch.
func TestDateParams_RejectWhatAShapeRegexAccepts(t *testing.T) {
	cases := []struct {
		name    string
		job     string
		params  map[string]string
		wantErr string // substring; empty means the call must succeed
	}{
		{
			name: "impossible day of month",
			job:  "backtest", params: map[string]string{"dates": "2026-02-31"},
			wantErr: "not a valid date",
		},
		{
			name: "month 13",
			job:  "backtest", params: map[string]string{"dates": "2026-13-01"},
			wantErr: "not a valid date",
		},
		{
			name: "range running backwards",
			job:  "backtest", params: map[string]string{"dates": "2026-04-19:2026-04-13"},
			wantErr: "ends",
		},
		{
			name: "invalid end of an otherwise fine range",
			job:  "backtest", params: map[string]string{"dates": "2026-04-13:2026-04-99"},
			wantErr: "not a valid date",
		},
		{
			name: "single valid day",
			job:  "backtest", params: map[string]string{"dates": "2026-04-13"},
		},
		{
			name: "valid range",
			job:  "backtest", params: map[string]string{"dates": "2026-04-13:2026-04-19"},
		},
		{
			name: "equal start and end is a legal one-day range",
			job:  "backtest", params: map[string]string{"dates": "2026-04-13:2026-04-13"},
		},
		{
			name: "leap day in a leap year",
			job:  "backtest", params: map[string]string{"dates": "2028-02-29"},
		},
		{
			name: "leap day in a non-leap year",
			job:  "backtest", params: map[string]string{"dates": "2026-02-29"},
			wantErr: "not a valid date",
		},
		{
			// date (single) must not quietly accept a range: team-values writes
			// one partition, so a range here would silently use only the part
			// before the colon.
			name: "range supplied to a single-date param",
			job:  "team-values", params: map[string]string{"date": "2026-04-13:2026-04-19"},
			wantErr: "not a valid date",
		},
		{
			name: "single date for team-values",
			job:  "team-values", params: map[string]string{"date": "2026-04-13"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := BuildJobArgs(tc.job, tc.params)
			if !ok {
				t.Fatalf("job %q is not in the allowlist", tc.job)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// The optimize job validates its custom range through its own build closure
// rather than through validateParam, so it needs its own check.
func TestOptimizeCustomPeriod_ValidatesTheDate(t *testing.T) {
	_, _, err := BuildJobArgs("optimize", map[string]string{"period": "custom", "dates": "2026-02-31"})
	if err == nil || !strings.Contains(err.Error(), "not a valid date") {
		t.Fatalf("want an invalid-date error, got %v", err)
	}

	args, _, err := BuildJobArgs("optimize", map[string]string{"period": "custom", "dates": "2026-04-13:2026-04-19"})
	if err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "--dates 2026-04-13:2026-04-19") {
		t.Fatalf("args = %q, missing the date range", got)
	}
}

// Every allowlisted job must produce runnable argv from an empty form — that is
// what the Run button does when you touch nothing. A job whose base or build is
// wrong fails here rather than as a FAILED ECS task minutes later.
func TestEveryJob_BuildsFromAnEmptyForm(t *testing.T) {
	for name := range jobSpecs {
		t.Run(name, func(t *testing.T) {
			args, ok, err := BuildJobArgs(name, map[string]string{})
			if !ok {
				t.Fatalf("job %q not found", name)
			}
			if err != nil {
				t.Fatalf("empty form rejected: %v", err)
			}
			if len(args) == 0 {
				t.Fatal("built empty argv")
			}
			if args[0] != strings.SplitN(name, " ", 2)[0] {
				t.Errorf("argv starts with %q, want the command %q", args[0], name)
			}
		})
	}
}
