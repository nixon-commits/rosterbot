package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
)

const (
	oldRunLedgerPrefix = "runs/"
	newRunLedgerPrefix = "runledger/"
)

var migrateRunLedgerDryRun bool

// migrate-run-ledger is a one-time internal command (rosterbot-432) that
// copies existing run ledger records from the old shared runs/ prefix to
// their own runledger/ prefix, then verifies the copy by re-listing the
// destination and diffing against the source key set. Copying is
// idempotent (the same source bytes overwrite the same destination key), so
// it's safe to rerun.
var migrateRunLedgerCmd = &cobra.Command{
	Use:    "migrate-run-ledger",
	Short:  "Internal: one-time copy of run ledger records from runs/ to runledger/ (rosterbot-432)",
	Hidden: true,
	RunE:   runMigrateRunLedger,
}

func init() {
	migrateRunLedgerCmd.Flags().BoolVar(&migrateRunLedgerDryRun, "dry-run", false, "list and count ledger records without copying")
	rootCmd.AddCommand(migrateRunLedgerCmd)
}

func runMigrateRunLedger(cmd *cobra.Command, args []string) error {
	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		return fmt.Errorf("migrate-run-ledger: STATE_BUCKET must be set")
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(cfg)

	if migrateRunLedgerDryRun {
		srcKeys, err := s3lineup.ListLedgerKeys(ctx, client, bucket, oldRunLedgerPrefix)
		if err != nil {
			return fmt.Errorf("list %s: %w", oldRunLedgerPrefix, err)
		}
		fmt.Printf("dry-run: would migrate %d ledger record(s) from %s to %s\n", len(srcKeys), oldRunLedgerPrefix, newRunLedgerPrefix)
		return nil
	}

	copied, err := s3lineup.MigrateLedgerPrefix(ctx, client, bucket, oldRunLedgerPrefix, newRunLedgerPrefix)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	fmt.Printf("copied %d ledger record(s) from %s to %s\n", len(copied), oldRunLedgerPrefix, newRunLedgerPrefix)

	dstKeys, err := s3lineup.ListLedgerKeys(ctx, client, bucket, newRunLedgerPrefix)
	if err != nil {
		return fmt.Errorf("verify: list %s: %w", newRunLedgerPrefix, err)
	}
	missing := diffLedgerKeySuffixes(copied, oldRunLedgerPrefix, dstKeys, newRunLedgerPrefix)
	if len(missing) > 0 {
		return fmt.Errorf("verify: %d record(s) missing from %s after migration: %v", len(missing), newRunLedgerPrefix, missing)
	}
	fmt.Printf("verify: %d record(s) present under %s, matches source count\n", len(dstKeys), newRunLedgerPrefix)
	return nil
}

// diffLedgerKeySuffixes returns the suffixes (the part after each prefix,
// e.g. "9999999999-abc.json") present in srcKeys but missing from dstKeys.
// An empty result means every source record was found at the destination.
func diffLedgerKeySuffixes(srcKeys []string, srcPrefix string, dstKeys []string, dstPrefix string) []string {
	dstSet := make(map[string]bool, len(dstKeys))
	for _, k := range dstKeys {
		dstSet[strings.TrimPrefix(k, dstPrefix)] = true
	}
	var missing []string
	for _, k := range srcKeys {
		suffix := strings.TrimPrefix(k, srcPrefix)
		if !dstSet[suffix] {
			missing = append(missing, suffix)
		}
	}
	return missing
}
