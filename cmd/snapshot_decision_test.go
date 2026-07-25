package cmd

import "testing"

// resolveWriteSnapshots collapses four inputs into the one decision the engine
// needs. These cases pin the behaviour that used to be spread across three
// Options fields plus an inline os.Getenv inside lineuprun.Run.
func TestResolveWriteSnapshots(t *testing.T) {
	cases := []struct {
		name        string
		dryRun      bool
		snapshot    bool
		archiveFlag bool
		archiveEnv  string
		want        bool
	}{
		// Default-on for real runs so the hourly cron accumulates backtest data.
		{"real run writes by default", false, false, false, "", true},
		{"real run still writes with flags set", false, true, true, "1", true},

		// Dry-run is side-effect-free unless explicitly opted in.
		{"dry-run writes nothing by default", true, false, false, "", false},
		{"dry-run + --snapshot", true, true, false, "", true},

		// Deprecated aliases must keep working from the cmd layer.
		{"dry-run + deprecated --archive-projections", true, false, true, "", true},
		{"dry-run + deprecated BACKTEST_ARCHIVE=1", true, false, false, "1", true},

		// Only the exact value "1" enables the env alias.
		{"BACKTEST_ARCHIVE=0 does not enable", true, false, false, "0", false},
		{"BACKTEST_ARCHIVE=true does not enable", true, false, false, "true", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveWriteSnapshots(tc.dryRun, tc.snapshot, tc.archiveFlag, tc.archiveEnv)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
