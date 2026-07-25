package cmd

// resolveWriteSnapshots decides whether a run writes projection snapshots.
//
// This is the flag layer's job, not the engine's (rosterbot-6rv criterion 5).
// lineuprun.Options used to carry three fields encoding one behaviour —
// SnapshotFlag, ArchiveProjections and ForceSnapshot — and Run additionally
// read os.Getenv("BACKTEST_ARCHIVE") inline, so the deprecated aliases leaked
// all the way into the orchestration engine. Resolving them here leaves
// Options with a single WriteSnapshots bool.
//
// Snapshots are default-on for real runs so the hourly cron accumulates
// backtest data automatically; dry-run stays side-effect-free unless explicitly
// opted in via --snapshot or one of the two deprecated aliases
// (--archive-projections, BACKTEST_ARCHIVE=1).
func resolveWriteSnapshots(dryRun, snapshotFlag, archiveProjections bool, archiveEnv string) bool {
	return !dryRun || snapshotFlag || archiveProjections || archiveEnv == "1"
}
