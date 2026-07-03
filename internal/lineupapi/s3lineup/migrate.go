package s3lineup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ListLedgerKeys lists every flat ledger key under prefix (skipping per-run
// output sub-keys such as <prefix><id>/output.json), following pagination
// until every match is collected. Used for the run-ledger migration's
// dry-run preview and post-copy verification (cmd/migrate_run_ledger.go).
func ListLedgerKeys(ctx context.Context, client listAPI, bucket, prefix string) ([]string, error) {
	return listFlatKeys(ctx, client, bucket, prefix, 0)
}

// MigrateLedgerPrefix copies every flat ledger key from srcPrefix to
// dstPrefix within the same bucket, byte-for-byte, skipping per-run output
// sub-keys. It is idempotent -- rerunning it overwrites each destination
// object with the current source bytes. Returns the full source keys it
// copied (still under srcPrefix) so the caller can print a count and spot-
// check IDs.
func MigrateLedgerPrefix(ctx context.Context, client listAPI, bucket, srcPrefix, dstPrefix string) ([]string, error) {
	keys, err := listFlatKeys(ctx, client, bucket, srcPrefix, 0)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", srcPrefix, err)
	}
	for _, k := range keys {
		obj, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &k})
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", k, err)
		}
		data, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", k, err)
		}
		dstKey := dstPrefix + strings.TrimPrefix(k, srcPrefix)
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      &bucket,
			Key:         &dstKey,
			Body:        bytes.NewReader(data),
			ContentType: ptr("application/json"),
		}); err != nil {
			return nil, fmt.Errorf("put %s: %w", dstKey, err)
		}
	}
	return keys, nil
}
